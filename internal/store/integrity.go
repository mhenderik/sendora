package store

import "fmt"

// Additive migration preserves existing rows; empty hashes are lazily baselined.
func (s *Store) migrateIntegrity() error {
	for _, column := range []struct{ table, name, declaration string }{
		{"upload_files", "sha256", "TEXT NOT NULL DEFAULT ''"},
		{"files", "sha256", "TEXT NOT NULL DEFAULT ''"},
		{"files", "integrity", "TEXT NOT NULL DEFAULT 'server_baseline'"},
		// enc=1: the stored file is chunked AES-256-GCM (internal/filecrypt).
		{"files", "enc", "INTEGER NOT NULL DEFAULT 0"},
		// enc_id: hex of the 16 byte file id (format v2) that binds the ciphertext
		// to this row; empty for plain files and format v1 files (0.1.2 - 0.1.4).
		{"files", "enc_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		rows, err := s.DB.Query("PRAGMA table_info(" + column.table + ")")
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
			if name == column.name {
				found = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !found {
			if _, err = s.DB.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", column.table, column.name, column.declaration)); err != nil {
				return err
			}
		}
	}
	return nil
}
