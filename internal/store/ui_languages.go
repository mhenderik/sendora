package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// UI languages are bundled in the frontend (en, nl, de, fr, es). Administrators can
// override single texts of those languages and add further languages at runtime
// through JSON; the stored dictionaries live here, keyed by language code.

// BuiltinUILanguage is a language whose dictionary ships with the frontend.
type BuiltinUILanguage struct{ Code, Name string }

var BuiltinUILanguages = []BuiltinUILanguage{
	{"en", "English"}, {"nl", "Nederlands"}, {"de", "Deutsch"}, {"fr", "Français"}, {"es", "Español"},
}

// MaxUILanguages bounds the stored dictionaries (custom languages plus overrides).
const MaxUILanguages = 32

var ErrTooManyUILanguages = errors.New("too many languages")

func BuiltinUILanguageName(code string) (string, bool) {
	for _, l := range BuiltinUILanguages {
		if l.Code == code {
			return l.Name, true
		}
	}
	return "", false
}

// ValidUICodeSyntax is the pure syntax check for a UI language code; it is the
// same bounded locale syntax as the email language codes (e.g. pt-br, zh-hant).
func ValidUICodeSyntax(code string) bool { return ValidMailCode(code) }

func (s *Store) migrateUITranslations() error {
	_, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS ui_translations(
  code TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  data TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	return err
}

// StoredUILanguage is one stored dictionary row.
type StoredUILanguage struct {
	Code         string
	Name         string
	Translations map[string]string
	Raw          string // exact stored JSON, used for ETags
}

// ListUITranslations returns all stored dictionaries ordered by code. The rows
// are fully read and closed before it returns (single pooled connection).
func (s *Store) ListUITranslations() ([]StoredUILanguage, error) {
	rows, err := s.DB.Query(`SELECT code,name,data FROM ui_translations ORDER BY code`)
	if err != nil {
		return nil, err
	}
	var out []StoredUILanguage
	for rows.Next() {
		var l StoredUILanguage
		if err := rows.Scan(&l.Code, &l.Name, &l.Raw); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, l)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Translations = map[string]string{}
		// A corrupt row serves as an empty dictionary rather than failing the list.
		_ = json.Unmarshal([]byte(out[i].Raw), &out[i].Translations)
		if out[i].Translations == nil {
			out[i].Translations = map[string]string{}
		}
	}
	return out, nil
}

// UITranslation returns one stored dictionary; ok is false when none is stored.
func (s *Store) UITranslation(code string) (StoredUILanguage, bool, error) {
	var l StoredUILanguage
	err := s.DB.QueryRow(`SELECT code,name,data FROM ui_translations WHERE code=?`, code).Scan(&l.Code, &l.Name, &l.Raw)
	if errors.Is(err, sql.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, err
	}
	l.Translations = map[string]string{}
	_ = json.Unmarshal([]byte(l.Raw), &l.Translations)
	if l.Translations == nil {
		l.Translations = map[string]string{}
	}
	return l, true, nil
}

// PutUITranslation replaces the stored dictionary of a language atomically.
// A new language beyond MaxUILanguages is refused.
func (s *Store) PutUITranslation(code, name string, translations map[string]string) error {
	data, err := json.Marshal(translations)
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM ui_translations WHERE code=?`, code).Scan(&exists); err != nil {
		return err
	}
	if _, builtin := BuiltinUILanguageName(code); exists == 0 && !builtin {
		// Overrides of built-in languages do not count towards the limit.
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM ui_translations WHERE code NOT IN ('en','nl','de','fr','es')`).Scan(&n); err != nil {
			return err
		}
		if n >= MaxUILanguages {
			return ErrTooManyUILanguages
		}
	}
	if _, err := tx.Exec(`INSERT INTO ui_translations(code,name,data,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP)
  ON CONFLICT(code) DO UPDATE SET name=excluded.name,data=excluded.data,updated_at=CURRENT_TIMESTAMP`, code, name, string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUITranslation removes a stored dictionary. Users whose menu language was
// the removed custom language fall back to English. Reports whether a row existed.
func (s *Store) DeleteUITranslation(code string) (bool, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM ui_translations WHERE code=?`, code)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if _, builtin := BuiltinUILanguageName(code); !builtin && n > 0 {
		if _, err := tx.Exec(`UPDATE users SET ui_language='en' WHERE ui_language=?`, code); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ValidUILanguage accepts the built-in languages and any stored custom language.
// It queries the database: never call it while a transaction or rows hold the
// single pooled connection.
func (s *Store) ValidUILanguage(code string) bool {
	if _, ok := BuiltinUILanguageName(code); ok {
		return true
	}
	if !ValidUICodeSyntax(code) {
		return false
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM ui_translations WHERE code=?`, code).Scan(&n); err != nil {
		return false
	}
	return n == 1
}
