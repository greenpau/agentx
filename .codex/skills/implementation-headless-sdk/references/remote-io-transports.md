# SDK URL and remote I/O transports

This reference owns the noninteractive transport adapters beneath structured I/O. It separates FIFO admission, socket observation, uploader completion, server acknowledgement, semantic processing, and transcript durability. None of these layers may be collapsed into “sent.”

[The CCR worker wire catalog](../../implementation-remote-bridge/references/ccr-wire-catalog.md) is the sole exact definition of CCR routes, headers, request/response bodies, SSE framing, status handling, and queue bounds. The `HTR-*` contracts below define how the headless adapter selects and composes those operations.

Open [the transport pipeline diagram](../assets/remote-io-transports.drawio) while implementing selection, recovery, queues, and close behavior.

## Contents

1. [Transport selection and Remote I/O](#transport-selection-and-remote-io)
2. [WebSocket transport](#websocket-transport)
3. [Hybrid transport and serial uploader](#hybrid-transport-and-serial-uploader)
4. [SSE transport](#sse-transport)
5. [CCR worker client](#ccr-worker-client)
6. [NDJSON and SDK event protection](#ndjson-and-sdk-event-protection)
7. [Acceptance scenarios](#acceptance-scenarios)

## Transport selection and Remote I/O

**HTR-001 — Ordered transport selection.** A generic SDK URL selects CCR SSE-plus-HTTP first when the CCR-v2 control is active; otherwise a WebSocket URL selects Hybrid when POST-ingress is active and plain WebSocket otherwise. CCR rewrites `ws/wss` to `http/https` and appends `/worker/events/stream` to the session path. A non-WebSocket URL outside CCR is rejected. This selection is independent of bridge topology.

**HTR-002 — Dynamic ingress authentication.** Construct initial headers from the current session-ingress token and optional environment-runner version. Supply a reconnect callback that rereads both rather than capturing a stale token. Missing initial token is diagnostic, not fabricated anonymous authorization. Token header shape follows `SDKAPI-*`/remote credential rules.

**HTR-003 — Callback-before-connect invariant.** Create the pass-through input, wire inbound data and close callbacks, construct the CCR client when selected, and let that constructor install its SSE received-event callback before connecting. Early catch-up frames must therefore have a delivery observer. A CCR selection paired with a non-SSE transport is a fatal invariant violation.

**HTR-004 — CCR integration.** CCR initialization restores external worker metadata concurrently with worker registration, installs internal-event transcript writer/readers, maps command lifecycle `started → processing` and `completed → processed`, and projects session state/metadata. Initialization failure records a non-PII reason and requests status-1 shutdown. Internal-event flush is a distinct method and no-op without CCR.

**HTR-005 — Bridge echo and keepalive.** In bridge process mode, echo outbound control requests to local stdout so the parent can relay permissions; echo other records only in debug mode. Inbound data may likewise be debug-echoed. A bridge-only configurable data-frame keepalive defaults through poll configuration and may be disabled with zero. It is filtered from semantic clients. Close clears the timer, closes transport, and ends input idempotently.

## WebSocket transport

**HTR-010 — Socket state machine.** States are `idle`, `connected`, `reconnecting`, `closing`, and `closed`. Connect is legal only from idle/reconnecting. Wire runtime-specific socket listeners, proxy and mutual-TLS options, then on open reset reconnect accounting, refresh activity, replay unacknowledged buffered messages, start ping/data keepalive timers, and notify connection. Detach listeners before closing an old socket.

**HTR-011 — Permanent and recoverable closes.** Protocol error 1002, missing/expired session 4001, and unauthorized 4003 are permanent. The sole 4003 exception is a reconnect header callback that returns a different authorization value; then refresh and retry. When automatic reconnect is disabled, any close becomes closed and the owning bridge loop decides recovery.

**HTR-012 — Reconnect budget.** Recoverable closes use exponential delay from 1,000 ms capped at 30,000 ms with ±25% jitter and an elapsed ceiling of 600,000 ms. A gap over 60,000 ms between attempts indicates sleep and resets attempt/time budget. One reconnect timer may exist. Budget exhaustion makes closed and calls the close observer exactly once for the terminal path.

**HTR-013 — Liveness.** Send protocol ping every 10,000 ms and require the prior pong. Also send a JSON keepalive data frame every 300,000 ms so intermediaries that ignore control frames observe activity. A timer gap over 60,000 ms forces reconnect without waiting for ping. User/session activity can request the same data keepalive. Timers and activity callback are removed on every disconnect.

**HTR-014 — UUID replay buffer.** Retain at most 1,000 UUID-bearing outbound messages and the most recent sent UUID. On reconnect send that UUID as the last-request header. When the server reports a confirmed UUID, evict it and every earlier buffered item, replay later items in order, and retain them until a later confirmation. Non-UUID records written while disconnected are not replayable and are an explicit loss class.

## Hybrid transport and serial uploader

**HTR-020 — Hybrid ordered writes.** Read through WebSocket and write through one serialized HTTP uploader. Hold stream deltas for at most 100 ms. Any nonstream record first takes and enqueues all held deltas, then itself, preserving order. A nonstream `write` and `writeBatch` await queue flush; a stream write returns after buffer admission. POST batches contain at most 500 records and time out each attempt at 15,000 ms.

**HTR-021 — Hybrid HTTP classification.** Derive the POST endpoint by converting scheme, replacing `/ws/` with `/session/`, and appending `/events` while retaining query. No token and non-429 4xx are terminal drops for that batch. Network errors, 429, and 5xx throw into retry. Successful 2xx completes the batch. A caller-configured consecutive-failure ceiling may drop one batch and increments an observable monotonic dropped-batch counter; absence of a ceiling retries indefinitely.

**HTR-022 — Serial uploader.** At most one send runs. Drain FIFO in count/optional byte-bounded batches. The first item may exceed the byte cap; later items stop before it. An unserializable item is removed so it cannot poison the head forever. Failed batch is prepended before later arrivals and retried exponentially with jitter; a valid server retry-after overrides the exponential value after clamp. `enqueue` blocks while its whole addition would exceed queue capacity and releases when space opens.

**HTR-023 — Flush and close.** Flush resolves when the live queue is empty, including after configured batch drops; callers that require no loss compare the drop counter. Close records pending count, drops pending items, interrupts retry sleep, and resolves blocked enqueue/flush waiters. Hybrid close discards its unadmitted stream buffer immediately, gives the admitted uploader a best-effort 3,000 ms race to drain, then closes it; nothing awaits that grace, so it is not delivery acknowledgement.

## SSE transport

**HTR-030 — Incremental SSE framing.** Decode byte chunks incrementally and retain trailing partial text. The compatibility parser splits only at LF/LF, does not normalize CRLF, recognizes `event`, `data`, and `id`, ignores `retry`, and discards an unterminated final frame. Advance numeric frame-ID high-water before event or JSON validation. A bounded seen set diagnoses duplicate IDs but does not suppress duplicate or older-frame dispatch. Exact parsing, including empty-data and leading-decimal behavior, is `RB-CCR-WIRE-040..053`.

**HTR-031 — SSE connection state.** Connect with last observed sequence as resume input and current dynamic auth headers. Treat 401, 403, and 404 as permanent HTTP failures. Reconnect starts at 1,000 ms, caps at 30,000 ms, and gives up after 600,000 ms; a 45,000 ms liveness timer reconnects an open stream that stops producing frames. Close aborts read and retry timers and prevents future reconnect.

**HTR-032 — SSE event dispatch.** Only `client_event` is recognized. After outer JSON parse, an object payload containing a `type` member reaches the structured-data observer; the typed observer then receives the parsed outer event even if payload admission failed. Malformed JSON is isolated after frame-ID observation, and unknown event names are ignored. The adapter itself does not runtime-validate every declared client-event field; strict validation is an explicit hardening boundary in `RB-CCR-WIRE-041`.

**HTR-033 — Compatibility SSE POST writer.** Derive the write endpoint by removing terminal `/stream` and post one structured record under auth captured at logical-write start. Accept only 200/201; stop on non-429 4xx; retry network error, 429, 3xx, and 5xx at most ten attempts with 500 ms exponential delay capped at 8,000 ms and no jitter, Retry-After handling, or explicit timeout. It resolves after exhaustion. CCR v2 worker events never use this writer; they use `/worker/events` under `RB-CCR-WIRE-061`.

## CCR worker client

**HTR-040 — Epoch registration.** Require nonempty auth and a numeric epoch from explicit input or worker environment. Concurrently read prior external metadata and PUT worker status idle with the epoch while clearing stale pending action/task summary. Only after successful PUT start the nominal 20,000 ms heartbeat and activity keepalive. `/bridge` and compatibility `/worker/register` are mutually exclusive registration paths. Exact tuple, scalar, body, and fencing rules are `RB-CCR-WIRE-004,010..022,060`.

**HTR-041 — Worker state coalescing.** Maintain one in-flight worker-state PUT and at most one pending patch. Top-level fields are last-write-wins. `external_metadata` and `internal_metadata` merge one level with null preserved for server deletion. Retry every nonfencing failure indefinitely with exponential delay until success or close, absorbing newly pending patches before each retry. Close drops the pending patch; this path has no flush acknowledgement. Exact body and projected action fields are `RB-CCR-WIRE-020..022`.

**HTR-042 — Visible event uploader.** Visible client events receive a UUID when missing. Stream events wait up to 100 ms and text deltas for the same message/content scope become full-so-far ephemeral snapshots. A nonstream event flushes stream snapshots first; a final assistant clears its accumulator. POST through the exact `/worker/events` envelope and 100-record/10-MiB/100,000-queue bounds in `RB-CCR-WIRE-030`. Explicit visible flush drains this queue; ordinary per-turn internal flush does not.

**HTR-043 — Internal and delivery uploaders.** Internal transcript/compaction events and delivery updates use the separate exact records in `RB-CCR-WIRE-031..033`. Both carry active epoch. Foreground and subagent reads page independently until opaque cursor exhaustion; any failed page discards the accumulated read. Delivery batch and queue are each 64. No delivery status proves transcript or client durability.

**HTR-044 — Close loss classes.** Close stops heartbeat and activity callbacks; discards held stream deltas and accumulators; and closes state, visible, internal, and delivery uploaders. Queue depths and drop counters may be observed for diagnostics, but close does not drain them. Graceful owners must invoke the specific visible/internal flush they require before close and still avoid claiming downstream durable client receipt.

**HTR-045 — Activity keepalive projection.** After successful CCR initialization, register the process-global session-activity callback from `SC-041..044`. While API calls or tool execution keep its shared refcount positive, its separate 30,000 ms timer asks this client to enqueue the visible event `{type: "keep_alive"}` through the ordinary `/worker/events` path in `RB-CCR-WIRE-030`; actual callback invocation is gated by `AGENTX_REMOTE_SEND_KEEPALIVES`. This activity event is distinct from the client's unconditional nominal 20,000 ms `/worker/heartbeat` state operation in `RB-CCR-WIRE-022`: their triggers, interval, route, payload, retry/queue behavior, and diagnostics must not be merged. Manual activity signalling can request the same visible event immediately under the gate. Client close unregisters the global callback before/while uploader teardown, so retained activity refcounts cannot continue targeting the closed client; queue admission still is not downstream durability.

## NDJSON and SDK event protection

**HTR-050 — Physical-line safety.** JSON serialization escapes U+2028 and U+2029 so each record remains one physical line. Install the stdout guard once before stream-json output. Buffer arbitrary writes to newline, parse each complete line, forward valid JSON/blank lines to original stdout, and divert invalid lines to stderr with the guard marker. On cleanup, validate and forward or divert a trailing partial line, restore the original writer, and clear state.

**HTR-051 — Guard callback semantics.** A diverted write is treated as handled and its callback runs asynchronously once buffering is complete. Return the original writer's backpressure value for forwarded lines. Do not write a diverted line back to stdout from diagnostics.

**HTR-052 — Lifecycle event queue.** The process-local queue accepts events only in noninteractive mode, caps at 1,000 by dropping the oldest, drains atomically, and adds fresh UUID/current session ID at drain. A direct task-terminal event closes a previously projected task start only on paths that did not already create the equivalent parsed model-facing notification.

## Acceptance scenarios

### `HTR-A01` — Early CCR catch-up

Deliver an SSE frame synchronously when connect begins. Verify the CCR delivery observer was already installed and `received` is queued before semantic dispatch.

### `HTR-A02` — Sleep resets reconnect

Exhaust several recoverable attempts, advance the clock by more than 60 seconds, and verify attempts/time budget restart. Then return 4001 and verify immediate terminal close.

### `HTR-A03` — Failed batch preserves order

Fail the first Hybrid batch, enqueue a later result, then recover. Verify the failed batch remains ahead of the result, only one POST is in flight, and flush waits for both.

### `HTR-A04` — Observed malformed SSE frame

Send a new numeric SSE ID with invalid JSON, reconnect from its sequence, and verify the cursor advanced even though no semantic event or processed acknowledgement exists.

### `HTR-A05` — Distinct CCR drains

Queue one internal transcript event and one visible result. Flush internal only and terminate. Verify the transcript may persist while the visible result remains an explicit loss; then test visible flush separately.

### `HTR-A06` — Stdout contamination

Split a valid JSON line and a stray banner across writes. Verify the JSON reaches stdout exactly once, the banner reaches marked stderr, callbacks fire, and a trailing fragment is accounted for during cleanup.

### `HTR-A07` — Queue close under backpressure

Fill a bounded uploader, block a producer, then close during retry sleep. Verify pending-count snapshot, producer and flush settlement, no more sends, and explicit dropped accounting.

### `HTR-A08` — Duplicate SSE sequence

Send the same valid frame ID twice. Verify the SSE adapter diagnoses the duplicate but dispatches both records, leaving semantic UUID deduplication to the bridge/session layer.

### `HTR-A09` — Two independent keepalive clocks

Initialize CCR, hold one API activity open, and advance a fake clock. Verify nominal 20-second worker heartbeats use `/worker/heartbeat` regardless of the activity environment gate, while 30-second activity ticks always diagnose locally and enqueue `{type:"keep_alive"}` to `/worker/events` only when the gate is truthy. Disable the gate, invoke a manual signal, then close with a positive activity refcount; verify no manual event is emitted and later activity ticks cannot target the closed client.

## Non-normative provenance

Evidence came from the remote structured-I/O adapter, transport selector, WebSocket/Hybrid/SSE implementations, serial and coalescing uploaders, CCR worker client, NDJSON serializer/guard, and SDK lifecycle queue. Private implementation classes and runtime libraries are non-normative.
