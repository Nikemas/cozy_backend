package staff

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
)

var staffColumns = []string{"id", "phone", "password_hash", "name", "role", "point_id", "is_active", "created_at"}

func newMockRepo(t *testing.T) (*Repo, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return NewRepo(db), mock
}

func staffRow(id, phone string, role Role, active bool) *sqlmock.Rows {
	return sqlmock.NewRows(staffColumns).AddRow(id, phone, "hash", "Name", string(role), nil, active, time.Now())
}

func TestRepoListScansRowsAndPropagatesErrors(t *testing.T) {
	t.Run("rows", func(t *testing.T) {
		repo, mock := newMockRepo(t)
		mock.ExpectQuery(`FROM staff\s+ORDER BY created_at DESC`).WillReturnRows(
			sqlmock.NewRows(staffColumns).
				AddRow("s1", "+996700000001", "h", "A", "owner", nil, true, time.Now()).
				AddRow("s2", "+996700000002", "h", "B", "point_staff", "p1", false, time.Now()))

		list, err := repo.List(context.Background())

		if err != nil || len(list) != 2 || list[1].PointID == nil || *list[1].PointID != "p1" {
			t.Fatalf("List = %+v, %v", list, err)
		}
	})
	t.Run("query error", func(t *testing.T) {
		repo, mock := newMockRepo(t)
		mock.ExpectQuery(`FROM staff`).WillReturnError(errors.New("boom"))
		if _, err := repo.List(context.Background()); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("scan error", func(t *testing.T) {
		repo, mock := newMockRepo(t)
		mock.ExpectQuery(`FROM staff`).WillReturnRows(
			sqlmock.NewRows(staffColumns).AddRow("s1", "p", "h", "A", "owner", nil, "not-a-bool", time.Now()))
		if _, err := repo.List(context.Background()); err == nil {
			t.Fatal("want scan error")
		}
	})
	t.Run("rows error", func(t *testing.T) {
		repo, mock := newMockRepo(t)
		mock.ExpectQuery(`FROM staff`).WillReturnRows(
			staffRow("s1", "+996700000001", RoleOwner, true).RowError(0, errors.New("broken")))
		if _, err := repo.List(context.Background()); err == nil {
			t.Fatal("want rows error")
		}
	})
}

func TestRepoCreateTranslatesErrors(t *testing.T) {
	in := StaffCreateInput{Phone: "+996700000009", PasswordHash: "h", Name: "N", Role: RolePointStaff}
	tests := []struct {
		name     string
		setup    func(sqlmock.Sqlmock)
		wantCode string
		wantErr  bool
	}{
		{
			name: "lookup failure",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`WHERE phone = \$1`).WillReturnError(errors.New("boom"))
			},
			wantErr: true,
		},
		{
			name: "same number in another format is taken",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`WHERE phone = \$1`).WillReturnRows(staffRow("s1", "0700000009", RolePointStaff, true))
			},
			wantCode: "phone_taken",
		},
		{
			name: "unique violation on insert",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`WHERE phone = \$1`).WillReturnRows(sqlmock.NewRows(staffColumns))
				m.ExpectQuery(`INSERT INTO staff`).WillReturnError(&pgconn.PgError{Code: pgUniqueViolation})
			},
			wantCode: "phone_taken",
		},
		{
			name: "unknown point",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`WHERE phone = \$1`).WillReturnRows(sqlmock.NewRows(staffColumns))
				m.ExpectQuery(`INSERT INTO staff`).WillReturnError(&pgconn.PgError{Code: pgForeignKeyViolation})
			},
			wantCode: "invalid_point_id",
		},
		{
			name: "other insert error passes through",
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectQuery(`WHERE phone = \$1`).WillReturnRows(sqlmock.NewRows(staffColumns))
				m.ExpectQuery(`INSERT INTO staff`).WillReturnError(errors.New("boom"))
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newMockRepo(t)
			tt.setup(mock)

			_, err := repo.Create(context.Background(), in)

			if tt.wantCode != "" {
				assertAppErrCode(t, err, tt.wantCode)
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestRepoCreateReturnsStoredRow(t *testing.T) {
	repo, mock := newMockRepo(t)
	mock.ExpectQuery(`WHERE phone = \$1`).WillReturnRows(sqlmock.NewRows(staffColumns))
	mock.ExpectQuery(`INSERT INTO staff`).WillReturnRows(staffRow("s9", "+996700000009", RolePointStaff, true))

	s, err := repo.Create(context.Background(), StaffCreateInput{Phone: "+996700000009", Role: RolePointStaff})

	if err != nil || s.ID != "s9" || !s.IsActive {
		t.Fatalf("Create = %+v, %v", s, err)
	}
}

func TestRepoUpdateLastOwnerInvariant(t *testing.T) {
	demote := StaffUpdateInput{Name: "Owner", Role: RoleManager, IsActive: true}
	tests := []struct {
		name     string
		in       StaffUpdateInput
		setup    func(sqlmock.Sqlmock)
		wantCode string
		wantErr  bool
		wantRole Role
	}{
		{
			name: "not found",
			in:   demote,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`FOR UPDATE`).WithArgs("s1").WillReturnError(sql.ErrNoRows)
				m.ExpectRollback()
			},
			wantCode: "staff_not_found",
		},
		{
			name: "select failure",
			in:   demote,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`FOR UPDATE`).WillReturnError(errors.New("boom"))
				m.ExpectRollback()
			},
			wantErr: true,
		},
		{
			name: "demoting the last owner is rejected",
			in:   demote,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`WHERE id = \$1\s+FOR UPDATE`).WillReturnRows(staffRow("s1", "+996700000001", RoleOwner, true))
				m.ExpectQuery(`role = 'owner' AND is_active = true AND id != \$1`).WithArgs("s1").
					WillReturnRows(sqlmock.NewRows([]string{"id"}))
				m.ExpectRollback()
			},
			wantCode: "last_owner",
		},
		{
			name: "owner count failure",
			in:   demote,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`WHERE id = \$1\s+FOR UPDATE`).WillReturnRows(staffRow("s1", "+996700000001", RoleOwner, true))
				m.ExpectQuery(`role = 'owner'`).WillReturnError(errors.New("boom"))
				m.ExpectRollback()
			},
			wantErr: true,
		},
		{
			name: "owner rows error",
			in:   demote,
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`WHERE id = \$1\s+FOR UPDATE`).WillReturnRows(staffRow("s1", "+996700000001", RoleOwner, true))
				m.ExpectQuery(`role = 'owner'`).WillReturnRows(
					sqlmock.NewRows([]string{"id"}).AddRow("s2").RowError(0, errors.New("broken")))
				m.ExpectRollback()
			},
			wantErr: true,
		},
		{
			name: "demote allowed with another active owner",
			in:   StaffUpdateInput{Name: "Owner", Role: RoleManager, IsActive: true, PasswordHash: strPtr("new")},
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`WHERE id = \$1\s+FOR UPDATE`).WillReturnRows(staffRow("s1", "+996700000001", RoleOwner, true))
				m.ExpectQuery(`role = 'owner'`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("s2"))
				m.ExpectQuery(`UPDATE staff`).WithArgs("s1", "Owner", RoleManager, nil, true, "new").
					WillReturnRows(staffRow("s1", "+996700000001", RoleManager, true))
				m.ExpectCommit()
			},
			wantRole: RoleManager,
		},
		{
			name: "unknown point on update",
			in:   StaffUpdateInput{Name: "S", Role: RolePointStaff, IsActive: true, PointID: strPtr("p404")},
			setup: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectQuery(`WHERE id = \$1\s+FOR UPDATE`).WillReturnRows(staffRow("s1", "+996700000001", RolePointStaff, true))
				m.ExpectQuery(`UPDATE staff`).WillReturnError(&pgconn.PgError{Code: pgForeignKeyViolation})
				m.ExpectRollback()
			},
			wantCode: "invalid_point_id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newMockRepo(t)
			tt.setup(mock)

			got, err := repo.Update(context.Background(), "s1", tt.in)

			switch {
			case tt.wantCode != "":
				assertAppErrCode(t, err, tt.wantCode)
			case tt.wantErr:
				if err == nil {
					t.Fatal("want error")
				}
			default:
				if err != nil || got.Role != tt.wantRole {
					t.Fatalf("Update = %+v, %v; want role %q", got, err, tt.wantRole)
				}
			}
		})
	}
}

