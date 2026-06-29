# Recall Hang Investigation

**Date:** 2026-06-29  
**Symptom:** `POST /v1/default/banks/{bank_id}/memories/recall` hangs at the HTTP body level for banks with content. The server logs show the recall completing in under 1 second; the client never receives a response body and times out.

---

## Observed Behaviour

Smoke test run from richard's machine (tailnet) against `http://ai-memory.baiji-cloud.ts.net:8888`:

| Bank | Nodes | Retain (POST /memories) | Recall (POST /memories/recall) |
|---|---|---|---|
| `team-sw` | 3 | ✅ 200 OK | ⚠️ client timeout (25s) |
| `team-product` | 6 | ✅ 200 OK | ⚠️ client timeout (25s) |
| `org-general` | 2 | ✅ 200 OK | ⚠️ 200 OK, 0 items (fast path) |

Retain works on all banks. Recall returns HTTP 200 headers immediately but the body never arrives. `org-general` responds quickly because it is nearly empty and likely hits a zero-result fast path before any serialization.

## Server-Side Evidence

Railway logs for `hindsight-app` show the recall completing normally:

```
[RECALL team-sw-8614-49dca6]  Complete: 3 facts (80 tok)  | 0.317s
[RECALL team-pro-84105-bbff65] Complete: 6 facts (189 tok) | 0.530s
[RECALL org-gene-4033-ef0fc9]  Complete: 2 facts (40 tok)  | 0.350s
```

The recall engine finishes in well under 1 second. The HTTP response is never flushed to the client.

## Proxy Behaviour

`hindsight-auth-proxy` is a transparent TCP proxy (tsnet listener → `http.ReverseProxy` to `hindsight-app.railway.internal:8888`). It does not buffer or modify response bodies. The hang is therefore upstream of the proxy — either in `hindsight-app`'s response serialization/flush, or in the Railway-internal network path.

Curl verbose confirms headers arrive, then stall:

```
< HTTP/1.1 200 OK
< Content-Type: application/json
< Transfer-Encoding: chunked
...
* Operation timed out after 25000ms with 0 bytes received [body]
```

`Transfer-Encoding: chunked` is set. The server starts a chunked response but sends no chunks.

## Hypotheses (in priority order)

1. **Response serialization blocks on a second LLM/embedding call.** The recall engine logs "complete" after graph traversal, but the HTTP handler may do a second pass (e.g. re-ranking, chunk expansion, or entity annotation) that hangs waiting on the embedding service or LLM. The log line may be emitted before that second pass.

2. **Background consolidation holds a read lock.** During the smoke test, `user-richard` was running heavy consolidation (56 memories, 12 LLM batches). If `hindsight-app` uses a single SQLite WAL per tenant (not per bank), a long-running write transaction could block reads on other banks. The fast `org-general` response would be explained by a read completing before the lock is re-acquired.

3. **Chunked flush never called.** The HTTP handler builds the response object but calls the ASGI/uvicorn response writer without an explicit flush between the json-serialised chunks, so the last chunk (including the zero-length terminator) is buffered indefinitely.

4. **Railway-internal TCP idle timeout.** A long-lived keep-alive connection from the proxy to `hindsight-app.railway.internal` is reset by Railway's internal load balancer after an idle period that happens to fall between the server finishing serialization and writing the socket.

## Investigation Steps

### 1. Reproduce cleanly (no background consolidation)

Wait for `user-richard` consolidation to fully drain (check Railway logs for no active `[CONSOLIDATION]` lines), then immediately issue a recall to `team-sw`:

```bash
curl -s --max-time 30 -X POST \
  -H 'Content-Type: application/json' \
  -d '{"query":"routing architecture go infra","budget":"low"}' \
  http://ai-memory.baiji-cloud.ts.net:8888/v1/default/banks/team-sw/memories/recall
```

If this succeeds, hypothesis 2 (lock contention) is confirmed.

### 2. Check for a second blocking call in hindsight-app logs

While a recall is in-flight (during the hang window), check Railway logs for `hindsight-app` to see if any further LLM/embedding calls are logged after the `[RECALL ...] Complete` line:

```
railway logs --service 80deadd0-9999-4f01-9f88-f5d0641f2b5f \
  --environment ab280ef6-e63e-44be-9fc3-8c62a43197a1 \
  -p d9265f9e-6a28-49e8-8d6e-880f86e07d2e 2>&1 | grep -E "recall|embed|llm|RECALL"
```

Look for any activity on `team-sw` or `team-product` after the `Complete` log line. If there is activity, hypothesis 1 (second LLM pass) is confirmed.

### 3. Bypass the proxy — call hindsight-app directly

From within the Railway network (e.g. via `railway run` or a shell on any service in the `ai-memory` project):

```bash
curl -s --max-time 15 -X POST \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer 2a7652723e2df17f4906a4b8a0b4906c905f1e679d07c508a6cfdd56e4f77ea9" \
  -d '{"query":"routing architecture","budget":"low"}' \
  http://hindsight-app.railway.internal:8888/v1/default/banks/team-sw/memories/recall
```

If this hangs too, the proxy is ruled out and the bug is in `hindsight-app` itself. If this succeeds, the issue is in the proxy→app connection (hypothesis 4, or a proxy response-streaming bug).

### 4. Check uvicorn / ASGI flush behaviour

In `hindsight-app` source, look at the recall HTTP handler. Confirm:
- Whether the response is built as a single `JSONResponse` or a `StreamingResponse`.
- Whether there is an explicit `await response.body()` or flush before the ASGI send call.
- Whether `uvicorn` is configured with `--no-access-log` or any response-buffering option.

---

## Affected Components

- `hindsight-app` (Railway service `80deadd0`, project `ai-memory`) — primary suspect
- `hindsight-auth-proxy` (Railway service `b61d4755`) — transparent TCP relay, not suspect but worth ruling out in step 3

## Not Affected

- Retain (`POST /memories`) — works correctly on all banks
- `/stats` — works correctly on all banks
- MCP `tools/list` — works correctly
- ACL enforcement — works correctly (deny returns 403 immediately)
