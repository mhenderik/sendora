package store

import (
	"regexp"
)

// Deliberately bounded locale syntax for stable template keys (e.g. pt-br, zh-hant).
var mailCodePattern = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8}){0,2}$`)

func ValidMailCode(code string) bool { return len(code) <= 24 && mailCodePattern.MatchString(code) }

type MailLanguage struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	BuiltIn bool   `json:"built_in"`
}

func (s *Store) migrateMailLanguages() error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS mail_languages(code TEXT PRIMARY KEY,name TEXT NOT NULL,built_in INTEGER NOT NULL DEFAULT 0)`); err != nil {
		return err
	}
	names := []string{"English", "Nederlands", "Deutsch", "Français", "Español"}
	for i, code := range MailLanguages {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO mail_languages(code,name,built_in) VALUES(?,?,1)`, code, names[i]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) ValidMailLanguage(code string) bool {
	if !ValidMailCode(code) {
		return false
	}
	var n int
	return s.DB.QueryRow(`SELECT COUNT(*) FROM mail_languages WHERE code=?`, code).Scan(&n) == nil && n == 1
}
func (s *Store) ListMailLanguages() ([]MailLanguage, error) {
	rows, err := s.DB.Query(`SELECT code,name,built_in FROM mail_languages ORDER BY built_in DESC,code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MailLanguage{}
	for rows.Next() {
		var l MailLanguage
		if err := rows.Scan(&l.Code, &l.Name, &l.BuiltIn); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
