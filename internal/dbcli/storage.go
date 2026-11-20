package dbcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mhenderik/sendora/internal/netguard"
	"github.com/mhenderik/sendora/internal/platform"
	"github.com/mhenderik/sendora/internal/storage"
	"github.com/mhenderik/sendora/internal/store"
)

// storageSetting returns a storage setting the way the server sees it:
// SENDORA_<KEY> or the file named by SENDORA_<KEY>_FILE when set (the same
// ConfigMap/Secret as the server), otherwise the value in the database.
func storageSetting(st *store.Store) func(string) string {
	return func(key string) string {
		name := "SENDORA_" + strings.ToUpper(key)
		if p := os.Getenv(name + "_FILE"); p != "" {
			if f, err := os.Open(p); err == nil {
				b, _ := io.ReadAll(io.LimitReader(f, 64<<10))
				f.Close()
				return strings.TrimRight(string(b), "\r\n")
			}
		}
		if v, ok := os.LookupEnv(name); ok {
			if norm, _, err := storage.NormalizeSetting(key, strings.TrimSpace(v)); err == nil {
				return norm
			}
			return strings.TrimSpace(v)
		}
		return st.Setting(key)
	}
}

// storageMigrate copies every share file that is not in the configured
// storage location there (verifying each copy), switches its record and
// deletes the source: the command-line form of Admin -> Storage ->
// Migrate. Stop the server first, or accept that a download of a file that is
// being moved at that moment may fail.
func storageMigrate(source, dataDir string, key []byte, dryRun bool, tc tenantContext) error {
	want, err := store.DBPath(dataDir)
	if err != nil {
		return err
	}
	a, _ := filepath.Abs(source)
	b, _ := filepath.Abs(want)
	if a != b {
		return fmt.Errorf("storage-migrate: -source must be the database of -data-dir (%s)", want)
	}
	var st *store.Store
	if tc.id != "" {
		st, err = store.OpenTenant(dataDir, key, tc.root, false)
	} else {
		st, err = store.OpenEncrypted(dataDir, key, false)
	}
	if err != nil {
		return err
	}
	defer st.Close()
	var cfg storage.Config
	if tc.id != "" {
		if cfg, err = tenantStorage(st, dataDir, tc); err != nil {
			return err
		}
	} else {
		cfg = storage.FromSettings(storageSetting(st))
	}
	cfg.LocalRoot = dataDir
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("storage configuration: %v", err)
	}
	target, err := store.EnsureLocation(st.DB, cfg)
	if err != nil {
		return err
	}
	remaining := store.MigrateRemaining(st.DB, target)
	fmt.Fprintf(stdout, "target: %s; %d file(s) to migrate\n", cfg.Describe(), remaining)
	if dryRun || remaining == 0 {
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	audit := func(action, outcome, detail string) {
		if _, err := st.AuditAppend(store.AuditRecord{Category: "system", ActorName: "sendora-db", ActorRole: "system", Action: action, Target: "storage", Outcome: outcome, Detail: detail}); err != nil {
			fmt.Fprintf(stderr, "storage-migrate: audit record not written: %v\n", err)
		}
	}
	res, err := st.DB.Exec(`INSERT INTO storage_migrations(status,target_location,target,started_by) VALUES('running',?,?,'sendora-db')`, target, cfg.Describe())
	if err != nil {
		return err
	}
	mid, _ := res.LastInsertId()
	audit("storage_migration_started", "ok", fmt.Sprintf("migration#%d to %s (sendora-db)", mid, cfg.Describe()))
	last := time.Time{}
	stats, err := store.MigrateFiles(ctx, st.DB, dataDir, st.FileKey(), st.Backends(), target, store.MigrateOptions{
		Progress: func(s store.MigrateStats) {
			if time.Since(last) > 2*time.Second || s.Done+s.Failed+s.Skipped == s.Total {
				last = time.Now()
				fmt.Fprintf(stderr, "storage-migrate: %d/%d copied, %d failed, %d skipped\n", s.Done, s.Total, s.Failed, s.Skipped)
			}
		},
	})
	store.ProcessPendingDeletes(context.WithoutCancel(ctx), st.DB, st.Backends(), 10000)
	detail := fmt.Sprintf("migration#%d to %s: %d copied, %d failed, %d skipped, %d bytes (sendora-db)", mid, cfg.Describe(), stats.Done, stats.Failed, stats.Skipped, stats.Bytes)
	status, action, outcome := "finished", "storage_migration_finished", "ok"
	errText := ""
	switch {
	case errors.Is(err, context.Canceled):
		status, action, errText = "cancelled", "storage_migration_cancelled", "interrupted"
	case err != nil:
		status, action, outcome, errText = "failed", "storage_migration_failed", "failed", err.Error()
	case stats.Failed > 0:
		status, action, outcome, errText = "failed", "storage_migration_failed", "failed", fmt.Sprintf("%d file(s) could not be migrated", stats.Failed)
	}
	st.DB.Exec(`UPDATE storage_migrations SET status=?,total=?,done=?,failed=?,bytes=?,error=?,finished_at=CURRENT_TIMESTAMP WHERE id=?`, status, stats.Total, stats.Done, stats.Failed, stats.Bytes, errText, mid)
	audit(action, outcome, detail)
	fmt.Fprintln(stdout, detail)
	if err != nil {
		return err
	}
	if stats.Failed > 0 {
		return fmt.Errorf("storage-migrate: %d file(s) failed; they stay readable in their old location", stats.Failed)
	}
	return nil
}

// tenantContext identifies a tenant of a multi-tenant installation (-tenant).
type tenantContext struct {
	id           string
	master, root []byte
}

// tenantStorage is the storage a tenant's server would use: what the platform
// assigned (its prefix in the platform default or its own bucket), or the
// tenant's own configuration when the platform lets the tenant manage it. The
// platform database is found two levels above the tenant's directory
// (DATA_DIR/tenants/<id>).
func tenantStorage(st *store.Store, tenantDir string, tc tenantContext) (storage.Config, error) {
	abs, err := filepath.Abs(tenantDir)
	if err != nil {
		return storage.Config{}, err
	}
	if filepath.Base(abs) != tc.id {
		return storage.Config{}, fmt.Errorf("storage-migrate: -data-dir must be the tenant's directory DATA_DIR/tenants/%s", tc.id)
	}
	platformDir := filepath.Dir(filepath.Dir(abs))
	p, err := platform.Open(platformDir, tc.master, false)
	if err != nil {
		return storage.Config{}, fmt.Errorf("platform database: %w", err)
	}
	defer p.Close()
	t, err := p.Tenant(tc.id)
	if err != nil {
		return storage.Config{}, fmt.Errorf("tenant %s not found in the platform database", tc.id)
	}
	sc, err := p.Storage(tc.id)
	if err != nil {
		return storage.Config{}, err
	}
	if !t.AllowPrivateTargets {
		// The SSRF guard of the tenant's server applies offline too.
		st.Backends().Guard = netguard.DialContext
	}
	if sc.TenantManaged {
		if own := storage.FromSettings(st.Setting); own.Backend != storage.KindLocal {
			own.TenantManaged = true
			return own, nil
		}
	}
	return p.Resolve(tc.id, sc), nil
}
