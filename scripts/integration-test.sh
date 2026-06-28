#!/usr/bin/env bash
# apps/hindsight-auth-proxy/scripts/integration-test.sh
# Full security integration test: every grant/deny case in the ACL matrix.
#
# Starts a real Hindsight upstream (Docker) and the proxy in dev mode,
# then asserts HTTP status codes for 40+ cases across:
#   - admin bypass
#   - user-specific grants
#   - team grants and cross-team denials
#   - shared bank access
#   - unknown users
#   - unauthenticated requests (401)
#   - unscoped path enforcement (403 for non-admins)
#   - HTTP API surface (retain/recall paths, not just MCP)
#   - case-insensitive email matching
#   - ACL hot-reload (SIGHUP)
#
# ACL: testdata/integration.acl.yaml (frozen; do not edit without updating assertions)
#
# Usage:
#   ./scripts/integration-test.sh [--keep] [--acl-file PATH]
#     --keep          leave docker container running after the test
#     --acl-file PATH use a different ACL file (default: testdata/integration.acl.yaml)
#
# Requirements: docker, go (in PATH or set GO=)

set -euo pipefail

app_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
keep=false
acl_file="${app_dir}/testdata/integration.acl.yaml"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) keep=true; shift ;;
    --acl-file) acl_file="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2; exit 1 ;;
  esac
done

GO="${GO:-go}"
BINARY="${app_dir}/.integration-proxy-$$"
UPSTREAM_PORT=18887
PROXY_PORT=19089
UPSTREAM_SECRET="integration-test-secret"
CONTAINER_NAME="hindsight-integration-$$"

PASS=0
FAIL=0
SKIP=0

