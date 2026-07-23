# API request, stream, and retry contract

This document defines client construction, request metadata, stream health, non-streaming fallback, retry classification, capacity behavior, cancellation, and terminal normalization. `NET-*` and `RETRY-*` identifiers are normative.

## Contents

- [Client construction and headers](#client-construction-and-headers)
- [Streaming lifecycle](#streaming-lifecycle)
- [Non-streaming fallback](#non-streaming-fallback)
- [Retry loop](#retry-loop)
- [529 and model fallback](#529-and-model-fallback)
- [Fast mode capacity behavior](#fast-mode-capacity-behavior)
- [Persistent unattended retry](#persistent-unattended-retry)
- [Result and error normalization](#result-and-error-normalization)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Client construction and headers

`NET-001` — Construct a client for the selected provider and current credential generation. Default SDK request timeout is 600 seconds, configurable by a bounded positive `API_TIMEOUT_MS`. SDK internal retries are disabled or coordinated so the shared retry loop remains authoritative.

`NET-002` — Common safe headers include product identifier and user agent. First-party requests additionally carry session ID and, when supplied by trusted launch context, remote container ID, remote session ID, SDK client-app identifier, and client request ID.

`NET-003` — Generate a random `x-client-request-id` for each first-party request unless the caller already supplied one. Log only URL path, source, and safe correlation ID. Do not send this header to Bedrock, Vertex, Foundry, or an unknown strict proxy.

`NET-004` — Optional additional-protection setting adds its first-party protection header. Custom headers are parsed from newline-delimited `Name: Value`, split on the first colon, trimmed, and ignore malformed/empty names. Apply destination policy; custom Authorization may intentionally override, but diagnostics reveal only its presence.

`NET-005` — Provider/beta compatibility is explicit. First-party-only request fields (fine-grained tool streaming, global cache scope, beta flags) are absent for cloud adapters and incompatible proxies. An experimental-beta kill switch strips all non-base tool fields except standard prompt-cache control.

`NET-006` — Cache the session-stable portion of tool schemas by canonical tool name plus behavior-bearing input schema. Per-request defer/cache overlays copy rather than mutate the cached base.

## Streaming lifecycle

`NET-010` — A streamed request emits typed protocol events and accumulates one assistant message: message start, content-block start/delta/stop, message delta/usage/stop, thinking/text/tool-use data, request metadata, and stop reason. Unknown future events are retained/ignored according to forward-compatible protocol rules, not treated as text.

`NET-011` — Reset per-attempt partial message, completed blocks, usage, stop reason, first-token timing, and advisor/tool-stream state before a retry. A prior attempt's partial blocks never appear in the retry result unless an explicit resumable-stream protocol says so.

`NET-012` — Optional stream watchdog is enabled only by its runtime gate. Default idle bound is 90 seconds with a warning at half. Reset both timers on every received chunk. When idle bound fires, abort/release stream resources and classify for fallback; record abort propagation timing. An injected HTTP client or response body may ignore context, block forever, or panic: isolate at most one body-read loop per attempt, select the coordinator on caller/attempt/watchdog cancellation, never synchronously await body close, and bound each class of abandoned transport, read, or cleanup work by the configured attempt ceiling.

`NET-013` — Independently record a streaming stall when the gap between post-first-chunk events exceeds 30 seconds. Stall detection is observability only; the watchdog owns active cancellation.

`NET-014` — Treat stream completion as incomplete when:

- no `message_start` arrived; or
- a message started but no content block completed and no stop reason arrived.

An empty turn with a valid stop reason is legitimate. Incomplete streams trigger failure/fallback, never false success.

`NET-015` — Explicit user abort propagates as user cancellation and is never retried/fallback. If the SDK reports an abort while the caller signal is not aborted, classify it as connection timeout.

## Non-streaming fallback

`NET-020` — On recoverable streaming error/incomplete stream, issue non-streaming fallback unless explicitly disabled by environment/feature policy. Disabling may be required when partial streamed tool execution could cause duplicate side effects.

`NET-021` — Fallback request starts from the authoritative pre-attempt message/context and captures new request correlation. It never reuses partial streamed tool-use blocks as completed calls.

`NET-022` — Per-attempt fallback timeout is `API_TIMEOUT_MS` when set; otherwise 120 seconds for remote sessions and 300 seconds locally. Cap non-streaming output at 64,000 tokens and adjust thinking/output allocation without violating minimum response semantics.

`NET-023` — Fallback uses the common retry loop. If the streaming error was a 529, seed the consecutive-529 count with one so model fallback timing is independent of whether overload occurred in streaming or non-streaming mode.

`NET-024` — A streaming endpoint 404 from a gateway may fall back to non-streaming when the latter endpoint is supported. Record the cause. Never reinterpret authentication/policy 404s without destination-aware classification.

## Retry loop

`RETRY-001` — Default maximum retries is 10, configurable by bounded integer. Total attempts are retries plus the initial attempt. Each retry checks cancellation before client construction and before/during delay.

`RETRY-002` — Delay algorithm:

```text
if retry-after parses as integer seconds:
    delay = seconds * 1000
else:
    base = min(500 ms * 2^(attempt-1), 32,000 ms)
    delay = base + random(0, 0.25 * base)
```

Server `retry-after` intentionally overrides the ordinary cap. Emit a normalized retry status before sleeping.

`RETRY-003` — Retryable conditions:

- explicit `x-should-retry:true` when subscriber tier permits;
- connection errors;
- HTTP 408, 409;
- HTTP 429 for nonconsumer or eligible enterprise account;
- HTTP 401 after clearing helper/OAuth cache;
- OAuth-revoked 403;
- server errors 5xx including 529/overloaded message;
- recognized max-token context-overflow error;
- recognized AWS/GCP credential errors after provider cache clear;
- remote managed-relay 401/403 as transient infrastructure auth.

`x-should-retry:false` suppresses retry except an internal privileged build may override it only for 5xx. Mock/test rate-limit errors never retry.

`RETRY-004` — Recreate the client after 401, OAuth-revoked 403, Bedrock/Vertex credential error, or stale connection (`ECONNRESET`/`EPIPE`). Under its gate, stale connection permanently disables keep-alive for the process so the retry opens a new socket.

`RETRY-005` — Parse legacy max-token overflow only for status 400 and exact shape `input length and max_tokens exceed context limit: input + max > limit`. Set available output to `limit - input - 1000` safety buffer. Require at least 3,000 output tokens and enough for thinking budget plus one; otherwise fail instead of retrying impossible context.

## 529 and model fallback

`RETRY-010` — Retry 529 only for foreground/user-blocking or safety-critical query sources. Undefined source conservatively counts foreground. Background summaries, suggestions, titles, and other auxiliary work fail immediately to avoid capacity amplification.

`RETRY-011` — Track consecutive 529s. After three, use configured fallback model when eligible. If no fallback and external ordinary session is repeatedly overloaded, return the explicit repeated-overload error rather than cycling indefinitely.

`RETRY-012` — A successful non-529 attempt resets the relevant consecutive state through request completion. Model fallback is an explicit control signal so the outer query engine rebuilds model-dependent context/tools, not an in-place string mutation.

## Fast mode capacity behavior

`RETRY-020` — When fast mode receives 429/529:

- a specific overage-disabled header permanently disables fast mode for the request/session policy;
- `retry-after < 20 seconds` sleeps and retries fast to preserve prompt cache;
- longer/unknown wait enters standard-speed cooldown.

Cooldown duration is max(server wait or default 30 minutes, minimum 10 minutes). Reason is rate limit or overload.

`RETRY-021` — If API rejects the fast-mode parameter as unavailable, disable fast mode and retry standard speed. Persistent unattended mode bypasses this short/long fast-mode branch and uses persistent heartbeats.

## Persistent unattended retry

`RETRY-030` — A build-gated unattended mode may retry 429/529 without a finite attempt count. Ordinary errors remain bounded. Backoff caps at five minutes and an absolute server/reset wait caps at six hours.

`RETRY-031` — For 429, prefer a valid unified rate-limit reset timestamp. For long waits, split sleep into 30-second chunks, emit a system retry heartbeat each chunk, and recheck cancellation. Keep a separate persistent attempt counter while clamping the finite loop counter.

`RETRY-032` — Persistent mode still respects hard cancellation, destination/auth policy, and request identity. It is not a general "retry everything" switch.

## Result and error normalization

`NET-030` — On each retry/fallback, correlate query source, model, provider, attempt, client request ID, server request ID when available, and originating stream request ID. Correlation metadata is diagnostic and not model-visible unless deliberately surfaced.

`NET-031` — Publish usage only from accepted response events. Do not add partial failed-attempt output usage to conversation semantics; accounting/telemetry may record it separately.

`NET-032` — Terminal errors distinguish authentication, permission/policy, rate limit, overload, timeout, connection, malformed stream, context overflow, provider unavailable, and user abort. Preserve safe server request ID and remediation without raw response secrets.

`NET-033` — Retry/fallback does not duplicate accepted tool side effects. If streamed tool execution has started and cannot be proven unexecuted, disable automatic non-stream fallback or reconcile tool-use identifiers before continuation.

## Acceptance scenarios

**NET-A01 — Backoff and server delay.** A request fails twice without headers. Delays use 500 ms then 1,000 ms bases plus independent 0–25% jitter. A later `retry-after: 100` waits 100 seconds despite 32-second ordinary cap.

**NET-A02 — Source-aware overload fallback.** A background title request gets 529 and fails immediately; a foreground turn gets three consecutive 529s and switches to its configured fallback model.

**NET-A03 — Incomplete stream fallback.** Stream emits message start but no completed blocks and no stop reason. It is incomplete and enters bounded non-stream fallback. A message start with end-turn stop and no blocks succeeds empty.

**NET-A04 — Watchdog versus user cancellation.** Watchdog fires but user signal is not aborted. Stream resources release and request falls back. Pressing cancel produces user-abort with no retry.

**NET-A05 — Context overflow adjustment.** Max-token error says 188,059 input plus 20,000 over 200,000. Available context subtracts 1,000 safety buffer; if below 3,000, request fails rather than retrying with an unusable budget.

**NET-A06 — Stale connection recovery.** `ECONNRESET` under the gate disables keep-alive, rebuilds client, and retries without changing logical request identity.

## Non-normative provenance

Reference behavior was specified from API client construction, model request/stream generator, retry and fallback generators, provider errors, fast-mode controller, usage/correlation projection, and tool schema conversion under `services/api/`, query transport, and network utilities. Paths and symbols are provenance only.
