package auth

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

const otpCleanupQuery = `DELETE FROM otp_codes WHERE created_at < $1`

func TestDeleteOTPCodesBeforeUsesCutoff(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	cutoff := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta(otpCleanupQuery)).WithArgs(cutoff).WillReturnResult(sqlmock.NewResult(0, 7))

	n, err := deleteOTPCodesBefore(context.Background(), db, cutoff)
	if err != nil || n != 7 {
		t.Fatalf("got %d, %v; want 7, nil", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestRunOTPCleanupPurgesOnStartAndStopsOnCancel(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	mock.ExpectExec(regexp.QuoteMeta(otpCleanupQuery)).WithArgs(now.Add(-retention)).WillReturnResult(sqlmock.NewResult(0, 3))

	ctx, cancel := context.WithCancel(context.Background())
	done := runOTPCleanup(ctx, db, retention, time.Hour, func() time.Time { return now })

	deadline := time.After(2 * time.Second)
	for mock.ExpectationsWereMet() != nil {
		select {
		case <-deadline:
			t.Fatalf("first cleanup pass did not run: %v", mock.ExpectationsWereMet())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup loop did not stop after cancel")
	}
}

func TestRunOTPCleanupSurvivesDBError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec(regexp.QuoteMeta(otpCleanupQuery)).WillReturnError(errors.New("db down"))
	mock.ExpectExec(regexp.QuoteMeta(otpCleanupQuery)).WillReturnResult(sqlmock.NewResult(0, 0))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runOTPCleanup(ctx, db, time.Hour, 10*time.Millisecond, time.Now)

	deadline := time.After(2 * time.Second)
	for mock.ExpectationsWereMet() != nil {
		select {
		case <-deadline:
			t.Fatalf("loop stopped after a failed pass: %v", mock.ExpectationsWereMet())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
