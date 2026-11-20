package store

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Tamper-evident audit log.
//
// Every record carries hash = HMAC-SHA256(auditKey, canonical(all fields,
// including seq and prev_hash)). prev_hash is the hash of the record before it,
// so editing, deleting, inserting or reordering a record breaks the chain at a
// definite sequence number. auditKey is derived from the database key with
// HKDF; anyone holding only the database file (and not the key) cannot forge a
// valid record.
//
// Records are only ever added. The one deletion path is AuditPurge, which
// removes the oldest records (retention) and stores the last removed record's
// seq and hash in audit_checkpoint inside the same transaction, so the
// remaining chain still verifies.
//
// Verification of the live store is incremental (AuditVerifyIncremental): the
// last verified position is kept in memory and only newer records are checked.
// Triggers count every UPDATE or DELETE of audit_log, and every INSERT that is
// not an append at the end, in audit_guard.gen; a changed counter or checkpoint
// makes the next check start again from the checkpoint, so the result always
// equals that of a full walk (verifyChain), which audit-verify runs offline.

// AuditGenesisHash is the prev_hash of the very first record.
var AuditGenesisHash = strings.Repeat("0", 64)

// AuditTimeLayout is the fixed-width UTC layout of audit timestamps, so text
// comparison equals time comparison.
const AuditTimeLayout = "2006-01-02T15:04:05.000000Z"

// Variables so tests can shorten them.
var (
	auditWriteWait = 4 * time.Second
	// auditVerifyChunk is how many records one read transaction of the
	// incremental verification checks before it frees the connection.
	auditVerifyChunk = 5000
)

const (
	auditKeyInfo = "sendora audit chain v1"
	// AuditRetentionMinDays is the shortest retention that can be configured, so
	// lowering the setting cannot be used to remove recent evidence.
	AuditRetentionMinDays = 90
)

// AuditFieldMax is the longest stored text field in bytes; longer values are cut
// at a character boundary.
const AuditFieldMax = 2000

// ErrAuditPurgeRefused marks a retention purge that was refused because the
// records to be removed do not verify.
var ErrAuditPurgeRefused = errors.New("retention purge refused")

// ctxMutex is a mutex whose lock gives up when the context ends, so a write
// deadline also covers the wait for the lock. The zero value is ready to use.
type ctxMutex struct {
	once sync.Once
	ch   chan struct{}
}

