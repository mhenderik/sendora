package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhenderik/sendora/internal/platform"
	"github.com/mhenderik/sendora/internal/store"
)

// Multi-tenant mode (SENDORA_MULTI_TENANT=true, docs/MULTI-TENANT.md).
//
// One process serves many tenants. TenantHost routes every request by its
// host: <slug>.<base domain> or a verified custom domain goes to that tenant's
// own *Server (own store, keys and secrets); admin.<base domain> goes to the
// platform console; any other host gets a plain 404, so hosts cannot be used
// to find out which tenants exist. Tenant servers are opened on the first
// request and closed when idle (least recently used first when the maximum
// number of open tenants is reached).

// MultiTenantConfig configures a TenantHost.
type MultiTenantConfig struct {
	Platform *platform.Platform
	// BaseDomain is SENDORA_BASE_DOMAIN: host name with an optional :port that
	// is only used to build URLs (e.g. "localhost:9090" for local testing).
	BaseDomain      string
	Static          fs.FS
	Version         string
	TrustedProxies  []*net.IPNet
	ForceLocalLogin bool
	// Managed are the platform settings set by the environment (read-only in the console).
	Managed map[string]bool
}

// TenantHost serves all tenants and the platform console.
type TenantHost struct {
	cfg      MultiTenantConfig
	P        *platform.Platform
	baseHost string // without port
	basePort string // ":9090" or ""
	scheme   string
	console  *platformConsole

	mu       sync.Mutex
	open     map[string]*tenantEntry
	opening  map[string]chan struct{}
	closing  map[string]chan struct{}
	deleting map[string]bool
	jobsCtx  context.Context
	shutdown bool

	now func() time.Time
}

type tenantEntry struct {
	id      string
	srv     *Server
	handler http.Handler
	cancel  context.CancelFunc
	refs    int
	last    time.Time
}

var (
	errTenantsBusy    = errors.New("too many tenants are open; try again shortly")
	errTenantUnusable = errors.New("tenant unavailable")
)

// NewTenantHost prepares multi-tenant serving; Start begins the background jobs.
func NewTenantHost(cfg MultiTenantConfig) (*TenantHost, error) {
	base := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cfg.BaseDomain), "."))
	if base == "" {
		return nil, errors.New("SENDORA_BASE_DOMAIN is required in multi-tenant mode")
	}
	host, port := base, ""
	if h, p, err := net.SplitHostPort(base); err == nil {
		host, port = h, ":"+p
	}
	if _, ok := platform.NormalizeDomain("x." + host); !ok {
		return nil, fmt.Errorf("SENDORA_BASE_DOMAIN %q is not a valid host name", cfg.BaseDomain)
	}
	h := &TenantHost{cfg: cfg, P: cfg.Platform, baseHost: host, basePort: port, scheme: "https",
		open: map[string]*tenantEntry{}, opening: map[string]chan struct{}{}, closing: map[string]chan struct{}{}, deleting: map[string]bool{},
		jobsCtx: context.Background(), now: time.Now}
	if loopbackHost(host) {
		h.scheme = "http"
	}
	h.console = newPlatformConsole(h)
	return h, nil
}

// ConsoleHost is the platform console's host name.
func (h *TenantHost) ConsoleHost() string { return "admin." + h.baseHost }

// ConsoleURL is the platform console's public URL.
func (h *TenantHost) ConsoleURL() string { return h.scheme + "://" + h.ConsoleHost() + h.basePort }

// tenantURL is the public URL of a tenant: its primary custom domain (https)
// or its subdomain.
func (h *TenantHost) tenantURL(r platform.Route) string {
	if r.PrimaryDomain != "" {
		return "https://" + r.PrimaryDomain
	}
	return h.scheme + "://" + platform.SubdomainHost(r.Slug, h.baseHost) + h.basePort
}

func (h *TenantHost) primaryHost(r platform.Route) string {
	if r.PrimaryDomain != "" {
		return r.PrimaryDomain
	}
	return platform.SubdomainHost(r.Slug, h.baseHost)
}

