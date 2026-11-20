// Package platform holds the data of multi-tenant mode: the platform database
// (DATA_DIR/platform.db) with tenants, their domains, quotas and storage
// configuration, the platform administrators and the platform audit log. It
// never opens or reads a tenant's database except in the offline conversion
// (import.go); serving tenants is internal/app's job (mt_*.go).
package platform

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mhenderik/sendora/internal/store"
)

// Tenant statuses.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	// StatusImporting marks a tenant whose offline conversion has not finished;
	// it is never served.
	StatusImporting = "importing"
)

// Key modes of a tenant (see store/tenant.go).
const (
	KeyDerived = "derived"
	KeyLegacy  = "legacy"
)

const schemaVersion = 1

var schema = []string{
	`CREATE TABLE IF NOT EXISTS tenants (
		id TEXT PRIMARY KEY,
		slug TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','importing')),
		created_at TEXT NOT NULL,
		max_users INTEGER NOT NULL DEFAULT 0 CHECK (max_users >= 0),
		max_storage_bytes INTEGER NOT NULL DEFAULT 0 CHECK (max_storage_bytes >= 0),
		key_mode TEXT NOT NULL DEFAULT 'derived' CHECK (key_mode IN ('derived','legacy')),
		legacy_root BLOB,
		primary_domain TEXT NOT NULL DEFAULT '',
		stat_users INTEGER NOT NULL DEFAULT 0,
		stat_storage_bytes INTEGER NOT NULL DEFAULT 0,
		stat_updated_at TEXT NOT NULL DEFAULT '',
		allow_private_targets INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS tenant_domains (
		domain TEXT PRIMARY KEY,
		tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
		kind TEXT NOT NULL DEFAULT 'custom' CHECK (kind IN ('custom')),
		verified INTEGER NOT NULL DEFAULT 0,
		verify_token TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_tenant_domains_tenant ON tenant_domains(tenant_id)`,
	`CREATE TABLE IF NOT EXISTS tenant_storage (
		tenant_id TEXT PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
		mode TEXT NOT NULL DEFAULT 'platform' CHECK (mode IN ('platform','own')),
		prefix TEXT NOT NULL DEFAULT '',
		config TEXT NOT NULL DEFAULT '{}',
		tenant_managed INTEGER NOT NULL DEFAULT 1,
		tenant_managed_explicit INTEGER NOT NULL DEFAULT 0,
		offer TEXT NOT NULL DEFAULT 'none',
		updated_at TEXT NOT NULL DEFAULT '')`,
	`CREATE TABLE IF NOT EXISTS platform_admins (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE COLLATE NOCASE,
		email TEXT NOT NULL DEFAULT '',
		password_hash TEXT NOT NULL,
		totp_secret TEXT NOT NULL DEFAULT '',
		totp_enabled INTEGER NOT NULL DEFAULT 0,
		totp_last_counter INTEGER NOT NULL DEFAULT 0,
		pending_totp_secret TEXT NOT NULL DEFAULT '',
		failures INTEGER NOT NULL DEFAULT 0,
		locked_until INTEGER NOT NULL DEFAULT 0,
		must_change_password INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS platform_sessions (
		hash TEXT PRIMARY KEY,
		admin_id INTEGER NOT NULL REFERENCES platform_admins(id) ON DELETE CASCADE,
		stage TEXT NOT NULL CHECK (stage IN ('mfa','enroll','full')),
		created_at INTEGER NOT NULL,
		last_seen INTEGER NOT NULL,
		expires_at INTEGER NOT NULL,
		mfa_at INTEGER NOT NULL DEFAULT 0)`,
	`INSERT OR IGNORE INTO settings(key,value,grp) VALUES('platform_schema_version','` + fmt.Sprint(schemaVersion) + `','system')`,
}

// Platform is the open platform database.
type Platform struct {
	St      *store.Store
	DataDir string
	master  []byte

	mu      sync.RWMutex
	domains map[string]string // verified custom domain -> tenant id (lookup cache)
	slugs   map[string]string // slug -> tenant id
	routes  map[string]Route  // tenant id -> routing data
}