func (m *ctxMutex) lock(ctx context.Context) error {
	m.once.Do(func() { m.ch = make(chan struct{}, 1) })
	select {
	case m.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *ctxMutex) unlock() { <-m.ch }

// AuditRecord is one stored (or to be stored) audit entry.
type AuditRecord struct {
	Seq       int64  `json:"seq"`
	TS        string `json:"ts"`
	Category  string `json:"category"`
	ActorID   int64  `json:"actor_id"`
	ActorName string `json:"actor_name"`
	ActorRole string `json:"actor_role"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Outcome   string `json:"outcome"`
	Detail    string `json:"detail"`
	IP        string `json:"ip"`
	PrevHash  string `json:"prev_hash"`
	Hash      string `json:"hash"`
}

// AuditStatus is the result of verifying the whole chain.
type AuditStatus struct {
	OK             bool   `json:"ok"`
	FirstBroken    int64  `json:"first_broken_seq,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Count          int64  `json:"count"`
	OldestTS       string `json:"oldest_ts,omitempty"`
	LastSeq        int64  `json:"last_seq"`
	CheckpointSeq  int64  `json:"checkpoint_seq"`
	CheckpointHash string `json:"checkpoint_hash"`
}

func auditKey(dbKey []byte) []byte {
	k := dbKey
	if len(k) == 0 { // plaintext fixture store only
		k = make([]byte, 32)
	}
	out, err := hkdf.Key(sha256.New, k, nil, auditKeyInfo, 32)
	if err != nil {
		panic(err) // cannot happen for these parameters
	}
	return out
}

// auditMAC returns the HMAC function of the chain without exposing the key.
func auditMAC(dbKey []byte) func([]byte) []byte {
	k := auditKey(dbKey)
	return func(data []byte) []byte {
		m := hmac.New(sha256.New, k)
		m.Write(data)
		return m.Sum(nil)
	}
}

// AuditMAC exposes the chain's HMAC as a closure; the key itself never leaves
// the store package.
func (s *Store) AuditMAC() func([]byte) []byte {
	s.auditOnce.Do(func() {
		root := s.rootKey
		if root == nil {
			root = s.cipherKey
		}
		s.auditMacFn = auditMAC(root)
	})
	return s.auditMacFn
}

func appendField(b []byte, v string) []byte {
	b = binary.AppendUvarint(b, uint64(len(v)))
	return append(b, v...)
}

// canonicalAudit is the exact byte string that is authenticated.
func canonicalAudit(r *AuditRecord) []byte {
	b := []byte("sendora-audit-v1\x00")
	for _, f := range []string{
		strconv.FormatInt(r.Seq, 10), r.TS, r.Category, strconv.FormatInt(r.ActorID, 10), r.ActorName,
		r.ActorRole, r.Action, r.Target, r.Outcome, r.Detail, r.IP, r.PrevHash,
	} {
		b = appendField(b, f)
	}
	return b
}

func recordHash(mac func([]byte) []byte, r *AuditRecord) string {
	return hex.EncodeToString(mac(canonicalAudit(r)))
}

func checkpointMAC(mac func([]byte) []byte, seq int64, hash string) string {
	b := []byte("sendora-audit-checkpoint-v1\x00")
	b = appendField(b, strconv.FormatInt(seq, 10))
	b = appendField(b, hash)
	return hex.EncodeToString(mac(b))
}

// auditGuardTriggers count every change to audit_log other than an append at
// the end; the incremental verification restarts when the count moves.
var auditGuardTriggers = []string{"audit_log_guard_update", "audit_log_guard_delete", "audit_log_guard_insert"}

// migrateAudit creates the audit tables, drops the retired optional event log
// and its settings, and adds the auditor flag. It is additive and idempotent;
// it never needs the database key (MACs are only computed when writing).
func (s *Store) migrateAudit() error {
	for _, q := range append(append([]string{}, auditSchema...),
		`DROP TABLE IF EXISTS event_log`,
		`DELETE FROM settings WHERE key IN ('logging_enabled','log_retention_hours')`,
	) {
		if _, err := s.DB.Exec(q); err != nil {
			return err
		}
	}
	return s.migrateAuditSettings()
}

// auditSchema creates the chain's tables, indexes and guard triggers. The
// platform database of multi-tenant mode (platform.go) uses the same schema.
var auditSchema = []string{
	`CREATE TABLE IF NOT EXISTS audit_log (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL,
			category TEXT NOT NULL,
			actor_id INTEGER NOT NULL DEFAULT 0,
			actor_name TEXT NOT NULL DEFAULT '',
			actor_role TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,
			target TEXT NOT NULL DEFAULT '',
			outcome TEXT NOT NULL DEFAULT 'ok',
			detail TEXT NOT NULL DEFAULT '',
			ip TEXT NOT NULL DEFAULT '',
			prev_hash TEXT NOT NULL,
			hash TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log(ts)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_category ON audit_log(category)`,
	// Filtered pages are read newest first by seq.
	`CREATE INDEX IF NOT EXISTS idx_audit_log_category_seq ON audit_log(category, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_action_seq ON audit_log(action, seq)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_actor ON audit_log(actor_name)`,
	`CREATE INDEX IF NOT EXISTS idx_audit_log_outcome_seq ON audit_log(outcome, seq)`,
	`CREATE TABLE IF NOT EXISTS audit_checkpoint (
			id INTEGER PRIMARY KEY CHECK (id=1),
			last_deleted_seq INTEGER NOT NULL DEFAULT 0,
			last_deleted_hash TEXT NOT NULL DEFAULT '',
			mac TEXT NOT NULL DEFAULT '')`,
	`INSERT OR IGNORE INTO audit_checkpoint(id,last_deleted_seq,last_deleted_hash,mac) VALUES(1,0,'','')`,
	`CREATE TABLE IF NOT EXISTS audit_guard (
			id INTEGER PRIMARY KEY CHECK (id=1),
			gen INTEGER NOT NULL DEFAULT 0)`,
	`INSERT OR IGNORE INTO audit_guard(id,gen) VALUES(1,0)`,
	`CREATE TRIGGER IF NOT EXISTS audit_log_guard_update AFTER UPDATE ON audit_log BEGIN UPDATE audit_guard SET gen=gen+1 WHERE id=1; END`,
	`CREATE TRIGGER IF NOT EXISTS audit_log_guard_delete AFTER DELETE ON audit_log BEGIN UPDATE audit_guard SET gen=gen+1 WHERE id=1; END`,
	`CREATE TRIGGER IF NOT EXISTS audit_log_guard_insert AFTER INSERT ON audit_log WHEN EXISTS(SELECT 1 FROM audit_log WHERE seq>NEW.seq) BEGIN UPDATE audit_guard SET gen=gen+1 WHERE id=1; END`,
}

func (s *Store) migrateAuditSettings() error {
	// The retention minimum is 90 days. A shorter stored value (from before the
	// minimum existed) is raised, so nobody can wipe recent evidence with it.
	res, err := s.DB.Exec(`UPDATE settings SET value=? WHERE key='audit_retention_days' AND CAST(value AS INTEGER) < ?`, strconv.Itoa(AuditRetentionMinDays), AuditRetentionMinDays)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("audit_retention_days was below the minimum of %d days and was raised to %d", AuditRetentionMinDays, AuditRetentionMinDays)
	}
	rows, err := s.DB.Query("PRAGMA table_info(users)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "is_auditor" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = s.DB.Exec(`ALTER TABLE users ADD COLUMN is_auditor INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	return nil
}

type auditQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readIssued returns the highest seq ever issued (sqlite_sequence), or 0.
func readIssued(ctx context.Context, q auditQuerier) (int64, error) {
	var n sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name='audit_log'`).Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return n.Int64, nil
}

// auditTail returns the seq and hash the next record must chain from, and the
// highest seq ever issued (sqlite_sequence), which exposes removed tail records.
func auditTail(ctx context.Context, q auditQuerier) (lastSeq int64, lastHash string, issued int64, err error) {
	var cpSeq int64
	var cpHash string
	if err = q.QueryRowContext(ctx, `SELECT last_deleted_seq,last_deleted_hash FROM audit_checkpoint WHERE id=1`).Scan(&cpSeq, &cpHash); err != nil {
		return
	}
	lastSeq, lastHash = cpSeq, cpHash
	if lastSeq == 0 {
		lastHash = AuditGenesisHash
	}
	var seq int64
	var hash string
	switch e := q.QueryRowContext(ctx, `SELECT seq,hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&seq, &hash); {
	case e == nil:
		lastSeq, lastHash = seq, hash
	case errors.Is(e, sql.ErrNoRows):
	default:
		err = e
		return
	}
	issued = lastSeq
	n, e := readIssued(ctx, q)
	if e != nil {
		err = e
		return
	}
	if n > issued {
		issued = n
	}
	return
}