cleanup() {
  kill "${PROXY_PID:-}" 2>/dev/null || true
  rm -f "${BINARY}"
  if ! "${keep}"; then
    docker rm -f "${CONTAINER_NAME}" 2>/dev/null || true
    docker volume rm "hs-integration-$$" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# ── Helpers ──────────────────────────────────────────────────────────────────

c() {
  # curl with 5s max-time; swallows transport errors (MCP is SSE — stream closes).
  curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$@" 2>/dev/null || echo "000"
}

assert_status() {
  local label="$1" want="$2" got="$3"
  if [[ "${got}" == "${want}" ]]; then
    printf "  ✅ %-65s → %s\n" "${label}" "${got}"
    PASS=$((PASS + 1))
  else
    printf "  ❌ %-65s → got %s, want %s\n" "${label}" "${got}" "${want}"
    FAIL=$((FAIL + 1))
  fi
}

# assert_forwarded: proxy must forward the request (not reject 401/403).
# Accepts "000" because MCP SSE streams close abruptly; 5xx from upstream
# also counts as forwarded (proxy did its job; upstream rejected for other reasons).
assert_forwarded() {
  local label="$1" got="$2"
  if [[ "${got}" == "401" || "${got}" == "403" ]]; then
    printf "  ❌ %-65s → got %s (proxy rejected; expected forward)\n" "${label}" "${got}"
    FAIL=$((FAIL + 1))
  else
    printf "  ✅ %-65s → %s (forwarded)\n" "${label}" "${got}"
    PASS=$((PASS + 1))
  fi
}

BASE="http://localhost:${PROXY_PORT}"

# ── 1. Start Hindsight ────────────────────────────────────────────────────────

echo "=== 1. Start Hindsight on :${UPSTREAM_PORT} ==="
docker run -d --name "${CONTAINER_NAME}" \
  -p "${UPSTREAM_PORT}:8888" \
  -e HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension \
  -e HINDSIGHT_API_TENANT_API_KEY="${UPSTREAM_SECRET}" \
  -e HINDSIGHT_API_MCP_AUTH_TOKEN="${UPSTREAM_SECRET}" \
  -e HINDSIGHT_API_LLM_PROVIDER=none \
  -v "hs-integration-$$:/home/hindsight/.pg0" \
  ghcr.io/vectorize-io/hindsight:latest

echo "  Waiting for Hindsight health..."
for i in $(seq 1 30); do
  if curl -sf "http://localhost:${UPSTREAM_PORT}/health" >/dev/null 2>&1; then
    echo "  Hindsight ready (${i}s)"
    break
  fi
  sleep 1
  if [[ "${i}" -eq 30 ]]; then
    echo "ERROR: Hindsight did not start in 30s" >&2
    docker logs "${CONTAINER_NAME}" | tail -20
    exit 1
  fi
done

# ── 2. Build + start proxy ────────────────────────────────────────────────────

echo ""
echo "=== 2. Build + start proxy in dev mode on :${PROXY_PORT} ==="
cd "${app_dir}"
"${GO}" build -o "${BINARY}" ./. 2>&1

DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL="http://localhost:${UPSTREAM_PORT}" \
  HINDSIGHT_UPSTREAM_TOKEN="${UPSTREAM_SECRET}" \
  ACL_FILE="${acl_file}" \
  LISTEN_PORT="${PROXY_PORT}" \
  "${BINARY}" &
PROXY_PID=$!

echo "  Waiting for proxy health..."
for i in $(seq 1 10); do
  if curl -sf "${BASE}/healthz" >/dev/null 2>&1; then
    echo "  Proxy ready (${i}s)"
    break
  fi
  sleep 1
  if [[ "${i}" -eq 10 ]]; then
    echo "ERROR: Proxy did not start in 10s" >&2
    exit 1
  fi
done

# ── 3. Assertions ─────────────────────────────────────────────────────────────

echo ""
echo "=== 3. Security assertions ==="

# ── Unauthenticated ──────────────────────────────────────────────────────────
echo ""
echo "-- A. Unauthenticated / no identity --"

got=$(c "${BASE}/healthz")
assert_status "A1  GET /healthz — no auth required" 200 "${got}"

got=$(c "${BASE}/mcp/hermes-alice/")
assert_status "A2  no identity header → 401" 401 "${got}"

got=$(c -H 'X-Dev-User:  ' "${BASE}/mcp/hermes-alice/")
assert_status "A3  whitespace-only identity header → 401" 401 "${got}"

# ── Admin (richard) ──────────────────────────────────────────────────────────
echo ""
echo "-- B. Admin (richard@brickeye.com) --"

got=$(c -H 'X-Dev-User: richard@brickeye.com' "${BASE}/mcp/")
assert_forwarded "B1  richard → /mcp/ (unscoped) — admin bypass" "${got}"

got=$(c -H 'X-Dev-User: richard@brickeye.com' "${BASE}/v1/default/banks")
assert_forwarded "B2  richard → /v1/default/banks (unscoped) — admin bypass" "${got}"

got=$(c -H 'X-Dev-User: richard@brickeye.com' "${BASE}/mcp/hermes-alice/")
assert_forwarded "B3  richard → hermes-alice — admin bypass (not his bank)" "${got}"

got=$(c -H 'X-Dev-User: richard@brickeye.com' "${BASE}/mcp/hermes-stranger/")
assert_forwarded "B4  richard → hermes-stranger — admin bypass (bank not in ACL)" "${got}"

got=$(c -H 'X-Dev-User: richard@brickeye.com' "${BASE}/mcp/team-rnd-experiments/")
assert_forwarded "B5  richard → team-rnd-experiments — admin bypass (not in team)" "${got}"

# ── Alice: sw + gen teams, personal bank ─────────────────────────────────────
echo ""
echo "-- C. Alice (sw team + gen team, hermes-alice) --"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-alice/")
assert_forwarded "C1  alice → hermes-alice — user grant" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/scratch-alice-notes/")
assert_forwarded "C2  alice → scratch-alice-notes — user glob" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_forwarded "C3  alice → team-sw-roadmap — team sw grant" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/team-gen-ops/")
assert_forwarded "C4  alice → team-gen-ops — team gen grant" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/org-handbook/")
assert_forwarded "C5  alice → org-handbook — shared grant" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-richard/")
assert_status "C6  alice → hermes-richard — not her bank → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-bob/")
assert_status "C7  alice → hermes-bob — not her bank → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/team-rnd-experiments/")
assert_status "C8  alice → team-rnd-experiments — not in rnd team → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/")
assert_status "C9  alice → /mcp/ (unscoped) — non-admin → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/v1/default/banks")
assert_status "C10 alice → /v1/default/banks (unscoped) — non-admin → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-stranger/")
assert_status "C11 alice → hermes-stranger — no ACL entry for this bank → 403" 403 "${got}"