func (h *TenantHost) peerTrusted(r *http.Request) bool {
	ip := net.ParseIP(peerHost(r))
	if ip == nil {
		return false
	}
	for _, n := range h.cfg.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// requestHost is the host the client asked for: X-Forwarded-Host only from a
// trusted proxy, otherwise the Host header. It returns the raw value (with
// port) and the normalised host name (lowercase, no port, no trailing dot).
func (h *TenantHost) requestHost(r *http.Request) (raw, host string) {
	raw = r.Host
	if h.peerTrusted(r) {
		if xf := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); xf != "" {
			raw = xf
		}
	}
	host = raw
	if hh, _, err := net.SplitHostPort(raw); err == nil {
		host = hh
	}
	return raw, strings.TrimSuffix(strings.ToLower(host), ".")
}

func plainNotFound(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.Error(w, "404 page not found", http.StatusNotFound)
}

const suspendedPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Unavailable</title>` +
	`<style>body{font-family:system-ui,sans-serif;display:grid;place-items:center;min-height:100vh;margin:0;background:#f8fafc;color:#0f172a}main{max-width:32rem;padding:2rem;text-align:center}</style></head>` +
	`<body><main><h1>Temporarily unavailable</h1><p>This service is currently not available. Please contact your administrator.</p>` +
	`<p lang="nl">Deze dienst is op dit moment niet beschikbaar. Neem contact op met je beheerder.</p></main></body></html>`

// ServeHTTP routes a request by host.
func (h *TenantHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, host := h.requestHost(r)
	// The container health check and orchestrator probes use any host.
	if r.URL.Path == "/api/health" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		jout(w, 200, map[string]any{"status": "ok"})
		return
	}
	if host == h.ConsoleHost() {
		h.console.ServeHTTP(w, r)
		return
	}
	id, ok := h.P.TenantForHost(host, h.baseHost)
	if !ok {
		plainNotFound(w)
		return
	}
	route, ok := h.P.RouteOf(id)
	h.mu.Lock()
	deleting := h.deleting[id]
	h.mu.Unlock()
	if !ok || deleting || route.Status == platform.StatusImporting {
		plainNotFound(w)
		return
	}
	if route.Status != platform.StatusActive {
		w.Header().Set("Retry-After", "3600")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			jerr(w, http.StatusServiceUnavailable, "this organisation is suspended")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(suspendedPage))
		return
	}
	// One origin per tenant: cookies, WebAuthn and emailed links all use the
	// primary host; other hosts of the same tenant redirect there.
	if host != h.primaryHost(route) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			http.Redirect(w, r, h.tenantURL(route)+r.URL.RequestURI(), http.StatusPermanentRedirect)
			return
		}
		jerr(w, http.StatusMisdirectedRequest, "use the organisation's primary address")
		return
	}
	e, err := h.acquire(id)
	if err != nil {
		if errors.Is(err, errTenantsBusy) {
			w.Header().Set("Retry-After", "5")
		}
		log.Printf("tenant %s: %v", route.Slug, err)
		jerr(w, http.StatusServiceUnavailable, "temporarily unavailable; try again shortly")
		return
	}
	defer h.release(e, false)
	r.Host = raw
	e.handler.ServeHTTP(w, r)
}

// acquire returns the open tenant, opening it when needed, with a reference
// that release gives back. Concurrent requests for a closed tenant open it once.
func (h *TenantHost) acquire(id string) (*tenantEntry, error) {
	for {
		h.mu.Lock()
		if h.shutdown || h.deleting[id] {
			h.mu.Unlock()
			return nil, errTenantUnusable
		}
		if e := h.open[id]; e != nil {
			e.refs++
			e.last = h.now()
			h.mu.Unlock()
			return e, nil
		}
		if ch := h.opening[id]; ch != nil {
			h.mu.Unlock()
			<-ch
			continue
		}
		if ch := h.closing[id]; ch != nil {
			h.mu.Unlock()
			<-ch
			continue
		}
		var victim *tenantEntry
		if len(h.open)+len(h.opening) >= h.maxOpen() {
			for _, e := range h.open {
				if e.refs == 0 && (victim == nil || e.last.Before(victim.last)) {
					victim = e
				}
			}
			if victim == nil {
				h.mu.Unlock()
				return nil, errTenantsBusy
			}
			delete(h.open, victim.id)
			h.closing[victim.id] = make(chan struct{})
		}
		ch := make(chan struct{})
		h.opening[id] = ch
		h.mu.Unlock()
		if victim != nil {
			h.finishClose(victim)
		}
		e, err := h.openTenant(id)
		h.mu.Lock()
		delete(h.opening, id)
		close(ch)
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		e.refs, e.last = 1, h.now()
		h.open[id] = e
		h.mu.Unlock()
		return e, nil
	}
}