// clipAudit makes v valid UTF-8 and cuts it to AuditFieldMax bytes at a
// character boundary, in one pass.
func clipAudit(v string) string {
	v = strings.ToValidUTF8(v, "")
	if len(v) <= AuditFieldMax {
		return v
	}
	i := AuditFieldMax
	for i > 0 && !utf8.RuneStart(v[i]) {
		i--
	}
	return v[:i]
}

// AuditAppend writes one record to the end of the chain. It is serialised and
// synchronous. seq, ts (when empty), prev_hash and hash are assigned here. The
// write deadline includes the wait for the lock.
func (s *Store) AuditAppend(rec AuditRecord) (AuditRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), auditWriteWait)
	defer cancel()
	if err := s.auditMu.lock(ctx); err != nil {
		return rec, err
	}
	defer s.auditMu.unlock()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return rec, err
	}
	defer tx.Rollback()
	_, lastHash, issued, err := auditTail(ctx, tx)
	if err != nil {
		return rec, err
	}
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(AuditTimeLayout)
	}
	for _, p := range []*string{&rec.Category, &rec.ActorName, &rec.ActorRole, &rec.Action, &rec.Target, &rec.Outcome, &rec.Detail, &rec.IP} {
		*p = clipAudit(*p)
	}
	rec.Seq = issued + 1
	rec.PrevHash = lastHash
	rec.Hash = recordHash(s.AuditMAC(), &rec)
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_log(seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		rec.Seq, rec.TS, rec.Category, rec.ActorID, rec.ActorName, rec.ActorRole, rec.Action, rec.Target, rec.Outcome, rec.Detail, rec.IP, rec.PrevHash, rec.Hash); err != nil {
		return rec, err
	}
	return rec, tx.Commit()
}

