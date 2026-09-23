package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func serve(t *testing.T, checks ...Check) (*httptest.ResponseRecorder, readyBody) {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux, 200*time.Millisecond, checks...)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body readyBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /readyz body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestHealthzAlwaysOK(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, time.Second, Check{Name: "x", Fn: func(context.Context) error { return errors.New("down") }})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 regardless of dependencies", rec.Code)
	}
}

func TestReadyzDBAndMinIOHealthy(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing()

	minio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/minio/health/live" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer minio.Close()

	rec, body := serve(t, DBCheck(db), MinIOCheck(minio.Client(), strings.TrimPrefix(minio.URL, "http://"), false))
	if rec.Code != http.StatusOK || body.Status != "ok" || body.Checks["db"] != "ok" || body.Checks["minio"] != "ok" {
		t.Fatalf("status=%d body=%+v, want 200 with both checks ok", rec.Code, body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReadyzDBDown(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectPing().WillReturnError(errors.New("connection refused host=secret"))

	rec, body := serve(t, DBCheck(db))
	if rec.Code != http.StatusServiceUnavailable || body.Checks["db"] != "error" {
		t.Fatalf("status=%d body=%+v, want 503 with db=error", rec.Code, body)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("error detail leaked into response: %s", rec.Body.String())
	}
}

func TestReadyzMinIOUnhealthyStatus(t *testing.T) {
	minio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer minio.Close()

	rec, body := serve(t, MinIOCheck(minio.Client(), strings.TrimPrefix(minio.URL, "http://"), false))
	if rec.Code != http.StatusServiceUnavailable || body.Checks["minio"] != "error" {
		t.Fatalf("status=%d body=%+v, want 503 with minio=error", rec.Code, body)
	}
}

func TestReadyzCheckTimesOut(t *testing.T) {
	slow := Check{Name: "slow", Fn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	start := time.Now()
	rec, body := serve(t, slow)
	if rec.Code != http.StatusServiceUnavailable || body.Checks["slow"] != "error" {
		t.Fatalf("status=%d body=%+v, want 503 for a hung check", rec.Code, body)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("readyz took %v, want it bounded by the per-check timeout", elapsed)
	}
}
