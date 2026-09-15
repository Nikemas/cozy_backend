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
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/staff"
	"github.com/Nikemas/cozy_backend/internal/web"
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

	var sms notify.OTPSender
	if cfg.SMSMockOTP {
		if cfg.Env == "prod" {
			return errors.New("SMS_MOCK_OTP is set but APP_ENV=prod — refusing to start with a fake OTP provider in production")
		}
		slog.Warn("notify: SMS_MOCK_OTP is on — every login accepts code 0000, no real SMS is sent. Never set this in production.")
		sms = notify.NewMockClient()
	} else {
		sms = notify.NewNikitaClient(cfg.NikitaAPIKey)
	}
	authSvc := auth.NewService(db, sms, []byte(cfg.JWTSecret))

	mediaClient, err := media.NewClient(cfg)
	if err != nil {
		return err
	}
	// A MinIO outage at startup shouldn't stop the whole backend from
	// booting — the site/API keep working, only photo uploads would fail
	// until this is resolved — so log and continue rather than returning
	// an error here.
	ensureBucketCtx, cancelEnsureBucket := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelEnsureBucket()
	if err := mediaClient.EnsureBucket(ensureBucketCtx); err != nil {
		slog.Warn("minio bucket check/create failed at startup; photo uploads will not work until this is resolved", "err", err)
	}

	mux := http.NewServeMux()
	registerHealthRoutes(mux, db)
	registerAPIRoutes(mux, db, authSvc)
	registerAdminRoutes(mux, db, mediaClient)
	if err := registerWebRoutes(mux, db, cfg, authSvc); err != nil {
		return err
	}

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
func registerAPIRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service) {
	httpapi.RegisterAuthRoutes(mux, authSvc)
	httpapi.RegisterCatalogRoutes(mux, db)
	httpapi.RegisterOrderRoutes(mux, db, authSvc)
	httpapi.RegisterCustomerRoutes(mux, db, authSvc)
}

// registerAdminRoutes mounts /admin/* — html/template pages behind a staff
// session, RBAC-gated per internal/auth.
func registerAdminRoutes(mux *http.ServeMux, db *sql.DB, mediaClient *media.Client) {
	staffSvc := staff.NewService(db)
	staff.RegisterRoutes(mux, staffSvc)
	media.RegisterRoutes(mux, mediaClient, staffSvc)
	httpapi.RegisterAdminCatalogRoutes(mux, db, staffSvc)
	httpapi.RegisterAdminOrdersRoutes(mux, db, staffSvc)
}

// registerWebRoutes mounts / — the public html/template storefront.
func registerWebRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config, authSvc *auth.Service) error {
	return web.RegisterRoutes(mux, db, cfg, authSvc)
}
