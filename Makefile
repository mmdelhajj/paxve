# Paxve developer tasks.
TEST_PG_PORT ?= 55432
TEST_DATABASE_URL ?= postgres://postgres:testpw@127.0.0.1:$(TEST_PG_PORT)/postgres?sslmode=disable

.PHONY: build vet test test-db test-db-stop web deploy

build:
	go build -o bin/paxve ./cmd/paxve

vet:
	go vet ./...

# Integration tests against a throwaway PostgreSQL 18 container (never the production database).
test: test-db
	TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -count=1 ./... ; status=$$?; $(MAKE) test-db-stop; exit $$status

test-db:
	@docker rm -f paxve-test-pg >/dev/null 2>&1 || true
	docker run -d --rm --name paxve-test-pg -e POSTGRES_PASSWORD=testpw -p 127.0.0.1:$(TEST_PG_PORT):5432 postgres:18 >/dev/null
	@for i in $$(seq 1 30); do docker exec paxve-test-pg pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
	@sleep 1

test-db-stop:
	@docker rm -f paxve-test-pg >/dev/null 2>&1 || true

web:
	cd web && npm ci && node scripts/check-i18n.mjs && npm run build

i18n:
	cd web && node scripts/check-i18n.mjs

# End-to-end browser tests (Playwright in Docker) against a running site.
# E2E_BASE_URL defaults to the live site; each run signs up a throwaway trial tenant.
E2E_BASE_URL ?= https://paxve.com
e2e:
	docker run --rm --ipc=host -v $(CURDIR)/web:/web -w /web/e2e -e E2E_BASE_URL=$(E2E_BASE_URL) \
		mcr.microsoft.com/playwright:v1.65.1-noble sh -c "npm install --no-audit --no-fund --silent && node node_modules/@playwright/test/cli.js test $(E2E_ARGS)"

deploy:
	docker compose up -d --build app
