---
name: acl-revoke
description: >
  Remove a bank access grant from the hindsight-auth-proxy ACL. Guides through
  removing a user's personal grants, removing team membership, revoking a shared
  bank pattern, or removing admin access. Use when offboarding an employee or
  tightening access. Warns about side effects before any change.
license: MIT
compatibility: Requires railway CLI (`railway`) or direct Railway dashboard access.
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL Revoke

Remove a bank access grant from the hindsight-auth-proxy ACL.

The ACL is the single source of truth for who can access which memory banks.
Removing a grant takes effect only after deploying — use the `acl-deploy` skill.

## Setup

Fetch the current ACL before editing:
```bash
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy \
  --environment dev        # or prod
```
Save it locally as `acl.yaml` to edit, then deploy the result.

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
  # DELETE the entire block for the departing person:
  # alice@brickeye.com:
  #   banks: [hermes-alice, scratch-alice-*]
```

**Step B** — Remove from every `teams[*].members` list they appear in:
```yaml
teams:
  sw:
    members:
      # DELETE the line:
      # - alice@brickeye.com
  gen:
    members:
      # DELETE the line:
      # - alice@brickeye.com
```

**Side effects to be aware of:**
- Removing from `users` does NOT remove team membership — you must do both.
- Removing from teams does NOT remove the personal bank entry — you must do both.
- The personal bank (`hermes-alice`) and any scratch banks remain in Hindsight's
  storage. Revocation prevents access but does not delete the data. Delete banks
  explicitly via the Hindsight Control Plane if required.
- If the person was an admin, also remove from `admins`.

### 2. Remove from a team

Remove only from `teams[slug].members`:
```yaml
teams:
  sw:
    members:
      - bob@brickeye.com
      # REMOVE: - alice@brickeye.com
```

Alice retains her personal bank grants and any other team memberships.
She loses access to all `team-sw-*` banks.

### 3. Remove a shared bank pattern

Remove from `shared`:
```yaml
shared:
  - org-*
  # REMOVE: - public-*
```

⚠️ This immediately blocks **every user** from the pattern, including those with
no other ACL entry. Verify no one actively depends on it before removing.

To restrict a shared bank to specific teams instead of removing it entirely,
move the pattern into the relevant `teams[slug].banks` lists and remove it
from `shared`.

### 4. Revoke admin

Remove from `admins`:
```yaml
admins:
  # REMOVE: - formeradmin@brickeye.com
  - richard@brickeye.com
```

The person's regular grants (team, users) remain active. They lose:
- access to unscoped paths (`/mcp/`, `/v1/default/banks`)
- access to banks outside their explicit grants

## Verify

After editing, test locally before deploying (no Railway changes yet):

```bash
# Start proxy in dev mode
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL=http://hindsight-dev.baiji-cloud.ts.net:8888 \
  HINDSIGHT_UPSTREAM_TOKEN=<token> \
  ACL_FILE=./acl.yaml \
  LISTEN_PORT=9090 \
  go run ./apps/hindsight-auth-proxy/.

# Confirm the revoked identity now gets 403
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: alice@brickeye.com' \
  http://localhost:9090/mcp/hermes-alice/
# → 403 (after offboard) or 200 (only team revoked — personal bank still active)

# Confirm other users are unaffected
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: bob@brickeye.com' \
  http://localhost:9090/mcp/hermes-bob/
# → 200
```

## Next step

Deploy the updated ACL → use the `acl-deploy` skill.
