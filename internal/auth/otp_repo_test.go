package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/apperr"
)

var testSendLimits = otpSendLimits{Cooldown: time.Minute, PerPhone: 5, PerIP: 30, GlobalDay: 1000}

func newMockOTPRepo(t *testing.T) (*otpRepo, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return newOTPRepo(db), mock
}

func expectLockAndPhone(mock sqlmock.Sqlmock, lastAt any, phoneHour int) {
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock\(hashtextextended\('otp:' \|\| \$1, 0\)\)`).
		WithArgs("+996700123456").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT max\(created_at\), count\(\*\)`).
		WithArgs("+996700123456").
		WillReturnRows(sqlmock.NewRows([]string{"max", "count"}).AddRow(lastAt, phoneHour))
}

func wantAppErr(t *testing.T, err error, status int, code string) {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Status != status || ae.Code != code {
		t.Fatalf("err = %#v, want %d %s", err, status, code)
	}
}

func TestReserveInsertsWhenAllLimitsPass(t *testing.T) {
	repo, mock := newMockOTPRepo(t)
	expectLockAndPhone(mock, nil, 0)
	mock.ExpectQuery(`FROM otp_codes WHERE request_ip = \$1`).WithArgs("198.51.100.7").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(29))
	mock.ExpectQuery(`FROM otp_codes WHERE created_at >= now\(\) - interval '24 hours'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(999))
	mock.ExpectQuery(`INSERT INTO otp_codes .* VALUES \(\$1, \$2, '', \$3, \$4\)`).
		WithArgs("+996700123456", "tx-1", sqlmock.AnyArg(), "198.51.100.7").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("otp-1"))
	mock.ExpectCommit()

	id, err := repo.reserve(context.Background(), "+996700123456", "198.51.100.7", "tx-1", time.Now().Add(time.Minute), testSendLimits)
	if err != nil || id != "otp-1" {
		t.Fatalf("reserve = %q, %v", id, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestReserveCooldownAndPhoneLimit(t *testing.T) {
	repo, mock := newMockOTPRepo(t)
	expectLockAndPhone(mock, time.Now().Add(-10*time.Second), 1)
	mock.ExpectRollback()
	_, err := repo.reserve(context.Background(), "+996700123456", "", "tx", time.Now(), testSendLimits)
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_cooldown")

	expectLockAndPhone(mock, time.Now().Add(-10*time.Minute), 5)
	mock.ExpectRollback()
	_, err = repo.reserve(context.Background(), "+996700123456", "", "tx", time.Now(), testSendLimits)
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_rate_limited")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestReservePerIPLimit(t *testing.T) {
	repo, mock := newMockOTPRepo(t)
	expectLockAndPhone(mock, nil, 0)
	mock.ExpectQuery(`FROM otp_codes WHERE request_ip = \$1`).WithArgs("198.51.100.7").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(30))
	mock.ExpectRollback()
	_, err := repo.reserve(context.Background(), "+996700123456", "198.51.100.7", "tx", time.Now(), testSendLimits)
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_rate_limited")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestReserveGlobalDailyLimitAndNoIP(t *testing.T) {
	repo, mock := newMockOTPRepo(t)
	// No client IP: the per-IP query is skipped entirely.
	expectLockAndPhone(mock, nil, 0)
	mock.ExpectQuery(`interval '24 hours'`).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1000))
	mock.ExpectRollback()
	_, err := repo.reserve(context.Background(), "+996700123456", "", "tx", time.Now(), testSendLimits)
	wantAppErr(t, err, http.StatusTooManyRequests, "otp_daily_limit")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestTakeAttemptAndConsume(t *testing.T) {
	repo, mock := newMockOTPRepo(t)
	mock.ExpectQuery(`SET verify_attempts = verify_attempts \+ 1\s+WHERE id = \$1 AND consumed_at IS NULL AND verify_attempts < \$2`).
		WithArgs("otp-1", 5).WillReturnRows(sqlmock.NewRows([]string{"verify_attempts"}).AddRow(3))
	mock.ExpectQuery(`SET verify_attempts`).WithArgs("otp-1", 5).WillReturnRows(sqlmock.NewRows([]string{"verify_attempts"}))
	mock.ExpectExec(`SET consumed_at = now\(\) WHERE id = \$1 AND consumed_at IS NULL`).
		WithArgs("otp-1").WillReturnResult(sqlmock.NewResult(0, 0))

	n, ok, err := repo.takeAttempt(context.Background(), "otp-1", 5)
	if err != nil || !ok || n != 3 {
		t.Fatalf("takeAttempt = %d %v %v", n, ok, err)
	}
	if _, ok, err := repo.takeAttempt(context.Background(), "otp-1", 5); err != nil || ok {
		t.Fatalf("exhausted takeAttempt ok=%v err=%v", ok, err)
	}
	if consumed, err := repo.consume(context.Background(), "otp-1"); err != nil || consumed {
		t.Fatalf("second consume = %v %v, want false", consumed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