// release gives a reference back; idleNow lets the reaper close the tenant
// at its next run (used by the maintenance sweep).
func (h *TenantHost) release(e *tenantEntry, idleNow bool) {
	h.mu.Lock()
	e.refs--
	if idleNow {
		e.last = time.Time{}
	} else {
		e.last = h.now()
	}
	h.mu.Unlock()
}

func (h *TenantHost) maxOpen() int {
	return int(h.P.SettingInt("max_open_tenants", 50))
}

func (h *TenantHost) idle() time.Duration {
	return time.Duration(h.P.SettingInt("tenant_idle_minutes", 15)) * time.Minute
}

// openTenant opens a tenant's store and server and starts its jobs.
func (h *TenantHost) openTenant(id string) (*tenantEntry, error) {
	t, err := h.P.Tenant(id)
	if err != nil {
		return nil, err
	}
	route, _ := h.P.RouteOf(id)
	dir, err := h.P.TenantDir(id)
	if err != nil {
		return nil, err
	}
	dbKey, root, err := h.P.TenantKeys(t)
	if err != nil {
		return nil, err
	}
	st, err := store.OpenTenant(dir, dbKey, root, false)
	clear(dbKey)
	clear(root)
	if err != nil {
		return nil, err
	}
	storage, _ := h.P.Storage(id)
	srv, err := NewTenantServer(st, h.cfg.Static, h.cfg.Version, TenantOptions{
		ID: t.ID, Slug: t.Slug, BaseURL: h.tenantURL(route), MailDefault: h.P.MailDefault, Storage: storage,
	})
	if err != nil {
		st.Close()
		return nil, err
	}
	srv.SetTrustedProxies(h.cfg.TrustedProxies)
	srv.SetForceLocalLogin(h.cfg.ForceLocalLogin)
	if err := srv.SetTenantLimits(t.MaxUsers, t.MaxStorageBytes); err != nil {
		st.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(h.jobsCtx)
	srv.RunJobs(ctx)
	return &tenantEntry{id: id, srv: srv, handler: srv.Router(), cancel: cancel}, nil
}

// finishClose closes an entry already removed from open and listed in closing.
func (h *TenantHost) finishClose(e *tenantEntry) {
	users, storage := e.srv.TenantStats()
	h.P.UpdateStats(e.id, users, storage)
	if err := e.srv.closeTenant(e.cancel); err != nil {
		log.Printf("closing tenant %s: %v", e.id, err)
	}
	h.mu.Lock()
	if ch := h.closing[e.id]; ch != nil {
		close(ch)
		delete(h.closing, e.id)
	}
	h.mu.Unlock()
}

// evict closes a tenant once its requests have finished (bounded wait). It
// reports whether the tenant is closed.
func (h *TenantHost) evict(id string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		h.mu.Lock()
		if ch := h.opening[id]; ch != nil {
			h.mu.Unlock()
			<-ch
			continue
		}
		e := h.open[id]
		if e == nil {
			ch := h.closing[id]
			h.mu.Unlock()
			if ch != nil {
				<-ch
			}
			return true
		}
		if e.refs == 0 {
			delete(h.open, id)
			h.closing[id] = make(chan struct{})
			h.mu.Unlock()
			h.finishClose(e)
			return true
		}
		h.mu.Unlock()
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// reap closes tenants idle for longer than the idle time.
func (h *TenantHost) reap() {
	idle := h.idle()
	h.mu.Lock()
	var victims []*tenantEntry
	for id, e := range h.open {
		if e.refs == 0 && h.now().Sub(e.last) >= idle {
			delete(h.open, id)
			h.closing[id] = make(chan struct{})
			victims = append(victims, e)
		}
	}
	h.mu.Unlock()
	for _, e := range victims {
		h.finishClose(e)
	}
}

// withTenant runs fn on a tenant's server (opening it when needed).
func (h *TenantHost) withTenant(id string, fn func(*Server) error) error {
	e, err := h.acquire(id)
	if err != nil {
		return err
	}
	defer h.release(e, false)
	return fn(e.srv)
}

// OpenTenants returns the IDs of the open tenants (sorted).
func (h *TenantHost) OpenTenants() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.open))
	for id := range h.open {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Start runs the background jobs until ctx ends: closing idle tenants,
// refreshing the console's counts, maintenance (clean-up, expiry mails, audit
// retention, backups) for tenants that are not open, and the platform's own
// session and audit retention clean-up.
func (h *TenantHost) Start(ctx context.Context) {
	h.mu.Lock()
	h.jobsCtx = ctx
	h.mu.Unlock()
	go func() {
		reap := time.NewTicker(30 * time.Second)
		defer reap.Stop()
		stats := time.NewTicker(5 * time.Minute)
		defer stats.Stop()
		sweep := time.NewTimer(5 * time.Minute)
		defer sweep.Stop()
		daily := time.NewTicker(24 * time.Hour)
		defer daily.Stop()
		h.platformMaintenance()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reap.C:
				h.reap()
				h.P.PruneSessions()
			case <-stats.C:
				h.refreshOpenStats()
			case <-sweep.C:
				h.MaintainIdleTenants(ctx)
				sweep.Reset(time.Duration(h.P.SettingInt("tenant_maintenance_hours", 6)) * time.Hour)
			case <-daily.C:
				h.platformMaintenance()
			}
		}
	}()
}

