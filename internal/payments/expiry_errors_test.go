package payments

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/Nikemas/cozy_backend/internal/orders"
)

const expiryTestTTL = 30 * time.Minute

var errExpiryDB = errors.New("db down")

func expectExpiryScan(mock sqlmock.Sqlmock) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta("SELECT o.id FROM orders o WHERE"))
}

func TestExpirePendingScanFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(sqlmock.Sqlmock)
	}{
		{"query error", func(m sqlmock.Sqlmock) { expectExpiryScan(m).WillReturnError(errExpiryDB) }},
		{"scan error", func(m sqlmock.Sqlmock) {
			expectExpiryScan(m).WillReturnRows(sqlmock.NewRows([]string{"id", "extra"}).AddRow("o1", "x"))
		}},
		{"rows error", func(m sqlmock.Sqlmock) {
			expectExpiryScan(m).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("o1").RowError(0, errExpiryDB))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})
			tt.setup(mock)

			n, err := svc.ExpirePending(context.Background(), expiryTestTTL)

			if err == nil || n != 0 {
				t.Fatalf("ExpirePending = %d, %v; want 0 and an error", n, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}

// A failing or vanished order must not stop the batch: the next id is
// still processed and only real expiries are counted.
func TestExpirePendingSkipsBadRowsAndContinues(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})
	expectExpiryScan(mock).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("gone").AddRow("lock-fails").AddRow("exists-fails").AddRow("cancel-fails"))

	// gone: deleted between scan and lock → silently skipped.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("gone").WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	// lock-fails: a real DB error → logged, rolled back, batch continues.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("lock-fails").WillReturnError(errExpiryDB)
	mock.ExpectRollback()

	// exists-fails: the re-check query errors.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("exists-fails").
		WillReturnRows(orderLockRows(orders.StatusPlaced, StatusPending))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS")).WillReturnError(errExpiryDB)
	mock.ExpectRollback()

	// cancel-fails: still abandoned, but cancelling errors.
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("cancel-fails").
		WillReturnRows(orderLockRows(orders.StatusPlaced, StatusPending))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS")).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta("FOR UPDATE")).WithArgs("cancel-fails").WillReturnError(errExpiryDB)
	mock.ExpectRollback()

	n, err := svc.ExpirePending(context.Background(), expiryTestTTL)

	if err != nil || n != 0 {
		t.Fatalf("ExpirePending = %d, %v; want 0, nil", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestExpirePendingStopsWhenContextCancelled(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})
	expectExpiryScan(mock).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("o1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	n, err := svc.ExpirePending(ctx, expiryTestTTL)

	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("ExpirePending = %d, %v; want 0, context.Canceled", n, err)
	}
}

func TestRunPendingExpiryDefaultsIntervalAndSurvivesErrors(t *testing.T) {
	svc, mock := newMockService(t, NewMockProvider("", "tok"), &fakeCreator{})
	expectExpiryScan(mock).WillReturnError(errExpiryDB)
	ctx, cancel := context.WithCancel(context.Background())

	done := RunPendingExpiry(ctx, svc, time.Minute, 0)
	deadline := time.Now().Add(2 * time.Second)
	for mock.ExpectationsWereMet() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunPendingExpiry did not stop after cancel")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}
