.PHONY: fmt vet test check-env check-db check-remnawave secrets-check build dev-db-up dev-db-down run docker-build

fmt:
	gofmt -w $$(find cmd internal migrations tests -name '*.go' -type f 2>/dev/null)

vet:
	go vet ./...

test:
	go test ./...

check-env:
	go run ./cmd/quota-api check-config

check-db:
	go run ./cmd/remna-quota check-db

check-remnawave:
	go run ./cmd/remna-quota check-remnawave

secrets-check:
	./scripts/secrets-check.sh

build:
	mkdir -p bin
	go build -trimpath -o bin/quota-api ./cmd/quota-api
	go build -trimpath -o bin/remna-quota ./cmd/remna-quota

dev-db-up:
	docker compose -f compose.dev.yaml up -d quota-postgres

dev-db-down:
	docker compose -f compose.dev.yaml down

run:
	go run ./cmd/quota-api

docker-build:
	docker build -t remna-quota:local .
