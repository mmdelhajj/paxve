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
	cd web && npm ci && npm run build

deploy:
	docker compose up -d --build app
