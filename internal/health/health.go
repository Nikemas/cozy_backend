// Package health serves the liveness (/healthz) and readiness (/readyz)
// probes. /healthz only proves the process is up and serving HTTP;
// /readyz additionally checks every dependency (Postgres, MinIO) with a
// short per-check timeout, so a hung dependency makes the probe fail fast
// instead of hanging the caller (scripts/deploy.sh polls it through Caddy
// after each deploy).
package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Check is one named readiness dependency.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// DBCheck pings Postgres.
func DBCheck(db *sql.DB) Check {
	return Check{Name: "db", Fn: db.PingContext}
}

// MinIOCheck GETs MinIO's unauthenticated liveness endpoint
// (/minio/health/live) on endpoint ("host:port", the same value as
// MINIO_ENDPOINT — the backend-internal address, not the public one).
// client may be nil (http.DefaultClient); the request honors ctx's
// deadline either way.
func MinIOCheck(client *http.Client, endpoint string, useSSL bool) Check {
	if client == nil {
		client = http.DefaultClient
	}
	scheme := "http"
	if useSSL {
		scheme = "https"
	}
	url := scheme + "://" + endpoint + "/minio/health/live"
	return Check{Name: "minio", Fn: func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("minio health: status %d", resp.StatusCode)
		}
		return nil
	}}
}

type readyBody struct {
	Status string            `json:"status"` // "ok" | "unavailable"
	Checks map[string]string `json:"checks"` // name -> "ok" | "error"
}

// Register mounts GET /healthz and GET /readyz on mux. Each readiness
// check runs concurrently with its own timeout. Failures are logged with
// their error; the response only says which check failed, never the
// underlying error text (it may contain hostnames/DSNs).
func Register(mux *http.ServeMux, timeout time.Duration, checks ...Check) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("GET /readyz", Readiness(timeout, checks...))
}

// Readiness returns the /readyz handler: 200 when every check passes, 503
// otherwise, with a JSON body listing each check's result.
func Readiness(timeout time.Duration, checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := readyBody{Status: "ok", Checks: make(map[string]string, len(checks))}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
			wg.Add(1)
			go func(c Check) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), timeout)
				defer cancel()
				result := "ok"
				if err := c.Fn(ctx); err != nil {
					result = "error"
					slog.WarnContext(r.Context(), "readiness check failed", "check", c.Name, "err", err)
				}
				mu.Lock()
				body.Checks[c.Name] = result
				if result != "ok" {
					body.Status = "unavailable"
				}
				mu.Unlock()
			}(c)
		}
		wg.Wait()

		status := http.StatusOK
		if body.Status != "ok" {
			status = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
}
