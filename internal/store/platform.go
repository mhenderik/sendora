package store

import (
	"errors"
	"os"
	"path/filepath"
)

// PlatformDBFile is the platform database of multi-tenant mode, in DATA_DIR.
const PlatformDBFile = "platform.db"

// OpenPlatform opens (or, with initialize, creates) the platform database of
// multi-tenant mode. The returned Store only has the settings table, the audit
// chain tables and whatever the caller adds with extra (the platform package's
// schema); tenant tables such as users or shares do not exist in it. The audit
// methods (AuditAppend, AuditVerify, AuditVerifyIncremental, AuditPurge) work
// as for a tenant store, with the audit key derived from key.
func OpenPlatform(dataDir string, key []byte, initialize bool, extra []string) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("platform database key must contain 32 bytes")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dataDir, PlatformDBFile)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if !initialize {
			return nil, errors.New("platform database missing; explicit initialization required (SENDORA_DB_INITIALIZE=true)")
		}
	} else if err != nil {
		return nil, err
	} else if db, err := openCipherDB(p, key, true); err != nil {
		return nil, err
	} else {
		db.Close()
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	db, err := openCipherDB(p, key, false)
	if err != nil {
		return nil, err
	}
	stmts := []string{
		`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA temp_store=MEMORY;`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', grp TEXT NOT NULL DEFAULT 'system')`,
	}
	stmts = append(stmts, auditSchema...)
	stmts = append(stmts, extra...)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{DB: db, DataDir: dataDir, Path: p, cipherKey: append([]byte(nil), key...), rootKey: append([]byte(nil), key...)}, nil
}
