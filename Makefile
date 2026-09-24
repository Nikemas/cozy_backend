-include .env
export

# golang-migrate is not a module dependency, so run it at a pinned version
# (same as the migrate/migrate image in scripts/deploy.sh and CI).
MIGRATE := go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3

# Integration tests create/drop their own cozy_it_* databases through this
# URL (a role allowed to CREATE DATABASE). Default matches
# docker/docker-compose.yml.
TEST_DATABASE_URL ?= postgres://cozy:cozy@localhost:5432/postgres?sslmode=disable

.PHONY: dev-up dev-down run test test-integration migrate-up migrate-down migrate-version migrate-force migrate-new

dev-up:
	docker compose -f docker/docker-compose.yml up -d

dev-down:
	docker compose -f docker/docker-compose.yml down

run:
	go run ./cmd/server

test:
	go test ./...

test-integration:
	go test -tags integration -race -count=1 ./internal/integration/...

migrate-up:
	$(MIGRATE) -database "$(DATABASE_URL)" -path migrations up

migrate-down:
	$(MIGRATE) -database "$(DATABASE_URL)" -path migrations down 1

migrate-version:
	$(MIGRATE) -database "$(DATABASE_URL)" -path migrations version

# usage: make migrate-force version=21 — mark a dirty DB as clean at that
# version without running anything (see README "Миграции: dirty и восстановление").
migrate-force:
	$(MIGRATE) -database "$(DATABASE_URL)" -path migrations force $(version)

# usage: make migrate-new name=create_something
migrate-new:
	$(MIGRATE) create -ext sql -dir migrations -seq $(name)
