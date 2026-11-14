package store

// migrateManagedProviders marks login providers that come from the server
// configuration (environment / Kubernetes ConfigMap) so the admin screen can show
// them read-only.
func (s *Store) migrateManagedProviders() error {
	rows, err := s.DB.Query("PRAGMA table_info(auth_providers)")
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
		found = found || name == "managed"
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = s.DB.Exec(`ALTER TABLE auth_providers ADD COLUMN managed INTEGER NOT NULL DEFAULT 0`)
	return err
}
