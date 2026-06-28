---
name: acl-revoke
description: >
  Remove a bank access grant from the hindsight-auth-proxy ACL. Guides through
  removing a user's personal grants, removing team membership, revoking a shared
  bank pattern, or removing admin access. Asks which environment (dev or prod),
  warns about side effects, and enforces dev-first validation before any prod change.
  Use when offboarding an employee or tightening access.
license: MIT
compatibility: Requires railway CLI (`railway`).
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL Revoke

Remove a bank access grant from the hindsight-auth-proxy ACL.

## Environments

| | Dev | Prod |
|---|---|---|
| Railway env | `dev` | `prod` |
| Service name | `hindsight-auth-proxy` | `hindsight-auth-proxy` |
| Proxy tailnet node | `ai-memory-dev` | `ai-memory` |
| Who uses it | Test personas (alice, bob, carol) | Real Brickeye employees |
| Risk | Low | **High — revocation is immediate after deploy** |

**Ask the user which environment they want to change before doing anything.**
For prod: a revoke blocks real employee access the moment the service restarts.
Confirm they have tested the same change in dev and are ready.

## Setup

Fetch the current ACL for the target environment:

```bash
# Dev
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy --environment dev > acl-dev.yaml

# Prod
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy --environment prod > acl-prod.yaml
```

## Ask the user

What to revoke?

1. **Offboard an employee** — remove all their grants (users entry + team memberships)
2. **Remove from a team** — revoke team bank access without removing personal grants
3. **Remove a shared bank pattern** — restrict a previously org-wide bank
4. **Revoke admin** — downgrade an admin to regular user access

---

### 1. Offboard an employee

⚠️ **Do both steps — missing either leaves dangling access.**

**Step A** — Remove from `users`:
```yaml
users:
  # DELETE the entire block for the departing person
  # alice@brickeye.com:
  #   banks: [hermes-alice, scratch-alice-*]
```

**Step B** — Remove from every `teams[*].members` list they appear in:
```yaml
teams:
  sw:
    members:
      # DELETE: - alice@brickeye.com
  gen:
    members:
      # DELETE: - alice@brickeye.com
```

If they were an admin, also remove from `admins`.

**Side effects:**
- Removing from `users` does NOT remove team membership — you must do both.
- The personal bank (`hermes-alice`) remains in Hindsight storage; revocation
  prevents access but does not delete data. Delete via the Hindsight Control
  Plane (`hindsight-dev.baiji-cloud.ts.net:9999` for dev, prod equivalent for prod)
  if the data must be purged.
- On prod: the employee loses access the moment the proxy redeploys.
  Coordinate timing with the offboarding process.

### 2. Remove from a team

Remove only from `teams[slug].members`:
```yaml
teams:
  sw:
    members:
      - bob@brickeye.com
      # DELETE: - alice@brickeye.com
```

Alice retains her personal bank grants and any other team memberships.
She loses access to all `team-sw-*` banks.

### 3. Remove a shared bank pattern

```yaml
shared:
  - org-*
  # DELETE: - public-*
```

⚠️ This blocks **every user** from the pattern, including those with no other ACL entry.
On prod: verify no one actively uses it before removing.
On dev: safe to test freely.

To restrict a shared bank to specific teams rather than removing it, move the pattern
into the relevant `teams[slug].banks` lists and remove it from `shared`.

### 4. Revoke admin

```yaml
admins:
  # DELETE: - formeradmin@brickeye.com
  - richard@brickeye.com
```

The person's team and user grants remain active. They lose:
- access to unscoped paths (`/mcp/`, `/v1/default/banks`)
- access to banks outside their explicit grants

## Verify locally before deploying

Test the edited ACL against dev Hindsight regardless of target environment:

```bash
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL=http://hindsight-dev.baiji-cloud.ts.net:8888 \
  HINDSIGHT_UPSTREAM_TOKEN=<dev-token> \
  ACL_FILE=./acl-dev.yaml \
  LISTEN_PORT=9090 \
  go run ./apps/hindsight-auth-proxy/.

# Confirm the revoked identity now gets 403
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: alice@brickeye.com' \
  http://localhost:9090/mcp/hermes-alice/
# → 403 (after full offboard) or 200 (only team revoked — personal bank still active)

# Confirm other users are unaffected
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: bob@brickeye.com' \
  http://localhost:9090/mcp/hermes-bob/
# → 200
```

## Next step

Deploy the updated ACL → use the `acl-deploy` skill.
