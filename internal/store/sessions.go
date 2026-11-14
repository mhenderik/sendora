package store

// migrateSessions adds the original sign-in time to refresh tokens (so rotation
// cannot extend a session forever) and a table of spent refresh tokens (so reuse
// of a rotated token can be detected and the session family revoked).
func (s *Store) migrateSessions() error {
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
		found = found || name == "session_started"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !found {
		if _, err := s.DB.Exec(`ALTER TABLE refresh_tokens ADD COLUMN session_started DATETIME`); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(`CREATE TABLE IF NOT EXISTS refresh_spent (
		hash TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		spent_at DATETIME NOT NULL,
		expires_at DATETIME NOT NULL)`)
	return err
}
