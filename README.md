# hindsight-auth-proxy (Railway)

Tailscale-identity authorizing reverse proxy for [Hindsight](https://hindsight.vectorize.io/).
Enforces per-caller bank access via Tailscale `WhoIs` → identity (an employee's
`@brickeye.com` login email, or a tagged agent node's tailnet hostname) → YAML bank allowlist.
Deployed as a standalone tsnet node (`ai-memory-dev` in dev, `ai-memory` at prod cutover).

## Architecture

```text
Engineer (tailnet) ──→ ai-memory-dev:8888 (this proxy, tsnet node)
                             │
                        identity: Tailscale WhoIs → email
                        ACL: email + bank_id → allow / 403
                             │
                             ▼  (ts.Dial over tailnet)
                       hindsight-dev:8888 (tsnet node, fresh empty volume)
```

Production Hindsight (`ai-memory-richard`, has data) is **never addressed** by this service.

## Why a proxy?

Hindsight OSS has no per-bank auth — both the HTTP API (`HINDSIGHT_API_TENANT_API_KEY`) and
MCP (`HINDSIGHT_API_MCP_AUTH_TOKEN`) use a single shared secret granting all-or-nothing access.
Because `bank_id` is always in the URL path (`/mcp/{bank_id}/` and `/v1/{tenant}/banks/{bank_id}/…`),
a path-aware proxy is the only way to enforce bank-level isolation without modifying Hindsight itself.

## Bank naming convention

| Pattern | Who | Example |
|---------|-----|---------|
| `hermes-<name>` | Personal private bank | `hermes-alice` |
| `scratch-<name>-*` | Personal scratch banks | `scratch-alice-drafts` |
| `team-<slug>-*` | Team shared banks | `team-sw-roadmap` |
| `org-*` | Org-wide shared banks | `org-handbook` |

Team slugs: `gen`, `sw`, `rnd`, `hw`, `exec`, `fin` — match LiteLLM team slugs.

## Prerequisites

- Railway project (`ai-workbench`) — same project as LiteLLM / LibreChat
- Tailscale non-ephemeral reusable auth key (tagged `tag:ai-memory`)
- Hindsight dev instance running as tsnet node `hindsight-dev` (Step 1 in the plan)
- `acl.yaml` populated for your team (see `acl.yaml.example`)

## Agent (tagged node) identity

Callers are not always human. A Hermes assistant process can join the tailnet as a
tagged node (e.g. `tag:agent`) with a stable hostname — `agent-product-assistant`,
`agent-support-assistant`. Tagged nodes have no personal Tailscale login, so
`internal/identity.Resolve` falls back to the node's short tailnet hostname instead
of an email. Grant these in `acl.yaml`'s `users:` map exactly like an email grant,
keyed by hostname (see `acl.yaml.example` and `acl.product-brain.yaml`):

```yaml
users:
  agent-product-assistant:
    banks: [team-product, team-product-*, team-fieldops, team-fieldops-*]
  agent-support-assistant:
    banks: [team-fieldops, team-fieldops-*]
```

This requires the proxy's tsnet listener (the default; see main.go's production
branch) — `Node.Tags`/`Node.Name` are only visible via `LocalClient.WhoIs`, not via
`tailscale serve`'s injected headers, which Tailscale never populates for tagged
devices.

## Railway services (dev environment)

| Service | Settings |
|---------|----------|
| **hindsight-auth-proxy** | This repo, root `apps/hindsight-auth-proxy`, Dockerfile; public URL **off**, private networking **on** |
| **Volume** | `/var/lib/tailscale` for stable tsnet node identity |

## Railway services (product-brain deployment)

Separate deployment: fronts the `product-brain` project's own `hindsight` service
(brain_ingest ingestion delivery — `team-product`/`team-fieldops` banks), not the
personal ai-memory Hindsight above. Repoints the project's existing
`tailscale-forwarder` service (previously an unused egress port-forwarder image)
at this repo.

| Service | Settings |
|---------|----------|
| **tailscale-forwarder** (product-brain) | This repo, Dockerfile source; public URL **off**, private networking **on** |
| **Volume** | `/var/lib/tailscale` for stable tsnet node identity (`TS_EPHEMERAL=false`) |

Env vars follow the same shape as below, with:
- `TS_HOSTNAME` — the proxy's own tailnet identity (its inbound listener), independent of caller hostnames.
- `HINDSIGHT_UPSTREAM_URL` = `http://hindsight.railway.internal:8888` (Railway private domain — the ingestion worker still talks to `hindsight` directly and unauthenticated over the same private network; this proxy only gates *tailnet* callers).
- `ACL_YAML_CONTENT` = contents of `acl.product-brain.yaml` (grants `agent-product-assistant` and `agent-support-assistant`; see [Agent (tagged node) identity](#agent-tagged-node-identity)).

**Residual gap:** the `hindsight` service in product-brain runs with no
`HINDSIGHT_API_TENANT_API_KEY`/`HINDSIGHT_API_MCP_AUTH_TOKEN` configured (open on
Railway's private network by design — the ingestion worker sends no token). This
proxy's tailnet ACL is therefore the only gate for tailnet callers; Hindsight itself
does not independently verify `HINDSIGHT_UPSTREAM_TOKEN`. Enabling
`ApiKeyTenantExtension` on `hindsight` would add defense-in-depth but requires also
updating the ingestion worker to send a matching key — out of scope here.

## Environment variables

Copy [`.env.example`](./.env.example) into Railway service variables.

| Variable | Value / note |
|----------|-------------|
| `TS_AUTHKEY` | Non-ephemeral reusable tailnet auth key |
| `TS_HOSTNAME` | `ai-memory-dev` (dev) / `ai-memory` (prod cutover) |
| `TS_STATE_DIR` | `/var/lib/tailscale` + Railway volume |
| `TS_EPHEMERAL` | `false` (stable MagicDNS name) |
| `LISTEN_PORT` | `8888` |
| `HINDSIGHT_UPSTREAM_URL` | `http://hindsight-dev.baiji-cloud.ts.net:8888` (dev) |
| `HINDSIGHT_UPSTREAM_TOKEN` | `openssl rand -hex 32` — same value as Hindsight's `HINDSIGHT_API_TENANT_API_KEY` / `HINDSIGHT_API_MCP_AUTH_TOKEN` |
| `ACL_FILE` | `/app/acl.yaml` (bake into image or mount as config) |
| `DEV_IDENTITY_HEADER` | Empty in production; set to `X-Dev-User` for local dev mode |

## ACL editing and hot-reload

Edit `acl.yaml` and send `SIGHUP` to the proxy process to reload without downtime:

```bash
# In Railway: use the Railway CLI or console to send SIGHUP
kill -HUP $(pgrep hindsight_auth_proxy)
```

The proxy logs `"ACL reloaded"` on success or `"ACL reload failed; keeping previous ACL"` on error.

See `acl.yaml.example` for the full schema. Key rules:
- `admins` — full access including unscoped paths (metrics, docs, bank list). Limit to ops.
- `shared` — patterns for every authenticated tailnet user (e.g. `org-*`).
- `teams` — bank globs for team members. Team slugs match LiteLLM teams.
- `users` — per-identity private bank grants: email (human) or tailnet hostname (tagged agent node).

## Control Plane UI note

Hindsight's Control Plane UI (`:9999`) uses a single `HINDSIGHT_CP_ACCESS_KEY` and makes
full-access API calls. It cannot be per-bank scoped. Access it directly on the tailnet
(`hindsight-dev.baiji-cloud.ts.net:9999`) as an admin operation only.

## Deploy

```bash
./apps/hindsight-auth-proxy/scripts/deploy.sh help
```

## Dev mode (local smoke test)

Run a local Hindsight + the proxy without tsnet or Railway:

```bash
# 1. Local Hindsight (no LLM key needed for path/ACL smoke tests)
docker run -p 8888:8888 \
  -e HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension \
  -e HINDSIGHT_API_TENANT_API_KEY=test \
  -e HINDSIGHT_API_MCP_AUTH_TOKEN=test \
  -e HINDSIGHT_API_LLM_PROVIDER=none \
  -v hs-test:/home/hindsight/.pg0 \
  ghcr.io/vectorize-io/hindsight:latest

# 2. Proxy in dev mode
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL=http://localhost:8888 \
  HINDSIGHT_UPSTREAM_TOKEN=test \
  ACL_FILE=./acl.yaml.example \
  LISTEN_PORT=9090 \
  go run .

# 3. Assertions (expected HTTP status in comment)
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: alice@brickeye.com' localhost:9090/healthz           # 200
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: alice@brickeye.com' localhost:9090/mcp/hermes-alice/ # 200
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: alice@brickeye.com' localhost:9090/mcp/hermes-bob/   # 403
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: alice@brickeye.com' localhost:9090/mcp/              # 403 (unscoped, not admin)
curl -s -o /dev/null -w '%{http_code}\n' \
  localhost:9090/mcp/hermes-alice/                                     # 401 (no identity header)
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: richard@brickeye.com' localhost:9090/mcp/            # 200 (admin, unscoped)
```

## Unit tests

```bash
cd apps/hindsight-auth-proxy
go test ./...
```

Tests cover `BankFromPath` (MCP + HTTP API paths, unscoped paths, edge cases),
`Allowed` (user grants, team grants, shared banks, admin bypass, unknown identity),
and `internal/identity.Resolve` (human vs. tagged-node identity, including that a
tagged node's stale/creator `UserProfile` is never trusted over its hostname).

## Deferred: prod cutover

**Do not execute until dev validation passes.** Steps pre-decided in the plan:

1. Deploy a prod proxy as tsnet node `ai-memory`, upstream = `ai-memory-richard.baiji-cloud.ts.net:8888`.
2. Apply lockdown env vars to prod Hindsight in a scheduled window (volume/data preserved).
3. Repoint `.mcp.json` from `ai-memory-richard.../mcp/hermes-richard/` to `ai-memory.../mcp/hermes-richard/`.
