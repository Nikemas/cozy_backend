#!/usr/bin/env bash
# Combined unit + integration test coverage for the whole module, merged via
# `go tool covdata`. This is the number reported as "backend coverage"
# (unit tests alone cover much less: most handlers are exercised by the
# integration suite).
#
# Usage: bash scripts/coverage.sh [OUT_DIR]
# Needs a local Postgres for the integration tests; override TEST_DATABASE_URL
# if it is not postgres://$(whoami)@localhost:5432/postgres.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=${1:-${TMPDIR:-/tmp}/cozy_coverage}
: "${TEST_DATABASE_URL:=postgres://$(whoami)@localhost:5432/postgres?sslmode=disable}"
export TEST_DATABASE_URL
rm -rf "$OUT" && mkdir -p "$OUT/unit" "$OUT/it"
PKGS=$(go list ./... | grep -v -E '/scripts/|/locales$' | paste -sd, -)
go test -count=1 -cover -coverpkg="$PKGS" ./... -args -test.gocoverdir="$OUT/unit" >/dev/null
go test -tags integration -count=1 -cover -coverpkg="$PKGS" ./internal/integration/... \
  -args -test.gocoverdir="$OUT/it" >/dev/null
go tool covdata textfmt -i="$OUT/unit,$OUT/it" -o "$OUT/merged.out"
echo "profile: $OUT/merged.out (go tool cover -html=$OUT/merged.out)"
go tool cover -func="$OUT/merged.out" | tail -1
