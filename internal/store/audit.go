package store

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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

// AuditGenesisHash is the prev_hash of the very first record.
var AuditGenesisHash = strings.Repeat("0", 64)

// AuditTimeLayout is the fixed-width UTC layout of audit timestamps, so text
// comparison equals time comparison.
const AuditTimeLayout = "2006-01-02T15:04:05.000000Z"

const (
	auditKeyInfo   = "sendora audit chain v1"
	auditFieldMax  = 2000
	auditWriteWait = 4 * time.Second
)

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
	s.auditOnce.Do(func() { s.auditMacFn = auditMAC(s.cipherKey) })
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

// migrateAudit creates the audit tables, drops the retired optional event log
// and its settings, and adds the auditor flag. It is additive and idempotent;
// it never needs the database key (MACs are only computed when writing).
func (s *Store) migrateAudit() error {
	for _, q := range []string{
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
		`CREATE TABLE IF NOT EXISTS audit_checkpoint (
			id INTEGER PRIMARY KEY CHECK (id=1),
			last_deleted_seq INTEGER NOT NULL DEFAULT 0,
			last_deleted_hash TEXT NOT NULL DEFAULT '',
			mac TEXT NOT NULL DEFAULT '')`,
		`INSERT OR IGNORE INTO audit_checkpoint(id,last_deleted_seq,last_deleted_hash,mac) VALUES(1,0,'','')`,
		`DROP TABLE IF EXISTS event_log`,
		`DELETE FROM settings WHERE key IN ('logging_enabled','log_retention_hours')`,
	} {
		if _, err := s.DB.Exec(q); err != nil {
			return err
		}
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
	var n sql.NullInt64
	if e := q.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name='audit_log'`).Scan(&n); e != nil && !errors.Is(e, sql.ErrNoRows) {
		err = e
		return
	}
	if n.Valid && n.Int64 > issued {
		issued = n.Int64
	}
	return
}

func clipAudit(v string) string {
	v = strings.ToValidUTF8(v, "")
	if len(v) <= auditFieldMax {
		return v
	}
	r := []rune(v)
	for len(string(r)) > auditFieldMax {
		r = r[:len(r)-1]
	}
	return string(r)
}

// AuditAppend writes one record to the end of the chain. It is serialised and
// synchronous. seq, ts (when empty), prev_hash and hash are assigned here.
func (s *Store) AuditAppend(rec AuditRecord) (AuditRecord, error) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), auditWriteWait)
	defer cancel()
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

// verifyChain walks the chain from the checkpoint. upTo > 0 limits the walk to
// records with seq <= upTo (used before purging) and skips the tail checks.
func verifyChain(ctx context.Context, db auditRowReader, mac func([]byte) []byte, upTo int64) (AuditStatus, error) {
	st := AuditStatus{OK: true}
	var cpMAC string
	if err := db.QueryRowContext(ctx, `SELECT last_deleted_seq,last_deleted_hash,mac FROM audit_checkpoint WHERE id=1`).Scan(&st.CheckpointSeq, &st.CheckpointHash, &cpMAC); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			st.OK, st.FirstBroken, st.Reason = false, 1, "checkpoint row missing"
			return st, nil
		}
		return st, err
	}
	broke := func(seq int64, reason string) {
		if st.OK {
			st.OK, st.FirstBroken, st.Reason = false, seq, reason
		}
	}
	prev := st.CheckpointHash
	if st.CheckpointSeq == 0 {
		prev = AuditGenesisHash
		if st.CheckpointHash != "" {
			broke(1, "checkpoint modified")
		}
	} else if !hmac.Equal([]byte(cpMAC), []byte(checkpointMAC(mac, st.CheckpointSeq, st.CheckpointHash))) {
		broke(st.CheckpointSeq, "checkpoint modified")
	}
	expect := st.CheckpointSeq + 1
	st.LastSeq = st.CheckpointSeq
	q := `SELECT seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash FROM audit_log ORDER BY seq ASC`
	args := []any{}
	if upTo > 0 {
		q = `SELECT seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash FROM audit_log WHERE seq<=? ORDER BY seq ASC`
		args = append(args, upTo)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.Seq, &r.TS, &r.Category, &r.ActorID, &r.ActorName, &r.ActorRole, &r.Action, &r.Target, &r.Outcome, &r.Detail, &r.IP, &r.PrevHash, &r.Hash); err != nil {
			rows.Close()
			return st, err
		}
		st.Count++
		if st.OldestTS == "" {
			st.OldestTS = r.TS
		}
		switch {
		case r.Seq < expect:
			broke(r.Seq, "record out of sequence")
		case r.Seq > expect:
			broke(expect, "record missing")
		}
		if r.PrevHash != prev {
			broke(r.Seq, "chain link broken")
		}
		if !hmac.Equal([]byte(r.Hash), []byte(recordHash(mac, &r))) {
			broke(r.Seq, "record modified or forged")
		}
		prev = r.Hash
		expect = r.Seq + 1
		st.LastSeq = r.Seq
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return st, err
	}
	if upTo == 0 {
		var n sql.NullInt64
		if e := db.QueryRowContext(ctx, `SELECT seq FROM sqlite_sequence WHERE name='audit_log'`).Scan(&n); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return st, e
		}
		if n.Valid && n.Int64 > st.LastSeq {
			broke(st.LastSeq+1, "newest records removed")
		}
	}
	return st, nil
}

// AuditVerify verifies the whole chain of the live store.
func (s *Store) AuditVerify() (AuditStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return verifyChain(ctx, s.DB, s.AuditMAC(), 0)
}

// VerifyAuditDatabase opens a database read-only (no migrations) and verifies
// its audit chain. A database from before the audit log has an empty chain.
func VerifyAuditDatabase(path string, key []byte) (AuditStatus, error) {
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
	return verifyChain(ctx, db, auditMAC(key), 0)
}

// AuditPurge removes records older than cutoff (a time in UTC) and moves the
// checkpoint to the newest removed record in one transaction. It refuses to
// purge when the part to be removed does not verify, so evidence of tampering is
// never destroyed by retention. It returns the number of records removed.
func (s *Store) AuditPurge(cutoff time.Time) (int64, error) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
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
	st, err := verifyChain(ctx, s.DB, mac, seq)
	if err != nil {
		return 0, err
	}
	if !st.OK {
		return 0, fmt.Errorf("audit chain broken at seq %d (%s); retention purge refused", st.FirstBroken, st.Reason)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE seq<=?`, seq)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE audit_checkpoint SET last_deleted_seq=?,last_deleted_hash=?,mac=? WHERE id=1`, seq, hash, checkpointMAC(mac, seq, hash)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CarryAuditLog replaces the audit tables of the database file at path (a
// prepared import that uses this store's key) by this store's live log, so that
// importing a backup can neither erase nor roll back the audit trail.
func (s *Store) CarryAuditLog(path string) error {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
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
	var issued sql.NullInt64
	if err := s.DB.QueryRow(`SELECT last_deleted_seq,last_deleted_hash,mac FROM audit_checkpoint WHERE id=1`).Scan(&cpSeq, &cpHash, &cpMAC); err != nil {
		return err
	}
	if e := s.DB.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='audit_log'`).Scan(&issued); e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, err := tx.Exec(`UPDATE audit_checkpoint SET last_deleted_seq=?,last_deleted_hash=?,mac=? WHERE id=1`, cpSeq, cpHash, cpMAC); err != nil {
		return err
	}
	rows, err := s.DB.Query(`SELECT seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash FROM audit_log ORDER BY seq ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.Seq, &r.TS, &r.Category, &r.ActorID, &r.ActorName, &r.ActorRole, &r.Action, &r.Target, &r.Outcome, &r.Detail, &r.IP, &r.PrevHash, &r.Hash); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO audit_log(seq,ts,category,actor_id,actor_name,actor_role,action,target,outcome,detail,ip,prev_hash,hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.Seq, r.TS, r.Category, r.ActorID, r.ActorName, r.ActorRole, r.Action, r.Target, r.Outcome, r.Detail, r.IP, r.PrevHash, r.Hash); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if issued.Valid {
		// Keep the high-water mark so a removed tail stays detectable.
		if _, err := tx.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='audit_log'`, issued.Int64); err != nil {
			return err
		}
	}
	return tx.Commit()
}
