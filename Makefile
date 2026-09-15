include .env
export

MIGRATE := go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate

.PHONY: dev-up dev-down run migrate-up migrate-down migrate-new

dev-up:
	docker compose -f docker/docker-compose.yml up -d

dev-down:
	docker compose -f docker/docker-compose.yml down

run:
	go run ./cmd/server

migrate-up:
	$(MIGRATE) -database "$(DATABASE_URL)" -path migrations up

migrate-down:
	$(MIGRATE) -database "$(DATABASE_URL)" -path migrations down 1

# usage: make migrate-new name=create_something
migrate-new:
	$(MIGRATE) create -ext sql -dir migrations -seq $(name)