type auditRowReader interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readSnapshot runs fn inside one read transaction on a dedicated connection, so
// every query of fn sees the same state of the database (concurrent appends or
// purges cannot make a verification report removed or missing records).
func readSnapshot(ctx context.Context, db *sql.DB, fn func(q auditRowReader) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN DEFERRED`); err != nil {
		return err
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `ROLLBACK`); err != nil && !strings.Contains(err.Error(), "no transaction is active") {
			// Never hand a connection with an open transaction back to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn(conn)
}

func readCheckpoint(ctx context.Context, q auditQuerier) (seq int64, hash, mac string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT last_deleted_seq,last_deleted_hash,mac FROM audit_checkpoint WHERE id=1`).Scan(&seq, &hash, &mac)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", false, nil
	}
	return seq, hash, mac, err == nil, err
}

// chainWalker checks records in seq order; verifyChain and the incremental
// verification share it, so both give the same answer.
type chainWalker struct {
	st     AuditStatus
	mac    func([]byte) []byte
	prev   string
	expect int64
	cursor int64 // seq of the last record read; math.MinInt64 before the first
}

func newChainWalker(mac func([]byte) []byte, cpSeq int64, cpHash, cpMAC string) *chainWalker {
	w := &chainWalker{st: AuditStatus{OK: true, CheckpointSeq: cpSeq, CheckpointHash: cpHash, LastSeq: cpSeq},
		mac: mac, prev: cpHash, expect: cpSeq + 1, cursor: math.MinInt64}
	if cpSeq == 0 {
		w.prev = AuditGenesisHash
		if cpHash != "" {
			w.broke(1, "checkpoint modified")
		}
	} else if !hmac.Equal([]byte(cpMAC), []byte(checkpointMAC(mac, cpSeq, cpHash))) {
		w.broke(cpSeq, "checkpoint modified")
	}
	return w
}

func (w *chainWalker) broke(seq int64, reason string) {
	if w.st.OK {
		w.st.OK, w.st.FirstBroken, w.st.Reason = false, seq, reason
	}
}

func (w *chainWalker) add(r *AuditRecord) {
	w.st.Count++
	if w.st.OldestTS == "" {
		w.st.OldestTS = r.TS
	}
	switch {
	case r.Seq < w.expect:
		w.broke(r.Seq, "record out of sequence")
	case r.Seq > w.expect:
		w.broke(w.expect, "record missing")
	}
	if r.PrevHash != w.prev {
		w.broke(r.Seq, "chain link broken")
	}
	if !hmac.Equal([]byte(r.Hash), []byte(recordHash(w.mac, r))) {
		w.broke(r.Seq, "record modified or forged")
	}
	w.prev = r.Hash
	w.expect = r.Seq + 1
	w.st.LastSeq = r.Seq
	w.cursor = r.Seq
}

// result is the status so far; with the highest issued seq it also reports a
// removed tail. The walker itself is not changed.
func (w *chainWalker) result(issued int64, checkTail bool) AuditStatus {
	st := w.st
	if checkTail && issued > st.LastSeq && st.OK {
		st.OK, st.FirstBroken, st.Reason = false, st.LastSeq+1, "newest records removed"
	}
	return st
}

