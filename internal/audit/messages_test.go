package audit

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRecordStoresMessageInDetails(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	mock.ExpectExec(`INSERT INTO audit_log`).
		WithArgs(nil, ActionProductActivate, EntityProduct, "p1", "Товар «Кеды» активирован (массово)",
			`{"bulk":true,"msg_args":{"name":"Кеды"},"msg_key":"`+MsgProductActivated+`","msg_via":"bulk"}`, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	details := map[string]any{"bulk": true}
	New(db).Record(context.Background(), Entry{
		Action: ActionProductActivate, EntityType: EntityProduct, EntityID: "p1", Summary: "Товар «Кеды» активирован (массово)",
		Details: details, MsgKey: MsgProductActivated, MsgArgs: Args{"name": "Кеды"}, MsgVia: ViaBulk,
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
	if len(details) != 1 {
		t.Errorf("Entry.Details was modified: %v", details)
	}
}

func TestCategoryMessage(t *testing.T) {
	cases := []struct {
		action, name, wantKey string
		wantArgs              bool
	}{
		{ActionCategoryCreate, "Кеды", MsgCategoryCreated, true},
		{ActionCategoryUpdate, "Кеды", MsgCategoryUpdated, true},
		{ActionCategoryDelete, "Кеды", MsgCategoryDeleted, true},
		{ActionCategoryDelete, "", MsgCategoryDeletedNoName, false},
		{ActionPointCreate, "x", "", false},
	}
	for _, c := range cases {
		key, args := CategoryMessage(c.action, c.name)
		if key != c.wantKey || (args != nil) != c.wantArgs {
			t.Errorf("CategoryMessage(%s, %q) = %q, %v", c.action, c.name, key, args)
		}
	}
}
