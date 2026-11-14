package store

// migratePendingRegistration marks accounts that were created by public
// registration and have not confirmed their email yet. Only these are removed by
// the 48-hour cleanup: an established account whose new address is not verified
// yet must never be deleted. Existing accounts start unmarked.
func (s *Store) migratePendingRegistration() error {
	rows, err := s.DB.Query("PRAGMA table_info(users)")
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
		found = found || name == "pending_registration"
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	_, err = s.DB.Exec(`ALTER TABLE users ADD COLUMN pending_registration INTEGER NOT NULL DEFAULT 0`)
	return err
}