// walk feeds the records after the walker's cursor (and up to upTo when > 0) to
// the walker, at most limit of them when limit > 0. It returns how many it read.
func (w *chainWalker) walk(ctx context.Context, db auditRowReader, upTo int64, limit int) (int, error) {
	q := `SELECT seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash FROM audit_log WHERE seq>?`
	args := []any{w.cursor}
	if upTo > 0 {
		q += ` AND seq<=?`
		args = append(args, upTo)
	}
	q += ` ORDER BY seq ASC`
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.Seq, &r.TS, &r.Category, &r.ActorID, &r.ActorName, &r.ActorRole, &r.Action, &r.Target, &r.Outcome, &r.Detail, &r.IP, &r.PrevHash, &r.Hash); err != nil {
			return n, err
		}
		w.add(&r)
		n++
	}
	return n, rows.Err()
}

// verifyChain walks the chain from the checkpoint. upTo > 0 limits the walk to
// records with seq <= upTo (used before purging) and skips the tail checks. The
// caller provides a consistent view (a transaction or a read snapshot).
func verifyChain(ctx context.Context, db auditRowReader, mac func([]byte) []byte, upTo int64) (AuditStatus, error) {
	cpSeq, cpHash, cpMAC, found, err := readCheckpoint(ctx, db)
	if err != nil {
		return AuditStatus{}, err
	}
	if !found {
		return AuditStatus{OK: false, FirstBroken: 1, Reason: "checkpoint row missing"}, nil
	}
	w := newChainWalker(mac, cpSeq, cpHash, cpMAC)
	if _, err := w.walk(ctx, db, upTo, 0); err != nil {
		return w.st, err
	}
	if upTo > 0 {
		return w.result(0, false), nil
	}
	issued, err := readIssued(ctx, db)
	if err != nil {
		return w.st, err
	}
	return w.result(issued, true), nil
}

// AuditVerify verifies the whole chain of the live store in one read snapshot.
func (s *Store) AuditVerify() (AuditStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var st AuditStatus
	err := readSnapshot(ctx, s.DB, func(q auditRowReader) error {
		var e error
		st, e = verifyChain(ctx, q, s.AuditMAC(), 0)
		return e
	})
	return st, err
}

// auditVerifyState is the in-memory progress of the incremental verification:
// the walker has checked every record up to its cursor, starting from the
// checkpoint (cpSeq, cpHash, cpMAC), while audit_guard.gen was gen.
type auditVerifyState struct {
	mu     sync.Mutex
	w      *chainWalker
	gen    int64
	cpSeq  int64
	cpHash string
	cpMAC  string
}

func (v *auditVerifyState) matches(gen, cpSeq int64, cpHash, cpMAC string) bool {
	return v.w != nil && gen >= 0 && v.gen == gen && v.cpSeq == cpSeq && v.cpHash == cpHash && v.cpMAC == cpMAC
}

