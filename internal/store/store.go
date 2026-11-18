package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sync/atomic"

	sqlite3 "github.com/mattn/go-sqlite3"
	"github.com/mhenderik/sendora/internal/filecrypt"
)

type Store struct {
	DB          *sql.DB
	DataDir     string
	Path        string
	cipherKey   []byte
	fileKey     []byte
	settingsGen atomic.Uint64
}

// SettingsGen changes whenever SetSetting writes; caches use it to refresh.
func (s *Store) SettingsGen() uint64 { return s.settingsGen.Load() }

// The legacy driver name is confined to plaintext migration fixtures/tools.
func init() { sql.Register("sqlite", &sqlite3.SQLiteDriver{}) }

const schema = `
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  email TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  admin INTEGER NOT NULL DEFAULT 0,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  is_guest INTEGER NOT NULL DEFAULT 0,
  email_verified INTEGER NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- Guard new identities without rewriting or dropping pre-existing accounts.
CREATE TRIGGER IF NOT EXISTS users_email_insert BEFORE INSERT ON users
WHEN NEW.email<>'' AND EXISTS(SELECT 1 FROM users WHERE lower(trim(email))=lower(trim(NEW.email)))
BEGIN SELECT RAISE(ABORT,'email already in use'); END;
CREATE TRIGGER IF NOT EXISTS users_email_update BEFORE UPDATE OF email ON users
WHEN NEW.email<>OLD.email AND NEW.email<>'' AND EXISTS(SELECT 1 FROM users WHERE id<>NEW.id AND lower(trim(email))=lower(trim(NEW.email)))
BEGIN SELECT RAISE(ABORT,'email already in use'); END;
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  grp TEXT NOT NULL DEFAULT 'system'
);
CREATE TABLE IF NOT EXISTS refresh_tokens (
  hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS revoked_access_tokens (
 hash TEXT PRIMARY KEY,
 expires_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS password_resets (
  hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS email_verifications (
  user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  code TEXT NOT NULL,
  expires_at DATETIME NOT NULL
);
CREATE TABLE IF NOT EXISTS shares (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
  invite_id INTEGER,
  long_id TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  password_hash TEXT NOT NULL DEFAULT '',
  public INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  download_limit INTEGER,
  download_count INTEGER NOT NULL DEFAULT 0,
  expires_at DATETIME,
  expiry_warned INTEGER NOT NULL DEFAULT 0,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  share_id INTEGER NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  full_path TEXT NOT NULL,
  size INTEGER NOT NULL DEFAULT 0,
  type TEXT NOT NULL DEFAULT '',
  disk_path TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS downloads (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  share_id INTEGER NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
  file_id INTEGER,
  ip TEXT NOT NULL DEFAULT '',
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS upload_sessions (
  id TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  invite_id INTEGER,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS upload_files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  session_id TEXT NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
  idx INTEGER NOT NULL,
  name TEXT NOT NULL,
  full_path TEXT NOT NULL,
  size INTEGER NOT NULL,
  type TEXT NOT NULL DEFAULT '',
  received INTEGER NOT NULL DEFAULT 0,
  UNIQUE(session_id, idx)
);
CREATE TABLE IF NOT EXISTS themes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  category TEXT NOT NULL DEFAULT 'custom',
  bundled INTEGER NOT NULL DEFAULT 0,
  data TEXT NOT NULL DEFAULT '{}',
  active INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS auth_providers (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  uuid TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 0,
  client_id TEXT NOT NULL DEFAULT '',
  client_secret TEXT NOT NULL DEFAULT '',
  issuer_url TEXT NOT NULL DEFAULT '',
  icon TEXT NOT NULL DEFAULT '',
  trust_email INTEGER NOT NULL DEFAULT 0,
  allow_registration INTEGER NOT NULL DEFAULT 1,
  sort INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS user_auth_providers (
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider_id INTEGER NOT NULL REFERENCES auth_providers(id) ON DELETE CASCADE,
  provider_user_id TEXT NOT NULL,
  PRIMARY KEY(provider_id, provider_user_id)
);
CREATE TABLE IF NOT EXISTS reverse_invites (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  recipient_name TEXT NOT NULL DEFAULT '',
  recipient_email TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL DEFAULT '',
  guest_user_id INTEGER,
  expires_at DATETIME NOT NULL,
  used INTEGER NOT NULL DEFAULT 0,
  completed_at DATETIME,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS event_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at DATETIME NOT NULL,
  actor_id INTEGER,
  actor_name TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_event_log_created ON event_log(created_at);
`

