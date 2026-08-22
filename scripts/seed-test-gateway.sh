#!/usr/bin/env bash
# Seed the disposable APISIX with synthetic resources for integration tests.
#
# Every value here is invented. There are no real hostnames, addresses or
# credentials in this file, and there must never be: this script is public.
set -euo pipefail

URL="${SIXSYNC_TEST_ADMIN_URL:-http://127.0.0.1:9180}/apisix/admin"
KEY="${SIXSYNC_TEST_ADMIN_KEY:-sixsync-test-admin-key-not-secret}"

put() {
  local path="$1" body="$2"
  local code
  code=$(curl -sS --max-time 10 -o /dev/null -w '%{http_code}' \
    -X PUT "$URL/$path" -H "X-API-KEY: $KEY" -d "$body")
  case "$code" in
    200|201) echo "  ok   $path" ;;
    *)       echo "  FAIL $path (HTTP $code)" >&2; return 1 ;;
  esac
}

echo "Seeding $URL"

put upstreams/payments-backend \
  '{"type":"roundrobin","nodes":{"10.20.0.11:8080":5,"10.20.0.12:8080":5},"scheme":"http","retries":2}'

put upstreams/reports-backend \
  '{"type":"chash","hash_on":"vars","key":"remote_addr","nodes":[{"host":"10.20.1.5","port":9090,"weight":1}]}'

put plugin_configs/auth-standard-v1 \
  '{"desc":"shared auth","plugins":{"cors":{},"request-id":{}}}'

put services/payments-svc \
  '{"name":"payments","upstream_id":"payments-backend","desc":"payments service"}'

put routes/pay-export \
  '{"name":"payments-export","uri":"/v1/report/payment/export","methods":["POST","OPTIONS"],"service_id":"payments-svc","plugin_config_id":"auth-standard-v1","plugins":{"proxy-rewrite":{"uri":"/api/export"}}}'

put routes/reports-list \
  '{"name":"reports-list","uris":["/v1/reports","/v1/reports/all"],"methods":["GET"],"upstream_id":"reports-backend","priority":10}'

# Deliberately minimal: this route sets no priority and no status, which is
# what proves APISIX 3.16+ does not write schema defaults back.
put routes/e2e-minimal \
  '{"uri":"/e2e-minimal","upstream_id":"payments-backend"}'

put global_rules/observability \
  '{"plugins":{"prometheus":{}}}'

echo "Seeded."
