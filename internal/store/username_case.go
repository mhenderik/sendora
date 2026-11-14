package store

import "log"

// migrateUsernameCase makes usernames unique regardless of ASCII letter case
// ("Admin" and "admin" are the same account). The display case stays as typed.
// If an existing database already holds names that differ only by case, the
// unique index cannot be created: startup continues, a warning gives the number
// of such names (find them with GROUP BY username COLLATE NOCASE), and logins
// prefer the exact-case match.
func (s *Store) migrateUsernameCase() error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM users GROUP BY username COLLATE NOCASE HAVING COUNT(*)>1)`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		log.Printf("WARNING: %d username(s) exist that differ only by letter case; rename them so case-insensitive uniqueness can be enforced", n)
		return nil
	}
	_, err := s.DB.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS users_username_nocase ON users(username COLLATE NOCASE)`)
	return err
}
