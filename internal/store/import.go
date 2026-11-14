package store

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
)

// ImportError is a rejection whose message is safe to show to an administrator
// (it never contains key material or file contents).
type ImportError struct{ Msg string }

func (e *ImportError) Error() string { return e.Msg }

// ImportSummary holds counts of an imported (re-keyed, migrated) database.
type ImportSummary struct {
	Users, Admins  int64 // non-guest accounts
	Shares, Files  int64 // shares that are not deleted, and their files
	DroppedUploads int64 // stale upload sessions removed
}

// ImportDatabase validates src (encrypted with srcKey, or with this store's key
// when srcKey is empty) and writes a copy re-keyed with this store's key to dest.
func (s *Store) ImportDatabase(src, dest string, srcKey []byte) error {
	if !s.IsEncrypted() {
		return errors.New("database import requires an encrypted store")
	}
	key := srcKey
	if len(key) == 0 {
		key = s.cipherKey
	}
	if len(key) != 32 {
		return &ImportError{"database key must contain 32 bytes"}
	}
	err := ExportDatabase(src, dest, key, s.cipherKey)
	if err == nil {
		return nil
	}
	var pathErr *fs.PathError
	switch {
	case errors.As(err, &pathErr):
		return err
	// openCipherDB reports a file it cannot decrypt (wrong key, garbage) through
	// the failing first query as either of these; the engine itself is proven
	// available by the live store being open.
	case errors.Is(err, errCannotRead), errors.Is(err, errEngineUnavailable):
		return &ImportError{"the file could not be opened: it is not an encrypted Sendora database, it is damaged, or the key is wrong"}
	default:
		return &ImportError{"the database failed validation (integrity check)"}
	}
}

// PrepareImportedCopy runs PrepareImported with this store's key.
func (s *Store) PrepareImportedCopy(path string) (*ImportSummary, error) {
	return PrepareImported(path, s.cipherKey, s.DataDir)
}

// ScanImportedCopy visits (shareID, disk_path) of files of non-deleted shares
// in a prepared copy, read-only.
func (s *Store) ScanImportedCopy(path string, fn func(shareID int64, diskPath string)) error {
	db, err := openCipherDB(path, s.cipherKey, true)
	if err != nil {
		return err
	}
	defer func() {
		db.Close()
		for _, suffix := range []string{"-wal", "-shm"} {
			os.Remove(path + suffix)
		}
	}()
	rows, err := db.Query(`SELECT f.share_id,f.disk_path FROM files f JOIN shares s ON s.id=f.share_id WHERE s.status<>'deleted'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p string
		if err := rows.Scan(&id, &p); err != nil {
			return err
		}
		fn(id, p)
	}
	return rows.Err()
}

var (
	refOnce   sync.Once
	refTables map[string]map[string]bool
	refErr    error
)

// Tables created outside the store package by the application (server.go).
var externalTables = map[string]bool{"verification_attempts": true}

// referenceSchema builds this binary's own schema in a throw-away encrypted DB.
func referenceSchema() (map[string]map[string]bool, error) {
	refOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sendora-schema-")
		if err != nil {
			refErr = err
			return
		}
		defer os.RemoveAll(dir)
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			refErr = err
			return
		}
		st, err := openStore(dir, key)
		if err != nil {
			refErr = err
			return
		}
		defer st.Close()
		refTables, refErr = readSchema(st.DB)
	})
	return refTables, refErr
}

func readSchema(db *sql.DB) (map[string]map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		names = append(names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, n := range names {
		cols := map[string]bool{}
		rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, n)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return nil, err
			}
			cols[c] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		out[n] = cols
	}
	return out, nil
}

func clipName(s string) string {
	s = strings.ToValidUTF8(s, "")
	if r := []rune(s); len(r) > 40 {
		return string(r[:40])
	}
	return s
}

// PrepareImported opens an already re-keyed copy writable, removes stale upload
// reservations, runs this binary's additive migrations, verifies required tables,
// rejects schemas from newer versions and databases without a usable
// administrator, and returns counts. On any error the file is left for the
// caller to delete. The caller must not use the file concurrently.
//
// An imported file is treated as untrusted input. Everything that is not data is
// discarded or rebuilt from this binary's own definitions: triggers, views and
// indexes are dropped (virtual tables are refused), sessions, reset/verification
// codes and the signing secret are cleared, every share ID must have a known
// format, and every file's disk_path is rewritten to this server's
// DataDir/shares/<share id>/<base name> so a crafted file can never point reads
// or deletes outside a share directory.
func PrepareImported(path string, key []byte, dataDir string) (*ImportSummary, error) {
	if len(key) != 32 {
		return nil, errors.New("database key must contain 32 bytes")
	}
	if dataDir == "" {
		return nil, errors.New("data directory required")
	}
	ref, err := referenceSchema()
	if err != nil {
		return nil, err
	}
	db, err := openCipherDB(path, key, false)
	if err != nil {
		return nil, err
	}
	closed := false
	defer func() {
		if !closed {
			db.Close()
		}
	}()
	// Required core tables must already exist: the idempotent schema would
	// otherwise silently turn an unrelated database into an empty Sendora one.
	have, err := readSchema(db)
	if err != nil {
		return nil, err
	}
	for _, t := range []string{"users", "settings", "shares", "files"} {
		if have[t] == nil {
			return nil, &ImportError{"the database is not a Sendora database (required table missing: " + t + ")"}
		}
	}
	if err := stripForeignObjects(db); err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		return nil, err
	}
	sum := &ImportSummary{}
	// Reservations/staging belong to the old environment; drop them before migration
	// (migration refuses unattributable reservations).
	if err := db.QueryRow(`SELECT COUNT(*) FROM upload_sessions`).Scan(&sum.DroppedUploads); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM upload_files`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`DELETE FROM upload_sessions`); err != nil {
		return nil, err
	}
	tmp := &Store{DB: db, Path: path}
	if err := tmp.runMigrations(); err != nil {
		return nil, &ImportError{"the database cannot be migrated to this version of Sendora"}
	}
	if err := clearImportedSecrets(db); err != nil {
		return nil, err
	}
	if err := rebaseImportedFiles(db, dataDir); err != nil {
		return nil, err
	}
	got, err := readSchema(db)
	if err != nil {
		return nil, err
	}
	for table, cols := range got {
		known, ok := ref[table]
		if !ok {
			if externalTables[table] {
				continue
			}
			return nil, &ImportError{"the database was created with a newer Sendora version (unknown table " + clipName(table) + ")"}
		}
		for c := range cols {
			if !known[c] {
				return nil, &ImportError{"the database was created with a newer Sendora version (unknown column " + clipName(table) + "." + clipName(c) + ")"}
			}
		}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_guest=0`).Scan(&sum.Users); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_guest=0 AND admin=1`).Scan(&sum.Admins); err != nil {
		return nil, err
	}
	if sum.Admins < 1 {
		return nil, &ImportError{"the database contains no administrator account; importing it would lock everyone out"}
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM shares WHERE status<>'deleted'`).Scan(&sum.Shares); err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM files WHERE share_id IN (SELECT id FROM shares WHERE status<>'deleted')`).Scan(&sum.Files); err != nil {
		return nil, err
	}
	// Fold the WAL into the main file so the copy is a single standalone file.
	rows, err := db.Query(`PRAGMA wal_checkpoint(TRUNCATE)`)
	if err != nil {
		return nil, err
	}
	rows.Close()
	closed = true
	if err := db.Close(); err != nil {
		return nil, err
	}
	// Final read-only verification of the prepared file with the live key.
	if _, err := VerifyDatabase(path, key); err != nil {
		return nil, &ImportError{"the prepared database failed verification"}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(path + suffix)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	return sum, nil
}

// RemoveImportFiles deletes a prepared/temporary database and its sidecars.
func RemoveImportFiles(path string) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		os.Remove(path + suffix)
	}
}

