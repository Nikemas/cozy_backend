package storefront

import (
	"context"
	"testing"
)

// SetName's blank-name check runs before any database access, so it's
// testable with a nil *sql.DB — the rest of SetName (and every other
// CustomerRepo method) needs a live Postgres, not available in this
// environment (see the Task 4 handoff report).
func TestSetNameRejectsBlankName(t *testing.T) {
	r := NewCustomerRepo(nil)
	if err := r.SetName(context.Background(), "some-id", "   "); err == nil {
		t.Fatal("SetName with a blank name: got nil error, want one")
	}
}
