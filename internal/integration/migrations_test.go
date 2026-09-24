//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMigrationsRoundTrip applies every up migration, every down
// migration (newest first), then every up migration again, on its own
// empty database — the same check CI runs with the migrate CLI. A down
// that forgets an object makes the second "up" fail with "already
// exists"; the leftover check below catches a down that forgets an object
// no later up recreates.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, drop, err := createTempDB(ctx, os.Getenv("TEST_DATABASE_URL"), "roundtrip")
	if err != nil {
		t.Fatal(err)
	}
	defer drop()

	if err := migrate(ctx, db, "up"); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := migrate(ctx, db, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	// After a full down only extensions' objects may remain in public.
	const leftoverQ = `
		SELECT 'table ' || c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'S')
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = c.oid AND d.deptype = 'e')
		UNION ALL
		SELECT 'type ' || t.typname FROM pg_type t
		JOIN pg_namespace n ON n.oid = t.typnamespace
		WHERE n.nspname = 'public' AND t.typtype = 'e'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.objid = t.oid AND d.deptype = 'e')`
	rows, err := db.QueryContext(ctx, leftoverQ)
	if err != nil {
		t.Fatal(err)
	}
	var leftovers []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		leftovers = append(leftovers, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(leftovers) > 0 {
		t.Errorf("objects left after all down migrations: %s", strings.Join(leftovers, ", "))
	}

	if err := migrate(ctx, db, "up"); err != nil {
		t.Fatalf("second up: %v", err)
	}
}
