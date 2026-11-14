package store

// migrateIPBans creates the table that keeps address bans across restarts.
func (s *Store) migrateIPBans() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS ip_bans (
		ip TEXT PRIMARY KEY,
		banned_until INTEGER NOT NULL,
		strikes INTEGER NOT NULL,
		last_strike INTEGER NOT NULL)`)
	return err
}
