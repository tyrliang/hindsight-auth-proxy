# hindsight-auth-proxy (Railway)

Tailscale-identity authorizing reverse proxy for [Hindsight](https://hindsight.vectorize.io/).
Enforces per-employee bank access via Tailscale `WhoIs` → `@brickeye.com` email → YAML bank allowlist.
Deployed as a standalone tsnet node (`ai-memory-dev` in dev, `ai-memory` at prod cutover).

## Architecture

```text
Engineer (tailnet) ──→ ai-memory-dev:8888 (this proxy, tsnet node)
                             │
                        identity: Tailscale WhoIs → email
                        ACL: email + bank_id → allow / 403
                             │
                             ▼  (Railway private networking)
                       hindsight-app:8888 (same Railway environment)
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

## Railway services (dev environment)

| Service | Settings |
|---------|----------|
| **hindsight-auth-proxy** | This repo, root `apps/hindsight-auth-proxy`, Dockerfile; public URL **off**, private networking **on** |
| **Volume** | `/var/lib/tailscale` for stable tsnet node identity |

## Environment variables

Copy [`.env.example`](./.env.example) into Railway service variables.

| Variable | Value / note |
|----------|-------------|
| `TS_AUTHKEY` | Non-ephemeral reusable tailnet auth key |
| `TS_HOSTNAME` | `ai-memory-dev` (dev) / `ai-memory` (prod cutover) |
| `TS_STATE_DIR` | `/var/lib/tailscale` + Railway volume |
| `TS_EPHEMERAL` | `false` (stable MagicDNS name) |
| `LISTEN_PORT` | `8888` |
| `HINDSIGHT_UPSTREAM_URL` | `http://${{hindsight-app.RAILWAY_PRIVATE_DOMAIN}}:8888` — Railway private domain (reference) |
| `HINDSIGHT_UPSTREAM_TOKEN` | `openssl rand -hex 32` — same value as Hindsight's `HINDSIGHT_API_TENANT_API_KEY` / `HINDSIGHT_API_MCP_AUTH_TOKEN` |
| `ACL_S3_ENDPOINT` | `${{<Bucket>.ENDPOINT}}` — Railway Variable Reference to bucket endpoint |
| `ACL_S3_BUCKET` | `${{<Bucket>.BUCKET}}` — Railway Variable Reference to bucket name |
| `ACL_S3_KEY` | `acl.yaml` (fixed key name inside the bucket) |
| `ACL_S3_REGION` | `${{<Bucket>.REGION}}` |
| `ACL_S3_ACCESS_KEY_ID` | `${{<Bucket>.ACCESS_KEY_ID}}` |
| `ACL_S3_SECRET_ACCESS_KEY` | `${{<Bucket>.SECRET_ACCESS_KEY}}` |
| `ACL_S3_USE_PATH_STYLE` | `false` for Railway (virtual-hosted); `true` for local MinIO |
| `ACL_FILE` | Fallback when `ACL_S3_BUCKET` is not set; point to a local file for dev/tests |
| `DEV_IDENTITY_HEADER` | Empty in production; set to `X-Dev-User` for local ACL testing |

## ACL editing and deployment

The ACL lives as `acl.yaml` in a Railway Storage Bucket — one per environment (dev and prod
are fully isolated). To update:

```bash
# Edit the ACL locally
$EDITOR acl.yaml

# Upload to the dev bucket (validates YAML first)
./scripts/acl-sync.sh put dev acl.yaml

# Redeploy the proxy — it re-fetches from S3 at boot
railway redeploy --service hindsight-auth-proxy --environment dev
```

The proxy logs `ACL loaded source=s3:<bucket>/acl.yaml` on boot.

**SIGHUP reload** re-fetches from the same source (file or S3) and is useful for local /
non-distroless runs. On Railway (distroless image, no shell), the effective ACL reload
path is a **service redeploy** — SIGHUP cannot be sent.

**Rollback:** re-upload the previous ACL version and redeploy:
```bash
./scripts/acl-sync.sh get dev acl-dev.backup.$(date +%Y%m%d-%H%M%S)   # save current first
./scripts/acl-sync.sh put dev acl-dev.backup.<timestamp>
railway redeploy --service hindsight-auth-proxy --environment dev
```
Enable bucket versioning in the Railway dashboard if you want automatic revision history.

See `acl.yaml.example` for the full schema. Key rules:
- `admins` — full access including unscoped paths (metrics, docs, bank list). Limit to ops.
- `shared` — patterns for every authenticated tailnet user (e.g. `org-*`).
- `teams` — bank globs for team members. Team slugs match LiteLLM teams.
- `users` — per-email private bank grants.

## Reproducibility / config-as-code

`railway.json` in the repo root defines the Docker build and restart policy — the Railway
service is reproducible from a fresh environment. Settings not expressible in config-as-code
(public networking **off**, the `/var/lib/tailscale` volume) are dashboard-only and documented
in `scripts/deploy.sh help`. Each Railway environment gets its own independent bucket, so dev
and prod ACLs never share an object.

## Control Plane UI note

Hindsight's Control Plane UI (`:9999`) uses a single `HINDSIGHT_CP_ACCESS_KEY` and makes
full-access API calls. It cannot be per-bank scoped. Access it directly on the tailnet
(`hindsight-dev.baiji-cloud.ts.net:9999`) as an admin operation only.

## Deploy

```bash
./apps/hindsight-auth-proxy/scripts/deploy.sh help
```

## Testing

### Unit tests

```bash
cd apps/hindsight-auth-proxy
go test -race ./...
```

Covers `BankFromPath`, `Allowed`, `IsAdmin`, ACL load validation, proxy handler (healthz,
401/403/200 routing, bearer injection, ACL hot-reload, concurrency).

### Mode A — fully local (no Railway, no tailnet)

Run a local Hindsight via Docker and the proxy in dev mode. No LLM key required for
path/ACL assertion checks; real retain/recall needs a valid `HINDSIGHT_API_LLM_API_KEY`.

```bash
# 1. Local Hindsight
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
```

Or use the full integration smoke script (builds binary, runs assertions, cleans up):

```bash
GO=/path/to/go bash apps/hindsight-auth-proxy/scripts/smoke-test.sh
```

### Mode B — proxy local, Hindsight on Railway (ACL testing with injected identities)

Use this when you only have one tailnet identity (e.g. the admin account) and need to test
403 cases for non-admin users. The proxy runs locally in dev mode; the upstream is the real
deployed Railway Hindsight reached via the tailnet forwarder. Any email can be injected as
the caller identity — no second tailnet device needed.

```bash
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL=http://hindsight-dev.baiji-cloud.ts.net:8888 \
  HINDSIGHT_UPSTREAM_TOKEN=<UPSTREAM_SECRET> \
  ACL_FILE=./acl.yaml.example \
  LISTEN_PORT=9090 \
  go run .
```

Then assert HTTP status codes by injecting any identity:

```bash
BASE=http://localhost:9090

# 200 — alice reaches her own bank (user grant)
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: alice@brickeye.com' $BASE/mcp/hermes-alice/

# 403 — alice cannot reach bob's bank
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: alice@brickeye.com' $BASE/mcp/hermes-bob/

# 403 — alice cannot reach rnd team bank (wrong team)
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: alice@brickeye.com' $BASE/mcp/team-rnd-experiments/

# 403 — carol cannot reach sw team bank (wrong team)
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: carol@brickeye.com' $BASE/mcp/team-sw-roadmap/

# 200 — shared bank accessible to any authenticated user
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: stranger@other.com' $BASE/mcp/org-handbook/

# 403 — unknown user cannot reach private banks
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: stranger@other.com' $BASE/mcp/hermes-alice/

# 401 — no identity header
curl -s -o /dev/null -w '%{http_code}\n' $BASE/mcp/hermes-alice/

# 403 — non-admin cannot enumerate (unscoped path)
curl -s -o /dev/null -w '%{http_code}\n' -H 'X-Dev-User: alice@brickeye.com' $BASE/mcp/
```

> **Note**: writes (retain) go to the real Railway Hindsight database. Use disposable
> bank IDs (`test-*`) or clean up after the session. 403 cases never reach the upstream.

## Deferred: prod cutover

**Do not execute until dev validation passes.** Steps pre-decided in the plan:

1. Deploy a prod proxy as tsnet node `ai-memory`, upstream = `ai-memory-richard.baiji-cloud.ts.net:8888`.
2. Apply lockdown env vars to prod Hindsight in a scheduled window (volume/data preserved).
3. Repoint `.mcp.json` from `ai-memory-richard.../mcp/hermes-richard/` to `ai-memory.../mcp/hermes-richard/`.