func (h *TenantHost) refreshOpenStats() {
	h.mu.Lock()
	var es []*tenantEntry
	for _, e := range h.open {
		e.refs++
		es = append(es, e)
	}
	h.mu.Unlock()
	for _, e := range es {
		users, storage := e.srv.TenantStats()
		h.P.UpdateStats(e.id, users, storage)
		h.release(e, false)
	}
}

// platformMaintenance applies the platform audit retention.
func (h *TenantHost) platformMaintenance() {
	days := h.P.SettingInt("audit_retention_days", 365)
	if n, err := h.P.St.AuditPurge(time.Now().AddDate(0, 0, -int(days))); err != nil {
		log.Printf("platform audit retention: %v", err)
	} else if n > 0 {
		h.P.Audit(0, "system", "audit_purged", "platform", "ok", fmt.Sprintf("%d records older than %d days", n, days), "")
	}
	// Directories of deleted tenants whose removal was interrupted.
	ents, _ := os.ReadDir(filepath.Join(h.P.DataDir, "tenants"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".deleting-") {
			os.RemoveAll(filepath.Join(h.P.DataDir, "tenants", e.Name()))
		}
	}
}

// MaintainIdleTenants opens every active tenant that is not open, one at a
// time, so its periodic jobs run (opening runs the clean-up; the daily backup
// runs when due), records its counts and lets the reaper close it again.
func (h *TenantHost) MaintainIdleTenants(ctx context.Context) {
	ts, err := h.P.Tenants("")
	if err != nil {
		return
	}
	for _, t := range ts {
		if ctx.Err() != nil {
			return
		}
		if t.Status != platform.StatusActive {
			continue
		}
		h.mu.Lock()
		_, isOpen := h.open[t.ID]
		busy := len(h.open)+1 >= h.maxOpen()
		h.mu.Unlock()
		if isOpen {
			continue
		}
		if busy {
			h.reap()
		}
		e, err := h.acquire(t.ID)
		if err != nil {
			log.Printf("tenant maintenance %s: %v", t.Slug, err)
			continue
		}
		if e.srv.BackupDue() {
			e.srv.autoBackup()
		}
		users, storage := e.srv.TenantStats()
		h.P.UpdateStats(t.ID, users, storage)
		h.release(e, true)
		h.evict(t.ID, 0)
	}
}

// Shutdown closes every open tenant (waiting a bounded time for requests).
func (h *TenantHost) Shutdown(wait time.Duration) {
	h.mu.Lock()
	h.shutdown = true
	ids := make([]string, 0, len(h.open))
	for id := range h.open {
		ids = append(ids, id)
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if !h.evict(id, wait) {
				// Still busy: close anyway, the process is ending.
				h.mu.Lock()
				e := h.open[id]
				if e != nil {
					delete(h.open, id)
					h.closing[id] = make(chan struct{})
				}
				h.mu.Unlock()
				if e != nil {
					h.finishClose(e)
				}
			}
		}(id)
	}
	wg.Wait()
}

