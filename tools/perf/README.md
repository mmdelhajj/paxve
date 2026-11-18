# Performance test bench (never production)

Builds a throwaway PostgreSQL 18 (127.0.0.1:55433) and a Paxve instance (127.0.0.1:18080), fills one company with
50,000 customers, 600,000 bills, 500,000 payments and 2.2 million journal lines, and times the main pages.

```sh
cd /srv/paxve
bash tools/perf/setup.sh                                   # container + migrations + app + sign-up
docker cp tools/perf/seed.sql paxve-perf-pg:/tmp/seed.sql
docker exec paxve-perf-pg psql -U postgres -d postgres -q -f /tmp/seed.sql   # ~3 min
bash tools/perf/restart.sh                                 # rebuild app after code changes, wait for the ledger worker
bash tools/perf/time.sh                                    # slowest first
bash tools/perf/explain.sh "SELECT ... WHERE company_id = '\$CID'"   # EXPLAIN ANALYZE as paxve_app
docker rm -f paxve-perf-pg; pkill -f 'paxve-perf serve'    # clean up
```

Results of 2026-11-18 are in docs/OPERATIONS.md (Performance).