#!/usr/bin/env bash
# apps/hindsight-auth-proxy/scripts/smoke-test.sh
# Local integration smoke test: real Hindsight upstream + proxy in dev mode.
#
# Requires: docker, go (in PATH or GOBIN)
# Usage: ./scripts/smoke-test.sh [--keep]
#   --keep  leave docker container running after the test

set -euo pipefail

app_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
keep=false
[[ "${1:-}" == "--keep" ]] && keep=true

GO="${GO:-go}"
BINARY="${app_dir}/.smoke-proxy-$$"
UPSTREAM_PORT=18888
PROXY_PORT=19090
UPSTREAM_SECRET="smoke-test-secret"
CONTAINER_NAME="hindsight-smoke-$$"

cleanup() {
  echo "--- cleanup ---"
  kill "${PROXY_PID:-}" 2>/dev/null || true
  rm -f "${BINARY}"
  if ! "${keep}"; then
    docker rm -f "${CONTAINER_NAME}" 2>/dev/null || true
    docker volume rm "hs-smoke-$$" 2>/dev/null || true
  fi
}
trap cleanup EXIT

echo "=== 1. Start Hindsight on :${UPSTREAM_PORT} ==="
docker run -d --name "${CONTAINER_NAME}" \
  -p "${UPSTREAM_PORT}:8888" \
  -e HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension \
  -e HINDSIGHT_API_TENANT_API_KEY="${UPSTREAM_SECRET}" \
  -e HINDSIGHT_API_MCP_AUTH_TOKEN="${UPSTREAM_SECRET}" \
  -e HINDSIGHT_API_LLM_PROVIDER=none \
  -v "hs-smoke-$$:/home/hindsight/.pg0" \
  ghcr.io/vectorize-io/hindsight:latest

# Wait for Hindsight to respond (up to 30s)
echo "  Waiting for Hindsight health..."
for i in $(seq 1 30); do
  if curl -sf "http://localhost:${UPSTREAM_PORT}/health" >/dev/null 2>&1; then
    echo "  Hindsight ready (${i}s)"
    break
  fi
  sleep 1
  if [[ "${i}" -eq 30 ]]; then
    echo "ERROR: Hindsight did not start in 30s"
    docker logs "${CONTAINER_NAME}" | tail -20
    exit 1
  fi
done

echo ""
echo "=== 2. Build + start proxy in dev mode on :${PROXY_PORT} ==="
cd "${app_dir}"
"${GO}" build -o "${BINARY}" ./. 2>&1
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL="http://localhost:${UPSTREAM_PORT}" \
  HINDSIGHT_UPSTREAM_TOKEN="${UPSTREAM_SECRET}" \
  ACL_FILE="${app_dir}/acl.yaml.example" \
  LISTEN_PORT="${PROXY_PORT}" \
  "${BINARY}" &
PROXY_PID=$!

# Wait for proxy to listen
for i in $(seq 1 10); do
  if curl -sf "http://localhost:${PROXY_PORT}/healthz" >/dev/null 2>&1; then
    echo "  Proxy ready (${i}s)"
    break
  fi
  sleep 1
  if [[ "${i}" -eq 10 ]]; then
    echo "ERROR: Proxy did not start in 10s"
    exit 1
  fi
done

echo ""
echo "=== 3. Assertions ==="

PASS=0
FAIL=0

# assert_status: exact match on HTTP status code.
assert_status() {
  local desc="$1"
  local want="$2"
  local got="$3"
  if [[ "${got}" == "${want}" ]]; then
    echo "  PASS  [${want}] ${desc}"
    (( PASS++ )) || true
  else
    echo "  FAIL  [want ${want}, got ${got}] ${desc}"
    (( FAIL++ )) || true
  fi
}

# assert_forwarded: proxy must have forwarded (not rejected with 401/403).
# Accepts "000" (curl transport error after proxy forwarded) as "forwarded"
# because MCP is SSE — the stream closes when the test ends.
assert_forwarded() {
  local desc="$1"
  local got="$2"
  if [[ "${got}" == "401" || "${got}" == "403" ]]; then
    echo "  FAIL  [proxy rejected: ${got}] ${desc}"
    (( FAIL++ )) || true
  else
    echo "  PASS  [forwarded:${got}] ${desc}"
    (( PASS++ )) || true
  fi
}

