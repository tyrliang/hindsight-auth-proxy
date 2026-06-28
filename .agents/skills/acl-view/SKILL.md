---
name: acl-view
description: >
  Show and explain the current hindsight-auth-proxy ACL for dev or prod.
  Downloads the ACL object from the Railway Storage Bucket for the target
  environment, renders who has access to what, and can compute the effective
  access for a given email. Use to audit access, answer "who can see bank X?",
  or verify a change before deploying.
license: MIT
compatibility: Requires railway CLI (`railway`), aws CLI.
metadata:
  author: Brickeye
  version: "2.0"
---

# ACL View

Show and explain the current hindsight-auth-proxy ACL.

## Environments

| | Dev | Prod |
|---|---|---|
| Railway env | `dev` | `prod` |
| Proxy tailnet node | `ai-memory-dev` | `ai-memory` |
| Who uses it | Test personas (alice, bob, carol) | Real Brickeye employees |
| ACL may differ? | Yes — dev may have test grants not in prod | Yes — prod is the source of truth for real access |

**Ask the user which environment to inspect.** Dev and prod maintain independent
bucket objects (`acl.yaml`); a grant in one does not imply a grant in the other.

## Fetch the current ACL

```bash
# Dev
./scripts/acl-sync.sh get dev

# Prod
./scripts/acl-sync.sh get prod
```

This downloads the bucket `acl.yaml` to `acl-<env>.yaml` in the current directory.

## Ask the user

1. **Show full ACL** — display and annotate the raw YAML
2. **Who can access bank `<bank_id>`?** — enumerate all emails that match
3. **What can `<email>` access?** — list all banks they're permitted to read/write

---

### 1. Show full ACL (annotated)

Display the YAML and annotate each section:

```
admins:   → bypass ALL bank checks; can enumerate /mcp/ and /v1/default/banks
shared:   → every authenticated tailnet user (even with no other entry)
teams:    → members get team.banks patterns; grants are additive
users:    → per-email private grants on top of shared + team grants
```

Print a summary: `N admins, M shared patterns, K teams, P user entries`.

### 2. Who can access bank `<bank_id>`?

Walk the ACL in grant order and print results:

1. **Admins** — list all (they always have access)
2. **Shared** — does any pattern in `shared` match `bank_id`? If yes, access is universal
3. **Teams** — for each team, does any `team.banks` pattern match? If yes, list `team.members`
4. **Users** — for each user, does any `user.banks` pattern match? List matching emails

Example output:
```
Bank: team-sw-roadmap  [env: prod]
  Admins (always):  richard@brickeye.com
  Shared:           no (org-* does not match)
  Teams → sw:       alice@brickeye.com, bob@brickeye.com
  User grants:      none
Total access:  richard, alice, bob
```

### 3. What can `<email>` access?

Resolve effective grants in order:

1. If email is in `admins` → **all banks + unscoped paths**; stop.
2. Collect `shared` patterns.
3. Collect `banks` from every team the email is a member of.
4. Collect `banks` from `users[email]` (if present).
5. Union all patterns; output grouped.

Example output:
```
alice@brickeye.com  [env: prod]
  Admin:          no
  Shared:         org-*
  Teams → sw:     team-sw-*
  Teams → gen:    team-gen-*
  Personal:       hermes-alice, scratch-alice-*
  Unscoped paths: NO (403 on /mcp/ and /v1/default/banks)
```

## Schema quick reference

```yaml
admins:
  - email           # full bypass; can enumerate all banks

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

**Glob:** `*` matches any non-`/` characters. Bank ids never contain `/`.
**Case:** emails are lowercased at request time; store them lowercase in the ACL.

## Output

Show the user:
- The raw YAML (or the requested view), labelled with the environment name
- Any anomalies: empty team member lists, admin emails duplicated in users
  (harmless but redundant), shared patterns that shadow all private grants
- Diff between dev and prod ACLs if the user wants to compare environments
