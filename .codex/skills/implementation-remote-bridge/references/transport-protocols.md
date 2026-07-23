# Bridge transport protocols

This reference specifies ordered projection between a local semantic session and remote services. It deliberately separates transport receipt, delivery-status claims, model-visible processing, transcript durability, and user-visible rendering.

Read [the CCR worker wire catalog](ccr-wire-catalog.md) for exact CCR URLs, methods, headers, bodies, SSE parsing, status classes, timeouts, retries, and queue bounds. This file owns cross-transport ordering, identity, and recovery and does not redefine those wire records.

## Contents

- [Contract map](#contract-map)
- [Common event model](#common-event-model)
- [Initial history and flush gate](#initial-history-and-flush-gate)
- [Deduplication and replay](#deduplication-and-replay)
- [Hybrid WebSocket and HTTP transport](#hybrid-websocket-and-http-transport)
- [CCR SSE and worker transport](#ccr-sse-and-worker-transport)
- [Inbound event normalization](#inbound-event-normalization)
- [Control request protocol](#control-request-protocol)
- [Permission relay](#permission-relay)
- [Credential and epoch recovery](#credential-and-epoch-recovery)
- [Explicit memory and crash-loss windows](#explicit-memory-and-crash-loss-windows)
- [Transport state machines](#transport-state-machines)
- [Failure and forward-compatibility rules](#failure-and-forward-compatibility-rules)

## Contract map

| ID | Requirement |
| --- | --- |
| RB-TRN-001 | A transport is an event adapter around the shared session; it does not mutate message semantics. |
| RB-FLG-001 | Initial history and concurrent live writes are serialized by an explicit FIFO flush gate. |
| RB-FLG-002 | Transport replacement retains queued writes; final teardown may drop only with explicit accounting. |
| RB-EVT-001 | Only eligible semantic messages cross the bridge; internal progress and tool-result plumbing stay local unless deliberately projected. |
| RB-DED-001 | Separate bounded rings suppress outbound echoes and inbound redelivery; sequence cursors remain the transport's replay position but do not prove semantic processing or durability. |
| RB-DEL-001 | `received`, `processing`, and `processed` are distinct delivery states even when an adapter deliberately collapses them. |
| RB-DEL-002 | Ordinary remote I/O observes and dispatches an event, reports `received`, performs command/local processing, then reports `processed`; these reports are transport delivery states, not transcript durability. |
| RB-DEL-003 | The subprocess bridge adapter reports `received` and `processed` immediately after its local callback returns because it cannot observe child processing. A crash after that report may lose the event without server replay. |
| RB-HYB-001 | Legacy Hybrid reads by WebSocket and writes through a serial, ordered HTTP uploader. |
| RB-CCR-001 | CCR reads server events through SSE and writes worker operations through the CCR HTTP client. Operations that mutate or report worker state carry the active worker epoch according to `RB-CCR-WIRE-*`; the SSE GET and prior-state GET are authenticated reads without an epoch body. |
| RB-SEQ-001 | CCR transport replacement resumes from the last observed sequence; legacy transport relies on its server cursor/request ID. |
| RB-SEQ-002 | CCR SSE advances its in-memory sequence high-water immediately after parsing a frame ID, before JSON parsing, semantic validation, dispatch, persistence, or delivery-state reporting. The cursor therefore proves observation, not successful processing. |
| RB-SEQ-003 | Cursor carryover exists only within the live process. The documented runtime defines no cross-process cursor record or commit protocol; a fresh remote session starts at zero and an environment-backed pointer carries no cursor. |
| RB-CTL-001 | Every accepted inbound server control is mapped to one correlated success/error enqueue or explicit transport failure. Outbound interactive permission control remains pending until a decisive response, remote cancellation, owning abort/disconnect, or process death; the specified bridge layer imposes no local prompt deadline. |
| RB-CTL-002 | Exact pending control correlation is process-local and keyed by request ID. The documented runtime does not bind a pending request to a durable local transcript, remote-session generation, worker epoch, or restart record. |
| RB-PERM-001 | Remote permission requests enter the local permission controller and return a structured allow or deny; transport credentials do not imply authorization. |
| RB-PERM-002 | When no exact pending request exists, the compatibility orphan-permission path may accept a successful response whose tool-use ID matches an unresolved transcript tool use; it does not validate remote session, epoch, or surface generation. |
| RB-REC-001 | Credential recovery uses one same-process critical section to gate writes, retain the in-memory cursor and pending order, replace epoch/transport, and drain after reconnect. It is not a durable transaction. |
| RB-REC-002 | Process death discards the recovery promise, queues, cursor, deduplication rings, and pending controls. Restart must use only explicit persistent session/pointer evidence and must not claim that the interrupted replacement resumed. |
| RB-CRS-001 | WebSocket buffers, Hybrid/CCR upload queues, flush-gate contents, cursor state, deduplication rings, and pending controls are memory-resident loss windows. An orderly same-process close, teardown, or credential-recovery path reports the exact count/class still observable before releasing it. Abrupt process death erases that evidence; restart can report only the documented unknown-loss window and any independent persistent evidence, never an invented count or callback. |
| RB-FWD-001 | Unknown event kinds with valid outer framing are ignored or passed through according to the surface; malformed events never crash the session loop. |

## Common event model

### Semantic layers

Do not collapse these layers:

| Layer | Meaning | Durable/replay role |
| --- | --- | --- |
| Local transcript message | Authoritative local conversation event | Specified by session persistence, not by bridge delivery status |
| Bridge projection event | Selected representation sent to remote service | May be retried/deduplicated; not automatically model-visible locally |
| Transport envelope | Protocol-specific upload or inbound frame | Ordered by an in-memory uploader/observed cursor and fenced by epoch |
| Delivery status | Remote acknowledgement state | Tracks adapter-reported receipt/processing, not query or transcript durability |
| Control request/response | Correlated request for local mutation or permission | Must terminally resolve independent of chat messages |
| Terminal session result | Summary that the logical session ended | Sent before archive when possible; does not replace transcript |

### Eligible outbound messages

Forward only:

- non-virtual user messages;
- non-virtual assistant messages;
- system messages explicitly classified as `local_command`.

Exclude by default:

- virtual or synthetic projection-only messages;
- internal progress records;
- raw tool-result plumbing;
- UI-only state and ephemeral status;
- secrets or credential metadata.

If a new message category is added, choose deliberately whether it is part of the remote semantic history, a remote status event, or local-only. Never inherit eligibility from a broad “serializable” predicate.

### Common correlation fields

Every envelope capable of correlation should carry the applicable subset of:

```text
session_id
worker_epoch
event_id or sequence_num
message_uuid
request_id
tool_use_id
type
subtype
payload
```

Treat these as opaque except for explicitly documented compatibility normalization. A missing required correlation identifier makes the event malformed.

## Initial history and flush gate

### Flush gate states

```text
inactive
  --start--> active
active
  --enqueue(event)--> active with FIFO pending event
active
  --end--> inactive, return all pending events in FIFO order
active
  --deactivate--> inactive, retain pending events for replacement transport
active|inactive
  --drop--> inactive, discard pending and return discarded count
```

`enqueue` queues only while active. While inactive, the caller sends directly through the active transport. `deactivate` is for recoverable transport replacement. `drop` is for final teardown or a declared unrecoverable path, and its count must be observable in diagnostics.

### Initial projection sequence

1. Set the gate active before inspecting/sending the history snapshot.
2. Freeze an ordered snapshot of eligible history.
3. Apply the configured history cap: if the cap is positive, retain the last `N` eligible events; a nonpositive or absent value follows the surface's explicit full/none policy.
4. Send snapshot events in original order through the serial writer.
5. Mark each successfully accepted outbound UUID in the echo cache.
6. End the gate and obtain queued live writes.
7. Send queued live writes in FIFO order.
8. Mark initial flush complete only after the uploader has accepted all required events, not merely after they were enumerated.

An environment-less session is freshly created, so reconnect/re-registration never suppresses history solely because a prior persistent-session “already flushed” marker exists. Legacy environment-backed reconnect does retain its persistent flushed evidence to avoid posting history twice. This difference is architecture-specific, not an accidental cache difference.

If initial upload silently loses an event, an uploader monotonic success/failure counter must make the flush fail rather than setting the completed latch.

## Deduplication and replay

Use three concepts:

| Mechanism | Contents | Purpose | Bound |
| --- | --- | --- | --- |
| Recent outbound UUID ring | UUIDs recently posted locally | Drop server echoes of local messages | Environment-less default 2,000; viewer projection may use 50 |
| Recent inbound UUID ring | UUIDs recently admitted from remote | Drop reconnect/SSE redelivery | Environment-less default 2,000 |
| Initial UUID set | UUIDs in the initial snapshot | Prevent initial-history echo/reprojection errors | May remain unbounded for that bridge lifetime |

A bounded UUID ring is implemented as FIFO insertion order plus membership lookup:

1. If UUID exists, classify duplicate and do not reinsert.
2. Otherwise add to set and FIFO.
3. If size exceeds capacity, evict oldest from both.

UUID absence never proves an event is new after eviction. The transport cursor provides only the protocol's defined replay position:

- CCR records the highest **observed frame ID** and recreates SSE with `from_sequence_num` or equivalent `Last-Event-ID` semantics. It updates this in-memory high-water as soon as the frame ID parses, before payload JSON parsing, type validation, dispatch, transcript persistence, `received`, or `processed`.
- A malformed frame whose ID parses can therefore advance the cursor even though its payload is ignored. Same-process reconnect resumes after that observed ID; the cursor must never be described as a processed or durable checkpoint.
- Legacy Hybrid reports logical sequence zero to callers because its WebSocket subscription maintains the server-side cursor/request ID, including `X-Last-Request-Id` behavior.
- Delivery-state reports do not advance or persist the CCR cursor. They are a separate service protocol.
- The documented client has no cross-process cursor serialization. Same-process transport replacement carries the value in memory; a fresh logical session starts at zero, while environment-backed restart follows server/pointer behavior without restoring a client cursor.

### Delivery states

Ordinary remote I/O uses this order:

```text
observe frame and update observed cursor when applicable
  -> dispatch local event callback
  -> report received
  -> perform control/command/local processing
  -> report processed
```

CCR delivery reporting therefore distinguishes:

```text
received -> processing -> processed
```

The subprocess bridge adapter cannot observe the child's internal processing boundary. After its local callback returns, it intentionally reports `received` and then `processed` immediately, without evidence that a child durably persisted or completed the event. If the parent or child crashes after `processed`, the server may suppress replay even though local handling was lost. Document this adapter-specific degradation; implementations with an observable child lifecycle should report all states accurately.

Neither path establishes what the remote service durably commits. A successful delivery-status request or HTTP 2xx is evidence only of the published service operation unless the service contract separately guarantees durable storage and replay.

## Hybrid WebSocket and HTTP transport

Hybrid transport uses WebSocket only for server-to-worker reads and HTTP POST only for worker-to-server writes.

### Writer contract

| Constant | Value |
| --- | ---: |
| Stream batching window | 100 ms |
| Maximum upload batch | 500 events |
| Maximum queued events | 100,000 |
| Retry base delay | 500 ms |
| Retry maximum delay | 8,000 ms |
| Additional jitter | Up to 1,000 ms |
| Per-attempt POST timeout | 15 seconds |
| Close/drain grace | 3 seconds |

Writer rules:

1. Maintain exactly one in-flight POST; later batches queue.
2. Buffer adjacent streaming events for up to 100 ms.
3. Before accepting a non-stream event, flush preceding streaming events so semantic order is retained.
4. Split queues into batches of at most 500.
5. Bound the total queue at 100,000; exceeding it is an explicit backpressure/failure outcome, not silent truncation.
6. Retry network errors, HTTP 429, and 5xx using exponential backoff capped at 8 seconds plus jitter.
7. Treat other non-429 4xx responses as permanent for that batch and advance with an explicit failure record.
8. When a configured consecutive-failure ceiling is reached, dropping a batch must increment a monotonic loss indicator so callers such as initial flush fail visibly.
9. On close, drain for up to three seconds, then report remaining loss and release resources.

### Reader contract

| Constant | Value |
| --- | ---: |
| Inbound frame buffer | 1,000 events |
| Initial reconnect delay | 1 second |
| Maximum reconnect delay | 30 seconds |
| Total reconnect budget | 10 minutes |
| Ping interval | 10 seconds |
| Keepalive interval | 5 minutes |
| Sleep-gap threshold | 60 seconds |

Reader rules:

- Reconnect with bounded exponential delay through the ten-minute budget.
- A host sleep/time gap over 60 seconds resets the reconnect budget because elapsed wall time did not represent active retry.
- Close codes 1002, 4001, and 4003 are permanent for the current connection/authentication condition.
- Keep read buffering bounded; overflow must force an explicit reconnect/replay path rather than silently discarding arbitrary frames.
- WebSocket reconnect and the bridge's sparse at-capacity poll are independent; neither replaces the other.

## CCR SSE and worker transport

CCR transport uses:

- SSE for server-to-worker events;
- a CCR worker HTTP client for worker-to-server events, metadata, heartbeat, delivery state, and session result;
- a worker JWT scoped to the current worker epoch.

Do not write worker events back through the SSE connection. Do not use a stale transport object's writer after credential implementation.

### Worker operation requirements

Every worker-side operation uses the applicable subset defined by the exact catalog:

```text
logical session identity
worker credential
active worker epoch for state, event, heartbeat, and delivery writes
operation-specific identity only where the record defines one
typed payload
```

The worker reports state and metadata separately from semantic messages. Heartbeat proves worker liveness; event delivery statuses prove handling; neither proves the model completed the session.

The default environment-less heartbeat is 20 seconds with 0.1 fractional jitter against a 60-second server worker TTL. The compatibility environment-backed version-2 keepalive default is 120 seconds where its service contract differs.

### Epoch conflict

An HTTP 409 epoch mismatch means another worker registration fenced this transport:

1. Stop all writes on the stale object.
2. Close/fence the reader.
3. In a implementable REPL, enter serialized credential/transport recovery and use a dedicated local close reason such as 4090 for observability.
4. In a child/subprocess that cannot safely transfer ownership, exit and let the parent reconcile/restart.
5. Never treat 409 as an ordinary retry of the same operation with the same epoch.

### Terminal result envelope

When adapting a local completion to a CCR result and no richer usage exists, synthesize the required result fields deterministically:

- unique result UUID;
- success or error classification from the actual terminal state;
- text/content payload as available;
- zero duration, API duration, turn count, and cost only when the adapter truly lacks those measurements;
- empty usage and permission-denial collections rather than fabricated values.

Mark synthetic/unknown measurements as adapter defaults in diagnostics. Never overwrite real measurements with zeros.

## Inbound event normalization

### Framing

1. Parse outer JSON defensively.
2. Require a string `type`; accept unknown string types for forward compatibility.
3. Normalize documented legacy/canonical control keys before narrowing.
4. Validate required fields only for the recognized type/subtype.
5. Log malformed input without secrets and ignore it; do not crash the query loop.
6. Process recognized control events before ordinary message admission.
7. After control handling, admit only user messages to the local prompt path. Ignore remote assistant/system/tool messages unless a specific projection contract allows them.

### Image content

Normalize supported image blocks into the local content schema:

- accept canonical and compatibility camel-case media-type spelling;
- infer image format only from validated content/metadata where permitted;
- reject unsupported or malformed binary/media combinations;
- preserve the remote message UUID for deduplication.

### Inbound prompt admission

Inbound user prompts pass through the same local input normalization, trust, queueing, cancellation, and transcript rules as locally entered prompts. A transport handler never calls the model directly. If the local session is busy, queue or reject according to the interactive session contract; do not create a concurrent query loop accidentally.

## Control request protocol

A control request contains at minimum a `request_id`, control subtype, and subtype payload. The specified inbound server-control handler maps the supported controls below inline and enqueues one correlated response; it does not establish delivery acknowledgement. The opposite-direction `can_use_tool` permission request may remain pending without a local bridge timeout until a responder, remote cancellation, owning abort/disconnect, or process death settles it. If a service or hardened adapter imposes a deadline, treat that as an external/versioned policy and define its correlated error/cancel behavior rather than attributing it to the reference bridge.

### Supported controls

| Subtype | Local action | Success payload | Error behavior |
| --- | --- | --- | --- |
| `initialize` | Report remote-controllable surface capabilities | Capabilities with empty commands/models, output style `normal`, available styles `[normal]`, empty account, process ID | Return error only if local state cannot be queried safely |
| `set_model` | Invoke the registered model-change callback | Correlated success | Missing callback, invalid model, policy denial, or callback exception becomes correlated error |
| `set_max_thinking_tokens` | Invoke thinking-budget callback | Correlated success | Missing/invalid/denied becomes error |
| `interrupt` | Request shared query cancellation | Correlated success once request is accepted | Missing callback or impossible state becomes error |
| `set_permission_mode` | Evaluate policy, then invoke permission-mode callback | Correlated success with effective mode | Managed denial, unsupported mode, missing callback, or exception becomes error |
| Unknown subtype | No mutation | None | Correlated unsupported-control error |

The response outer envelope is a `control_response` correlated by request ID, with either a typed success value or an error description safe for the remote user.

### Outbound-only behavior

An outbound-only mirror may answer `initialize` with its nonmutating capability description. Every mutable subtype returns a correlated error equivalent to:

```text
This session is outbound-only. Enable Remote Control locally to allow inbound control.
```

Never silently acknowledge a mutation that was not applied.

### Control cancellation

A cancellation event identifies the pending control request. It:

1. removes/dismisses any local approval UI or waiter;
2. marks the correlation terminal as cancelled;
3. prevents a late callback result from emitting a second terminal response;
4. returns worker activity to `running` or `idle` as appropriate.

## Permission relay

### Request envelope

The exact request, permission-update, success, deny, error, and cancellation schemas are defined once in [the SDK permission wire catalog](../../implementation-headless-sdk/references/sdk-permission-wire.md). The field summary below is routing guidance only.

A remote `can_use_tool` control request carries:

```text
request_id
tool_name
input
tool_use_id
description
permission_suggestions
blocked_path or equivalent protected-resource context, when present
```

Flow:

1. Validate correlation and tool identifiers.
2. Report worker state `requires_action` before displaying/relaying the request.
3. Invoke the same local permission composition used for a local tool call: managed rules, mode, scoped rules, hook/tool checks, path/command analysis, sandbox, and interactive choice.
4. On decision or cancellation, report worker state `running`.
5. Emit exactly one correlated response.

### Response envelope

The outer response is a successful control transport response when the permission interaction itself completed. Its inner result is:

```text
allow {
  behavior: "allow"
  updatedInput: object
  updatedPermissions?: permission update[]
}

deny {
  behavior: "deny"
  message: string
}
```

Use an outer error when the permission controller failed, the request was malformed, or local policy infrastructure was unavailable—not merely because the user denied the tool.

The winning allow follows the specified one-shot edited-approval contract: `updatedInput` is selected for this tool-use ID and reaches remote execution without a second schema, semantic, tool-permission, rule, safety, classifier, sandbox, or prompt pass. The original request remains correlation/audit evidence. This compatibility gap is distinct from permission updates, which are applied only to their declared scope/destination; a remote controller cannot directly author an allow rule outside the local permission service. A hardened revalidation/reauthorization pass is an intentional divergence and must define bounded reprompt or terminal denial behavior.

During credential/epoch recovery, the compatibility adapter deliberately drops control request, control response, cancellation, and terminal-result traffic rather than risk sending under two epochs. This is the explicit `RB-LSE-001` lossy window. Surface it in diagnostics and return to replay/reconciliation where the service protocol permits; never claim those controls succeeded.

### Correlation lifetime and stale generations

The ordinary structured-control path resolves an exact process-local pending entry by `request_id`. A bridge-injected response likewise resolves only the exact live pending request. Restart or surface replacement loses those maps; the documented runtime has no durable pending-control journal.

The compatibility orphan path is narrower but weaker: when a successful response has no exact pending entry and its permission-result payload includes camel-case `toolUseID`, it may look for an unresolved tool use in the local transcript and apply the response there. The request field remains snake-case `tool_use_id`; these spellings are not interchangeable. The orphan path does not prove that the response belongs to the current remote logical session, worker epoch, connection, or surface generation. A late response from an older connection can therefore match a still-unresolved tool use.

**Safer divergence, not reference behavior:** assign every control-capable surface a non-reused generation, bind pending entries to `{request_id, remote_session_id, generation, worker_epoch?}`, and reject orphan responses outside that tuple. If adopting this fence, record it as an intentional hardening change and define migration behavior for unresolved legacy transcript entries; do not claim the generation field exists in the reference wire or persistence format.

## Credential and epoch recovery

### Recovery triggers

- proactive worker-JWT refresh before expiry;
- worker operation or SSE authorization failure (401);
- worker epoch conflict (409);
- explicit service instruction to reconnect;
- transport irrecoverable failure within a still-live logical session.

### Serialized same-process recovery algorithm

```text
if recovery already in flight:
    await that recovery
else:
    establish one recovery promise
    deactivate flush gate, retaining FIFO pending writes
    mark transport unavailable for direct sends
    capture inbound sequence high-water
    detach and close old transport
    refresh base OAuth/trusted-device credential if needed
    register worker once, obtaining JWT + new epoch
    construct replacement with same session + captured sequence
    attach handlers only if teardown has not begun
    connect reader and writer
    reset/replay initial history according to architecture
    end gate and drain pending writes FIFO
    clear recovery promise
```

On failure, keep the gate deactivated while a bounded retry policy applies. On permanent same-process failure, terminally close and explicitly account for waiters/messages still present, except traffic already discarded by the named credential-recovery compatibility window. After process death no such map survives, so restart can classify but cannot synchronously settle the former callers. Never let callers race to create two replacement transports because each `/bridge` registration advances the epoch.

This serialization is an in-memory mutual-exclusion boundary, not an atomic persistence boundary. A crash can occur after the old transport closes, after the cursor is observed, after registration advances the epoch, after the replacement connects, or while the queue drains. No transaction commits cursor, epoch, queue, pending controls, and session association together. On process restart, discard the interrupted recovery state and follow `RB-REC-002`.

## Explicit memory and crash-loss windows

| Window | Reference behavior | Required accounting |
| --- | --- | --- |
| Hybrid outbound batches | Pending, streaming, retry, and in-flight batch state is memory-only; a three-second close drain may expire with events remaining | Report failed/dropped batch or monotonic loss evidence; never claim those events survived restart |
| WebSocket/Hybrid inbound buffer | Buffered frames and reconnect bookkeeping are memory-only | Reconnect through the server cursor when possible; process death has no client buffer recovery |
| CCR outbound/flush queues | Serial writes, gate contents, and retry state are memory-only | Retain across same-process replacement; explicitly fail/drop on permanent close; restart cannot recover them |
| SSE observed cursor and UUID rings | Cursor and deduplication evidence are memory-only and can get ahead of semantic processing | Same-process reconnect uses observed high-water; cross-process implementation must not invent it |
| Immediate `processed` bridge report | The adapter reports completion before child/transcript durability is observable | Accept and document the crash-loss interval; server replay may no longer occur |
| Credential recovery control traffic | Compatibility behavior drops control request, response, cancellation, and terminal-result traffic while epochs are changing | Diagnose the correlation/loss class and never emit success |
| Pending control/permission map | Exact waiters live only in process memory; the orphan fallback is not generation-fenced | Cancel/fail known waiters on close; treat late/orphan acceptance as the documented compatibility risk |
| Fire-and-forget teardown | Some close, archive, or upload calls can still be outstanding when process ownership ends | Preserve local terminal evidence when available and report that remote durability is unknown |
| Perpetual initial UUID evidence | Initial UUIDs may be treated as already flushed for deduplication without end-to-end server confirmation | Treat as replay suppression evidence only, never as delivery durability |

Closing an adapter releases memory even when a lower layer cannot confirm upload or remote durability. A standalone implementation may introduce a durable outbox, cursor journal, or two-phase delivery record, but that is a safer extension requiring its own compatibility rules rather than an unstated interpretation of the documented client.

## Transport state machines

### Connectivity

```text
new
  -> connecting
  -> connected
  -> reconnect_wait
  -> connecting
  -> draining
  -> closed

connecting --permanent auth/protocol error--> failed
connected --credential refresh--> replacing
replacing -> connecting (new epoch/cursor)
any nonterminal --teardown--> draining
```

Only `connected` admits direct sends. `connecting`, `reconnect_wait`, and `replacing` route eligible live sends through the flush/backpressure gate when recovery is allowed.

### Control correlation

```text
received
  -> validating
  -> awaiting_local_action
  -> responding
  -> succeeded | denied | errored | cancelled

received/validating --malformed--> errored
awaiting_local_action --remote cancel--> cancelled
awaiting_local_action --owning abort/disconnect--> cancelled | errored
```

Terminal states are immutable. Late UI/callback results after cancellation or abort are discarded. The reference bridge supplies no local permission deadline; a versioned host deadline may enter as a cancellation/error event.

## Failure and forward-compatibility rules

| Failure | Required outcome |
| --- | --- |
| Invalid JSON or missing string type | Ignore/log safely; no query-loop crash |
| Unknown valid event type | Ignore or pass to a generic observer; preserve connection |
| Unsupported control subtype | Correlated error, not disconnect |
| Queue capacity exceeded | Apply backpressure or explicit failure; no silent arbitrary drop |
| Permanent non-rate-limit 4xx upload | Hybrid and compatibility SSE writers stop that write; CCR worker uploaders retry all nonfencing failures, including such 4xx, under `RB-CCR-WIRE-060` |
| 429, 5xx, timeout, network reset | Hybrid and compatibility writers use their bounded retry profiles; CCR worker uploaders preserve order and retry every nonfencing failure without an attempt ceiling until success, close, or fencing |
| Reader buffer overflow | Close/reconnect from cursor; diagnose overflow |
| Duplicate UUID | Suppress duplicate semantic admission at the bridge/session layer; the CCR SSE seen-sequence set diagnoses but does not suppress duplicate frame dispatch |
| Sequence gap | Request/reconnect from the known in-memory observed cursor or fail visibly; do not invent a processed checkpoint |
| 401 | Serialized credential recovery |
| 409 worker epoch conflict | Fence old writer and implement/exit; never same-epoch retry |
| Local permission dialog unavailable | Return correlated error/deny according to policy; never default allow |
| Teardown while callback/recovery runs | Teardown latch wins; late objects/results cannot attach or emit a second terminal event |
| Process death during replacement | Lose in-memory cursor/queues/pending controls and implement only from explicit persistent evidence; never resume an imaginary transaction |
| Orphan successful permission response | Compatibility path may match unresolved `tool_use_id` without session/epoch/generation validation; expose the risk or adopt the explicitly documented safer generation-fence divergence |