# ── Bob: sw team only ─────────────────────────────────────────────────────────
echo ""
echo "-- D. Bob (sw team only, hermes-bob) --"

got=$(c -H 'X-Dev-User: bob@brickeye.com' "${BASE}/mcp/hermes-bob/")
assert_forwarded "D1  bob → hermes-bob — user grant" "${got}"

got=$(c -H 'X-Dev-User: bob@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_forwarded "D2  bob → team-sw-roadmap — team sw grant" "${got}"

got=$(c -H 'X-Dev-User: bob@brickeye.com' "${BASE}/mcp/org-handbook/")
assert_forwarded "D3  bob → org-handbook — shared grant" "${got}"

got=$(c -H 'X-Dev-User: bob@brickeye.com' "${BASE}/mcp/team-gen-ops/")
assert_status "D4  bob → team-gen-ops — not in gen team → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: bob@brickeye.com' "${BASE}/mcp/hermes-alice/")
assert_status "D5  bob → hermes-alice — not his bank → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: bob@brickeye.com' "${BASE}/mcp/team-rnd-experiments/")
assert_status "D6  bob → team-rnd-experiments — not in rnd team → 403" 403 "${got}"

# ── Carol: rnd team only ──────────────────────────────────────────────────────
echo ""
echo "-- E. Carol (rnd team only, hermes-carol) --"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/hermes-carol/")
assert_forwarded "E1  carol → hermes-carol — user grant" "${got}"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/team-rnd-experiments/")
assert_forwarded "E2  carol → team-rnd-experiments — team rnd grant" "${got}"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/org-handbook/")
assert_forwarded "E3  carol → org-handbook — shared grant" "${got}"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_status "E4  carol → team-sw-roadmap — not in sw team → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/team-gen-ops/")
assert_status "E5  carol → team-gen-ops — not in gen team → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/hermes-richard/")
assert_status "E6  carol → hermes-richard — not her bank → 403" 403 "${got}"

# ── Dave: no team, no users entry ────────────────────────────────────────────
echo ""
echo "-- F. Dave (no team, no users entry — shared-only access) --"

got=$(c -H 'X-Dev-User: dave@brickeye.com' "${BASE}/mcp/org-handbook/")
assert_forwarded "F1  dave → org-handbook — shared grant (no other ACL entry)" "${got}"

got=$(c -H 'X-Dev-User: dave@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_status "F2  dave → team-sw-roadmap — no team membership → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: dave@brickeye.com' "${BASE}/mcp/hermes-dave/")
assert_status "F3  dave → hermes-dave — no user entry → 403" 403 "${got}"

# ── Stranger: completely unknown identity ─────────────────────────────────────
echo ""
echo "-- G. Stranger (completely unknown identity) --"

got=$(c -H 'X-Dev-User: stranger@other.com' "${BASE}/mcp/org-handbook/")
assert_forwarded "G1  stranger → org-handbook — shared grant applies to any authed user" "${got}"

got=$(c -H 'X-Dev-User: stranger@other.com' "${BASE}/mcp/team-sw-roadmap/")
assert_status "G2  stranger → team-sw-roadmap — no membership → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: stranger@other.com' "${BASE}/mcp/hermes-richard/")
assert_status "G3  stranger → hermes-richard — no user entry → 403" 403 "${got}"

