---
name: acl-view
description: >
  Show and explain the current hindsight-auth-proxy ACL. Fetches ACL_YAML_CONTENT
  from Railway, renders who has access to what, and can compute the effective access
  for a given email. Use to audit access, answer "who can see bank X?", or verify
  a change before deploying.
license: MIT
compatibility: Requires railway CLI (`railway`).
metadata:
  author: Brickeye
  version: "1.0"
---

# ACL View

Show and explain the current hindsight-auth-proxy ACL.

## Setup

```bash
railway link   # link to the ai-memory project if not already done
```

## Fetch the current ACL

```bash
railway variable get ACL_YAML_CONTENT \
  --service hindsight-auth-proxy \
  --environment dev        # or: prod
```

## Ask the user

1. **Show full ACL** — display and annotate the raw YAML
2. **Who can access bank `<bank_id>`?** — enumerate all emails that match
3. **What can `<email>` access?** — list all banks they're permitted to read/write

---

### 1. Show full ACL (annotated)

Display the YAML and annotate each section:

```
admins:       → bypass ALL bank checks; can enumerate /mcp/ and /v1/default/banks
shared:       → every authenticated tailnet user (even without any other entry)
teams:        → members get access to team.banks patterns; grants are additive
users:        → per-email private grants on top of shared + team grants
```

Count summary: `N admins, M shared patterns, K teams, P user entries`.

### 2. Who can access bank `<bank_id>`?

Walk the ACL in grant order:

1. **Admins** — list all (they always have access)
2. **Shared** — check if any pattern in `shared` matches `bank_id`; if yes, access is universal
3. **Teams** — for each team, check if any pattern in `team.banks` matches; if yes, list `team.members`
4. **Users** — for each user entry, check if any pattern in `user.banks` matches; list matching emails

Example output:
```
Bank: team-sw-roadmap
  Admins (always):  richard@brickeye.com
  Shared:           no (org-* does not match)
  Teams:            sw → alice@brickeye.com, bob@brickeye.com
  User grants:      none
Total access:  richard, alice, bob
```

### 3. What can `<email>` access?

Resolve effective grants in order:

1. If email is in `admins` → **all banks, unscoped paths**; stop.
2. Collect `shared` patterns.
3. Collect `banks` patterns from every team the email is a member of.
4. Collect `banks` patterns from `users[email]` (if present).
5. Union all patterns; output them grouped.

Example output:
```
alice@brickeye.com
  Admin:          no
  Shared:         org-*
  Teams (sw):     team-sw-*
  Teams (gen):    team-gen-*
  Personal:       hermes-alice, scratch-alice-*
  Unscoped paths: NO (403 on /mcp/ and /v1/default/banks)
```

## Schema quick reference

```yaml
admins:
  - email           # full bypass; can enumerate banks

shared:
  - glob            # every authenticated user; use only for org-wide content

teams:
  slug:             # matches LiteLLM team slug
    banks:
      - glob        # bank id patterns all members may access
    members:
      - email       # Tailscale identities (lowercase; case-insensitive at runtime)

users:
  email:
    banks:
      - glob        # additive on top of shared + team grants
```

**Glob syntax:** `*` matches any non-`/` characters. Bank ids never contain `/`.

**Case handling:** emails are lowercased at request time; store them lowercase in the ACL.

## Output

Show:
- The raw YAML (or the requested view)
- Any anomalies: empty team member lists, overlapping user+shared patterns,
  admin emails also in users (redundant but harmless)
- Suggested cleanup if anomalies found
