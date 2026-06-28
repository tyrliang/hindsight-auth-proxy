#!/usr/bin/env bash
# apps/hindsight-auth-proxy/scripts/deploy.sh — operator checklist for
# hindsight-auth-proxy on Railway.
#
# Usage:
#   ./scripts/deploy.sh help
#   ./scripts/deploy.sh print-acl-template
#   ./scripts/deploy.sh print-hindsight-env

set -euo pipefail

app_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cmd="${1:-help}"

case "${cmd}" in
print-acl-template)
  cat "${app_dir}/acl.yaml.example"
  ;;

print-hindsight-env)
  # Env vars for the ghcr.io/vectorize-io/hindsight:latest service in Railway dev.
  cat <<'EOF'
hindsight-dev Railway service variables (dev environment):

  HINDSIGHT_API_LLM_PROVIDER=openai
  HINDSIGHT_API_LLM_API_KEY=<TEAM_LLM_KEY>
  HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension
  HINDSIGHT_API_TENANT_API_KEY=<UPSTREAM_SECRET>       # openssl rand -hex 32
  HINDSIGHT_API_MCP_AUTH_TOKEN=<UPSTREAM_SECRET>       # same value
  HINDSIGHT_API_MCP_STATELESS=true
  HINDSIGHT_API_MCP_ENABLED_TOOLS=retain,recall,reflect,list_memories,get_memory,list_tags,list_mental_models,get_mental_model,list_directives,list_documents,get_document,get_bank,get_bank_stats

  # Tailscale sidecar (ts-hindsight forwarder) — separate Railway service:
  # Image: ghcr.io/brody192/tailscale-forwarder:v0.0.8
  #   TS_AUTHKEY=tskey-auth-...       # non-ephemeral key for hindsight-dev node
  #   TS_HOSTNAME=hindsight-dev
  #   TS_STATE_DIR=/data              # + Railway volume at /data
  #   TS_EPHEMERAL=false
  #   TS_DEBUG_MTU=1200
  #   CONNECTION_MAPPING_01=8888:${{hindsight-app.RAILWAY_PRIVATE_DOMAIN}}:8888
  #   CONNECTION_MAPPING_02=https:443:${{hindsight-app.RAILWAY_PRIVATE_DOMAIN}}:9999
  #
  # CONNECTION_MAPPING_01: port 8888  → Hindsight API + MCP
  # CONNECTION_MAPPING_02: HTTPS :443 → Hindsight Control Plane UI (:9999)

  # Volume: fresh empty disk at /home/hindsight/.pg0 (pgvector data)
  # NEVER mount, clone, or reference the prod volume.

UPSTREAM_SECRET must equal HINDSIGHT_UPSTREAM_TOKEN on the hindsight-auth-proxy.
EOF
  ;;

help | *)
  cat <<EOF
hindsight-auth-proxy on Railway — operator checklist

Source: ${app_dir}
README: ${app_dir}/README.md

Prerequisites:
  - Railway project (ai-workbench), dev environment created
  - Tailscale non-ephemeral reusable auth key (tag:ai-memory)
  - Upstream Hindsight dev instance deployed (hindsight-dev tsnet node)
  - acl.yaml populated (see: $0 print-acl-template)
  - UPSTREAM_SECRET generated: openssl rand -hex 32

Step 1 — Hindsight dev instance (hindsight-dev):
  Deploy ghcr.io/vectorize-io/hindsight:latest in Railway dev environment.
  Set env vars:
    $0 print-hindsight-env
  Verify tailnet node appears as hindsight-dev in https://login.tailscale.com/admin/machines
  Confirm fresh empty volume: no prod bank IDs in list_banks response.

Step 2 — Auth proxy (this service):
  Railway: new service in dev environment, root apps/hindsight-auth-proxy, Dockerfile.
  Public URL: OFF. Private networking: ON.
  Volume: /var/lib/tailscale (stable node identity across restarts).

  Set env vars (copy from ${app_dir}/.env.example):
    TS_AUTHKEY              = non-ephemeral reusable key for ai-memory-dev node
    TS_HOSTNAME             = ai-memory-dev
    TS_STATE_DIR            = /var/lib/tailscale
    TS_EPHEMERAL            = false
    LISTEN_PORT             = 8888
    HINDSIGHT_UPSTREAM_URL  = http://hindsight-app.railway.internal:8888
    HINDSIGHT_UPSTREAM_TOKEN= <UPSTREAM_SECRET>
    ACL_YAML_CONTENT        = <paste full YAML from acl.yaml.example>

  After deploy, confirm the proxy appears as ai-memory-dev on the tailnet.

Step 3 — ACL
  Edit acl.yaml for your team (see: $0 print-acl-template).
  Reload without restart: kill -HUP \$(pgrep hindsight_auth_proxy)
  Proxy logs: "ACL reloaded" on success.

Step 4 — Dev E2E test
  From a tailnet device (not the proxy node itself):
    # Should succeed (alice's bank):
    curl http://ai-memory-dev.baiji-cloud.ts.net:8888/mcp/hermes-alice/
    # Should return 403 (wrong user's bank):
    curl http://ai-memory-dev.baiji-cloud.ts.net:8888/mcp/hermes-bob/    # as alice's identity

  MCP client test (throwaway config, NOT the prod .mcp.json):
    Endpoint: http://ai-memory-dev.baiji-cloud.ts.net:8888/mcp/<bank>/
    Auth: none (Tailscale WhoIs provides identity)

Step 5 — Prod cutover (DEFERRED — do not execute yet)
  See README.md § Deferred: prod cutover

Commands:
  $0 print-acl-template
  $0 print-hindsight-env
  $0 help

Local smoke test (dev mode, no Railway/Tailscale):
  See README.md § Dev mode (local smoke test)
EOF
  ;;
esac
