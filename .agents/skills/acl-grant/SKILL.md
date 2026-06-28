---
name: acl-grant
description: >
  Add a bank access grant to the hindsight-auth-proxy ACL. Guides through
  adding a user's personal bank grant, assigning team membership, adding a
  shared bank pattern, or granting admin access. Use when onboarding a new
  employee, creating a new team bank, or expanding access.
license: MIT
compatibility: Requires railway CLI (`railway`) or direct Railway dashboard access.
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL Grant

Add a bank access rule to the hindsight-auth-proxy ACL.

The ACL is the single source of truth for who can access which memory banks.
It lives in the `ACL_YAML_CONTENT` environment variable on the `hindsight-auth-proxy`
Railway service. After any edit you must redeploy — use the `acl-deploy` skill.

## Setup

Fetch the current ACL before editing:
```bash
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy \
  --environment dev        # or prod
```
Save it locally as `acl.yaml` to edit, then deploy the result.

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
    members:       # Tailscale email addresses
      - alice@brickeye.com
      - bob@brickeye.com

users:             # Per-email private grants (additive on top of shared + team grants)
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
`team-sw-*` matches `team-sw-roadmap`, `team-sw-sprint-42`, etc.

**Default deny:** an email that matches no grant pattern → `403`.

## Ask the user

What type of grant?

1. **New employee onboarding** — add personal bank + team memberships
2. **Team bank pattern** — allow a team to access a new family of banks
3. **Shared bank** — make a bank pattern accessible to every authenticated user
4. **Admin grant** — give an email full unrestricted access

---

### 1. New employee onboarding

Collect:
- Tailscale email (e.g. `newperson@brickeye.com`) — must match their Tailscale identity exactly
- Personal bank id (convention: `hermes-<firstname>`) and any scratch banks (`scratch-<firstname>-*`)
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

Collect:
- Team slug (existing or new)
- New bank glob pattern (e.g. `team-sw-q4-*`)

Add to the team's `banks` list:
```yaml
teams:
  sw:
    banks:
      - team-sw-*
      - team-sw-q4-*   # ← add if more specific pattern needed
```

For a new team, add the full block:
```yaml
teams:
  newteam:
    banks:
      - team-newteam-*
    members:
      - person@brickeye.com
```

### 3. Shared bank pattern

Add to `shared`:
```yaml
shared:
  - org-*
  - public-*    # ← add new pattern here
```

Shared patterns are accessible to **every authenticated tailnet user**, including
people with no other ACL entry. Use only for genuinely org-wide content.

### 4. Admin grant

Add to `admins`:
```yaml
admins:
  - richard@brickeye.com
  - newadmin@brickeye.com   # ← add here
```

Admins bypass all bank checks and can enumerate banks via unscoped paths (`/mcp/`, `/v1/default/banks`).
Restrict this list to ops/platform only.

## Verify

After editing, test the grant locally before deploying (no Railway changes yet):

```bash
# Start proxy in dev mode pointing at dev Railway Hindsight
DEV_IDENTITY_HEADER=X-Dev-User \
  HINDSIGHT_UPSTREAM_URL=http://hindsight-dev.baiji-cloud.ts.net:8888 \
  HINDSIGHT_UPSTREAM_TOKEN=<token> \
  ACL_FILE=./acl.yaml \
  LISTEN_PORT=9090 \
  go run ./apps/hindsight-auth-proxy/.

# Confirm the new grant works
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: newperson@brickeye.com' \
  http://localhost:9090/mcp/hermes-newperson/
# → 200

# Confirm isolation still holds (they cannot access another user's bank)
curl -s -o /dev/null -w '%{http_code}\n' \
  -H 'X-Dev-User: newperson@brickeye.com' \
  http://localhost:9090/mcp/hermes-richard/
# → 403
```

## Next step

Deploy the updated ACL → use the `acl-deploy` skill.
