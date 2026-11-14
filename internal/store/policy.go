package store

import (
	"database/sql"
	"fmt"
	"math"
	"strconv"
)

// DefaultQuotaBytes seeds the global default for new installations: 10 GB.
const DefaultQuotaBytes int64 = 10_000_000_000

// LegacyQuotaBytes was the former fixed per-user allowance (10 GiB). It is only
// used to recognise old rows during the one-time inheritance migration.
const LegacyQuotaBytes int64 = 10 * 1024 * 1024 * 1024
const MaxSafeQuotaBytes int64 = 1<<53 - 1
const DefaultStorageQuotaKey = "default_storage_quota_bytes"

// ParseQuotaBytes accepts a complete base-ten integer, never SQLite's permissive
// numeric coercion. Both persisted policy and API settings use this validator.
func ParseQuotaBytes(raw string) (int64, error) {
	for _, digit := range raw {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid storage quota")
		}
	}
	q, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || q < 0 || q > MaxSafeQuotaBytes {
		return 0, fmt.Errorf("invalid storage quota")
	}
	return q, nil
}

// ResolveQuota is pure so it can safely be used while holding the only database
// connection. Missing or corrupt policy fails closed, including stale overrides.
func ResolveQuota(quota, inheritance, defaultQuota sql.NullString) (int64, bool, error) {
	if !quota.Valid || !inheritance.Valid || !defaultQuota.Valid || (inheritance.String != "0" && inheritance.String != "1") {
		return 0, false, fmt.Errorf("invalid storage quota policy")
	}
	q, err := ParseQuotaBytes(quota.String)
	if err != nil {
		return 0, false, err
	}
	d, err := ParseQuotaBytes(defaultQuota.String)
	if err != nil {
		return 0, false, err
	}
	inherits := inheritance.String == "1"
	if inherits {
		q = d
	}
	return q, inherits, nil
}

// StorageUsage counts retained metadata, including expired shares and the full
// declared size of pending uploads (not just bytes already received).
type StorageUsage struct {
	QuotaBytes           int64 `json:"quota_bytes"`
	QuotaInheritsDefault bool  `json:"quota_inherits_default"`
	StoredBytes          int64 `json:"storage_used_bytes"`
	ReservedBytes        int64 `json:"storage_reserved_bytes"`
	AvailableBytes       int64 `json:"storage_available_bytes"`
}

