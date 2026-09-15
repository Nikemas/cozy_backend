package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Nikemas/cozy_backend/internal/apperr"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/httpapi"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/staff"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return err
	}

	mux := http.NewServeMux()
	registerHealthRoutes(mux, db)
	registerAPIRoutes(mux, db, cfg)
	registerAdminRoutes(mux, db)
	registerWebRoutes(mux, db)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// registerHealthRoutes wires liveness/readiness checks used by the deploy
// pipeline and load balancer.
func registerHealthRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /readyz", apperr.Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if err := db.PingContext(r.Context()); err != nil {
			return apperr.New(http.StatusServiceUnavailable, "db_unavailable", "database unreachable")
		}
		w.WriteHeader(http.StatusOK)
		return nil
	}))
}

// registerAPIRoutes mounts /api/v1/* — JSON REST for the Flutter app and
// HTMX/AJAX calls from the site. Handlers land here as each domain package
// (auth, catalog, orders, ...) is implemented.
func registerAPIRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config) {
	sms := notify.NewNikitaClient(cfg.NikitaAPIKey)
	authSvc := auth.NewService(db, sms, []byte(cfg.JWTSecret))
	httpapi.RegisterAuthRoutes(mux, authSvc)
	httpapi.RegisterCatalogRoutes(mux, db)
}

// registerAdminRoutes mounts /admin/* — html/template pages behind a staff
// session, RBAC-gated per internal/auth.
func registerAdminRoutes(mux *http.ServeMux, db *sql.DB) {
	staffSvc := staff.NewService(db)
	staff.RegisterRoutes(mux, staffSvc)
}

// registerWebRoutes mounts / — the public html/template storefront.
func registerWebRoutes(mux *http.ServeMux, db *sql.DB) {
	_ = db
}
