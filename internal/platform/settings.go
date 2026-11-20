package platform

import (
	"fmt"
	"io"
	"net/mail"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Platform settings: stored in platform.db, editable in the console, and set
// (read-only in the console) by SENDORA_<KEY IN CAPITALS> or its _FILE form,
// like the tenant settings. The base domain is environment-only
// (SENDORA_BASE_DOMAIN): the console itself is reached through it.

type settingDef struct{ Key, Grp, Val string }

var settingDefaults = []settingDef{
	{"tenant_idle_minutes", "system", "15"},
	{"max_open_tenants", "system", "50"},
	{"tenant_maintenance_hours", "system", "6"},
	{"default_max_users", "system", "0"},
	{"default_max_storage_bytes", "system", "0"},
	{"audit_retention_days", "system", "365"},
	{"platform_smtp_host", "email", ""},
	{"platform_smtp_port", "email", "587"},
	{"platform_smtp_user", "email", ""},
	{"platform_smtp_pass", "email", ""},
	{"platform_smtp_from", "email", ""},
	{"platform_smtp_tls", "email", "starttls"},
}

// SecretSettings are never returned to the console.
var SecretSettings = map[string]bool{"platform_smtp_pass": true}

var settingAliases = map[string][]string{"platform_smtp_pass": {"SENDORA_PLATFORM_SMTP_PASSWORD"}}

// SettingEnvNames lists every environment variable the platform settings read
// (for the unknown-variable warning).
func SettingEnvNames() []string {
	var out []string
	for _, d := range settingDefaults {
		for _, n := range append([]string{envName(d.Key)}, settingAliases[d.Key]...) {
			out = append(out, n, n+"_FILE")
		}
	}
	return out
}

func envName(key string) string { return "SENDORA_" + strings.ToUpper(key) }

// NormalizeSetting validates one platform setting value.
func NormalizeSetting(key, v string) (string, error) {
	if !SecretSettings[key] {
		v = strings.TrimSpace(v)
	}
	intRange := func(lo, hi int64) (string, error) {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < lo || n > hi {
			return "", fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		return strconv.FormatInt(n, 10), nil
	}
	switch key {
	case "tenant_idle_minutes":
		return intRange(1, 1440)
	case "max_open_tenants":
		return intRange(1, 10000)
	case "tenant_maintenance_hours":
		return intRange(1, 168)
	case "default_max_users":
		return intRange(0, 1000000)
	case "default_max_storage_bytes":
		return intRange(0, 1<<60)
	case "audit_retention_days":
		return intRange(90, 36500)
	case "platform_smtp_port":
		return intRange(1, 65535)
	case "platform_smtp_tls":
		if v != "none" && v != "tls" && v != "starttls" {
			return "", fmt.Errorf("must be none, tls or starttls")
		}
		return v, nil
	case "platform_smtp_from":
		if v == "" {
			return v, nil
		}
		if _, err := mail.ParseAddress(v); err != nil || strings.ContainsAny(v, "\r\n") {
			return "", fmt.Errorf("invalid sender address")
		}
		return v, nil
	case "platform_smtp_host", "platform_smtp_user":
		if strings.ContainsAny(v, "\r\n\x00 ") || len(v) > 255 {
			return "", fmt.Errorf("invalid value")
		}
		return v, nil
	case "platform_smtp_pass":
		if strings.ContainsAny(v, "\r\n\x00") || len(v) > 1024 {
			return "", fmt.Errorf("invalid value")
		}
		return v, nil
	}
	return "", fmt.Errorf("unknown setting")
}

// Setting returns a platform setting.
func (p *Platform) Setting(key string) string {
	var v string
	p.St.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return v
}

// SettingInt returns a platform setting as a number (def when unset/invalid).
func (p *Platform) SettingInt(key string, def int64) int64 {
	n, err := strconv.ParseInt(p.Setting(key), 10, 64)
	if err != nil {
		return def
	}
	return n
}

// Settings returns all editable settings; secrets only say whether they are set.
func (p *Platform) Settings() map[string]any {
	out := map[string]any{}
	for _, d := range settingDefaults {
		v := p.Setting(d.Key)
		if SecretSettings[d.Key] {
			out[d.Key+"_set"] = v != ""
			continue
		}
		out[d.Key] = v
	}
	return out
}

// PutSettings validates and stores settings; managed (environment) ones are refused.
func (p *Platform) PutSettings(in map[string]string, managed map[string]bool) ([]string, error) {
	known := map[string]bool{}
	for _, d := range settingDefaults {
		known[d.Key] = true
	}
	norm := map[string]string{}
	for k, v := range in {
		if !known[k] {
			return nil, fmt.Errorf("%s: unknown setting", k)
		}
		if managed[k] {
			return nil, fmt.Errorf("%s is set by the server configuration", k)
		}
		if SecretSettings[k] && v == "" {
			continue // empty = keep
		}
		n, err := NormalizeSetting(k, v)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", k, err)
		}
		norm[k] = n
	}
	tx, err := p.St.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var changed []string
	for k, v := range norm {
		if _, err := tx.Exec(`UPDATE settings SET value=? WHERE key=?`, v, k); err != nil {
			return nil, err
		}
		changed = append(changed, k)
	}
	sort.Strings(changed)
	return changed, tx.Commit()
}

// ApplyEnv writes settings given by the environment and returns the set of
// managed keys. An invalid value is an error naming the variable.
func (p *Platform) ApplyEnv() (map[string]bool, error) {
	managed := map[string]bool{}
	for _, d := range settingDefaults {
		for _, name := range append([]string{envName(d.Key)}, settingAliases[d.Key]...) {
			v, ok, err := readEnv(name)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			n, err := NormalizeSetting(d.Key, v)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", name, err)
			}
			if _, err := p.St.DB.Exec(`UPDATE settings SET value=? WHERE key=?`, n, d.Key); err != nil {
				return nil, err
			}
			managed[d.Key] = true
			break
		}
	}
	return managed, nil
}

func readEnv(name string) (string, bool, error) {
	if f := os.Getenv(name + "_FILE"); f != "" {
		fh, err := os.Open(f)
		if err != nil {
			return "", true, fmt.Errorf("%s_FILE: %v", name, err)
		}
		defer fh.Close()
		b, err := io.ReadAll(io.LimitReader(fh, 64<<10+1))
		if err != nil || len(b) > 64<<10 {
			return "", true, fmt.Errorf("%s_FILE: unreadable or larger than 64 KiB", name)
		}
		return strings.TrimRight(string(b), "\r\n"), true, nil
	}
	v, ok := os.LookupEnv(name)
	return v, ok, nil
}

// MailDefault maps a tenant SMTP setting (smtp_host, ...) to the platform's
// default SMTP value, used by tenants that have no SMTP server of their own.
func (p *Platform) MailDefault(key string) string {
	return p.Setting("platform_" + key)
}
