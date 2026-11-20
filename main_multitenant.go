package main

import (
	"context"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mhenderik/sendora/internal/app"
	"github.com/mhenderik/sendora/internal/platform"
	"github.com/mhenderik/sendora/internal/store"
)

// runMultiTenant serves all tenants and the platform console from one process
// (SENDORA_MULTI_TENANT=true, docs/MULTI-TENANT.md).
func runMultiTenant(dataDir string, key []byte) {
	for _, name := range []string{"JWT_SECRET", "JWT_SECRET_FILE", "APP_URL"} {
		if os.Getenv(name) != "" {
			log.Printf("WARNING: %s is ignored in multi-tenant mode (every tenant has its own signing secret and URL)", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, store.DBFile)); err == nil {
		log.Fatal("a single-tenant database (" + store.DBFile + ") is in the data directory: convert it first with 'sendora-db tenant-import' (docs/MULTI-TENANT.md) or use another DATA_DIR")
	}
	p, err := platform.Open(dataDir, key, os.Getenv("SENDORA_DB_INITIALIZE") == "true")
	clear(key)
	if err != nil {
		log.Fatal("platform database: ", err)
	}
	managed, err := p.ApplyEnv()
	if err != nil {
		log.Fatal(err)
	}
	static, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		log.Fatal(err)
	}
	proxies, err := app.ParseTrustedProxies(os.Getenv("SENDORA_TRUSTED_PROXIES"))
	if err != nil {
		log.Fatal(err)
	}
	h, err := app.NewTenantHost(app.MultiTenantConfig{
		Platform: p, BaseDomain: os.Getenv("SENDORA_BASE_DOMAIN"), Static: static, Version: version,
		TrustedProxies: proxies, ForceLocalLogin: os.Getenv("SENDORA_FORCE_LOCAL_LOGIN") == "true", Managed: managed,
	})
	if err != nil {
		log.Fatal(err)
	}
	ts, _ := p.Tenants("")
	log.Printf("multi-tenant mode: %d tenant(s), platform console at %s, sqlcipher=%s", len(ts), h.ConsoleURL(), p.St.CipherVersion())
	p.Audit(0, "system", "system_start", "platform", "ok", "version "+version, "")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobsCtx, cancelJobs := context.WithCancel(ctx)
	defer cancelJobs()
	h.Start(jobsCtx)
	hs := &http.Server{
		Addr:              net.JoinHostPort(env("HOST", "127.0.0.1"), env("PORT", "8080")),
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		cancelJobs()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := hs.Shutdown(sctx); err != nil {
			hs.Close()
		}
		cancel()
		h.Shutdown(5 * time.Second)
		close(stopped)
	}()
	log.Printf("sendora %s listening on %s", version, hs.Addr)
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-stopped
	p.Audit(0, "system", "system_stop", "platform", "ok", "", "")
	if err := p.Close(); err != nil {
		log.Printf("closing the platform database: %v", err)
	}
}
