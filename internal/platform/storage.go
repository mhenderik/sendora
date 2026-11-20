package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/mhenderik/sendora/internal/storage"
)

// Object storage in multi-tenant mode (docs/MULTI-TENANT.md, "Storage").
//
// The platform has a default backend (platform settings platform_storage_*,
// or SENDORA_PLATFORM_STORAGE_*, the same fields as a single-tenant
// installation's storage_* settings). Each tenant uses one of:
//
//	platform  the platform default with the tenant's own prefix
//	          tenants/<tenant id>/ appended to the platform prefix
//	          (storage.Config.WithPrefix). The prefix is fixed: it is what keeps
//	          tenants apart in a shared bucket, and what deleting the tenant
//	          removes. While the platform default is the local data directory,
//	          the tenant's files stay in the tenant's directory.
//	own       a bucket or container of the tenant, configured by a platform
//	          administrator in the console (storage_* fields in Config).
//
// TenantManaged additionally lets the tenant's administrators configure a
// bucket of their own in the tenant's Admin -> Storage. Those configurations
// are marked storage.Config.TenantManaged: explicit credentials only and the
// SSRF guard on every connection. Without it the tenant sees its storage
// read-only.
//
// Files keep the location they were written to (storage_locations in the
// tenant's database), so changing any of this only affects new files; the
// tenant's migration job moves earlier files.

// Storage modes.
const (
	StorageModePlatform = "platform"
	StorageModeOwn      = "own"
)

// StorageConfig is a tenant's storage choice. Config holds the storage_*
// settings of mode own (secrets included: only for wiring the backend, never
// for the console).
type StorageConfig struct {
	Mode          string            `json:"mode"`
	Prefix        string            `json:"prefix"`
	Config        map[string]string `json:"config"`
	TenantManaged bool              `json:"tenant_managed"`
	UpdatedAt     string            `json:"updated_at"`
}

// TenantPrefix is the fixed prefix of a tenant inside the platform default.
func TenantPrefix(tenantID string) string { return "tenants/" + tenantID + "/" }

// storageKeys are the storage_* settings a tenant's own bucket uses (the
// remote backup switch stays a setting of the tenant).
func storageKeys() []string {
	var out []string
	for _, s := range storage.Settings {
		if s.Key != "backup_remote_enabled" {
			out = append(out, s.Key)
		}
	}
	return out
}

func defaultStorageSettings() map[string]string {
	out := map[string]string{}
	for _, s := range storage.Settings {
		if s.Key != "backup_remote_enabled" {
			out[s.Key] = s.Default
		}
	}
	return out
}

// Storage returns a tenant's storage choice (the default, platform mode, when
// nothing is stored).
func (p *Platform) Storage(tenantID string) (*StorageConfig, error) {
	c := &StorageConfig{Mode: StorageModePlatform, Config: defaultStorageSettings()}
	var cfg string
	var managed int
	err := p.St.DB.QueryRow(`SELECT mode,config,tenant_managed,updated_at FROM tenant_storage WHERE tenant_id=?`, tenantID).Scan(&c.Mode, &cfg, &managed, &c.UpdatedAt)
	if err == nil {
		var m map[string]any
		if json.Unmarshal([]byte(cfg), &m) == nil {
			for k, v := range m {
				if str, ok := v.(string); ok && storage.IsSetting(k) && k != "backup_remote_enabled" {
					c.Config[k] = str
				}
			}
		}
		c.TenantManaged = managed != 0
	} else if _, terr := p.Tenant(tenantID); terr != nil {
		return nil, ErrNotFound
	}
	c.Prefix = TenantPrefix(tenantID)
	return c, nil
}

// PlatformStorage is the platform default backend (platform_storage_* settings).
func (p *Platform) PlatformStorage() storage.Config {
	return storage.FromSettings(func(k string) string { return p.Setting("platform_" + k) })
}

// Resolve returns the backend configuration of a tenant's choice as set in
// the console (a tenant-managed bucket is applied by the tenant's server on
// top of this). LocalRoot is left for the caller (the tenant's directory).
func (p *Platform) Resolve(tenantID string, c *StorageConfig) storage.Config {
	if c != nil && c.Mode == StorageModeOwn {
		own := c.Config
		return storage.FromSettings(func(k string) string { return own[k] })
	}
	def := p.PlatformStorage()
	if def.Backend == storage.KindLocal {
		return storage.Config{Backend: storage.KindLocal}
	}
	return def.WithPrefix(TenantPrefix(tenantID))
}

// StorageView is a tenant's storage choice for the console: secrets only say
// whether they are set.
type StorageView struct {
	Mode          string            `json:"mode"`
	Prefix        string            `json:"prefix"`
	TenantManaged bool              `json:"tenant_managed"`
	Settings      map[string]string `json:"settings"`
	Set           map[string]bool   `json:"set"`
	UpdatedAt     string            `json:"updated_at"`
}