# ── HTTP API surface (request-response, not MCP SSE) ─────────────────────────
echo ""
echo "-- H. HTTP API surface (/v1/tenant/banks/...) --"

got=$(c -X POST -H 'X-Dev-User: alice@brickeye.com' \
  "${BASE}/v1/default/banks/hermes-alice/memories")
assert_forwarded "H1  alice POST /v1/.../hermes-alice/memories — user grant, forwarded" "${got}"

got=$(c -X POST -H 'X-Dev-User: alice@brickeye.com' \
  "${BASE}/v1/default/banks/hermes-bob/memories")
assert_status "H2  alice POST /v1/.../hermes-bob/memories — not her bank → 403" 403 "${got}"

got=$(c -X POST -H 'X-Dev-User: carol@brickeye.com' \
  "${BASE}/v1/default/banks/team-sw-roadmap/memories/recall")
assert_status "H3  carol POST /v1/.../team-sw-roadmap/recall — wrong team → 403" 403 "${got}"

got=$(c -X POST -H 'X-Dev-User: bob@brickeye.com' \
  "${BASE}/v1/default/banks/team-sw-roadmap/memories/recall")
assert_forwarded "H4  bob POST /v1/.../team-sw-roadmap/recall — team grant, forwarded" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' \
  "${BASE}/v1/default/banks/hermes-alice")
assert_forwarded "H5  alice GET /v1/.../hermes-alice — user grant, forwarded" "${got}"

got=$(c -X POST -H 'X-Dev-User: alice@brickeye.com' \
  "${BASE}/v1/default/banks/team-sw-roadmap/memories")
assert_forwarded "H6  alice POST /v1/.../team-sw-roadmap/memories — team sw grant, forwarded" "${got}"

# ── Case-insensitive email matching ───────────────────────────────────────────
echo ""
echo "-- I. Case-insensitive email matching --"

got=$(c -H 'X-Dev-User: ALICE@BRICKEYE.COM' "${BASE}/mcp/hermes-alice/")
assert_forwarded "I1  ALICE@BRICKEYE.COM → hermes-alice — uppercase email normalized" "${got}"

got=$(c -H 'X-Dev-User: Alice@Brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_forwarded "I2  Alice@Brickeye.com → team-sw-roadmap — mixed-case email normalized" "${got}"

got=$(c -H 'X-Dev-User: ALICE@BRICKEYE.COM' "${BASE}/mcp/hermes-bob/")
assert_status "I3  ALICE@BRICKEYE.COM → hermes-bob — uppercase, still denied → 403" 403 "${got}"

# ── ACL hot-reload (SIGHUP) ───────────────────────────────────────────────────
echo ""
echo "-- J. ACL hot-reload (SIGHUP) --"

kill -HUP "${PROXY_PID}"
sleep 0.5

got=$(c "${BASE}/healthz")
assert_status "J1  after SIGHUP — proxy alive, /healthz = 200" 200 "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-alice/")
assert_forwarded "J2  after SIGHUP — alice can still access hermes-alice" "${got}"

got=$(c -H 'X-Dev-User: alice@brickeye.com' "${BASE}/mcp/hermes-bob/")
assert_status "J3  after SIGHUP — alice still denied hermes-bob → 403" 403 "${got}"

got=$(c -H 'X-Dev-User: carol@brickeye.com' "${BASE}/mcp/team-sw-roadmap/")
assert_status "J4  after SIGHUP — carol still denied team-sw-roadmap → 403" 403 "${got}"

# ── Results ───────────────────────────────────────────────────────────────────

echo ""
echo "=== Results: ${PASS} passed, ${FAIL} failed ==="
if [[ "${FAIL}" -gt 0 ]]; then
  echo ""
  echo "SECURITY GATE FAILED: ${FAIL} case(s) did not match expected access control."
  echo "Do not ship this version until all cases pass."
  exit 1
fi
echo "All security assertions passed. ACL enforcement is correct."
