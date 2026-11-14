package store

// migrateShareAppend adds upload_sessions.share_id: an upload session with a
// share_id appends its files to that existing share instead of creating a new
// one. It is additive and idempotent; existing sessions keep share_id NULL.
// Deleting a share cascades to its pending append sessions.
func (s *Store) migrateShareAppend() error {
	rows, err := s.DB.Query("PRAGMA table_info(upload_sessions)")
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
		if name == "share_id" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err = s.DB.Exec(`ALTER TABLE upload_sessions ADD COLUMN share_id INTEGER REFERENCES shares(id) ON DELETE CASCADE`); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_upload_sessions_share ON upload_sessions(share_id)`)
	return err
}
