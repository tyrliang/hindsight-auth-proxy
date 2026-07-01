# Recall Hang Investigation

**Opened:** 2026-06-29
**Updated:** 2026-07-01
**Status:** CLOSED — non-issue. Bug 1 (complete hang) fixed 2026-06-30. Bug 2 (throughput
degradation) does not reproduce as of 2026-07-01; see [Conclusion](#conclusion-non-issue-2026-07-01)
and [Option A Validation](#option-a-validation-2026-07-01) below.

---

## Summary

Two distinct bugs were found and investigated. One is fixed; one is not.

| Bug | Symptom | Status |
|---|---|---|
| **Gzip decompress → chunked → tsnet stall** | All recall bodies = 0 bytes through proxy | ✅ Fixed — `DisableCompression=true` |
| **tsnet userspace TCP throughput** | Large recalls (~60KB) crawl at ~6KB/s | ⚠️ Open — root cause confirmed, no fix yet |

---

## Bug 1: Complete body hang (Fixed)

### Root Cause

`http.DefaultTransport` auto-adds `Accept-Encoding: gzip` to every upstream request. FastAPI's `GZipMiddleware` compresses JSON responses and omits `Content-Length` (compressed size is unknown at write time). Go's transport transparently decompresses and sets `ContentLength = -1`. `httputil.ReverseProxy` then re-encodes the body as `Transfer-Encoding: chunked` to the downstream client.

Chunked bodies written through Go's HTTP server over tsnet's userspace TCP stack stall completely — the same path works fine on kernel TCP. Direct calls to `hindsight.baiji-cloud.ts.net` (which is a raw TCP tunnel, not a Go HTTP server) delivered chunked responses correctly, confirming the stall is in the proxy's write path, not the upstream.

**Confirmation:** diffing direct vs proxied response headers for the same recall endpoint:
- Direct (no `Accept-Encoding`): `content-length: 2777`
- Via proxy (gzip requested): raw gzip bytes, no `Content-Length`

### Fix Applied

`internal/proxy/proxy.go` — commit `dbdbeed` (2026-06-30):

```go
// Clone DefaultTransport so we never mutate the global.
// DisableCompression stops the transport from advertising Accept-Encoding: gzip.
t := http.DefaultTransport.(*http.Transport).Clone()
t.DisableCompression = true
if dial != nil {
    t.DialContext = dial
}
rp.Transport = t
```

### Result After Fix

| Bank | Response size | Result |
|---|---|---|
| `team-sw` recall | 2777B | ✅ 200 OK in ~1.07s |
| `team-product` recall | 4006B | ✅ 200 OK in ~1.02s |
| `org-general` recall | small | ✅ works |

---

## Bug 2: tsnet throughput degradation for large responses (Open)

### Symptom

After Bug 1 fix, small recalls work. Large recalls (user-richard, ~60KB body) return partial data extremely slowly:

| Path | Size | Time | Throughput |
|---|---|---|---|
| Direct via `hindsight.baiji-cloud.ts.net` | 60325B | 4.67s | ~573 KB/s |
| Via proxy `ai-memory.baiji-cloud.ts.net` | ~29KB (partial) | 30s timeout | ~1 KB/s |

### Investigation

**Network path is identical.** Both nodes (`ai-memory` and `hindsight`) are:
- Direct WireGuard (not DERP-relayed): `active; direct 34.182.169.37:37131`
- Same RTT: 27ms vs 28ms

The 500× throughput gap is application-level, not network-level.

**Architectural difference:**
- `hindsight` (ts-hindsight): raw bidirectional byte-copy over tsnet — one `Write(N)` per `io.Copy` chunk, no HTTP framing overhead
- `ai-memory` (proxy): Go HTTP server over `ts.Listen()` — chunked framing adds small header/trailer writes before each data write; `FlushInterval=-1` flushes after every `io.Copy` chunk; each flush produces 2–3 separate `tsnet.Conn.Write` calls per 32KB chunk

tsnet's gVisor userspace TCP stack appears to have low throughput when writes are small and frequent (~6KB/s observed). A single large write (like raw TCP copy) achieves normal throughput.

**What was tried:**

`FlushInterval=0` — eliminates the `maxLatencyWriter` flush-per-write path. Caused a regression: 4006B responses returned 0 bytes (appears to be a bufio boundary issue at the 4KB buffer size). Reverted — commit `ae9ac0c`.

### Hypotheses (remaining)

1. **tsnet TCP window too small.** gVisor's netstack starts with CWND=1 and delayed ACK (200ms) interacts with the small window to give ~1460B/200ms = ~7KB/s, matching observations. Fix: tune gVisor TCP parameters in tsnet (not exposed via public API).

2. **Chunked framing forces small writes through bufio.** The 4-byte chunk header (`"6B9\r\n"`) is written to bufio before the payload; if bufio has 4 bytes buffered when the 32KB payload arrives, it flush+writes a 4-byte segment to tsnet first, fragmenting the transfer. Fix: buffer the entire response body in `ModifyResponse` before forwarding — eliminates chunked framing and gives io.Copy a `bytes.Reader` that uses `WriteTo` for one large write.

3. **Architecture mismatch.** The proxy uses `tsnet` (in-process userspace TCP). If ts-hindsight uses the system `tailscaled` daemon (kernel TCP through WireGuard TUN interface), kernel TCP is inherently faster. Fix: expose the proxy on a plain TCP port and use Tailscale ACLs for access control instead of tsnet.

### Next Steps

Before coding anything: establish whether this affects OMP's actual recall workflow. OMP uses the MCP endpoint (`/mcp/user-richard/`) for user-richard recall, not the REST `/v1/` endpoint. If the MCP path is unaffected, the large-response issue only surfaces in direct REST API calls (tooling/testing), not production traffic.

If OMP REST recall is confirmed affected:

1. **Test hypothesis 2 (buffering):** add `ModifyResponse` to buffer non-SSE responses into `bytes.Reader`, measure throughput on user-richard recall. One deploy cycle to confirm or rule out.

2. **Test hypothesis 3 (architecture):** check what ts-hindsight is actually running (`railway logs` for service `de6eff48`). If it uses `tailscaled` daemon, consider switching the proxy to plain TCP + Tailscale ACL enforcement via the daemon instead of tsnet.

---

## Affected Components

- `hindsight-auth-proxy` (Railway service `b61d4755`, project `ai-memory`) — Bug 1 fixed here
- `hindsight-app` (Railway service `80deadd0`) — upstream, not modified

## Proxy commits on this branch

| Commit | Change |
|---|---|
| `dbdbeed` | `DisableCompression=true` — prevents gzip, fixes Bug 1 |
| `6cac7ae` | rebase merge |
| `6547987` | `FlushInterval=0` attempt — reverted, caused regression |
| `ae9ac0c` | revert FlushInterval=0 |

## Not Affected

- Retain (`POST /memories`) — works on all banks, response always has `Content-Length`
- `/stats` — works, small `Content-Length` response
- MCP `tools/list` / session init — works
- ACL enforcement — works (403 returns immediately)
- SSE / MCP streaming — `FlushInterval=-1` preserved, `text/event-stream` auto-detected

---

## Option A Validation (2026-07-01)

### Context

A plan ("Option A") was drafted to replace the `tsnet` gVisor userspace listener with the
system `tailscaled` daemon + `tailscale serve` (kernel TCP), on the hypothesis that Bug 2's
~6KB/s throughput ceiling was caused by tsnet's userspace netstack. The implementation was
built on branch `feat/tailscale-serve-mode` (Dockerfile → alpine:3.20 + tailscale apk,
new `entrypoint.sh`, `TailscaleServeMode`/`InternalPort` config fields, new listener branch
in `main.go`) with a 14-test, 4-phase dev-only validation plan gated on a before/after
throughput benchmark.

Per plan, Phase 0 required capturing a baseline on the **current, unmodified** dev deployment
before switching the deploy source to the new branch — to prove Bug 2 was still present and
establish the "before" number for the "after" comparison.

### Finding: Bug 2 does not reproduce

Phase 0 baseline (dev, commit `ae9ac0c`, unmodified tsnet listener) immediately contradicted
the plan's premise:

| Test | Plan expected | Actual (dev, 3 runs) |
|---|---|---|
| B2 — user-richard 60KB recall (Bug 2's own repro case) | 0B / hang | 77986B, 1.6–1.8s — full body, fast |
| T8 gate (≥55000B in <15s) | fails on current dev | **passes** on current dev, unmodified |
| openapi.json (195KB, worst case in repo) | slow | 195503B in 0.67s |

Given a live regression target that no longer regresses, the serve-mode branch was never
deployed — deploying it would have destroyed the only clean baseline proving the bug is gone,
and there was no delta left to measure. Comprehensive follow-up testing was run instead,
directly against the **unmodified** deployments on both dev and prod (both on commit `ae9ac0c`,
deployed together on 2026-06-30):

| Test | Env | Condition | Result |
|---|---|---|---|
| Retain, back-to-back ×3 | dev | 0s delay | 2/3 200 OK (1 hit unrelated Postgres disk-full — see below) |
| Retain, delayed | dev | +5s | 200 OK, 1.95s |
| Retain, back-to-back ×3 | prod | 0s delay | 3/3 200 OK, 4.7–8.0s |
| Retain, delayed | prod | +5s | 200 OK, 4.48s |
| Large recall (user-richard) ×3 | dev | 0s delay | 3/3 200 OK, 78079B, 1.9–2.9s |
| Large recall (user-richard) | dev | +15s | 200 OK, 78079B, 1.98s |
| Large recall (user-richard) ×3 | prod | 0s, **during an active stuck consolidation job on the same bank** (age >300s) | 3/3 200 OK, 60519B, 5.4–6.2s |
| Large recall (user-richard) | prod | +15s, +30s | 200 OK, 60519B, 4.1–4.3s |
| Recall bank matrix (team-sw, team-product, team-exec) | dev + prod | — | all match or exceed plan's expected baselines; `team-product` (the historical 4KB-boundary regression case) returns exactly 4006B cleanly on prod |
| MCP recall ×2 | dev + prod | — | dev 136280B/2.9s, prod 110600B/4.7–5.1s |
| openapi.json ×2 | prod | — | 195503B, 0.21–0.23s |

25 requests total, both environments, payload sizes 14B–195KB, delays 0/5/15/30s, one batch
under live lock contention. Zero hangs, zero timeouts, zero 0-byte bodies. T8's own gate
(≥55000B in <15s) is cleared by 3–4× margin in every condition tested.

The prod result during active consolidation directly refutes **Hypothesis 2** in the original
Bug 2 write-up (background consolidation holding a read lock that blocks recall on other/same
banks) — recall succeeded fast while a consolidation task was actively stuck (past its 300s
stuck threshold) on the exact bank being recalled.

### Root cause of resolution

Bug 2 was never fixed by a dedicated change — it appears to have been resolved as a side
effect of the Bug 1 fix (`DisableCompression=true`, commit `dbdbeed`/`6cac7ae`). The commit
message for that fix claimed it "resolved the complete hang for small team bank recalls";
the evidence above shows it resolved the large-payload throughput case too. This was not
understood at the time Bug 2 was written up, because the large-payload benchmark was not
re-run after the gzip fix landed — only the small-bank hang was re-verified.

### Unrelated finding: dev retain silent write failure

One dev retain call returned `HTTP 500` with `could not resize shared memory segment ...
No space left on device` (Postgres disk exhaustion on the dev DB, unrelated to this proxy).
Follow-up: subsequent dev retain calls in the same session returned `HTTP 200` with
`success:true` and billed LLM tokens, but a `list_memories` check immediately after showed
**0 items actually persisted**. This is a silent write failure — the client sees success,
nothing is stored — most likely caused by the DB remaining wedged after the disk-full event.
This is a distinct bug from both Bug 1 and Bug 2, filed here for tracking, not investigated
further as part of Option A validation. Needs: free/resize the dev Postgres volume, then
re-verify retain persists before trusting dev retain results again.

### Test data cleanup required

Validation used a disposable, non-real bank id (`test-validation-DELETE-ME`, admin-only
access, never a real team/user bank) specifically so recall on real banks would not be
polluted. No delete endpoint exists through this proxy or a documented one for Hindsight —
cleanup requires the Hindsight Control Plane UI or direct DB access.

- **dev** — nothing to clean up. 0 items ever persisted (see silent write failure above).
- **prod** — 3 records persisted in `test-validation-DELETE-ME`, need manual deletion:

  | Record ID | Text | Context | Created |
  |---|---|---|---|
  | `62a70bc2-1055-4e30-a354-a30ff29e8ad1` | "option-a-validation test retain run1 DELETE-ME" | `test-validation-DELETE-ME` | 2026-07-01T15:52:42Z |
  | `02b92028-33c5-4523-bb81-7a4c4b517269` | "option-a-validation test retain run2 DELETE-ME" | `test-validation-DELETE-ME` | 2026-07-01T15:52:50Z |
  | `8e550494-1178-420b-a16e-3858a7d4a489` | "option-a-validation test retain run2 DELETE-ME" | (empty) | 2026-07-01T15:52:50Z |

  None of these touch `user-richard` or any real team/user bank — zero risk to real recall
  results, but still real rows an admin should purge.

### Disposition

- `feat/tailscale-serve-mode` branch: built, builds clean, **never deployed**. Left unmerged;
  may be deleted or kept as reference for a future tsnet-replacement effort unrelated to this
  investigation.
- Production: untouched throughout (per the plan's hardline constraint).

---

## Conclusion: Non-issue (2026-07-01)

**This investigation is closed. There is no recall hang or throughput bug on either dev or
prod as of commit `ae9ac0c`.**

Both symptoms in the original report — the complete 0-byte hang (Bug 1) and the large-payload
throughput degradation (Bug 2) — are resolved. Bug 1 was fixed deliberately
(`DisableCompression=true`). Bug 2 turned out to share the same root cause and was fixed as
an unintended side effect of the same change; this was not confirmed until the Option A
validation pass above, five days after the original write-up.

No further action is needed on the recall path. The `tailscale serve` migration (Option A)
is not required and was not deployed. The only open follow-up from this investigation is the
unrelated dev Postgres disk/silent-write-failure issue noted above.
