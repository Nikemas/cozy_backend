package dbtx

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestWithTx(t *testing.T) {
	errFn := errors.New("fn failed")
	errBegin := errors.New("begin failed")
	errCommit := errors.New("commit failed")

	tests := []struct {
		name    string
		expect  func(m sqlmock.Sqlmock)
		fnErr   error
		wantErr error
		wantRan bool
	}{
		{
			name: "commits when fn succeeds",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("UPDATE t").WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectCommit()
			},
			wantRan: true,
		},
		{
			name: "rolls back and returns fn's error unchanged",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("UPDATE t").WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectRollback()
			},
			fnErr:   errFn,
			wantErr: errFn,
			wantRan: true,
		},
		{
			name:    "begin failure skips fn",
			expect:  func(m sqlmock.Sqlmock) { m.ExpectBegin().WillReturnError(errBegin) },
			wantErr: errBegin,
		},
		{
			name: "commit failure is returned",
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("UPDATE t").WillReturnResult(sqlmock.NewResult(0, 1))
				m.ExpectCommit().WillReturnError(errCommit)
			},
			wantErr: errCommit,
			wantRan: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			tc.expect(mock)

			ran := false
			err = WithTx(context.Background(), db, func(tx *sql.Tx) error {
				ran = true
				if _, err := tx.Exec("UPDATE t SET x = 1"); err != nil {
					return err
				}
				return tc.fnErr
			})

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if ran != tc.wantRan {
				t.Fatalf("fn ran = %v, want %v", ran, tc.wantRan)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