// ---------- tenant lifecycle (used by the console) ----------

// CreateTenant registers a tenant, creates its database and its first
// administrator. It returns the one-time password link when it could not be
// emailed ("" when emailed).
func (h *TenantHost) CreateTenant(actor, slug, name, adminUser, adminEmail string, maxUsers, maxStorage int64) (*platform.Tenant, string, bool, error) {
	t, err := h.P.CreateTenant(slug, name, maxUsers, maxStorage)
	if err != nil {
		return nil, "", false, err
	}
	fail := func(err error) (*platform.Tenant, string, bool, error) {
		h.deleteTenantData(t.ID)
		h.P.DeleteTenantRow(t.ID)
		return nil, "", false, err
	}
	dir, err := h.P.TenantDir(t.ID)
	if err != nil {
		return fail(err)
	}
	dbKey, _, err := h.P.TenantKeys(t)
	if err != nil {
		return fail(err)
	}
	st, err := store.OpenTenant(dir, dbKey, nil, true)
	clear(dbKey)
	if err != nil {
		return fail(err)
	}
	st.Close()
	var link string
	var emailed bool
	err = h.withTenant(t.ID, func(s *Server) error {
		s.RecordPlatformEvent(actor, "tenant_created", "slug="+t.Slug+fmt.Sprintf(" max_users=%d max_storage_bytes=%d", maxUsers, maxStorage))
		var perr error
		link, emailed, perr = s.ProvisionAdmin(actor, adminUser, adminEmail)
		return perr
	})
	if err != nil {
		h.evict(t.ID, 5*time.Second)
		return fail(err)
	}
	return t, link, emailed, nil
}

// SetTenantStatus suspends or resumes a tenant. A suspended tenant is closed
// once its requests have finished; new requests get 503.
func (h *TenantHost) SetTenantStatus(actor, id, status string) error {
	if status == platform.StatusSuspended {
		// Recorded in the tenant's own log first, while it can still be opened.
		h.withTenant(id, func(s *Server) error { s.RecordPlatformEvent(actor, "tenant_suspended", ""); return nil })
	}
	if err := h.P.SetStatus(id, status); err != nil {
		return err
	}
	if status == platform.StatusSuspended {
		go h.evict(id, 10*time.Minute)
	} else {
		h.withTenant(id, func(s *Server) error { s.RecordPlatformEvent(actor, "tenant_resumed", ""); return nil })
	}
	return nil
}

// SetTenantQuotas changes a tenant's limits and applies them at once when open.
func (h *TenantHost) SetTenantQuotas(actor, id string, maxUsers, maxStorage int64) error {
	if err := h.P.SetQuotas(id, maxUsers, maxStorage); err != nil {
		return err
	}
	return h.withTenant(id, func(s *Server) error {
		s.RecordPlatformEvent(actor, "tenant_quota_changed", fmt.Sprintf("max_users=%d max_storage_bytes=%d", maxUsers, maxStorage))
		return s.SetTenantLimits(maxUsers, maxStorage)
	})
}

// reopen closes an open tenant so that changed routing data (primary domain,
// storage configuration) applies at its next request.
func (h *TenantHost) reopen(id string) { go h.evict(id, 10*time.Minute) }

// DeleteTenant removes a tenant: its database, files and backups. It refuses
// while requests are still running after a short wait.
func (h *TenantHost) DeleteTenant(id string) error {
	h.mu.Lock()
	h.deleting[id] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.deleting, id)
		h.mu.Unlock()
	}()
	if !h.evict(id, 30*time.Second) {
		return errors.New("the tenant still has requests in progress; try again shortly")
	}
	if err := h.deleteTenantData(id); err != nil {
		return err
	}
	return h.P.DeleteTenantRow(id)
}

// deleteTenantData moves the tenant's directory aside (atomic) and removes it.
func (h *TenantHost) deleteTenantData(id string) error {
	dir, err := h.P.TenantDir(id)
	if err != nil {
		return err
	}
	aside := filepath.Join(filepath.Dir(dir), ".deleting-"+id)
	if err := os.Rename(dir, aside); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.RemoveAll(aside); err != nil {
		// platformMaintenance retries the removal.
		log.Printf("removing the data of deleted tenant %s: %v", id, err)
	}
	return nil
}