// Open is the plaintext legacy/fixture opener. The application entrypoint must
// use OpenEncrypted; this helper never serves as a production encryption fallback.
func Open(dataDir string) (*Store, error) { return openStore(dataDir, nil) }

func openStore(dataDir string, key []byte) (*Store, error) {
	for _, d := range []string{"", "shares", "uploads", "backups", "branding", "backgrounds"} {
		p := filepath.Join(dataDir, d)
		if err := os.MkdirAll(p, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(p, 0o700); err != nil {
			return nil, err
		}
	}
	p, err := DBPath(dataDir)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(p+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	db, err := openCipherDB(p, key, false)
	if err != nil {
		return nil, err
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{DB: db, DataDir: dataDir, Path: p, cipherKey: append([]byte(nil), key...)}
	if len(key) == 32 {
		if s.fileKey, err = filecrypt.MasterKey(key); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := s.runMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// initSchema applies connection pragmas and the base (idempotent) schema.
func initSchema(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA temp_store=MEMORY;`); err != nil {
		return err
	}
	_, err := db.Exec(schema)
	return err
}

// runMigrations is the additive migration sequence shared by openStore and
// PrepareImported. Order matters: seed runs between the policy and brand steps.
func (s *Store) runMigrations() error {
	for _, step := range []func() error{s.MigratePolicy, s.migrateIntegrity, s.migrateReverseOpen, s.migrateShareAppend, s.migrateMFA, s.migrateIdle, s.migrateUITranslations, s.migrateUsernameCase, s.migrateIPBans, s.migrateReplyToFormat, s.migrateSessions, s.migratePendingRegistration, s.migrateManagedProviders} {
		if err := step(); err != nil {
			return err
		}
	}
	s.seed()
	if err := s.migrateMailLocalization(); err != nil {
		return err
	}
	return s.migrateMailLanguages()
}

// Close releases the database. The caller must have stopped all users of DB.
func (s *Store) Close() error { return s.DB.Close() }

// Default settings: key -> {group, value}. seed() uses INSERT OR IGNORE, so these
// only apply to NEW installations; existing ones keep their stored values.
var defaults = []struct{ Key, Grp, Val string }{
	{"application_name", "ui", "Sendora"},
	{"application_url", "system", ""},
	{"default_language", "ui", "en"},
	{"show_language_selector", "ui", "true"},
	{"allow_reverse_shares", "system", "true"},
	{"max_expiry_time", "system", "30"},
	{"default_expiry_time", "system", "7"},
	{"max_share_size", "system", "2048"}, // MB
	{"allow_self_registration", "system", "false"},
	{"registration_email_domains", "system", ""},
	{"require_email_verification", "system", "true"},
	{"clean_files_days", "system", "7"},
	{"expiry_warning_days", "system", "2"},
	{"auto_clean_files", "system", "true"},
	{"backups_enabled", "system", "true"},
	{"backup_keep", "system", "7"},
	{"require_share_password", "system", "false"},
	{"share_password_min_length", "system", "10"},
	{"share_password_require_upper", "system", "false"},
	{"share_password_require_lower", "system", "false"},
	{"share_password_require_digit", "system", "false"},
	{"share_password_require_symbol", "system", "false"},
	{"mfa_policy", "system", "admins"},
	{"idle_logout_minutes", "system", "60"},
	{"ip_ban_enabled", "system", "true"},
	{"ip_ban_allowlist", "system", ""},
	{"mfa_totp_enabled", "system", "true"},
	{"mfa_webauthn_enabled", "system", "true"},
	{"account_password_min_length", "system", "12"},
	{"account_password_require_upper", "system", "false"},
	{"account_password_require_lower", "system", "false"},
	{"account_password_require_digit", "system", "false"},
	{"account_password_require_symbol", "system", "false"},
	{"allow_preview", "system", "true"},
	{"local_login_enabled", "system", "true"},
	{"logging_enabled", "system", "false"},
	{"log_retention_hours", "system", "24"},
	{"smtp_host", "email", ""},
	{"smtp_port", "email", "587"},
	{"smtp_user", "email", ""},
	{"smtp_pass", "email", ""},
	{"smtp_from", "email", ""},
	{"smtp_reply_to", "email", ""},
	{"smtp_tls", "email", "starttls"},
	{"ui_background", "ui", ""},
	{"ui_logo", "ui", ""},
	{"ui_favicon", "ui", ""},
	{"ui_use_unsplash", "ui", "false"},
	{"ui_slideshow_seconds", "ui", "30"},
	{"ui_default_background", "ui", "true"},
	{"ui_builtin_background", "ui", "koenigssee"},
	{"tpl_share_created", "email_templates", "Hi {{.RecipientName}},\n\n{{.SenderName}} shared files with you: {{.URL}}\n\n{{.Message}}\n\nExpires: {{.Expires}}"},
	{"tpl_share_downloaded", "email_templates", "Your share \"{{.ShareName}}\" was downloaded."},
	{"tpl_share_expiring", "email_templates", "Your share \"{{.ShareName}}\" expires on {{.Expires}}: {{.URL}}"},
	{"tpl_password_reset", "email_templates", "Reset your password: {{.URL}}\n\nLink valid for 1 hour."},
	{"tpl_verify_email", "email_templates", "Your verification code is {{.Code}}"},
	{"tpl_reverse_invite", "email_templates", "Hi {{.RecipientName}},\n\n{{.SenderName}} invites you to upload files: {{.URL}}\n\n{{.Message}}"},
}

// Keep insertion order stable, with Light as the fresh-install default.
var builtinThemes = []struct{ Name, Data string }{
	{"Light", `{"primary":"#2563eb","bg":"#f8fafc","surface":"#ffffff","text":"#0f172a","muted":"#64748b","border":"#e2e8f0","danger":"#dc2626"}`},
	{"Dark", `{"primary":"#60a5fa","bg":"#0b1220","surface":"#111a2e","text":"#e5e7eb","muted":"#94a3b8","border":"#1f2a44","danger":"#f87171"}`},
	{"Forest", `{"primary":"#16a34a","bg":"#f0fdf4","surface":"#ffffff","text":"#052e16","muted":"#4d7c5f","border":"#bbf7d0","danger":"#dc2626"}`},
	{"Ocean", `{"primary":"#036985","bg":"#f0f9fc","surface":"#ffffff","text":"#123444","muted":"#465963","border":"#cbdde5","danger":"#b42332"}`},
	{"Midnight", `{"primary":"#93c5fd","bg":"#0b1020","surface":"#151e32","text":"#f1f5f9","muted":"#c1cedd","border":"#334155","danger":"#fda4af"}`},
	{"Violet", `{"primary":"#6d28d9","bg":"#f7f3ff","surface":"#ffffff","text":"#2e164a","muted":"#665477","border":"#e1d5ee","danger":"#b42332"}`},
	{"Sand", `{"primary":"#80521f","bg":"#faf6ef","surface":"#fffdf8","text":"#382b1e","muted":"#6b5c4b","border":"#e2d6c6","danger":"#b42332"}`},
	{"Rose", `{"primary":"#be185d","bg":"#fff1f4","surface":"#ffffff","text":"#4c1830","muted":"#775363","border":"#efd3df","danger":"#b42332"}`},
	{"Slate", `{"primary":"#67e8f9","bg":"#17212b","surface":"#22303d","text":"#f1f5f9","muted":"#cbd5e1","border":"#475569","danger":"#fda4af"}`},
	{"High Contrast", `{"primary":"#ffff00","bg":"#000000","surface":"#000000","text":"#ffffff","muted":"#ffffff","border":"#767676","danger":"#ffb3b3"}`},
	{"Nord", `{"primary":"#88c0d0","bg":"#2e3440","surface":"#3b4252","text":"#eceff4","muted":"#d8dee9","border":"#4c566a","danger":"#ff9aa2"}`},
	{"Solarized Light", `{"primary":"#0b6aa2","bg":"#fdf6e3","surface":"#fffaf0","text":"#073642","muted":"#586e75","border":"#e4dcc4","danger":"#b42318"}`},
	{"Solarized Dark", `{"primary":"#4aa8e0","bg":"#002b36","surface":"#073642","text":"#eee8d5","muted":"#b4c0bf","border":"#2a5560","danger":"#ff8a80"}`},
	{"Mint", `{"primary":"#0f766e","bg":"#f0fdfa","surface":"#ffffff","text":"#042f2e","muted":"#46635f","border":"#c3ece5","danger":"#b42332"}`},
	{"Sunset", `{"primary":"#c2410c","bg":"#fff7ed","surface":"#ffffff","text":"#431407","muted":"#7a5340","border":"#f3d9c3","danger":"#b42332"}`},
	{"Lavender", `{"primary":"#5b4bc4","bg":"#f5f3ff","surface":"#ffffff","text":"#1e1b4b","muted":"#5d5a85","border":"#ddd8f5","danger":"#b42332"}`},
	{"Graphite", `{"primary":"#fbbf24","bg":"#18181b","surface":"#27272a","text":"#fafafa","muted":"#d4d4d8","border":"#52525b","danger":"#fca5a5"}`},
	{"Coffee", `{"primary":"#e0b084","bg":"#1f1612","surface":"#2b201a","text":"#f5ebe0","muted":"#d6c3b0","border":"#4d3a2e","danger":"#ffab91"}`},
	{"Citrus", `{"primary":"#4d7c0f","bg":"#f7fee7","surface":"#ffffff","text":"#1a2e05","muted":"#4f6330","border":"#d9ecb0","danger":"#b42332"}`},
	{"Aqua Night", `{"primary":"#5eead4","bg":"#042f2e","surface":"#0b3f3d","text":"#f0fdfa","muted":"#b5d9d4","border":"#1f5f5a","danger":"#fda4af"}`},
}

func (s *Store) seed() {
	// Carry an earlier "built-in background off" choice over to the new setting,
	// before the default row for it can be inserted.
	s.DB.Exec(`INSERT OR IGNORE INTO settings(key,value,grp)
	  SELECT 'ui_builtin_background','none','ui' FROM settings WHERE key='ui_default_background' AND value='false'`)
	for _, d := range defaults {
		s.DB.Exec(`INSERT OR IGNORE INTO settings(key,value,grp) VALUES(?,?,?)`, d.Key, d.Val, d.Grp)
	}
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM themes`).Scan(&n)
	for _, theme := range builtinThemes {
		active := 0
		if n == 0 && theme.Name == "Light" {
			active = 1
		}
		// Add new bundles on upgrades without replacing custom data or selection.
		s.DB.Exec(`INSERT OR IGNORE INTO themes(name,category,bundled,data,active) VALUES(?,?,?,?,?)`, theme.Name, "bundled", 1, theme.Data, active)
	}
}

func (s *Store) Setting(key string) string {
	var v string
	s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

func (s *Store) SettingBool(key string) bool {
	return strings.EqualFold(s.Setting(key), "true")
}

func (s *Store) SettingInt(key string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s.Setting(key), "%d", &n); err != nil {
		return def
	}
	return n
}

func (s *Store) SetSetting(key, val string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key,value,grp) VALUES(?,?, 'system')
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, val)
	s.settingsGen.Add(1)
	return err
}