// View masks a storage choice for the console.
func (c *StorageConfig) View() StorageView {
	v := StorageView{Mode: c.Mode, Prefix: c.Prefix, TenantManaged: c.TenantManaged, Settings: map[string]string{}, Set: map[string]bool{}, UpdatedAt: c.UpdatedAt}
	for k, val := range c.Config {
		if storage.SettingSecret(k) {
			v.Set[k] = val != ""
			v.Settings[k] = ""
			continue
		}
		v.Settings[k] = val
	}
	return v
}

// MergeStorageSettings validates the storage_* settings in (as sent by the
// console's form) against the stored ones in prev: unknown keys are refused,
// an empty secret keeps the stored value, and a stored secret must be typed
// again when the place it is sent to changes. It returns the complete set.
func MergeStorageSettings(prev map[string]string, in map[string]string) (map[string]string, error) {
	next := map[string]string{}
	for k, v := range prev {
		next[k] = v
	}
	changed := map[string]bool{}
	typed := map[string]bool{}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := in[k]
		if !storage.IsSetting(k) || k == "backup_remote_enabled" {
			return nil, fmt.Errorf("%s: unknown storage setting", k)
		}
		if storage.SettingSecret(k) && v == "" {
			continue
		}
		norm, _, err := storage.NormalizeSetting(k, v)
		if err != nil {
			return nil, err
		}
		if storage.SettingSecret(k) {
			typed[k] = true
		}
		if norm != prev[k] {
			changed[k] = true
		}
		next[k] = norm
	}
	if secret := storage.SecretRetypeNeeded(func(k string) bool { return changed[k] }, func(k string) bool { return typed[k] }, func(k string) bool { return prev[k] != "" }); secret != "" {
		return nil, fmt.Errorf("enter the storage secret again when changing the endpoint, account or access key (%s)", secret)
	}
	return next, nil
}

// SetStorage stores a tenant's storage choice. settings are the complete
// storage_* settings of mode own (see MergeStorageSettings).
func (p *Platform) SetStorage(tenantID, mode string, settings map[string]string, tenantManaged bool) error {
	if mode != StorageModePlatform && mode != StorageModeOwn {
		return errors.New("mode must be platform or own")
	}
	if mode == StorageModeOwn {
		cfg := storage.FromSettings(func(k string) string { return settings[k] })
		if cfg.Backend == storage.KindLocal {
			return errors.New("choose S3, Azure or GCS for the tenant's own bucket")
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		if err := p.checkOverlap(tenantID, cfg); err != nil {
			return err
		}
	}
	b, err := json.Marshal(settings)
	if err != nil || len(b) > 128<<10 {
		return errors.New("configuration too large")
	}
	managed := 0
	if tenantManaged {
		managed = 1
	}
	res, err := p.St.DB.Exec(`INSERT INTO tenant_storage(tenant_id,mode,prefix,config,tenant_managed,updated_at) SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM tenants WHERE id=?)
		ON CONFLICT(tenant_id) DO UPDATE SET mode=excluded.mode,prefix=excluded.prefix,config=excluded.config,tenant_managed=excluded.tenant_managed,updated_at=excluded.updated_at`,
		tenantID, mode, TenantPrefix(tenantID), string(b), managed, now(), tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// checkOverlap refuses an own bucket that could reach the objects of another
// tenant: the platform default's tenants/ area or another tenant's own bucket.
func (p *Platform) checkOverlap(tenantID string, cfg storage.Config) error {
	if def := p.PlatformStorage(); def.Backend != storage.KindLocal && cfg.Overlaps(def.WithPrefix("tenants/")) {
		return errors.New("this bucket and prefix overlap the platform's default storage; choose another bucket or prefix")
	}
	rows, err := p.St.DB.Query(`SELECT tenant_id,config FROM tenant_storage WHERE mode='own' AND tenant_id<>?`, tenantID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, js string
		if rows.Scan(&id, &js) != nil {
			continue
		}
		m := map[string]string{}
		if json.Unmarshal([]byte(js), &m) != nil {
			continue
		}
		other := storage.FromSettings(func(k string) string { return m[k] })
		if cfg.Overlaps(other) {
			return errors.New("this bucket and prefix overlap another tenant's storage; choose another bucket or prefix")
		}
	}
	return rows.Err()
}

// StorageLocations lists every backend configuration a tenant's data may be
// in according to the platform (its fixed prefix in the platform default and
// its own bucket), for removal when the tenant is deleted.
func (p *Platform) StorageLocations(tenantID string) []storage.Config {
	var out []storage.Config
	if def := p.PlatformStorage(); def.Backend != storage.KindLocal {
		out = append(out, def.WithPrefix(TenantPrefix(tenantID)))
	}
	if c, err := p.Storage(tenantID); err == nil && c.Mode == StorageModeOwn {
		if own := p.Resolve(tenantID, c); own.Backend != storage.KindLocal {
			out = append(out, own)
		}
	}
	return out
}
