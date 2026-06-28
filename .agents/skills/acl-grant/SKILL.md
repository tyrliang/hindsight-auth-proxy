---
name: acl-grant
description: >
  Add a bank access grant to the hindsight-auth-proxy ACL. Guides through
  adding a user's personal bank grant, assigning team membership, adding a
  shared bank pattern, or granting admin access. Asks which environment
  (dev or prod) and enforces dev-first validation before any prod change.
  Use when onboarding a new employee, creating a new team bank, or expanding access.
license: MIT
compatibility: Requires railway CLI (`railway`).
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL Grant

Add a bank access rule to the hindsight-auth-proxy ACL.

## Environments

| | Dev | Prod |
|---|---|---|
| Railway env | `dev` | `prod` |
| Service name | `hindsight-auth-proxy` | `hindsight-auth-proxy` |
| Proxy tailnet node | `ai-memory-dev` | `ai-memory` |
| Hindsight tailnet node | `hindsight-dev` | `ai-memory-richard` (current; becomes `hindsight` at cutover) |
| Who uses it | Test personas (alice, bob, carol) | Real Brickeye employees |
| Risk | Low — safe to experiment | High — affects real employee access immediately |

**Ask the user which environment they want to change before doing anything.**
If prod: confirm they have already tested the same change in dev first.

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

Edit the appropriate file locally, then use `acl-deploy` to push.

## Schema reference

```yaml
admins:            # Full access including unscoped paths (bank list, metrics, docs)
  - richard@brickeye.com

shared:            # Bank glob patterns accessible to every authenticated user
  - org-*          # matches org-handbook, org-policies, org-playbooks, …

teams:
  sw:              # Team slug (matches LiteLLM team slug)
    banks:         # Bank glob patterns all members may access
      - team-sw-*
    members:       # Tailscale email addresses (lowercase)
      - alice@brickeye.com
      - bob@brickeye.com

users:             # Per-email private grants, additive on top of shared + team grants
  alice@brickeye.com:
    banks:
      - hermes-alice       # exact bank id
      - scratch-alice-*    # glob: any bank starting with "scratch-alice-"
```

**Grant precedence (additive, never overriding):**
1. `admins` → full access everywhere, always
2. `shared` → every authenticated tailnet user
3. `teams` → members of that team
4. `users` → per-email additions

**Glob syntax:** `*` matches any run of non-`/` characters. Bank ids never contain `/`.

**Default deny:** an email matching no grant pattern → `403`.

## Ask the user

What type of grant?

1. **New employee onboarding** — add personal bank + team memberships
2. **Team bank pattern** — allow a team to access a new family of banks
3. **Shared bank** — make a bank pattern accessible to every authenticated user
4. **Admin grant** — give an email full unrestricted access

---

### 1. New employee onboarding

Collect:
- Tailscale email (e.g. `newperson@brickeye.com`) — must match their Tailscale identity exactly; store lowercase
- Personal bank id (convention: `hermes-<firstname>`) and scratch banks (`scratch-<firstname>-*`)
- Team slugs they belong to: `gen`, `sw`, `rnd`, `hw`, `exec`, `fin`

Add under `users`:
```yaml
users:
  newperson@brickeye.com:
    banks:
      - hermes-newperson
      - scratch-newperson-*
```

Add to each team's `members` list:
```yaml
teams:
  sw:
    members:
      - alice@brickeye.com
      - newperson@brickeye.com   # ← add here
```

### 2. Team bank pattern

Collect: team slug + new bank glob (e.g. `team-sw-q4-*`)

```yaml
teams:
  sw:
    banks:
      - team-sw-*
      - team-sw-q4-*   # ← add if narrower pattern needed
```

For a brand-new team:
```yaml
teams:
  newteam:
    banks:
      - team-newteam-*
    members:
      - person@brickeye.com
```

### 3. Shared bank pattern

```yaml
shared:
  - org-*
  - public-*    # ← add here
```

Shared banks are accessible to **every authenticated tailnet user** including people
with no other ACL entry. Use only for genuinely org-wide content.

### 4. Admin grant

```yaml
admins:
  - richard@brickeye.com
  - newadmin@brickeye.com   # ← add here
```

Admins bypass all bank checks and can enumerate banks via unscoped paths.
Restrict to ops/platform only.

## Verify locally before deploying

Regardless of target environment, test the edited ACL locally first using Mode B
(proxy local, Railway dev Hindsight as upstream — safe, not prod):

```bash
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL=http://hindsight-dev.baiji-cloud.ts.net:8888 \
  HINDSIGHT_UPSTREAM_TOKEN=<dev-token> \
  ACL_FILE=./acl-dev.yaml \
  LISTEN_PORT=9090 \
  go run ./apps/hindsight-auth-proxy/.

# Confirm the new grant works
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: newperson@brickeye.com' \
  http://localhost:9090/mcp/hermes-newperson/
# → 200

# Confirm isolation still holds
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: newperson@brickeye.com' \
  http://localhost:9090/mcp/hermes-richard/
# → 403
```

## Next step

Deploy the updated ACL → use the `acl-deploy` skill.