// MigratePolicy is additive and idempotent. Unattributable legacy reservations
// prevent startup rather than silently releasing their storage commitments.
func (s *Store) MigratePolicy() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range []struct{ table, name, declaration string }{
		{"users", "quota_bytes", "INTEGER NOT NULL DEFAULT 10737418240"},
		{"users", "quota_inherits_default", "INTEGER NOT NULL DEFAULT 1"},
		{"users", "login_failures", "INTEGER NOT NULL DEFAULT 0"},
		{"users", "login_locked_until", "INTEGER NOT NULL DEFAULT 0"},
		{"upload_sessions", "quota_owner_id", "INTEGER REFERENCES users(id) ON DELETE CASCADE"},
		{"password_resets", "kind", "TEXT NOT NULL DEFAULT 'reset'"},
	} {
		rows, err := tx.Query("PRAGMA table_info(" + c.table + ")")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == c.name {
				found = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !found {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", c.table, c.name, c.declaration)); err != nil {
				return err
			}
			if c.name == "quota_inherits_default" {
				if _, err := tx.Exec(`UPDATE users SET quota_inherits_default=CASE WHEN quota_bytes=? THEN 1 ELSE 0 END`, LegacyQuotaBytes); err != nil {
					return err
				}
			}
		}
	}
	// Policy migration runs before the ordinary settings seed.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key,value,grp) VALUES(?,?,'uploads')`, DefaultStorageQuotaKey, strconv.FormatInt(DefaultQuotaBytes, 10)); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE upload_sessions SET quota_owner_id=user_id
  WHERE quota_owner_id IS NULL AND invite_id IS NULL
  AND EXISTS(SELECT 1 FROM users WHERE id=upload_sessions.user_id AND is_guest=0)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE upload_sessions SET quota_owner_id=(SELECT r.user_id FROM reverse_invites r WHERE r.id=upload_sessions.invite_id)
  WHERE quota_owner_id IS NULL AND EXISTS(SELECT 1 FROM users WHERE id=upload_sessions.user_id AND is_guest=1)
  AND EXISTS(SELECT 1 FROM reverse_invites r JOIN users u ON u.id=r.user_id AND u.is_guest=0
   WHERE r.id=upload_sessions.invite_id AND r.guest_user_id=upload_sessions.user_id)
  AND (SELECT COUNT(*) FROM reverse_invites WHERE guest_user_id=upload_sessions.user_id)=1`); err != nil {
		return err
	}
	var invalid int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM upload_sessions s
  LEFT JOIN users u ON u.id=s.quota_owner_id LEFT JOIN users uploader ON uploader.id=s.user_id
  WHERE s.quota_owner_id IS NULL OR u.id IS NULL OR u.is_guest<>0 OR uploader.id IS NULL`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return fmt.Errorf("cannot attribute %d upload storage reservations", invalid)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS upload_sessions_quota_owner ON upload_sessions(quota_owner_id);
  CREATE TRIGGER IF NOT EXISTS upload_quota_owner_required BEFORE INSERT ON upload_sessions
  WHEN NEW.quota_owner_id IS NULL BEGIN SELECT RAISE(ABORT,'quota owner required'); END;
  CREATE TRIGGER IF NOT EXISTS upload_quota_owner_immutable BEFORE UPDATE OF quota_owner_id ON upload_sessions
  WHEN NEW.quota_owner_id IS NOT OLD.quota_owner_id BEGIN SELECT RAISE(ABORT,'quota owner is immutable'); END;`); err != nil {
		return err
	}
	return tx.Commit()
}

// StorageUsageTx uses its caller's transaction; it never acquires another DB
// connection. This is also the admission snapshot, not a separate preflight.
func (s *Store) StorageUsageTx(tx *sql.Tx, id int64) (*StorageUsage, error) {
	out := &StorageUsage{}
	var quota, inheritance, defaultQuota sql.NullString
	if err := tx.QueryRow(`SELECT quota_bytes,quota_inherits_default,(SELECT value FROM settings WHERE key='default_storage_quota_bytes') FROM users WHERE id=?`, id).Scan(&quota, &inheritance, &defaultQuota); err != nil {
		return nil, err
	}
	var err error
	out.QuotaBytes, out.QuotaInheritsDefault, err = ResolveQuota(quota, inheritance, defaultQuota)
	if err != nil {
		return nil, err
	}
	var invalid int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM upload_sessions s LEFT JOIN users u ON u.id=s.quota_owner_id
  LEFT JOIN users uploader ON uploader.id=s.user_id WHERE u.id IS NULL OR u.is_guest<>0 OR uploader.id IS NULL`).Scan(&invalid); err != nil {
		return nil, err
	}
	if invalid != 0 {
		return nil, fmt.Errorf("unattributed storage reservation")
	}
	for _, q := range []struct {
		sql    string
		target *int64
	}{
		{`SELECT f.size FROM files f JOIN shares s ON s.id=f.share_id WHERE s.user_id=?`, &out.StoredBytes},
		{`SELECT f.size FROM upload_files f JOIN upload_sessions s ON s.id=f.session_id WHERE s.quota_owner_id=?`, &out.ReservedBytes},
	} {
		rows, err := tx.Query(q.sql, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var size int64
			if err := rows.Scan(&size); err != nil {
				rows.Close()
				return nil, err
			}
			if size < 0 || size > math.MaxInt64-*q.target {
				rows.Close()
				return nil, fmt.Errorf("invalid storage accounting")
			}
			*q.target += size
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if out.StoredBytes > math.MaxInt64-out.ReservedBytes {
		return nil, fmt.Errorf("storage accounting overflow")
	}
	used := out.StoredBytes + out.ReservedBytes
	if used < out.QuotaBytes {
		out.AvailableBytes = out.QuotaBytes - used
	}
	return out, nil
}

func (s *Store) UserStorageUsage(id int64) (*StorageUsage, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := s.StorageUsageTx(tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
