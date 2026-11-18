package store

import "fmt"

// migrateAccounts adds the account lifecycle columns used by deactivation and
// the access-review report, and the per-session details of refresh tokens.
// Every step is additive: existing rows keep working with NULL / empty values
// ("unknown" in the report).
func (s *Store) migrateAccounts() error {
	userCols := []struct{ name, def string }{
		{"disabled_at", "DATETIME"},
		{"disabled_by", "TEXT NOT NULL DEFAULT ''"},
		{"disabled_reason", "TEXT NOT NULL DEFAULT ''"},
		{"created_by", "TEXT NOT NULL DEFAULT ''"},
		{"last_login_at", "DATETIME"},
		{"role_changed_at", "DATETIME"},
		{"role_changed_by", "TEXT NOT NULL DEFAULT ''"},
		{"email_changed_at", "DATETIME"},
		{"email_changed_by", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, c := range userCols {
		if err := s.addColumn("users", c.name, c.def); err != nil {
			return err
		}
	}
	// A session (refresh-token family) keeps one id across rotations; the
	// access tokens of that session carry it, so revoking the session also ends
	// them before they expire.
	tokenCols := []struct{ name, def string }{
		{"session_id", "TEXT"},
		{"user_agent", "TEXT NOT NULL DEFAULT ''"},
		{"ip", "TEXT NOT NULL DEFAULT ''"},
		{"auth_method", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, c := range tokenCols {
		if err := s.addColumn("refresh_tokens", c.name, c.def); err != nil {
			return err
		}
	}
	if _, err := s.DB.Exec(`UPDATE refresh_tokens SET session_id=lower(hex(randomblob(16))) WHERE session_id IS NULL OR session_id=''`); err != nil {
		return err
	}
	_, err := s.DB.Exec(`CREATE INDEX IF NOT EXISTS refresh_tokens_session ON refresh_tokens(session_id)`)
	return err
}

// addColumn adds a column unless the table already has it.
func (s *Store) addColumn(table, column, def string) error {
	rows, err := s.DB.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		found = found || name == column
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = s.DB.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, def))
	return err
}