// readGuard returns audit_guard.gen, or -1 when the guard (row or triggers) is
// missing, in which case only a full walk can be trusted.
func readGuard(ctx context.Context, q auditQuerier) (int64, error) {
	var triggers int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='audit_log' AND name IN (?,?,?)`,
		auditGuardTriggers[0], auditGuardTriggers[1], auditGuardTriggers[2]).Scan(&triggers); err != nil {
		return 0, err
	}
	if triggers != len(auditGuardTriggers) {
		return -1, nil
	}
	var gen int64
	switch err := q.QueryRowContext(ctx, `SELECT gen FROM audit_guard WHERE id=1`).Scan(&gen); {
	case errors.Is(err, sql.ErrNoRows):
		return -1, nil
	case err != nil:
		return 0, err
	}
	return gen, nil
}

// AuditVerifyIncremental returns the same status as a full verification of the
// live chain, but only checks the records added since the last call (the first
// call, and any call after a change to existing records or to the checkpoint,
// walks from the checkpoint). Long walks run in read transactions of
// auditVerifyChunk records, freeing the connection in between.
func (s *Store) AuditVerifyIncremental(ctx context.Context) (AuditStatus, error) {
	v := &s.auditVer
	v.mu.Lock()
	defer v.mu.Unlock()
	mac := s.AuditMAC()
	for {
		var st AuditStatus
		done := false
		err := readSnapshot(ctx, s.DB, func(q auditRowReader) error {
			gen, err := readGuard(ctx, q)
			if err != nil {
				return err
			}
			if gen < 0 {
				v.w = nil
				st, err = verifyChain(ctx, q, mac, 0)
				done = true
				return err
			}
			cpSeq, cpHash, cpMAC, found, err := readCheckpoint(ctx, q)
			if err != nil {
				return err
			}
			if !found {
				v.w = nil
				st, done = AuditStatus{OK: false, FirstBroken: 1, Reason: "checkpoint row missing"}, true
				return nil
			}
			if !v.matches(gen, cpSeq, cpHash, cpMAC) {
				v.w = newChainWalker(mac, cpSeq, cpHash, cpMAC)
				v.gen, v.cpSeq, v.cpHash, v.cpMAC = gen, cpSeq, cpHash, cpMAC
			}
			n, err := v.w.walk(ctx, q, 0, auditVerifyChunk)
			if err != nil {
				return err
			}
			if n == auditVerifyChunk {
				return nil // more records: continue in a new snapshot
			}
			issued, err := readIssued(ctx, q)
			if err != nil {
				return err
			}
			st, done = v.w.result(issued, true), true
			return nil
		})
		if err != nil {
			v.w = nil // a half-read chunk cannot be trusted
			return st, err
		}
		if done {
			return st, nil
		}
	}
}

// VerifyAuditDatabase opens a database read-only (no migrations) and verifies
// its audit chain. A database from before the audit log has an empty chain.
func VerifyAuditDatabase(path string, key []byte) (AuditStatus, error) {
	return VerifyAuditDatabaseRoot(path, key, key)
}

// VerifyAuditDatabaseRoot is VerifyAuditDatabase for a database whose audit
// key is derived from root instead of the database key (an imported tenant).
func VerifyAuditDatabaseRoot(path string, key, root []byte) (AuditStatus, error) {
	db, err := openCipherDB(path, key, true)
	if err != nil {
		return AuditStatus{}, err
	}
	defer db.Close()
	if err := checkCipherIntegrity(db, len(key) != 0); err != nil {
		return AuditStatus{}, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('audit_log','audit_checkpoint')`).Scan(&n); err != nil {
		return AuditStatus{}, err
	}
	if n != 2 {
		return AuditStatus{}, errors.New("the database has no audit log (created before the audit log existed)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var st AuditStatus
	err = readSnapshot(ctx, db, func(q auditRowReader) error {
		var e error
		st, e = verifyChain(ctx, q, auditMAC(root), 0)
		return e
	})
	return st, err
}

// AuditPurge removes records older than cutoff (a time in UTC) and moves the
// checkpoint to the newest removed record in one transaction. It refuses to
// purge when the part to be removed does not verify (ErrAuditPurgeRefused), so
// evidence of tampering is never destroyed by retention. It returns the number
// of records removed.
//
// A refused purge is not repaired automatically, and there is deliberately no
// command that re-anchors the chain. Recovery: verify the database offline
// (sendora-db audit-verify), investigate what changed, then restore a backup
// taken before the damage.
func (s *Store) AuditPurge(cutoff time.Time) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := s.auditMu.lock(ctx); err != nil {
		return 0, err
	}
	defer s.auditMu.unlock()
	var seq int64
	var hash string
	err := s.DB.QueryRowContext(ctx, `SELECT seq,hash FROM audit_log WHERE ts<? ORDER BY seq DESC LIMIT 1`, cutoff.UTC().Format(AuditTimeLayout)).Scan(&seq, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	mac := s.AuditMAC()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	genBefore, err := readGuard(ctx, tx)
	if err != nil {
		return 0, err
	}
	cpSeq, cpHash, cpMAC, _, err := readCheckpoint(ctx, tx)
	if err != nil {
		return 0, err
	}
	// The incremental verification may already cover the part to be removed. It
	// is only consulted when free (TryLock): this transaction holds the single
	// connection, which a running verification may be waiting for.
	v := &s.auditVer
	haveV := v.mu.TryLock()
	if haveV {
		defer v.mu.Unlock()
	}
	covered := haveV && v.matches(genBefore, cpSeq, cpHash, cpMAC) && v.w.cursor >= seq
	if covered && !v.w.st.OK && v.w.st.FirstBroken <= seq {
		return 0, fmt.Errorf("%w: audit chain broken at seq %d (%s)", ErrAuditPurgeRefused, v.w.st.FirstBroken, v.w.st.Reason)
	}
	if !covered {
		st, err := verifyChain(ctx, tx, mac, seq)
		if err != nil {
			return 0, err
		}
		if !st.OK {
			return 0, fmt.Errorf("%w: audit chain broken at seq %d (%s)", ErrAuditPurgeRefused, st.FirstBroken, st.Reason)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE seq<=?`, seq)
	if err != nil {
		return 0, err
	}
	newMAC := checkpointMAC(mac, seq, hash)
	if _, err := tx.ExecContext(ctx, `UPDATE audit_checkpoint SET last_deleted_seq=?,last_deleted_hash=?,mac=? WHERE id=1`, seq, hash, newMAC); err != nil {
		return 0, err
	}
	genAfter, err := readGuard(ctx, tx)
	if err != nil {
		return 0, err
	}
	var oldest sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT ts FROM audit_log ORDER BY seq ASC LIMIT 1`).Scan(&oldest); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if covered && genAfter >= 0 {
		// Every removed record had been verified: move the verified state along
		// with the checkpoint instead of walking the whole chain again.
		v.w.st.Count -= n
		v.w.st.CheckpointSeq, v.w.st.CheckpointHash, v.w.st.OldestTS = seq, hash, oldest.String
		v.gen, v.cpSeq, v.cpHash, v.cpMAC = genAfter, seq, hash, newMAC
	}
	return n, nil
}

// CarryAuditLog replaces the audit tables of the database file at path (a
// prepared import that uses this store's key) by this store's live log, so that
// importing a backup can neither erase nor roll back the audit trail.
func (s *Store) CarryAuditLog(path string) error {
	if err := s.auditMu.lock(context.Background()); err != nil {
		return err
	}
	defer s.auditMu.unlock()
	dst, err := openCipherDB(path, s.cipherKey, false)
	if err != nil {
		return err
	}
	defer func() {
		dst.Close()
		for _, suffix := range []string{"-wal", "-shm"} {
			os.Remove(path + suffix)
		}
	}()
	tx, err := dst.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM audit_log`, `DELETE FROM sqlite_sequence WHERE name='audit_log'`} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	var cpSeq int64
	var cpHash, cpMAC string
	if err := s.DB.QueryRow(`SELECT last_deleted_seq,last_deleted_hash,mac FROM audit_checkpoint WHERE id=1`).Scan(&cpSeq, &cpHash, &cpMAC); err != nil {
		return err
	}
	issued, err := readIssued(context.Background(), s.DB)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE audit_checkpoint SET last_deleted_seq=?,last_deleted_hash=?,mac=? WHERE id=1`, cpSeq, cpHash, cpMAC); err != nil {
		return err
	}
	rows, err := s.DB.Query(`SELECT seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash FROM audit_log ORDER BY seq ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	maxSeq := cpSeq
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.Seq, &r.TS, &r.Category, &r.ActorID, &r.ActorName, &r.ActorRole, &r.Action, &r.Target, &r.Outcome, &r.Detail, &r.IP, &r.PrevHash, &r.Hash); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO audit_log(seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Seq, r.TS, r.Category, r.ActorID, r.ActorName, r.ActorRole, r.Action, r.Target, r.Outcome, r.Detail, r.IP, r.PrevHash, r.Hash); err != nil {
			return err
		}
		maxSeq = max(maxSeq, r.Seq)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Keep the high-water mark so a removed tail stays detectable and the next
	// record continues the sequence. sqlite_sequence has no unique key: update
	// the row the inserts created, or add it when there is none (an empty or
	// fully purged log), which gives INSERT OR REPLACE semantics.
	if hw := max(issued, maxSeq); hw > 0 {
		res, err := tx.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='audit_log'`, hw)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := tx.Exec(`INSERT INTO sqlite_sequence(name,seq) VALUES('audit_log',?)`, hw); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