# c: curl with 5s max-time; swallows transport errors (SSE / abrupt close).
c() { curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$@" || echo "000"; }

BASE="http://localhost:${PROXY_PORT}"

# healthz: no auth required
got=$(c "${BASE}/healthz")
assert_status "GET /healthz — no auth required" 200 "${got}"

# No identity header → 401
got=$(c "${BASE}/mcp/hermes-alice/")
assert_status "no identity header → 401" 401 "${got}"

# Empty identity header → 401
got=$(c -H 'X-Dev-User:  ' "${BASE}/mcp/hermes-alice/")
assert_status "empty identity header → 401" 401 "${got}"

# alice → hermes-alice → proxied (MCP SSE; proxy does not reject)
got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-alice/")
assert_forwarded "alice → /mcp/hermes-alice/ — user grant, proxy forwards" "${got}"

# alice → hermes-bob → 403 (other user's bank; proxy rejects before upstream)
got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-bob/")
assert_status "alice → /mcp/hermes-bob/ — 403" 403 "${got}"

# alice → team-sw-roadmap → proxied (team grant)
got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_forwarded "alice → /mcp/team-sw-roadmap/ — team grant, proxy forwards" "${got}"

# carol → team-sw-roadmap → 403 (wrong team)
got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_status "carol → /mcp/team-sw-roadmap/ — wrong team → 403" 403 "${got}"

# dave (no ACL entry) → org-handbook → proxied (shared bank)
got=$(c -H 'X-Dev-User: dave@brickeye.com' "${BASE}/mcp/org-handbook/")
assert_forwarded "dave → /mcp/org-handbook/ — shared bank, proxy forwards" "${got}"

# non-admin → /mcp/ (unscoped) → 403
got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/")
assert_status "alice → /mcp/ — unscoped, non-admin → 403" 403 "${got}"

# admin → /mcp/ (unscoped) → proxied
got=$(c -H 'X-Dev-User: richard@brickeye.com' "${BASE}/mcp/")
assert_forwarded "richard → /mcp/ — unscoped, admin, proxy forwards" "${got}"

# HTTP API (request-response): alice → her own bank — proxy forwards
got=$(c -X POST -H 'X-Dev-User: alice@brickeye.com' "${BASE}/v1/default/banks/hermes-alice/retain")
assert_forwarded "alice POST /v1/.../hermes-alice/retain — proxy forwards (upstream=${got})" "${got}"

# alice → bob's bank via HTTP API → 403
got=$(c -X POST -H 'X-Dev-User: alice@brickeye.com' "${BASE}/v1/default/banks/hermes-bob/retain")
assert_status "alice POST /v1/.../hermes-bob/retain — 403" 403 "${got}"

# unknown email + non-shared bank → 403
got=$(c -H 'X-Dev-User: stranger@other.com' "${BASE}/mcp/hermes-stranger/")
assert_status "stranger → /mcp/hermes-stranger/ — no ACL entry → 403" 403 "${got}"

# case-insensitive email
got=$(c -H 'X-Dev-User: ALICE@BRICKEYE.COM' "${BASE}/mcp/hermes-alice/")
assert_forwarded "ALICE@BRICKEYE.COM → /mcp/hermes-alice/ — case-insensitive" "${got}"

# SIGHUP reload
echo ""
echo "--- SIGHUP ACL reload ---"
kill -HUP "${PROXY_PID}"
sleep 0.5
got=$(c "${BASE}/healthz")
assert_status "after SIGHUP — proxy alive, /healthz = 200" 200 "${got}"
got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-bob/")
assert_status "after SIGHUP — alice still cannot access hermes-bob" 403 "${got}"
got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-alice/")
assert_forwarded "after SIGHUP — alice can still access hermes-alice" "${got}"

echo ""
echo "=== Results: ${PASS} passed, ${FAIL} failed ==="
if [[ "${FAIL}" -gt 0 ]]; then
  exit 1
fi
