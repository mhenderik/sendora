package store

import (
	"log"
	"strconv"
)

// AccountPasswordMinFloor is the lowest allowed account password length.
const AccountPasswordMinFloor = 8

// accountSecurityDefaults are the settings for security notifications, the
// password screening, administrator step-up, the administrator network
// restriction and the privacy/terms links. INSERT OR IGNORE: existing values stay.
var accountSecurityDefaults = []struct{ Key, Val string }{
	{"notify_new_signin", "true"},
	{"password_breach_check_online", "false"},
	{"admin_stepup_minutes", "5"},
	{"admin_idle_logout_minutes", "15"},
	{"admin_ip_allowlist", ""},
	{"privacy_url", ""},
	{"terms_url", ""},
	{"terms_acceptance_required", "false"},
	{"terms_version", ""},
}

// migrateAccountSecurity is additive and idempotent:
//   - users.terms_accepted_version / terms_accepted_at: the terms version a user accepted;
//   - users.erased_at: set when an account was erased (pseudonymised) on request;
//   - user_signin_seen: hashed (browser family, address class) fingerprints of
//     recent sign-ins, used for the "new sign-in" notification. No raw addresses;
//   - the new settings;
//   - account_password_min_length below 8 is raised to 8 (logged once, when it happens).
func (s *Store) migrateAccountSecurity() error {
	rows, err := s.DB.Query("PRAGMA table_info(users)")
	if err != nil {
		return err
	}
	have := map[string]bool{}
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
	for _, c := range []struct{ name, ddl string }{
		{"terms_accepted_version", `ALTER TABLE users ADD COLUMN terms_accepted_version TEXT NOT NULL DEFAULT ''`},
		{"terms_accepted_at", `ALTER TABLE users ADD COLUMN terms_accepted_at DATETIME`},
		{"erased_at", `ALTER TABLE users ADD COLUMN erased_at DATETIME`},
	} {
		if !have[c.name] {
			if _, err := s.DB.Exec(c.ddl); err != nil {
				return err
			}
		}
	}
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS user_signin_seen (
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		fingerprint TEXT NOT NULL,
		last_seen DATETIME NOT NULL,
		PRIMARY KEY(user_id, fingerprint))`); err != nil {
		return err
	}
	for _, d := range accountSecurityDefaults {
		if _, err := s.DB.Exec(`INSERT OR IGNORE INTO settings(key,value,grp) VALUES(?,?,'system')`, d.Key, d.Val); err != nil {
			return err
		}
	}
	res, err := s.DB.Exec(`UPDATE settings SET value=? WHERE key='account_password_min_length' AND CAST(value AS INTEGER) < ?`,
		strconv.Itoa(AccountPasswordMinFloor), AccountPasswordMinFloor)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.Printf("account_password_min_length was below %d and has been raised to %d", AccountPasswordMinFloor, AccountPasswordMinFloor)
	}
	return nil
}
