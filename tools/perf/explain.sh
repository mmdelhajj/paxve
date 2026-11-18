#!/usr/bin/env bash
# EXPLAIN ANALYZE a query as paxve_app with the request settings: perf-explain.sh "<sql with $CID>"
CID=$(cat /tmp/perf.cid)
TID=$(docker exec paxve-perf-pg psql -U postgres -d postgres -At -c "select tenant_id from companies where id='$CID'")
SQL=${1//\$CID/$CID}
docker exec -i -e PGPASSWORD=perfapp paxve-perf-pg psql -h 127.0.0.1 -U paxve_app -d postgres -At <<EOF
BEGIN;
SELECT set_config('app.tenant_id', '$TID', true), set_config('app.company_id', '$CID', true), set_config('app.company_ids', '', true) \g /dev/null
EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) $SQL;
ROLLBACK;
EOF
