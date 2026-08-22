#!/usr/bin/env bash
# Run the SixSync integration suite against every supported APISIX version.
#
# Each version gets its own Compose project and its own set of host ports, so a
# failure in one does not contaminate another and they can run side by side.
#
# Disposable containers only. This must never be pointed at a real gateway.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$REPO_ROOT/deploy/compose/docker-compose.yml"

ADMIN_KEY=sixsync-test-admin-key-not-secret
VIEWER_KEY=sixsync-test-viewer-key-not-secret

if [ "$#" -gt 0 ]; then
  VERSIONS=("$@")
else
  VERSIONS=(3.16.0 3.17.0 3.18.0)
fi

overall=0
declare -A results

# Ports are derived from the minor version so stacks never collide.
suffix_for() {
  case "$1" in
    3.16.*) echo 16 ;;
    3.17.*) echo 17 ;;
    3.18.*) echo 18 ;;
    *)      echo 99 ;;
  esac
}

for version in "${VERSIONS[@]}"; do
  s="$(suffix_for "$version")"
  project="sixsync-compat-${version//./-}"
  export APISIX_VERSION="$version"
  export ADMIN_PORT="93${s}" PROXY_PORT="94${s}"
  export ETCD_PORT="23${s}" ETCD_PEER_PORT="24${s}"
  export CONTROL_PORT="90${s}"
  url="http://127.0.0.1:${ADMIN_PORT}"

  echo "==================================================================="
  echo "APISIX ${version}   admin ${url}   etcd :${ETCD_PORT}"
  echo "==================================================================="

  compose=(docker compose -p "$project" -f "$COMPOSE_FILE")
  "${compose[@]}" down -v >/dev/null 2>&1

  if ! "${compose[@]}" up -d >/dev/null 2>&1; then
    echo "  FAILED to start"
    "${compose[@]}" logs --tail 15 2>&1 | tail -15
    results[$version]="START-FAILED"; overall=1
    continue
  fi

  ready=0
  for _ in $(seq 1 60); do
    if curl -sf -o /dev/null --max-time 2 -H "X-API-KEY: $ADMIN_KEY" \
        "${url}/apisix/admin/routes"; then ready=1; break; fi
    sleep 2
  done
  if [ "$ready" -ne 1 ]; then
    echo "  Admin API never became ready"
    "${compose[@]}" logs --tail 30 apisix 2>&1 |
      grep -viE "load\(\): new plugins|load_stream" | tail -10
    results[$version]="NOT-READY"; overall=1
    "${compose[@]}" down -v >/dev/null 2>&1
    continue
  fi

  SIXSYNC_TEST_ADMIN_URL="$url" SIXSYNC_TEST_ADMIN_KEY="$ADMIN_KEY" \
    "$REPO_ROOT/scripts/seed-test-gateway.sh" >/dev/null 2>&1

  log="/tmp/sixsync-compat-${version}.log"
  if SIXSYNC_TEST_ADMIN_URL="$url" \
     SIXSYNC_TEST_ADMIN_KEY="$ADMIN_KEY" \
     SIXSYNC_TEST_VIEWER_KEY="$VIEWER_KEY" \
     SIXSYNC_TEST_APISIX_VERSION="$version" \
     go test -tags integration -count=1 -timeout 10m "$REPO_ROOT/test/integration/" -v > "$log" 2>&1
  then
    results[$version]="PASS"
  else
    results[$version]="FAIL"; overall=1
  fi

  printf '  passed: %s   failed: %s\n' \
    "$(grep -c '^--- PASS' "$log")" "$(grep -c '^--- FAIL' "$log")"
  grep '^--- FAIL' "$log" | head -15
  # Version-specific observations the tests logged.
  grep -oE 'compat_test\.go:[0-9]+: APISIX [^\\]*' "$log" | head -10

  "${compose[@]}" down -v >/dev/null 2>&1
done

echo
echo "=================== COMPATIBILITY MATRIX ==========================="
for version in "${VERSIONS[@]}"; do
  printf '  APISIX %-10s %s\n' "$version" "${results[$version]:-UNKNOWN}"
done
echo "===================================================================="
exit "$overall"
