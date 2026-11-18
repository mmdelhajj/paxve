set -e
cd /srv/paxve
export PATH=$PATH:/usr/local/go/bin
pkill -f 'paxve-perf serve' || true
go build -o /tmp/paxve-perf ./cmd/paxve
export MIGRATE_DATABASE_URL='postgres://postgres:perfpw@127.0.0.1:55433/postgres?sslmode=disable'
export APP_DB_PASSWORD=perfapp
/tmp/paxve-perf migrate 2>&1 | grep -v 'no migrations' | tail -3 || true
export DATABASE_URL='postgres://paxve_app:perfapp@127.0.0.1:55433/postgres?sslmode=disable'
ADDR=127.0.0.1:18080 COOKIE_SECURE=false WEB_DIR=/nonexistent nohup /tmp/paxve-perf serve > /tmp/perf-app.log 2>&1 &
for i in $(seq 1 30); do curl -sf http://127.0.0.1:18080/api/health >/dev/null && break; sleep 1; done
# wait for the ledger worker to catch up
for i in $(seq 1 120); do
  n=$(docker exec paxve-perf-pg psql -U postgres -d postgres -At -c "select coalesce(max(last_number),0) from ledger_watermark")
  m=$(docker exec paxve-perf-pg psql -U postgres -d postgres -At -c "select max(number) from journal_entries where status='posted'")
  [ "$n" = "$m" ] && break; sleep 2
done
echo "watermark $n / $m"; grep -i 'ledger' /tmp/perf-app.log | tail -3
docker exec paxve-perf-pg psql -U postgres -d postgres -At -c "select count(*), pg_size_pretty(pg_total_relation_size('ledger_daily')) from ledger_daily"
docker exec paxve-perf-pg psql -U postgres -d postgres -qc "ANALYZE"
