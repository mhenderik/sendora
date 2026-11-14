package store

import "fmt"

// migrateMFA adds multi-factor authentication storage. It is additive and
// idempotent; secrets live inside the (SQLCipher) database like everything else.
//
//	users.mfa_policy  'inherit' | 'required' | 'exempt' (per-user override)
//	users.mfa_epoch   bumped on every factor change/reset; folded into the
//	                  credential version so older access tokens stop working
func (s *Store) migrateMFA() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range []struct{ name, declaration string }{
		{"mfa_policy", "TEXT NOT NULL DEFAULT 'inherit'"},
		{"mfa_epoch", "INTEGER NOT NULL DEFAULT 0"},
	} {
		rows, err := tx.Query("PRAGMA table_info(users)")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var def any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == c.name {
				found = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !found {
			if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE users ADD COLUMN %s %s", c.name, c.declaration)); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`
CREATE TABLE IF NOT EXISTS user_totp (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  secret TEXT NOT NULL,
  confirmed INTEGER NOT NULL DEFAULT 0,
  last_counter INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE IF NOT EXISTS user_recovery_codes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at DATETIME,
  UNIQUE(user_id, code_hash));
CREATE TABLE IF NOT EXISTS user_webauthn (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  cred_id BLOB NOT NULL UNIQUE,
  public_key BLOB NOT NULL,
  alg INTEGER NOT NULL,
  sign_count INTEGER NOT NULL DEFAULT 0,
  name TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  last_used_at DATETIME);
CREATE INDEX IF NOT EXISTS user_webauthn_user ON user_webauthn(user_id);`); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetUserMFA removes every MFA factor and all sessions of one local user in
// an offline database (the application must be stopped). It is the recovery path
// for an administrator who is locked out by a lost authenticator.
func ResetUserMFA(path string, key []byte, username string) error {
	db, err := openCipherDB(path, key, false)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE username=? COLLATE NOCASE AND is_guest=0 ORDER BY username=? DESC LIMIT 1`, username, username).Scan(&id); err != nil {
		return fmt.Errorf("user not found")
	}
	for _, q := range []string{
		`DELETE FROM user_totp WHERE user_id=?`,
		`DELETE FROM user_recovery_codes WHERE user_id=?`,
		`DELETE FROM user_webauthn WHERE user_id=?`,
		`DELETE FROM refresh_tokens WHERE user_id=?`,
		`UPDATE users SET mfa_epoch=mfa_epoch+1,login_failures=0,login_locked_until=0 WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