// Open opens or (with initialize) creates DATA_DIR/platform.db. The master key
// is kept to derive the tenants' database keys.
func Open(dataDir string, master []byte, initialize bool) (*Platform, error) {
	key, err := store.PlatformDBKey(master)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	st, err := store.OpenPlatform(dataDir, key, initialize, schema)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{"tenants"} {
		if err := os.MkdirAll(filepath.Join(dataDir, d), 0o700); err != nil {
			st.Close()
			return nil, err
		}
	}
	// Columns added after the first 0.2.0 development builds. Tenants stored
	// before the storage offer existed keep what they had: the platform default
	// (with their prefix, or their directory while the default is local).
	var hadOffer int
	st.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('tenant_storage') WHERE name='offer'`).Scan(&hadOffer)
	for _, c := range []struct{ table, name, decl string }{
		{"tenants", "allow_private_targets", "INTEGER NOT NULL DEFAULT 0"},
		{"tenant_storage", "tenant_managed", "INTEGER NOT NULL DEFAULT 1"},
		{"tenant_storage", "tenant_managed_explicit", "INTEGER NOT NULL DEFAULT 0"},
		{"tenant_storage", "offer", "TEXT NOT NULL DEFAULT 'none'"},
		{"platform_admins", "must_change_password", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := addColumn(st.DB, c.table, c.name, c.decl); err != nil {
			st.Close()
			return nil, err
		}
	}
	// Tenants may configure their own storage unless a platform administrator
	// switched it off for them (an explicit choice); older rows had no choice.
	if hadOffer == 0 {
		if _, err := st.DB.Exec(`UPDATE tenant_storage SET offer='platform' WHERE mode='platform'`); err != nil {
			st.Close()
			return nil, err
		}
	}
	if _, err := st.DB.Exec(`UPDATE tenant_storage SET tenant_managed=1 WHERE tenant_managed_explicit=0 AND tenant_managed=0`); err != nil {
		st.Close()
		return nil, err
	}
	p := &Platform{St: st, DataDir: dataDir, master: append([]byte(nil), master...)}
	for _, d := range settingDefaults {
		st.DB.Exec(`INSERT OR IGNORE INTO settings(key,value,grp) VALUES(?,?,?)`, d.Key, d.Val, d.Grp)
	}
	if err := p.reloadDomains(); err != nil {
		st.Close()
		return nil, err
	}
	return p, nil
}

func addColumn(db *sql.DB, table, name, decl string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, name).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := db.Exec("ALTER TABLE " + table + " ADD COLUMN " + name + " " + decl)
	return err
}

// Close closes the platform database and wipes the kept master key.
func (p *Platform) Close() error {
	clear(p.master)
	return p.St.Close()
}

// Tenant is one row of tenants.
type Tenant struct {
	ID              string `json:"id"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	MaxUsers        int64  `json:"max_users"`
	MaxStorageBytes int64  `json:"max_storage_bytes"`
	KeyMode         string `json:"key_mode"`
	PrimaryDomain   string `json:"primary_domain"`
	StatUsers       int64  `json:"user_count"`
	StatStorage     int64  `json:"storage_used_bytes"`
	StatUpdatedAt   string `json:"stats_updated_at"`
	// AllowPrivateTargets lets the tenant's administrators point SMTP, sign-in
	// providers, audit forwarding and their own storage at private, loopback
	// and link-local addresses (off: the multi-tenant SSRF guard applies).
	AllowPrivateTargets bool `json:"allow_private_targets"`
	legacyRoot          []byte
}

const tenantCols = `id,slug,name,status,created_at,max_users,max_storage_bytes,key_mode,legacy_root,primary_domain,stat_users,stat_storage_bytes,stat_updated_at,allow_private_targets`

func scanTenant(row interface{ Scan(...any) error }) (*Tenant, error) {
	t := &Tenant{}
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.CreatedAt, &t.MaxUsers, &t.MaxStorageBytes, &t.KeyMode, &t.legacyRoot, &t.PrimaryDomain, &t.StatUsers, &t.StatStorage, &t.StatUpdatedAt, &t.AllowPrivateTargets)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// ErrNotFound is returned for an unknown tenant, domain or administrator.
var ErrNotFound = errors.New("not found")

