---
name: acl-deploy
description: >
  Deploy an updated ACL to the hindsight-auth-proxy on Railway. Updates the
  ACL_YAML_CONTENT environment variable and triggers a redeploy. Validates the
  YAML before pushing, keeps the previous value for rollback, and verifies the
  service is healthy after the deploy. Use after acl-grant or acl-revoke.
license: MIT
compatibility: Requires railway CLI (`railway`) and go (for local validation).
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL Deploy

Push an updated ACL to the hindsight-auth-proxy on Railway and verify the deploy.

## Setup

```bash
railway link   # link to ai-memory project if not already done
```

Have the edited `acl.yaml` file ready locally.

## Step 1 — Validate the YAML locally

Parse and syntax-check before touching Railway:

```bash
# Check it parses as valid YAML
python3 -c "import yaml, sys; yaml.safe_load(open('acl.yaml'))" && echo "YAML valid"

# Check the proxy accepts it (build and dry-run load)
cd apps/hindsight-auth-proxy
ACL_FILE=../acl.yaml go run . -validate-acl 2>&1 || true
# If -validate-acl is not a flag, use the local dev-mode proxy instead:
# Start it and hit /healthz; a bad ACL causes a non-zero exit on startup.
```

Also run the integration test against the new ACL to catch logic errors:

```bash
bash apps/hindsight-auth-proxy/scripts/integration-test.sh --acl-file ./acl.yaml
```

This runs the full 37-case security matrix and fails if any 200/403 expectation is violated.

## Step 2 — Save the previous ACL for rollback

```bash
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy \
  --environment dev \
  > acl.yaml.backup.$(date +%Y%m%d-%H%M%S)
echo "backup saved"
```

## Step 3 — Push the updated ACL

```bash
NEW_ACL="$(cat acl.yaml)"

railway variable set \
  "ACL_YAML_CONTENT=${NEW_ACL}" \
  --service hindsight-auth-proxy \
  --environment dev    # or: prod
```

## Step 4 — Redeploy the service

Setting an env var does NOT automatically redeploy. Trigger one explicitly:

```bash
railway redeploy \
  --service hindsight-auth-proxy \
  --environment dev
```

Wait for the deploy to complete (~30s):

```bash
railway status \
  --service hindsight-auth-proxy \
  --environment dev
```

## Step 5 — Verify

```bash
# Proxy must be healthy
curl -sf https://ai-memory-dev.baiji-cloud.ts.net:8888/healthz && echo "healthy"
# → 200

# Spot-check a known grant
curl -s -o /dev/null -w '%{http_code}\n' \
  -H "Authorization: Bearer <mcp-token>" \
  https://ai-memory-dev.baiji-cloud.ts.net:8888/mcp/hermes-richard/
# → 200 for richard (admin), 403 for unauthenticated

# If the tailnet is unavailable, check Railway logs instead:
railway logs --service hindsight-auth-proxy --environment dev | grep -E "ACL|error|panic"
```

## Rollback

If anything breaks, restore from the backup:

```bash
PREV_ACL="$(cat acl.yaml.backup.<timestamp>)"
railway variable set \
  "ACL_YAML_CONTENT=${PREV_ACL}" \
  --service hindsight-auth-proxy \
  --environment dev
railway redeploy \
  --service hindsight-auth-proxy \
  --environment dev
```

Or send SIGHUP instead of redeploying if the proxy is still running
(SIGHUP reloads ACL_YAML_CONTENT in-place with zero downtime):

```bash
# From Railway console shell or a Railway exec session:
kill -HUP $(pgrep hindsight_auth_proxy)
# Proxy logs: "ACL reloaded" on success or "ACL reload failed; keeping previous ACL" on error
```

## Output

Show the user:
- ✅ Deploy succeeded — proxy healthy, spot-check passed
- ❌ Deploy failed — paste relevant Railway log lines and suggest rollback
- Any side effects noted (e.g. a team now has zero members after a revoke)
