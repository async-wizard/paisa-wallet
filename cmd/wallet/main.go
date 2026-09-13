// Command wallet runs the Paisa Wallet HTTP service.
//
// Run with -healthcheck to probe a running instance's /healthz and exit 0 if it is healthy.
// The container image is distroless (no shell, no curl), so the Docker HEALTHCHECK has to
// be the binary itself.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/async-wizard/paisa-wallet/internal/api"
	"github.com/async-wizard/paisa-wallet/internal/obs"
	"github.com/async-wizard/paisa-wallet/internal/store"
	"github.com/async-wizard/paisa-wallet/internal/wallet"
	"github.com/async-wizard/paisa-wallet/migrations"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe the local /healthz endpoint and exit")
	flag.Parse()

	port := envOr("PORT", "8080")
	if *healthcheck {
		os.Exit(probe(port))
	}

	logs := obs.NewStream()
	slog.SetDefault(obs.NewLogger(io.MultiWriter(os.Stdout, logs)))

	if err := run(port, logs); err != nil {
		slog.Error("service stopped", "err", err)
		os.Exit(1)
	}
}

func run(port string, logs *obs.Stream) error {
	// Cloud Run and docker stop both send SIGTERM before killing the container.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	maxConns, err := envInt("DB_MAX_CONNS", 6)
	if err != nil {
		return err
	}
	adminToken, err := loadAdminToken()
	if err != nil {
		return err
	}

	pool, err := store.Open(ctx, dsn, int32(maxConns))
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.Migrate(ctx, pool, migrations.FS); err != nil {
		return err
	}

	svc := wallet.NewService(pool)
	obs.Registry.MustRegister(api.NewInvariantsCollector(svc))

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api.NewRouter(svc, adminToken, logs),
		ReadHeaderTimeout: 5 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	slog.Info("service started", "port", port, "version", version, "db_max_conns", maxConns)

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func probe(port string) int {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck failed:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck failed: status", resp.StatusCode)
		return 1
	}
	return 0
}

// devAdminToken is the faucet token docker-compose uses locally. It is public (it's in the
// repo), so the service refuses it unless APP_ENV=local says this is a local stack.
const devAdminToken = "local-dev-admin-token-do-not-deploy"

// loadAdminToken returns the faucet token. The faucet mints money, so a missing, short or
// publicly known token stops the service from starting rather than exposing it.
func loadAdminToken() (string, error) {
	token := os.Getenv("ADMIN_TOKEN")
	if len(token) < 32 {
		return "", errors.New("ADMIN_TOKEN must be set to at least 32 characters")
	}
	if token == devAdminToken && os.Getenv("APP_ENV") != "local" {
		return "", errors.New("ADMIN_TOKEN is the local development default; set a real secret or APP_ENV=local")
	}
	return token, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", key, v)
	}
	return n, nil
}
