package store

import "fmt"

// migrateReverseOpen adds the columns used by reusable (open) reverse-share
// links. It is additive and idempotent; existing invites keep their meaning
// (reusable=0, no parent).
func (s *Store) migrateReverseOpen() error {
	have := map[string]bool{}
	rows, err := s.DB.Query("PRAGMA table_info(reverse_invites)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err = rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range []struct{ name, decl string }{
		{"reusable", "INTEGER NOT NULL DEFAULT 0"},
		{"max_uploads", "INTEGER NOT NULL DEFAULT 0"},
		{"parent_id", "INTEGER"},
	} {
		if have[c.name] {
			continue
		}
		if _, err = s.DB.Exec(fmt.Sprintf("ALTER TABLE reverse_invites ADD COLUMN %s %s", c.name, c.decl)); err != nil {
			return err
		}
	}
	_, err = s.DB.Exec(`CREATE INDEX IF NOT EXISTS idx_reverse_invites_parent ON reverse_invites(parent_id)`)
	return err
}
