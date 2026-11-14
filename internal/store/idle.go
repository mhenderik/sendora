package store

import "time"

// migrateIdle adds refresh_tokens.issued_at, the time a refresh token was
// created or rotated. The server uses it to end sessions that were abandoned
// longer than the configured inactivity window. It is additive and idempotent;
// existing rows are backfilled with "now" so an upgrade never signs anyone out.
func (s *Store) migrateIdle() error {
	rows, err := s.DB.Query("PRAGMA table_info(refresh_tokens)")
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
		if name == "issued_at" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = s.DB.Exec(`ALTER TABLE refresh_tokens ADD COLUMN issued_at DATETIME`); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(`UPDATE refresh_tokens SET issued_at=? WHERE issued_at IS NULL`, time.Now())
	return err
}
