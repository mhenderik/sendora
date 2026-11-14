package app

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Server configuration from the environment, typically a Kubernetes ConfigMap
// (settings) and Secret (passwords and client secrets).
//
// Every setting of Admin -> Settings has a variable SENDORA_<KEY IN CAPITALS>,
// for example SENDORA_SMTP_HOST or SENDORA_ALLOW_SELF_REGISTRATION. A variable
// with the suffix _FILE names a file holding the value instead (for a mounted
// Secret). Login providers use SENDORA_PROVIDER_<ID>_<FIELD>.
//
// Configured values are validated with the same rules as the admin screen,
// written to the database at every start and shown read-only in the screen:
// the configuration always wins. An invalid value stops the server with a
// message naming the variable. Removing a variable makes the setting editable
// again, keeping its last value.

const configMaxFileBytes = 64 << 10

// configExcluded are not plain settings: the signing secret is generated, logo
// and favicon are uploaded files.
var configExcluded = map[string]bool{"jwt_secret": true, "ui_logo": true, "ui_favicon": true}

// configAliases are friendlier extra names for some variables.
var configAliases = map[string][]string{"smtp_pass": {"SENDORA_SMTP_PASSWORD"}}

func settingEnvName(key string) string { return "SENDORA_" + strings.ToUpper(key) }

// readConfigValue returns the value of NAME or of the file named by NAME_FILE.
func readConfigValue(name string) (string, bool, error) {
	if p := os.Getenv(name + "_FILE"); p != "" {
		f, err := os.Open(p)
		if err != nil {
			return "", true, fmt.Errorf("%s_FILE: %v", name, err)
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, configMaxFileBytes+1))
		if err != nil || len(b) > configMaxFileBytes {
			return "", true, fmt.Errorf("%s_FILE: unreadable or larger than 64 KiB", name)
		}
		return strings.TrimRight(string(b), "\r\n"), true, nil
	}
	v, ok := os.LookupEnv(name)
	return v, ok, nil
}

func (s *Server) isManagedSetting(key string) bool { return s.managedSettings[key] }

