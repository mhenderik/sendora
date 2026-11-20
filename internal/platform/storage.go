package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Per-tenant object storage configuration: the integration point for the
// pluggable storage backends (internal/storage, branch feature/storage-backends).
//
// This package only stores the choice; it does not interpret Config. Modes:
//
//	platform  the platform's default backend (configured for the whole
//	          process), with Prefix (default "tenants/<id>/") prepended to every
//	          object key, so tenants never share a key space.
//	own       the tenant's own bucket/container; Config is the backend
//	          configuration as JSON in the storage package's format (for example
//	          {"type":"s3","bucket":"...","region":"...","access_key_id":"...",
//	          "secret_access_key":"..."}).
//
// Wiring after both branches are merged (see docs/MULTI-TENANT.md, "Storage
// backends"): app.TenantHost passes StorageConfig to every tenant *Server it
// opens (TenantOptions.Storage); the server builds its storage backend from it
// instead of from the process-wide setting, and local disk (the tenant's
// directory) stays the default when the storage package is absent.

// StorageConfig is a tenant's storage choice.
type StorageConfig struct {
	Mode      string          `json:"mode"`
	Prefix    string          `json:"prefix"`
	Config    json.RawMessage `json:"config"`
	UpdatedAt string          `json:"updated_at"`
}

// StorageSecretMask replaces secret values in the console.
const StorageSecretMask = "********"

var secretField = regexp.MustCompile(`(?i)(secret|password|key|token|credential|sas|connection)`)

var prefixPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{0,200}$`)

// Storage returns a tenant's storage configuration (secrets included: only
// for wiring the backend, never for the console).
func (p *Platform) Storage(tenantID string) (*StorageConfig, error) {
	c := &StorageConfig{}
	var cfg string
	err := p.St.DB.QueryRow(`SELECT mode,prefix,config,updated_at FROM tenant_storage WHERE tenant_id=?`, tenantID).Scan(&c.Mode, &c.Prefix, &cfg, &c.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	c.Config = json.RawMessage(cfg)
	return c, nil
}

// MaskedStorage is Storage with secret-looking string values replaced.
func (p *Platform) MaskedStorage(tenantID string) (*StorageConfig, error) {
	c, err := p.Storage(tenantID)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if json.Unmarshal(c.Config, &m) == nil {
		for k, v := range m {
			if s, ok := v.(string); ok && s != "" && secretField.MatchString(k) {
				m[k] = StorageSecretMask
			}
		}
		c.Config, _ = json.Marshal(m)
	}
	return c, nil
}

// SetStorage stores a tenant's storage choice. Masked secret values keep the
// stored value, so the console can save without seeing secrets.
func (p *Platform) SetStorage(tenantID, mode, prefix string, config json.RawMessage) error {
	if mode != "platform" && mode != "own" {
		return fmt.Errorf("mode must be platform or own")
	}
	prefix = strings.TrimSpace(prefix)
	if !prefixPattern.MatchString(prefix) || strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("invalid prefix")
	}
	if prefix == "" {
		prefix = "tenants/" + tenantID + "/"
	}
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	if len(config) > 16<<10 {
		return fmt.Errorf("configuration too large")
	}
	var m map[string]any
	if err := json.Unmarshal(config, &m); err != nil {
		return errors.New("configuration must be a JSON object")
	}
	if old, err := p.Storage(tenantID); err == nil {
		var prev map[string]any
		if json.Unmarshal(old.Config, &prev) == nil {
			for k, v := range m {
				if v == StorageSecretMask {
					if pv, ok := prev[k]; ok {
						m[k] = pv
					} else {
						delete(m, k)
					}
				}
			}
		}
	}
	b, _ := json.Marshal(m)
	res, err := p.St.DB.Exec(`INSERT INTO tenant_storage(tenant_id,mode,prefix,config,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(tenant_id) DO UPDATE SET mode=excluded.mode,prefix=excluded.prefix,config=excluded.config,updated_at=excluded.updated_at`, tenantID, mode, prefix, string(b), now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
