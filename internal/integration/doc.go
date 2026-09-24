// Package integration runs the real service code (orders, cart, ...)
// against a real PostgreSQL with every migration from migrations/ applied.
// Unit tests elsewhere use sqlmock, which cannot catch a typo in a column
// name, a missing migration or a constraint the SQL violates; these tests
// can.
//
// The tests live behind the `integration` build tag, so `go test ./...`
// never needs a database. To run them:
//
//	TEST_DATABASE_URL='postgres://user:pass@localhost:5432/postgres?sslmode=disable' \
//	  go test -tags integration -count=1 ./internal/integration/...
//
// TEST_DATABASE_URL must point at a role allowed to CREATE DATABASE: each
// run creates throwaway databases named cozy_it_*, applies the migrations
// there and drops them at the end, so it never touches existing data. CI
// runs this in the "migrations + integration" job (.github/workflows/ci.yml).
//
// This file has no build tag on purpose: it keeps the package non-empty
// for `go vet ./...` / golangci-lint without the tag.
package integration
