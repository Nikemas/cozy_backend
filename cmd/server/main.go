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

	"github.com/Nikemas/cozy_backend/internal/admin"
	"github.com/Nikemas/cozy_backend/internal/auth"
	"github.com/Nikemas/cozy_backend/internal/config"
	"github.com/Nikemas/cozy_backend/internal/csrf"
	"github.com/Nikemas/cozy_backend/internal/health"
	"github.com/Nikemas/cozy_backend/internal/httpapi"
	"github.com/Nikemas/cozy_backend/internal/httpmw"
	"github.com/Nikemas/cozy_backend/internal/media"
	"github.com/Nikemas/cozy_backend/internal/notify"
	"github.com/Nikemas/cozy_backend/internal/orders"
	"github.com/Nikemas/cozy_backend/internal/payments"
	"github.com/Nikemas/cozy_backend/internal/points"
	"github.com/Nikemas/cozy_backend/internal/reqid"
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

	setupLogger(cfg.LogFormat)

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(cfg.DB.MaxOpenConns)
	db.SetMaxIdleConns(cfg.DB.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.DB.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.DB.ConnMaxIdleTime)

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

	// Must be installed before any orders.Service is constructed/used by
	// the route registrations below.
	notifier := buildNotifications(db, cfg)
	orders.SetDefaultNotifier(notifier)

	payProvider, err := payments.NewProvider(cfg)
	if err != nil {
		return err
	}
	if cfg.PaymentsProvider == config.PaymentsProviderMock {
		if cfg.Env == "prod" {
			slog.Warn("payments: PAYMENTS_PROVIDER=mock with APP_ENV=prod — anyone can mark online orders paid through the mock checkout page. Staging only; never on the live shop.")
		} else {
			slog.Info("payments: mock provider active — online_card orders are paid on a local test page", "checkout", cfg.PaymentsBaseURL()+payments.MockCheckoutPath+"{id}")
		}
	}

	mux := http.NewServeMux()
	health.Register(mux, 2*time.Second,
		health.DBCheck(db),
		health.MinIOCheck(nil, cfg.MinIOEndpoint, cfg.MinIOUseSSL),
	)
	registerAPIRoutes(mux, db, authSvc, cfg, payProvider)
	if err := registerAdminRoutes(mux, db, mediaClient, cfg); err != nil {
		return err
	}
	if err := registerWebRoutes(mux, db, cfg, authSvc); err != nil {
		return err
	}

	// Outermost first: every request gets an ID, then is access-logged
	// (after Recover has turned any panic into a 500 it can log), then
	// hits the existing CSRF guard and the router.
	handler := httpmw.Chain(csrf.Protect(mux), httpmw.RequestID, httpmw.AccessLog, httpmw.Recover)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	// Let in-flight push/Telegram jobs for just-committed orders finish.
	if nerr := notifier.Shutdown(shutdownCtx); nerr != nil {
		slog.Warn("notifications: shutdown timed out, some notifications may be lost", "err", nerr)
	}
	return err
}

// setupLogger installs the process-wide slog logger: text (default) or
// JSON lines, wrapped so any *Context log call made while serving a
// request carries that request's request_id (see internal/reqid).
func setupLogger(format string) {
	var h slog.Handler = slog.NewTextHandler(os.Stdout, nil)
	if format == "json" {
		h = slog.NewJSONHandler(os.Stdout, nil)
	}
	slog.SetDefault(slog.New(reqid.NewLogHandler(h)))
}

// registerAPIRoutes mounts /api/v1/* — JSON REST for the Flutter app and
// HTMX/AJAX calls from the site. Handlers land here as each domain package
// (auth, catalog, orders, ...) is implemented.
func registerAPIRoutes(mux *http.ServeMux, db *sql.DB, authSvc *auth.Service, cfg *config.Config, payProvider payments.Provider) {
	ordersSvc := orders.NewService(db)
	paySvc := payments.NewService(db, payProvider, ordersSvc, cfg.PaymentsBaseURL())

	httpapi.RegisterAuthRoutes(mux, authSvc)
	httpapi.RegisterCatalogRoutes(mux, db, cfg)
	httpapi.RegisterPublicPointsRoutes(mux, db)
	httpapi.RegisterOrderRoutes(mux, db, authSvc, cfg, ordersSvc, paySvc)
	httpapi.RegisterPaymentRoutes(mux, paySvc, cfg.PaymentsBaseURL())
	httpapi.RegisterCustomerRoutes(mux, db, authSvc)
	httpapi.RegisterAppConfigRoutes(mux, cfg)
}

// registerAdminRoutes mounts /admin/* — both the JSON API under
// /admin/api/* (Wave 3) and, as of Wave 4 Task 1, the html/template admin
// panel itself under /admin/* — behind a staff session, RBAC-gated per
// internal/staff.
func registerAdminRoutes(mux *http.ServeMux, db *sql.DB, mediaClient *media.Client, cfg *config.Config) error {
	staffSvc := staff.NewService(db)
	staff.RegisterRoutes(mux, staffSvc)
	media.RegisterRoutes(mux, mediaClient, staffSvc, cfg)
	httpapi.RegisterAdminCatalogRoutes(mux, db, staffSvc)
	points.RegisterRoutes(mux, db, staffSvc)
	httpapi.RegisterAdminOrdersRoutes(mux, db, staffSvc)
	httpapi.RegisterAdminReportsRoutes(mux, db, staffSvc)
	httpapi.RegisterAdminImportRoutes(mux, db, staffSvc)
	return admin.RegisterRoutes(mux, db, staffSvc, mediaClient, cfg)
}

// registerWebRoutes mounts / — the public html/template storefront.
func registerWebRoutes(mux *http.ServeMux, db *sql.DB, cfg *config.Config, authSvc *auth.Service) error {
	return web.RegisterRoutes(mux, db, cfg, authSvc)
}
