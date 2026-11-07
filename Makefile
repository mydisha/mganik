# Pintu tunggal tugas harian. Jalankan `make` untuk daftar perintah.

COMPOSE ?= docker compose
DB_PORT ?= 55432
API_PORT ?= 8089
DEV_DATABASE_URL ?= postgres://mganik:mganik@localhost:$(DB_PORT)/mganik?sslmode=disable

.DEFAULT_GOAL := help
.PHONY: help up down logs ps clean db migrate api web test test-api test-web lint gen hooks

help: ## daftar perintah
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*## "} {printf "  %-10s %s\n", $$1, $$2}'

up: ## build dan jalankan seluruh stack di background (db, migrate, api, web)
	$(COMPOSE) up --build -d
	@echo "Web http://localhost:8088  ·  API http://localhost:8089  (default, lihat .env)"

down: ## hentikan stack, data DB tetap ada
	$(COMPOSE) down

logs: ## ikuti log semua service
	$(COMPOSE) logs -f --tail=100

ps: ## status container
	$(COMPOSE) ps -a

clean: ## hentikan stack DAN hapus volume database proyek ini
	$(COMPOSE) down -v --remove-orphans

db: ## hanya Postgres, untuk development lokal
	$(COMPOSE) up -d db

migrate: ## jalankan migrasi ke DB development
	cd apps/api && DATABASE_URL='$(DEV_DATABASE_URL)' go run ./cmd/api migrate

api: ## backend dengan hot reload (air) di port API_PORT; stack compose harus mati
	cd apps/api && export DATABASE_URL='$(DEV_DATABASE_URL)' HTTP_ADDR=':$(API_PORT)' LOG_FORMAT=text APP_ENV=development; \
	if command -v air >/dev/null 2>&1; then air; else go run ./cmd/api serve; fi

web: ## Vite dev server (proxy ke API_PORT)
	pnpm --filter web dev

test: test-api test-web ## semua test

test-api: ## test Go dengan race detector
	cd apps/api && go test -race -count=1 ./...

test-web: ## test frontend
	pnpm --filter web test

lint: ## gofmt, go vet, typecheck
	cd apps/api && test -z "$$(gofmt -l .)" && go vet ./...
	pnpm --filter web typecheck

gen: ## generate tipe TypeScript dari packages/contracts/openapi.yaml
	pnpm --filter web gen:api

hooks: ## aktifkan hook git repo ini (sekali per clone)
	git config core.hooksPath .githooks