// managedSettingList is the sorted list for the admin screen.
func (s *Server) managedSettingList() []string {
	out := make([]string, 0, len(s.managedSettings))
	for k := range s.managedSettings {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyConfig loads configured settings and providers into the database.
func (s *Server) applyConfig() error {
	s.managedSettings = map[string]bool{}
	var keys []string
	rows, err := s.St.DB.Query(`SELECT key FROM settings WHERE grp NOT IN ('secret','email_templates') ORDER BY key`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && !configExcluded[k] {
			keys = append(keys, k)
		}
	}
	rows.Close()
	for _, key := range keys {
		names := append([]string{settingEnvName(key)}, configAliases[key]...)
		for _, name := range names {
			v, ok, err := readConfigValue(name)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			if !secretKeys[key] {
				v = strings.TrimSpace(v)
				if lv := strings.ToLower(v); lv == "true" || lv == "false" {
					v = lv
				}
			}
			norm, err := s.normalizeSetting(key, v)
			if err != nil {
				return fmt.Errorf("%s: %v", name, err)
			}
			if _, err := s.St.DB.Exec(`UPDATE settings SET value=? WHERE key=?`, norm, key); err != nil {
				return err
			}
			s.managedSettings[key] = true
			break
		}
	}
	return s.applyProviderConfig()
}

var providerFields = []string{"CLIENT_SECRET", "ALLOW_REGISTRATION", "TRUST_EMAIL", "ISSUER_URL", "TENANT_ID", "CLIENT_ID", "ENABLED", "NAME", "TYPE", "ICON"}
var providerIDPattern = regexp.MustCompile(`^[A-Z0-9]{1,32}$`)

func parseConfigBool(name, v string, def bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return def, nil
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	}
	return false, fmt.Errorf("%s must be true or false", name)
}

// applyProviderConfig creates or updates login providers described by
// SENDORA_PROVIDER_<ID>_<FIELD> variables. <ID> (letters and digits) keeps the
// provider's callback URL stable: /api/auth/oidc/config-<id>/callback.
func (s *Server) applyProviderConfig() error {
	ids := map[string]bool{}
	for _, kv := range os.Environ() {
		name := strings.SplitN(kv, "=", 2)[0]
		rest, ok := strings.CutPrefix(name, "SENDORA_PROVIDER_")
		if !ok {
			continue
		}
		rest = strings.TrimSuffix(rest, "_FILE")
		matched := false
		for _, f := range providerFields {
			if id, ok := strings.CutSuffix(rest, "_"+f); ok && providerIDPattern.MatchString(id) {
				ids[id] = true
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: unknown provider variable (expected SENDORA_PROVIDER_<ID>_<%s>)", name, strings.Join(providerFields, "|"))
		}
	}
	configured := []string{}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)
	for _, id := range sorted {
		get := func(field string) (string, error) {
			v, _, err := readConfigValue("SENDORA_PROVIDER_" + id + "_" + field)
			if field != "CLIENT_SECRET" {
				v = strings.TrimSpace(v)
			}
			return v, err
		}
		prefix := "SENDORA_PROVIDER_" + id + "_"
		p := Provider{UUID: "config-" + strings.ToLower(id)}
		var err error
		if p.Name, err = get("NAME"); err != nil {
			return err
		}
		if p.Name == "" {
			p.Name = strings.ToLower(id)
		}
		vals := map[string]*string{"TYPE": &p.Type, "TENANT_ID": &p.TenantID, "CLIENT_ID": &p.ClientID, "CLIENT_SECRET": &p.ClientSecret, "ISSUER_URL": &p.IssuerURL, "ICON": &p.Icon}
		for f, dst := range vals {
			if *dst, err = get(f); err != nil {
				return err
			}
		}
		p.Type = strings.ToLower(p.Type)
		for f, b := range map[string]struct {
			dst *bool
			def bool
		}{"ENABLED": {&p.Enabled, true}, "ALLOW_REGISTRATION": {&p.AllowRegistration, false}, "TRUST_EMAIL": {&p.TrustEmail, false}} {
			v, err := get(f)
			if err != nil {
				return err
			}
			if *b.dst, err = parseConfigBool(prefix+f, v, b.def); err != nil {
				return err
			}
		}
		if msg := validProvider(&p); msg != "" {
			return fmt.Errorf("%s*: %s", prefix, msg)
		}
		var existingID int64
		var oldType, oldClient, oldIssuer, oldSecret string
		err = s.St.DB.QueryRow(`SELECT id,type,client_id,issuer_url,client_secret FROM auth_providers WHERE uuid=?`, p.UUID).Scan(&existingID, &oldType, &oldClient, &oldIssuer, &oldSecret)
		if err == nil {
			var linked int
			s.St.DB.QueryRow(`SELECT COUNT(*) FROM user_auth_providers WHERE provider_id=?`, existingID).Scan(&linked)
			if linked > 0 && (oldType != p.Type || oldClient != p.ClientID || oldIssuer != p.IssuerURL) {
				return fmt.Errorf("%s*: the type, client ID or issuer of a provider with linked accounts cannot change; use a new <ID>", prefix)
			}
			if p.ClientSecret == "" {
				p.ClientSecret = oldSecret
			}
			if _, err := s.St.DB.Exec(`UPDATE auth_providers SET name=?,type=?,enabled=?,client_id=?,client_secret=?,issuer_url=?,icon=?,trust_email=?,allow_registration=?,managed=1 WHERE id=?`,
				p.Name, p.Type, p.Enabled, p.ClientID, p.ClientSecret, p.IssuerURL, p.Icon, p.TrustEmail, p.AllowRegistration, existingID); err != nil {
				return err
			}
		} else {
			if _, err := s.St.DB.Exec(`INSERT INTO auth_providers(uuid,name,type,enabled,client_id,client_secret,issuer_url,icon,trust_email,allow_registration,managed) VALUES(?,?,?,?,?,?,?,?,?,?,1)`,
				p.UUID, p.Name, p.Type, p.Enabled, p.ClientID, p.ClientSecret, p.IssuerURL, p.Icon, p.TrustEmail, p.AllowRegistration); err != nil {
				return err
			}
		}
		configured = append(configured, p.UUID)
	}
	// Providers no longer in the configuration become editable again.
	q := `UPDATE auth_providers SET managed=0 WHERE managed=1`
	args := []any{}
	if len(configured) > 0 {
		q += ` AND uuid NOT IN (?` + strings.Repeat(",?", len(configured)-1) + `)`
		for _, u := range configured {
			args = append(args, u)
		}
	}
	_, err := s.St.DB.Exec(q, args...)
	return err
}