func TestRepoSetPassword(t *testing.T) {
	tests := []struct {
		name     string
		result   func(sqlmock.Sqlmock)
		wantCode string
		wantErr  bool
	}{
		{"updated", func(m sqlmock.Sqlmock) {
			m.ExpectExec(`UPDATE staff SET password_hash`).WithArgs("s1", "h").WillReturnResult(sqlmock.NewResult(0, 1))
		}, "", false},
		{"missing row", func(m sqlmock.Sqlmock) {
			m.ExpectExec(`UPDATE staff SET password_hash`).WillReturnResult(sqlmock.NewResult(0, 0))
		}, "staff_not_found", false},
		{"exec error", func(m sqlmock.Sqlmock) {
			m.ExpectExec(`UPDATE staff SET password_hash`).WillReturnError(errors.New("boom"))
		}, "", true},
		{"rows affected error", func(m sqlmock.Sqlmock) {
			m.ExpectExec(`UPDATE staff SET password_hash`).WillReturnResult(sqlmock.NewErrorResult(errors.New("n/a")))
		}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, mock := newMockRepo(t)
			tt.result(mock)

			err := repo.SetPassword(context.Background(), "s1", "h")

			switch {
			case tt.wantCode != "":
				assertAppErrCode(t, err, tt.wantCode)
			case tt.wantErr:
				if err == nil {
					t.Fatal("want error")
				}
			case err != nil:
				t.Fatalf("SetPassword: %v", err)
			}
		})
	}
}
