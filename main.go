package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mhenderik/sendora/internal/app"
	"github.com/mhenderik/sendora/internal/store"
)

//go:embed all:web/dist
var distFS embed.FS

var version = "dev"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	// A short signing secret could be guessed offline from any captured token.
	if sec, err := app.JWTSecretFromEnv(); err != nil {
		log.Fatal(err)
	} else if sec != "" && len(sec) < 32 {
		log.Fatal("JWT_SECRET must be at least 32 characters (leave it unset to use a generated secret)")
	}
	key, err := store.ReadDatabaseKey(os.Getenv("SENDORA_DB_KEY_FILE"))
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.OpenEncryptedRecovering(env("DATA_DIR", "./data"), key, os.Getenv("SENDORA_DB_INITIALIZE") == "true", log.Printf)
	if err != nil {
		log.Fatal(err)
	}
	static, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		log.Fatal(err)
	}
	srv := app.New(st, static, version)
	proxies, err := app.ParseTrustedProxies(os.Getenv("SENDORA_TRUSTED_PROXIES"))
	if err != nil {
		log.Fatal(err)
	}
	srv.SetTrustedProxies(proxies)
	if len(proxies) > 0 {
		log.Printf("trusting X-Forwarded-For/-Proto from %d configured proxy range(s)", len(proxies))
	}
	srv.SetForceLocalLogin(os.Getenv("SENDORA_FORCE_LOCAL_LOGIN") == "true")
	srv.LogLocalLoginState()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobsCtx, cancelJobs := context.WithCancel(ctx)
	defer cancelJobs()
	srv.RunJobs(jobsCtx)

	hs := &http.Server{
		Addr:              net.JoinHostPort(env("HOST", "127.0.0.1"), env("PORT", "8080")),
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// A confirmed database import stops serving and jobs, then swaps and re-execs.
	srv.SetImportStopper(func() {
		cancelJobs()
		sc, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if err := hs.Shutdown(sc); err != nil {
			hs.Close()
		}
	})
	go func() {
		<-ctx.Done()
		sc, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		hs.Shutdown(sc)
	}()
	log.Printf("sendora %s listening on %s", version, hs.Addr)
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	// During an applied import the swap/re-exec runs in a goroutine; do not exit.
	srv.AwaitImport()
}
