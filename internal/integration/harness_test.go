//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// migrationsDir is relative to this package's directory, which is the
// working directory `go test` runs the test binary in.
const migrationsDir = "../../migrations"

// testDB is a fresh database with every up migration applied, shared by
// all tests in the package. Tests create their own uniquely named rows
// (see fixture.go) instead of truncating, so they can run in parallel.
var testDB *sql.DB

func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		fmt.Fprintln(os.Stderr, "integration: TEST_DATABASE_URL is not set (a role that may CREATE DATABASE, e.g. "+
			"postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable)")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, drop, err := createTempDB(ctx, adminURL, "main")
	if err != nil {
		fmt.Fprintln(os.Stderr, "integration:", err)
		return 1
	}
	defer drop()

	if err := migrate(ctx, db, "up"); err != nil {
		_ = db.Close()
		fmt.Fprintln(os.Stderr, "integration: applying migrations:", err)
		return 1
	}
	testDB = db
	defer func() { _ = testDB.Close() }()

	return m.Run()
}

// createTempDB creates an empty database cozy_it_<suffix>_<nanos> next to
// adminURL's database and returns a handle to it plus a func that closes
// nothing but drops it (callers close their own handle first).
func createTempDB(ctx context.Context, adminURL, suffix string) (*sql.DB, func(), error) {
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		return nil, nil, err
	}
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		return nil, nil, fmt.Errorf("connect to TEST_DATABASE_URL: %w", err)
	}

	name := fmt.Sprintf("cozy_it_%s_%d", suffix, time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		_ = admin.Close()
		return nil, nil, fmt.Errorf("create database %s: %w", name, err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		_ = admin.Close()
		return nil, nil, err
	}
	u.Path = "/" + name
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		_ = admin.Close()
		return nil, nil, err
	}

	drop := func() {
		_ = db.Close()
		dctx, dcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dcancel()
		// WITH (FORCE) (PG 13+): a test that leaked a connection must not
		// leave the database behind.
		if _, err := admin.ExecContext(dctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			fmt.Fprintf(os.Stderr, "integration: drop database %s: %v\n", name, err)
		}
		_ = admin.Close()
	}
	return db, drop, nil
}

var migrationFileRE = regexp.MustCompile(`^(\d+)_.+\.(up|down)\.sql$`)

type migrationFile struct {
	version int
	path    string
}

// migrationFiles lists migrations/*.<direction>.sql ordered by version:
// ascending for "up", descending for "down" — the order golang-migrate
// applies them in.
func migrationFiles(direction string) ([]migrationFile, error) {
	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return nil, err
	}
	var files []migrationFile
	for _, e := range entries {
		m := migrationFileRE.FindStringSubmatch(e.Name())
		if m == nil || m[2] != direction {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, err
		}
		files = append(files, migrationFile{version: v, path: filepath.Join(migrationsDir, e.Name())})
	}
	sort.Slice(files, func(i, j int) bool {
		if direction == "down" {
			return files[i].version > files[j].version
		}
		return files[i].version < files[j].version
	})
	for i := 1; i < len(files); i++ {
		if files[i].version == files[i-1].version {
			return nil, fmt.Errorf("two %s migrations with version %d", direction, files[i].version)
		}
	}
	return files, nil
}

// migrate applies every migration file in direction ("up" or "down") to
// db. Each file is sent as one multi-statement simple-protocol query, which
// is exactly how golang-migrate's postgres driver runs it (the file is
// implicitly one transaction; CREATE INDEX CONCURRENTLY would fail here
// just as it does in production).
func migrate(ctx context.Context, db *sql.DB, direction string) error {
	files, err := migrationFiles(direction)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no %s migrations found in %s", direction, migrationsDir)
	}
	for _, f := range files {
		body, err := os.ReadFile(f.path)
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f.path), err)
		}
	}
	return nil
}
