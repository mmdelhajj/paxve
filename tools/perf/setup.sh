#!/usr/bin/env bash
# Throwaway performance database + app (never production): postgres on 127.0.0.1:55433, app on 127.0.0.1:18080
set -e
cd /srv/paxve
export PATH=$PATH:/usr/local/go/bin
docker rm -f paxve-perf-pg >/dev/null 2>&1 || true
pkill -f 'paxve-perf serve' || true
docker run -d --name paxve-perf-pg -e POSTGRES_PASSWORD=perfpw -p 127.0.0.1:55433:5432 --shm-size=256m postgres:18 \
  -c shared_buffers=512MB -c work_mem=32MB -c maintenance_work_mem=256MB -c max_wal_size=4GB -c synchronous_commit=off >/dev/null
for i in $(seq 1 40); do docker exec paxve-perf-pg pg_isready -U postgres >/dev/null 2>&1 && break; sleep 1; done
sleep 2
go build -o /tmp/paxve-perf ./cmd/paxve
export MIGRATE_DATABASE_URL='postgres://postgres:perfpw@127.0.0.1:55433/postgres?sslmode=disable'
export APP_DB_PASSWORD=perfapp
/tmp/paxve-perf migrate
export DATABASE_URL='postgres://paxve_app:perfapp@127.0.0.1:55433/postgres?sslmode=disable'
ADDR=127.0.0.1:18080 COOKIE_SECURE=false WEB_DIR=/nonexistent nohup /tmp/paxve-perf serve > /tmp/perf-app.log 2>&1 &
for i in $(seq 1 30); do curl -sf http://127.0.0.1:18080/api/health >/dev/null && break; sleep 1; done
rm -f /tmp/perf.jar
curl -s -c /tmp/perf.jar -H 'Content-Type: application/json' -X POST http://127.0.0.1:18080/api/v1/signup \
  -d '{"organisation":"Perf Org","company_name":"Perf Co","base_currency":"USD","local_currency":"LBP","template":"lebanon","full_name":"Perf Owner","email":"perf@example.com","password":"perf-password-1","locale":"en"}' | head -c 300; echo
CID=$(curl -s -b /tmp/perf.jar http://127.0.0.1:18080/api/v1/companies | python3 -c 'import sys,json; print(json.load(sys.stdin)[0]["id"])')
curl -s -b /tmp/perf.jar -H 'Content-Type: application/json' -X POST http://127.0.0.1:18080/api/v1/companies/$CID/plans \
  -d '{"name":"Perf 5A","billing_type":"fixed","currency":"USD","base_fee":"20"}' | head -c 200; echo
echo "CID=$CID"
echo $CID > /tmp/perf.cid