func quoteIdent(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

// stripForeignObjects refuses virtual tables and drops every trigger, view and
// explicit index found in the file. initSchema then recreates only this
// binary's own objects (all of them are CREATE ... IF NOT EXISTS).
func stripForeignObjects(db *sql.DB) error {
	var virtual int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND sql LIKE 'CREATE VIRTUAL TABLE%'`).Scan(&virtual); err != nil {
		return err
	}
	if virtual > 0 {
		return &ImportError{"the database contains virtual tables, which are not allowed"}
	}
	rows, err := db.Query(`SELECT type,name FROM sqlite_master WHERE type IN ('trigger','view','index') AND name NOT LIKE 'sqlite_%' AND sql IS NOT NULL`)
	if err != nil {
		return err
	}
	type object struct{ kind, name string }
	var drop []object
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name); err != nil {
			rows.Close()
			return err
		}
		drop = append(drop, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range drop {
		var kind string
		switch o.kind {
		case "trigger":
			kind = "TRIGGER"
		case "view":
			kind = "VIEW"
		default:
			kind = "INDEX"
		}
		if _, err := db.Exec("DROP " + kind + " IF EXISTS " + quoteIdent(o.name)); err != nil {
			return err
		}
	}
	return nil
}

// clearImportedSecrets removes everything that grants or proves access: sessions,
// reset and verification codes, revocation lists and the stored signing secret
// (a fresh one is generated at startup unless JWT_SECRET is set).
func clearImportedSecrets(db *sql.DB) error {
	for _, table := range []string{"refresh_tokens", "password_resets", "email_verifications", "revoked_access_tokens", "verification_attempts", "ip_bans"} {
		var present int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&present); err != nil {
			return err
		}
		if present == 1 {
			if _, err := db.Exec("DELETE FROM " + quoteIdent(table)); err != nil {
				return err
			}
		}
	}
	_, err := db.Exec(`DELETE FROM settings WHERE key='jwt_secret'`)
	return err
}

// rebaseImportedFiles validates share IDs and rewrites each file's disk_path to
// this server's data directory (see PrepareImported).
func rebaseImportedFiles(db *sql.DB, dataDir string) error {
	rows, err := db.Query(`SELECT long_id FROM shares`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var lid string
		if err := rows.Scan(&lid); err != nil {
			rows.Close()
			return err
		}
		if !ValidLongID(lid) {
			rows.Close()
			return &ImportError{"the database contains a share with an invalid link ID; importing it is refused"}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = db.Query(`SELECT f.id,s.long_id,f.disk_path FROM files f JOIN shares s ON s.id=f.share_id`)
	if err != nil {
		return err
	}
	type rewrite struct {
		id   int64
		path string
	}
	var updates []rewrite
	for rows.Next() {
		var id int64
		var lid, disk string
		if err := rows.Scan(&id, &lid, &disk); err != nil {
			rows.Close()
			return err
		}
		safe, perr := SharePath(dataDir, lid, disk)
		if perr != nil {
			rows.Close()
			return &ImportError{"the database contains a file record with an unsafe storage path; importing it is refused"}
		}
		updates = append(updates, rewrite{id, safe})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		if _, err := db.Exec(`UPDATE files SET disk_path=? WHERE id=?`, u.path, u.id); err != nil {
			return err
		}
	}
	return nil
}
