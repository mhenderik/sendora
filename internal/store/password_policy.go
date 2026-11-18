package store

import "time"

// Password rotation and history settings.
const (
	SettingPasswordMaxAgeDays = "account_password_max_age_days"
	SettingPasswordHistory    = "account_password_history"
	// PasswordMaxAgeDefault is the maximum password age of a NEW installation.
	PasswordMaxAgeDefault = "365"
)

// migratePasswordPolicy is additive and idempotent:
//   - users.password_changed_at: when the local password was last set. Rows that
//     exist when the column is added get the migration time, so nobody's password
//     counts as old at the upgrade;
//   - users.password_rotation_exempt: per-account exemption from the maximum age;
//   - users.password_expiry_warned_at: when the "password expires soon" mail was
//     sent for the current password (cleared whenever the password changes);
//   - password_history: bcrypt hashes of earlier passwords (newest N per account);
//   - account_password_max_age_days: an installation that already has accounts
//     gets 0 (off), so an upgrade never forces anyone to change their password.
//     It runs before seed(), whose defaults list then gives a new installation
//     the default (365) through INSERT OR IGNORE.
func (s *Store) migratePasswordPolicy() error {
	hadChangedAt, err := s.hasColumn("users", "password_changed_at")
	if err != nil {
		return err
	}
	for _, c := range []struct{ name, def string }{
		{"password_changed_at", "DATETIME"},
		{"password_rotation_exempt", "INTEGER NOT NULL DEFAULT 0"},
		{"password_expiry_warned_at", "DATETIME"},
	} {
		if err := s.addColumn("users", c.name, c.def); err != nil {
			return err
		}
	}
	if !hadChangedAt {
		if _, err := s.DB.Exec(`UPDATE users SET password_changed_at=? WHERE password_changed_at IS NULL`, time.Now().UTC()); err != nil {
			return err
		}
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS password_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		hash TEXT NOT NULL,
		created_at DATETIME NOT NULL)`); err != nil {
		return err
	}
	if _, err := s.DB.Exec(`CREATE INDEX IF NOT EXISTS password_history_user ON password_history(user_id, id)`); err != nil {
		return err
	}
	_, err = s.DB.Exec(`INSERT OR IGNORE INTO settings(key,value,grp) SELECT ?,'0','system' WHERE EXISTS(SELECT 1 FROM users)`, SettingPasswordMaxAgeDays)
	return err
}

// hasColumn reports whether table has the column.
func (s *Store) hasColumn(table, column string) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n)
	return n > 0, err
}
