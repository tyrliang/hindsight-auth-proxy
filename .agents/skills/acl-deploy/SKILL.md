---
name: acl-deploy
description: >
  Deploy an updated ACL to the hindsight-auth-proxy on Railway. Asks which
  environment (dev or prod), enforces dev-first validation before any prod deploy,
  validates the YAML, saves a rollback backup, pushes ACL_YAML_CONTENT, triggers
  a redeploy, and verifies health. Use after acl-grant or acl-revoke.
license: MIT
compatibility: Requires railway CLI (`railway`).
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL Deploy

Push an updated ACL to the hindsight-auth-proxy on Railway.

## Environments

| | Dev | Prod |
|---|---|---|
| Railway env flag | `--environment dev` | `--environment prod` |
| Proxy tailnet node | `ai-memory-dev` | `ai-memory` |
| Healthz URL | `https://ai-memory-dev.baiji-cloud.ts.net:8888/healthz` | `https://ai-memory.baiji-cloud.ts.net:8888/healthz` |
| Who is affected | Test personas only | Real Brickeye employees |
| ACL_YAML_CONTENT | Independent variable | Independent variable |

## Ask the user

**Which environment are you deploying to — dev or prod?**

- **Dev first** is always required. Never deploy directly to prod without a successful dev deploy of the same change.
- If they say prod: confirm the same ACL change was deployed to dev and verified. If not, do dev first.

---

## Deploying to DEV

### Step 1 — Validate locally

```bash
python3 -c "import yaml; yaml.safe_load(open('acl-dev.yaml'))" && echo "YAML valid"
```

Run the integration test against the edited ACL to catch logic errors before touching Railway:

```bash
bash apps/hindsight-auth-proxy/scripts/integration-test.sh --acl-file ./acl-dev.yaml
```

All 50 cases must pass. If any fail, fix the ACL before continuing.

### Step 2 — Save rollback

```bash
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy --environment dev \
  > acl-dev.backup.$(date +%Y%m%d-%H%M%S)
```

### Step 3 — Push

```bash
railway variable set \
  "ACL_YAML_CONTENT=$(cat acl-dev.yaml)" \
  --service hindsight-auth-proxy --environment dev
```

### Step 4 — Redeploy

```bash
railway redeploy --service hindsight-auth-proxy --environment dev
railway status   --service hindsight-auth-proxy --environment dev
```

### Step 5 — Verify

```bash
# Proxy healthy
curl -sf https://ai-memory-dev.baiji-cloud.ts.net:8888/healthz && echo "healthy"

# Spot-check: a known-good grant still works
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Authorization: Bearer <mcp-token>' \
  https://ai-memory-dev.baiji-cloud.ts.net:8888/mcp/hermes-richard/
# → 200

# Spot-check: a known deny still blocks
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'Authorization: Bearer <mcp-token-for-alice>' \
  https://ai-memory-dev.baiji-cloud.ts.net:8888/mcp/hermes-richard/
# → 403
```

### Dev rollback

```bash
railway variable set \
  "ACL_YAML_CONTENT=$(cat acl-dev.backup.<timestamp>)" \
  --service hindsight-auth-proxy --environment dev
railway redeploy --service hindsight-auth-proxy --environment dev
```

---

## Deploying to PROD

⚠️ **Prod gates** — all three must be true before pushing:
1. The same ACL change was deployed to dev and the proxy is healthy there.
2. Spot-checks on dev confirm the intended behaviour (grants work, denials block).
3. The integration test passed against the edited ACL file.

If any gate is not met, stop and do dev first.

### Step 1 — Validate

```bash
python3 -c "import yaml; yaml.safe_load(open('acl-prod.yaml'))" && echo "YAML valid"
bash apps/hindsight-auth-proxy/scripts/integration-test.sh --acl-file ./acl-prod.yaml
```

### Step 2 — Save rollback (keep this)

```bash
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy --environment prod \
  > acl-prod.backup.$(date +%Y%m%d-%H%M%S)
echo "Backup saved — keep this until the change is confirmed stable in prod."
```

### Step 3 — Push

```bash
railway variable set \
  "ACL_YAML_CONTENT=$(cat acl-prod.yaml)" \
  --service hindsight-auth-proxy --environment prod
```

### Step 4 — Redeploy

```bash
railway redeploy --service hindsight-auth-proxy --environment prod
railway status   --service hindsight-auth-proxy --environment prod
```

Prod restarts take ~30s. The proxy serves the old ACL from memory until the new
process starts — no window of no-auth.

### Step 5 — Verify

```bash
# Proxy healthy
curl -sf https://ai-memory.baiji-cloud.ts.net:8888/healthz && echo "healthy"

# Spot-check: richard (admin) can still reach his bank
curl -s -o /dev/null -w '%{http_code}\n' \
  https://ai-memory.baiji-cloud.ts.net:8888/mcp/hermes-richard/
# → 200 (from richard's machine; WhoIs resolves to richard@brickeye.com)
```

If tailnet is unavailable, check Railway logs:
```bash
railway logs --service hindsight-auth-proxy --environment prod \
  | grep -E "ACL|error|panic"
```

### Prod rollback

```bash
railway variable set \
  "ACL_YAML_CONTENT=$(cat acl-prod.backup.<timestamp>)" \
  --service hindsight-auth-proxy --environment prod
railway redeploy --service hindsight-auth-proxy --environment prod
```

Or use SIGHUP for zero-downtime reload if the proxy process is still running
(Railway console shell or exec session):
```bash
kill -HUP $(pgrep hindsight_auth_proxy)
# Proxy logs: "ACL reloaded" on success, "ACL reload failed; keeping previous ACL" on error
```

## Output

Tell the user:
- ✅ Deploy succeeded — proxy healthy, spot-checks passed, which environment
- ❌ Deploy failed — paste relevant Railway log lines, run rollback
- If prod: note whether a dev deploy preceded it and which backup file to use for rollback