// Tenant returns one tenant by ID.
func (p *Platform) Tenant(id string) (*Tenant, error) {
	t, err := scanTenant(p.St.DB.QueryRow(`SELECT `+tenantCols+` FROM tenants WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// TenantBySlug returns one tenant by slug.
func (p *Platform) TenantBySlug(slug string) (*Tenant, error) {
	t, err := scanTenant(p.St.DB.QueryRow(`SELECT `+tenantCols+` FROM tenants WHERE slug=?`, strings.ToLower(slug)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// Tenants lists tenants, optionally filtered by a search over slug, name and
// domains.
func (p *Platform) Tenants(q string) ([]*Tenant, error) {
	query := `SELECT ` + tenantCols + ` FROM tenants`
	var args []any
	if q = strings.TrimSpace(strings.ToLower(q)); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		query += ` WHERE lower(slug) LIKE ? ESCAPE '\' OR lower(name) LIKE ? ESCAPE '\' OR id IN (SELECT tenant_id FROM tenant_domains WHERE domain LIKE ? ESCAPE '\')`
		args = append(args, like, like, like)
	}
	query += ` ORDER BY slug`
	rows, err := p.St.DB.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NewTenantID returns a random tenant ID (26 lowercase base32 characters).
func NewTenantID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))
}

var slugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// reservedSlugs cannot be tenant subdomains: they are the console's host or
// commonly used for other services on the same domain.
var reservedSlugs = map[string]bool{
	"admin": true, "www": true, "api": true, "app": true, "mail": true, "smtp": true, "imap": true, "pop": true,
	"ftp": true, "static": true, "assets": true, "cdn": true, "platform": true, "console": true, "status": true,
	"root": true, "sendora": true, "ns1": true, "ns2": true, "autodiscover": true, "autoconfig": true, "localhost": true,
	"default": true, "system": true, "support": true, "help": true, "docs": true, "auth": true, "login": true, "sso": true,
}

// ValidSlug reports whether s can be a tenant subdomain.
func ValidSlug(s string) bool {
	return len(s) >= 2 && slugPattern.MatchString(s) && !strings.Contains(s, "--") && !reservedSlugs[s]
}

var hostLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeDomain lowercases a host name and checks it is a plausible DNS name
// with at least two labels.
func NormalizeDomain(d string) (string, bool) {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if len(d) > 253 || !strings.Contains(d, ".") {
		return "", false
	}
	for _, l := range strings.Split(d, ".") {
		if !hostLabel.MatchString(l) {
			return "", false
		}
	}
	return d, true
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// CreateTenant inserts a new tenant with its subdomain. The caller creates the
// tenant's database afterwards (app.TenantHost.Provision).
func (p *Platform) CreateTenant(slug, name string, maxUsers, maxStorage int64) (*Tenant, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !ValidSlug(slug) {
		return nil, fmt.Errorf("invalid or reserved subdomain")
	}
	if maxUsers < 0 || maxStorage < 0 {
		return nil, fmt.Errorf("invalid quota")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = slug
	}
	if len(name) > 200 {
		return nil, fmt.Errorf("name too long")
	}
	t := &Tenant{ID: NewTenantID(), Slug: slug, Name: name, Status: StatusActive, CreatedAt: now(), MaxUsers: maxUsers, MaxStorageBytes: maxStorage, KeyMode: KeyDerived}
	if err := p.insertTenant(t, nil); err != nil {
		return nil, err
	}
	return t, nil
}

// ErrSlugTaken is returned when the subdomain is in use.
var ErrSlugTaken = errors.New("this subdomain is already in use")

func (p *Platform) insertTenant(t *Tenant, legacyRoot []byte) error {
	tx, err := p.St.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	tx.QueryRow(`SELECT COUNT(*) FROM tenants WHERE slug=?`, t.Slug).Scan(&n)
	if n > 0 {
		return ErrSlugTaken
	}
	var root any
	if legacyRoot != nil {
		root = legacyRoot
	}
	if _, err := tx.Exec(`INSERT INTO tenants(id,slug,name,status,created_at,max_users,max_storage_bytes,key_mode,legacy_root,primary_domain) VALUES(?,?,?,?,?,?,?,?,?,'')`,
		t.ID, t.Slug, t.Name, t.Status, t.CreatedAt, t.MaxUsers, t.MaxStorageBytes, t.KeyMode, root); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tenant_storage(tenant_id,mode,prefix,offer,tenant_managed,updated_at) VALUES(?, 'platform', ?, 'none', 1, ?)`, t.ID, "tenants/"+t.ID+"/", now()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return p.reloadDomains()
}

// baseHost strips an optional port from the base domain.
func baseHost(base string) string {
	base = strings.ToLower(strings.TrimSpace(base))
	if i := strings.LastIndexByte(base, ':'); i >= 0 && !strings.Contains(base[i:], "]") {
		base = base[:i]
	}
	return strings.TrimSuffix(base, ".")
}

// SetStatus changes a tenant's status (active or suspended).
func (p *Platform) SetStatus(id, status string) error {
	if status != StatusActive && status != StatusSuspended {
		return fmt.Errorf("invalid status")
	}
	res, err := p.St.DB.Exec(`UPDATE tenants SET status=? WHERE id=? AND status IN ('active','suspended')`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return p.reloadDomains()
}

// SetQuotas changes a tenant's limits (0 = unlimited).
func (p *Platform) SetQuotas(id string, maxUsers, maxStorage int64) error {
	if maxUsers < 0 || maxStorage < 0 {
		return fmt.Errorf("invalid quota")
	}
	res, err := p.St.DB.Exec(`UPDATE tenants SET max_users=?, max_storage_bytes=? WHERE id=?`, maxUsers, maxStorage, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetName renames a tenant (display name only).
func (p *Platform) SetName(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return fmt.Errorf("invalid name")
	}
	_, err := p.St.DB.Exec(`UPDATE tenants SET name=? WHERE id=?`, name, id)
	return err
}

// UpdateStats stores the counts shown in the console (never content).
func (p *Platform) UpdateStats(id string, users, storage int64) {
	p.St.DB.Exec(`UPDATE tenants SET stat_users=?, stat_storage_bytes=?, stat_updated_at=? WHERE id=?`, users, storage, now(), id)
}

// SetAllowPrivateTargets switches the SSRF guard of a tenant off (true) or on.
func (p *Platform) SetAllowPrivateTargets(id string, allow bool) error {
	v := 0
	if allow {
		v = 1
	}
	res, err := p.St.DB.Exec(`UPDATE tenants SET allow_private_targets=? WHERE id=?`, v, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTenantRow removes the tenant and its domains and storage config from
// the platform database. The caller removes the tenant's directory first.
func (p *Platform) DeleteTenantRow(id string) error {
	if _, err := p.St.DB.Exec(`DELETE FROM tenants WHERE id=?`, id); err != nil {
		return err
	}
	return p.reloadDomains()
}

// TenantDir is the tenant's data directory.
func (p *Platform) TenantDir(id string) (string, error) {
	if !store.TenantIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid tenant id")
	}
	return filepath.Join(p.DataDir, "tenants", id), nil
}

// TenantKeys returns the tenant's database key and its file/audit root (nil
// for a native tenant). The caller wipes both after use.
func (p *Platform) TenantKeys(t *Tenant) (dbKey, root []byte, err error) {
	dbKey, err = store.TenantDBKey(p.master, t.ID)
	if err != nil {
		return nil, nil, err
	}
	if t.KeyMode == KeyLegacy {
		if len(t.legacyRoot) == 32 {
			root = append([]byte(nil), t.legacyRoot...)
		} else {
			root = append([]byte(nil), p.master...)
		}
	}
	return dbKey, root, nil
}

// ---------- domains ----------

// Domain is one row of tenant_domains.
type Domain struct {
	Domain      string `json:"domain"`
	TenantID    string `json:"tenant_id"`
	Kind        string `json:"kind"`
	Verified    bool   `json:"verified"`
	VerifyToken string `json:"verify_token,omitempty"`
	CreatedAt   string `json:"created_at"`
}

func (p *Platform) reloadDomains() error {
	load := func(q string) (map[string]string, error) {
		rows, err := p.St.DB.Query(q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		m := map[string]string{}
		for rows.Next() {
			var k, id string
			if err := rows.Scan(&k, &id); err != nil {
				return nil, err
			}
			m[k] = id
		}
		return m, rows.Err()
	}
	domains, err := load(`SELECT d.domain,d.tenant_id FROM tenant_domains d JOIN tenants t ON t.id=d.tenant_id WHERE d.verified=1 AND d.kind='custom'`)
	if err != nil {
		return err
	}
	rows, err := p.St.DB.Query(`SELECT id,slug,status,primary_domain FROM tenants`)
	if err != nil {
		return err
	}
	defer rows.Close()
	slugs, routes := map[string]string{}, map[string]Route{}
	for rows.Next() {
		var r Route
		if err := rows.Scan(&r.ID, &r.Slug, &r.Status, &r.PrimaryDomain); err != nil {
			return err
		}
		slugs[r.Slug], routes[r.ID] = r.ID, r
	}
	if err := rows.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.domains, p.slugs, p.routes = domains, slugs, routes
	p.mu.Unlock()
	return nil
}

// Route is the cached routing data of a tenant.
type Route struct {
	ID, Slug, Status, PrimaryDomain string
}

// RouteOf returns the cached routing data of a tenant.
func (p *Platform) RouteOf(id string) (Route, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.routes[id]
	return r, ok
}

// TenantForHost resolves a request host (lowercase, without port) to a tenant
// ID: <slug>.<base domain> or a verified custom domain. ok is false for any
// other host, including the console host and the bare base domain.
func (p *Platform) TenantForHost(host, baseDomain string) (string, bool) {
	base := baseHost(baseDomain)
	p.mu.RLock()
	defer p.mu.RUnlock()
	if label, found := strings.CutSuffix(host, "."+base); found && base != "" {
		if strings.Contains(label, ".") {
			return "", false
		}
		id, ok := p.slugs[label]
		return id, ok
	}
	id, ok := p.domains[host]
	return id, ok
}

// SubdomainHost is the tenant's host under the base domain (without port).
func SubdomainHost(slug, baseDomain string) string { return slug + "." + baseHost(baseDomain) }

// Domains lists a tenant's domains.
func (p *Platform) Domains(tenantID string) ([]Domain, error) {
	rows, err := p.St.DB.Query(`SELECT domain,tenant_id,kind,verified,verify_token,created_at FROM tenant_domains WHERE tenant_id=? ORDER BY domain`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.Domain, &d.TenantID, &d.Kind, &d.Verified, &d.VerifyToken, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// VerifyRecordName is the DNS name that must hold the TXT verification record
// of a custom domain.
func VerifyRecordName(domain string) string { return "_sendora-challenge." + domain }

// VerifyRecordValue is the TXT value that proves control of a custom domain.
func VerifyRecordValue(token string) string { return "sendora-verify=" + token }

// ErrDomainTaken is returned when a domain is already mapped.
var ErrDomainTaken = errors.New("this domain is already in use")

// AddCustomDomain maps an unverified custom domain to a tenant and returns its
// verification token.
func (p *Platform) AddCustomDomain(tenantID, domain, baseDomain string) (*Domain, error) {
	d, ok := NormalizeDomain(domain)
	if !ok {
		return nil, fmt.Errorf("invalid domain")
	}
	base := baseHost(baseDomain)
	if d == base || strings.HasSuffix(d, "."+base) {
		return nil, fmt.Errorf("subdomains of the platform's base domain cannot be custom domains")
	}
	if _, err := p.Tenant(tenantID); err != nil {
		return nil, err
	}
	tok := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(randomBytes(20)))
	if _, err := p.St.DB.Exec(`INSERT INTO tenant_domains(domain,tenant_id,kind,verified,verify_token,created_at) VALUES(?,?,'custom',0,?,?)`, d, tenantID, tok, now()); err != nil {
		return nil, ErrDomainTaken
	}
	return &Domain{Domain: d, TenantID: tenantID, Kind: "custom", VerifyToken: tok, CreatedAt: now()}, nil
}

// MarkDomainVerified sets a custom domain verified (after the TXT check).
func (p *Platform) MarkDomainVerified(tenantID, domain string) error {
	res, err := p.St.DB.Exec(`UPDATE tenant_domains SET verified=1 WHERE domain=? AND tenant_id=? AND kind='custom'`, domain, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return p.reloadDomains()
}

// RemoveCustomDomain unmaps a custom domain; the primary domain falls back to
// the subdomain when it was this one.
func (p *Platform) RemoveCustomDomain(tenantID, domain string) error {
	res, err := p.St.DB.Exec(`DELETE FROM tenant_domains WHERE domain=? AND tenant_id=? AND kind='custom'`, domain, tenantID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	p.St.DB.Exec(`UPDATE tenants SET primary_domain='' WHERE id=? AND primary_domain=?`, tenantID, domain)
	return p.reloadDomains()
}

// SetPrimaryDomain chooses the domain used for the tenant's links and emails
// ("" = the subdomain).
func (p *Platform) SetPrimaryDomain(tenantID, domain string) error {
	var n int
	p.St.DB.QueryRow(`SELECT COUNT(*) FROM tenant_domains WHERE domain=? AND tenant_id=? AND verified=1`, domain, tenantID).Scan(&n)
	if n == 0 && domain != "" {
		return fmt.Errorf("only a verified domain of this tenant can be the primary domain")
	}
	if _, err := p.St.DB.Exec(`UPDATE tenants SET primary_domain=? WHERE id=?`, domain, tenantID); err != nil {
		return err
	}
	return p.reloadDomains()
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
