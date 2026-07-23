# CCR worker wire catalog

This reference is the canonical client-visible wire catalog for the CCR v2
worker plane and its OAuth bootstrap calls. It defines only behavior visible at
the client/service boundary. Server storage, server-side deduplication, worker
lease internals, and delivery durability remain opaque unless a response below
states otherwise.

## Contents

1. [Addressing and authentication](#addressing-and-authentication)
2. [OAuth bootstrap operations](#oauth-bootstrap-operations)
3. [Worker operation catalog](#worker-operation-catalog)
4. [Visible and internal event records](#visible-and-internal-event-records)
5. [SSE stream](#sse-stream)
6. [Cursor and delivery semantics](#cursor-and-delivery-semantics)
7. [HTTP status, retry, and epoch rules](#http-status-retry-and-epoch-rules)
8. [Uploader bounds and close behavior](#uploader-bounds-and-close-behavior)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Opaque server boundaries](#opaque-server-boundaries)

## Addressing and authentication

Let the canonical session base be:

```text
{api-base-without-trailing-slash}/v1/code/sessions/{session-id}
```

The worker client accepts only `http` or `https`, removes one trailing slash,
and derives its local session ID from the final path segment without validating
its tag. An SDK URL using `ws` or `wss` is converted to `http` or `https` before
the worker path is selected.

- **RB-CCR-WIRE-001 — Address derivation.** The inbound worker stream is
  `GET {session-base}/worker/events/stream`. Worker writes and reads append the
  paths in the operation catalog to the same session base. Query parameters on
  the input stream URL survive URL construction; worker-operation methods use
  the normalized session base without inherited query parameters.
- **RB-CCR-WIRE-002 — Worker authentication.** Every worker request obtains
  current authentication at request/connect time. A token beginning with
  `sk-ant-sid` produces `Cookie: sessionKey=<token>` and optional
  `X-Organization-Uuid`; every other token produces
  `Authorization: Bearer <token>`. When cookie authentication is selected for
  SSE, remove a stale Authorization header. Empty authentication makes worker
  initialization fail and makes later operations return failure without a
  network request.
- **RB-CCR-WIRE-003 — Common headers.** Worker JSON POST/PUT requests send
  current authentication, `Content-Type: application/json`,
  `agentx-version: 2023-06-01`, and the product `User-Agent`. Worker GETs
  omit Content-Type. SSE sends current authentication, `Accept:
  text/event-stream`, the same version, and User-Agent. An optional
  `x-environment-runner-version` may be present in the initial/reconnect header
  layer.
- **RB-CCR-WIRE-004 — Epoch scalar.** OAuth `/bridge` and compatibility
  registration responses accept either a JSON number or a decimal string only
  when it converts to a finite safe integer. The lower-level worker initializer
  is less strict: an explicit numeric input is rejected only when it is NaN,
  and the environment spelling is parsed as a leading decimal integer without
  a safe-integer or positivity check. Normal callers therefore pass a validated
  bootstrap epoch; a compatible hardened client should reject unsafe or
  negative direct/environment values rather than serialize them ambiguously.
  Worker operations serialize epoch as a JSON number. Epoch is fencing identity,
  not an event cursor.

## OAuth bootstrap operations

These control-plane calls use an OAuth bearer token, JSON content type, and
`agentx-version: 2023-06-01`. Their caller supplies the timeout.

### Create a bridge-backed session

```text
POST {api-base}/v1/code/sessions

request = {
  "title": string,
  "bridge": {},
  "tags"?: string[]
}

accepted response = {
  "session": { "id": string beginning "cse_", ...opaque },
  ...opaque
}
```

`tags` is omitted when absent or empty. Only status 200 or 201 is success. A
network error, status outside 200/201, missing object path, nonstring ID, or
wrong prefix returns no session ID.

- **RB-CCR-WIRE-010 — Session-create oneof.** The empty `bridge` object is a
  required positive runner selector. Omitting it or substituting an empty
  environment identifier is not equivalent.

### Obtain worker credentials and advance epoch

```text
POST {api-base}/v1/code/sessions/{session-id}/bridge

request = {}
optional header = X-Trusted-Device-Token: <token>

response = {
  "worker_jwt": string,
  "api_base_url": string,
  "expires_in": number,
  "worker_epoch": number | decimal-string
}
```

Only status 200 is success. All four fields are required; epoch must convert to
a finite safe integer. The JWT is opaque and is not decoded by this adapter.
Each successful `/bridge` call is itself worker registration and advances the
server epoch; do not make concurrent speculative calls.

- **RB-CCR-WIRE-011 — Credential tuple atomicity.** Use the JWT, API base, expiry
  duration, and epoch only as one response tuple. A malformed member invalidates
  the whole tuple. A replacement tuple fences the old worker even if local
  transport construction later fails.

### Compatibility registration

When no epoch was returned by `/bridge`, the compatibility path sends:

```text
POST {session-base}/worker/register
Authorization: Bearer <access-token>
Content-Type: application/json
agentx-version: 2023-06-01
timeout: 10,000 ms
body: {}

response = { "worker_epoch": number | decimal-string, ...opaque }
```

Only the HTTP client's ordinary 2xx success path is accepted; malformed epoch
throws. Never call this after `/bridge` already supplied an epoch.

- **RB-CCR-WIRE-012 — Single registration path.** Select `/bridge` registration
  or `/worker/register`, never both for the same handshake. Every registration
  can invalidate an already active writer.

## Worker operation catalog

All paths are relative to the session base. A request body includes the active
numeric `worker_epoch` exactly where shown.

| Method and path | Timeout | Request body | Successful response consumed by client |
| --- | ---: | --- | --- |
| `PUT /worker` | 10,000 ms | state/metadata patch plus `worker_epoch` | any 2xx; body ignored |
| `GET /worker` | 30,000 ms per attempt | none | optional `worker.external_metadata` object |
| `POST /worker/heartbeat` | 5,000 ms | `session_id`, `worker_epoch` | any 2xx; body ignored |
| `POST /worker/events` | 10,000 ms | `worker_epoch`, `events` | any 2xx; body ignored |
| `POST /worker/internal-events` | 10,000 ms | `worker_epoch`, `events` | any 2xx; body ignored |
| `GET /worker/internal-events` | 30,000 ms per attempt | query cursor and optional subagent selector | page record below |
| `POST /worker/events/delivery` | 10,000 ms | `worker_epoch`, `updates` | any 2xx; body ignored |

Initialization concurrently starts `GET /worker` and sends:

```text
PUT /worker
{
  "worker_status": "idle",
  "worker_epoch": epoch,
  "external_metadata": {
    "pending_action": null,
    "task_summary": null
  }
}
```

The null metadata values deliberately request server-side deletion. Only after
the PUT succeeds does the client consider initialization complete and start
heartbeats. The concurrent GET result is reported/restored only after that
success; a failed GET yields null metadata.

- **RB-CCR-WIRE-020 — Worker state patch.** Subsequent state reports send
  `worker_status` and `requires_action_details`. The details are either null or
  exactly `{tool_name, action_description, request_id}`; `tool_use_id` and
  original tool input are deliberately absent from the worker-state wire.
  Repeating the same state without details is suppressed locally.
- **RB-CCR-WIRE-021 — Metadata patch.** External metadata uses
  `{worker_epoch, external_metadata: object}`. A single in-flight PUT and one
  pending coalesced patch are allowed. Top-level keys are last-write-wins;
  `external_metadata` and `internal_metadata` merge one object level and retain
  null values for server deletion.
- **RB-CCR-WIRE-022 — Heartbeat.** The body is exactly
  `{session_id, worker_epoch}`. At most one heartbeat is in flight. Default
  cadence is 20,000 ms; a caller may add symmetric fractional jitter. A
  heartbeat proves liveness only.

## Visible and internal event records

### Visible events

```text
POST /worker/events
{
  "worker_epoch": epoch,
  "events": [
    {
      "payload": {
        "uuid": string,
        "type": string,
        ...SDK stdout record
      },
      "ephemeral"?: boolean
    }
  ]
}
```

If the SDK record lacks a string UUID, generate one. Ordinary records omit
`ephemeral`. Buffered stream records use `ephemeral=true`. Text deltas are
coalesced to full-so-far snapshots per `{session_id,parent_tool_use_id}` active
message and content-block index; the first delta UUID for that block in a flush
is retained. A complete assistant message clears that message accumulator.

- **RB-CCR-WIRE-030 — Visible batch.** Batch at most 100 event records and 10
  MiB of serialized event items, with a process-memory queue bound of 100,000.
  A stream event waits at most 100 ms; a nonstream record flushes all prior
  stream snapshots before its own admission.

### Internal events

```text
POST /worker/internal-events
{
  "worker_epoch": epoch,
  "events": [
    {
      "payload": {
        "type": event-type,
        ...event-payload,
        "uuid": string
      },
      "is_compaction"?: true,
      "agent_id"?: string
    }
  ]
}
```

Normalization first seeds `type` from the event-type argument, then overlays
the caller payload so its `type` wins, and finally forces UUID to the caller's
string UUID or a generated UUID. `is_compaction` is emitted only when true and
`agent_id` only when nonempty.

- **RB-CCR-WIRE-031 — Internal batch.** Batch at most 100 records and 10 MiB
  with a queue bound of 200. Internal events are not visible client events;
  they are a separate resume/transcript storage channel.

### Internal-event pages

Foreground read:

```text
GET /worker/internal-events[?cursor=<opaque>]
```

Subagent read:

```text
GET /worker/internal-events?subagents=true[&cursor=<opaque>]
```

Page shape:

```text
{
  "data": [{
    "event_id": string,
    "event_type": string,
    "payload": object,
    "event_metadata"?: object | null,
    "is_compaction": boolean,
    "created_at": string,
    "agent_id"?: string
  }],
  "next_cursor"?: string
}
```

The client treats cursor as opaque and continues only while it is a nonempty
value. It does not runtime-validate individual page fields; missing `data` is
treated as an empty array. Failure of any page discards all pages accumulated
for that read and returns null, never a partial implementation.

- **RB-CCR-WIRE-032 — Page completeness.** Foreground and subagent reads are
  separate complete-result operations. A null result means unavailable or
  incomplete and must not be presented as an authoritative empty transcript.

### Delivery updates

```text
POST /worker/events/delivery
{
  "worker_epoch": epoch,
  "updates": [{
    "event_id": string,
    "status": "received" | "processing" | "processed"
  }]
}
```

Batch and queue bounds are both 64. The worker constructor registers an SSE
typed-event callback immediately and enqueues `received` for every parsed
`client_event`. Later command lifecycle maps `started` to `processing` and
`completed` to `processed`. The subprocess bridge compatibility adapter instead
reports `received` and `processed` together because it cannot observe child
processing.

- **RB-CCR-WIRE-033 — Delivery is not durability.** A 2xx delivery-update POST
  confirms only acceptance of that service operation. It does not prove model
  admission, transcript persistence, visible rendering, or terminal result.

## SSE stream

### Request

The connection is an HTTP GET to `/worker/events/stream`. When the in-memory
high-water is positive, send both:

```text
query:  from_sequence_num=<decimal-high-water>
header: Last-Event-ID: <decimal-high-water>
```

The client does not require or validate the response Content-Type. Any 2xx with
a body enters the read loop.

### Exact frame parser

- Decode chunks incrementally as UTF-8 and retain an incomplete text suffix.
- Delimit frames only with the two-character sequence LF LF (`\n\n`). The
  reference parser does not normalize CRLF; CR characters therefore remain in
  field values and a CRLF/CRLF boundary is not recognized as LF/LF.
- Split a complete frame on LF. A line beginning `:` is a comment. Other lines
  without `:` are ignored.
- For recognized `event`, `id`, and `data`, remove at most one ASCII space
  immediately after the colon. Repeated event or ID fields use the last value.
  Data fields join with LF, subject to the compatibility parser's truthy-empty
  behavior: an initial empty `data:` line does not preserve a leading LF.
- Ignore `retry` and every unknown field.
- Emit only a frame with nonempty data or at least one comment. An empty data
  frame without a comment is discarded. A final unterminated buffered frame is
  discarded when the stream ends.

- **RB-CCR-WIRE-040 — Exact SSE envelope.** The only recognized worker event
  name is `client_event`. Its `data` is the direct JSON record below; there is
  no additional `{type,data}` wrapper.

```text
event: client_event
id: <parseable sequence text>
data: {
  "event_id": string,
  "sequence_num": number,
  "event_type": string,
  "source": string,
  "payload": object,
  "created_at": string
}

```

After JSON parse, an object payload containing any member named `type` is
serialized as one NDJSON line to the structured-data observer. The `type`
value is not runtime-checked as a string here. The typed event observer is then
called even when the payload was absent or lacked `type`, provided the outer
JSON parse succeeded. Unknown SSE event names are logged and ignored. Invalid
JSON is logged and isolated.

- **RB-CCR-WIRE-041 — Parser versus declared shape.** The record above is the
  declared shape, but the reference SSE adapter uses structural casts rather
  than a runtime schema. A standalone client may validate it strictly before
  semantic admission; if it does, retain frame-ID observation ordering and do
  not emit a false delivery status for a rejected record.

## Cursor and delivery semantics

- **RB-CCR-WIRE-050 — Frame-ID high-water.** Parse frame `id` with a
  leading-decimal integer conversion. If successful, record it before event-name
  or JSON validation and raise high-water only when greater than the current
  value. The JSON body's `sequence_num` is diagnostic data and does not drive
  reconnect.
- **RB-CCR-WIRE-051 — Duplicate compatibility behavior.** Seen numeric IDs are
  tracked and duplicate IDs are diagnosed, but the reference adapter does not
  suppress dispatch. Older IDs also dispatch. When the seen set exceeds 1,000,
  remove entries strictly below `high-water - 200`; this is a memory bound aid,
  not durable deduplication.
- **RB-CCR-WIRE-052 — Observation precedes processing.** A malformed or unknown
  frame can advance high-water. Same-process reconnect may therefore skip it.
  The cursor is memory-only, is carried into a replacement transport only when
  its owner explicitly passes it, and is never a processed or durable
  checkpoint.
- **RB-CCR-WIRE-053 — Liveness.** Every emitted parser frame, including a pure
  comment, resets a 45,000 ms timer. Silence aborts and reconnects. Bytes that
  never form an emitted frame do not reset liveness.

## HTTP status, retry, and epoch rules

### SSE connect

Status 401, 403, or 404 closes permanently and calls the close observer with
that status. Other non-2xx, missing response body, read failure, normal stream
end, or liveness timeout reconnect. Delay starts at 1,000 ms, doubles to 30,000
ms, adds symmetric 25% jitter, and gives up after 600,000 ms of a continuous
failed-connect period. A successful connection resets attempts and elapsed
budget before reading; repeated successful-but-short streams can therefore
restart the budget.

### Worker POST and PUT

The primitive request performs one attempt. Any 2xx succeeds and resets the
shared consecutive-auth-failure count. Network failure and every non-2xx return
failure to the caller except fencing:

- 409 invokes epoch-mismatch handling immediately;
- 401/403 checks the process-global ingress JWT expiry. If it appears expired,
  fence immediately. Otherwise increment a client-wide consecutive count and
  fence on the tenth failure;
- with a per-instance auth closure, expiry inspection still consults the
  process-global token. This is a reference compatibility limitation; the
  ten-failure fence remains when no matching global expiry can be read;
- 429 reads a string `Retry-After` as integer seconds when possible and passes
  that hint to the uploader;
- response bodies are ignored.

Only a 2xx resets the shared authentication-failure count; an intervening
non-auth failure does not reset it.

The visible, internal, delivery, and coalesced state uploaders retry every
nonfencing failure, including non-429 4xx. Their delay starts at 500 ms, doubles
to 30,000 ms, and adds uniform 0–500 ms jitter. A valid Retry-After hint is
clamped to 500–30,000 ms before jitter. These worker uploaders have no attempt
ceiling and retry until success, close, or fencing.

- **RB-CCR-WIRE-060 — Worker retry classification.** Do not apply the generic
  “non-429 4xx is permanent” rule to CCR worker uploaders. That rule belongs to
  other transports. CCR retries all nonfencing failures through its ordered
  uploaders.

### Worker GET

Each page or state GET makes at most ten attempts. Any 2xx returns the body.
Every other status and network error retries, except 409 which fences. Delay is
500 ms times powers of two, capped at 30,000 ms, plus uniform 0–500 ms jitter.
There is no Retry-After handling for GET. Exhaustion returns null.

### Compatibility SSE writer

The SSE adapter also exposes a POST writer derived by removing a terminal
`/stream`. CCR v2 does not use it for worker events; CCR uses the cataloged
worker endpoints. When invoked by another surface, it accepts only 200/201,
retries network errors, 429, 3xx, and 5xx for ten attempts, and stops on other
4xx. Delays are 500 ms exponential capped at 8,000 ms, with no jitter,
Retry-After, or explicit request timeout. It captures auth once per logical
write and resolves without throwing after exhaustion.

- **RB-CCR-WIRE-061 — Writer separation.** Never send a worker event through
  the compatibility SSE POST writer. Its URL, success classes, retries, and
  body shape differ from `/worker/events`.

## Uploader bounds and close behavior

- **RB-CCR-WIRE-070 — Serial FIFO.** Each event uploader permits one in-flight
  HTTP call. A failed batch is prepended ahead of arrivals and retried. An item
  that cannot be serialized is removed so it cannot poison the queue. The
  first item may exceed the byte cap; subsequent items stop before exceeding
  it.
- **RB-CCR-WIRE-071 — Backpressure.** Admission waits while the complete new
  addition would exceed queue capacity. Closing releases blocked producers and
  flush waiters, records the pending count, and drops the memory queue.
- **RB-CCR-WIRE-072 — Flush distinction.** Visible flush first drains the
  100-ms stream buffer and then the visible uploader. Internal flush drains only
  internal events. State and delivery uploaders have no public flush. Close
  stops heartbeat/activity callbacks, discards buffered deltas and all queues,
  and does not imply remote durability.

## Acceptance scenarios

### `RB-CCR-WIRE-A01` — Bootstrap tuple and fencing

Create a bridge session, obtain `/bridge` credentials with a decimal-string
epoch, and construct the worker from the returned tuple. Verify no additional
registration occurs. Call `/bridge` again and verify the old worker's next 409
fences it rather than retrying under the stale epoch.

### `RB-CCR-WIRE-A02` — Exact state projection

Initialize while the prior metadata GET is delayed. Verify the init PUT clears
both stale metadata keys with null, heartbeat begins only after PUT success, and
restoration is reported afterward. Publish a permission action and verify the
worker status contains only tool name, description, and request ID.

### `RB-CCR-WIRE-A03` — Malformed frame advances cursor

Send `id: 41` with invalid `client_event` JSON. Verify high-water becomes 41,
no semantic payload is delivered, and reconnect sends both resume query and
header at 41. Restart the process and verify 41 is not invented from persistent
state.

### `RB-CCR-WIRE-A04` — Duplicate frame behavior

Send the same valid numeric frame ID twice. Verify a duplicate diagnostic and
two typed/data dispatches from the transport; semantic UUID deduplication, if
desired, occurs at the higher bridge/session layer rather than in the SSE
sequence set.

### `RB-CCR-WIRE-A05` — CRLF compatibility edge

Feed a stream framed only with CRLF/CRLF and verify the reference parser retains
it as an incomplete buffer until disconnect, then drops it. Feed LF/LF and
verify it parses. A stricter standards-compliant replacement must version this
behavior if interoperability depends on accepting CRLF that the reference did
not.

### `RB-CCR-WIRE-A06` — Retry class separation

Return 422 from `/worker/events` twice and then 200; verify the worker uploader
retries in order. Return 422 from the compatibility SSE writer and verify it
stops that write immediately. Return 409 on either worker write and verify epoch
fencing, not same-epoch retry.

### `RB-CCR-WIRE-A07` — Partial pagination failure

Return one internal-event page with `next_cursor`, then fail ten attempts for
the next page. Verify the whole read returns null and none of the first page is
presented as a complete transcript.

### `RB-CCR-WIRE-A08` — Distinct drains

Queue visible, internal, delivery, and worker-state records. Flush internal
only, then close. Verify only internal completion was awaited and every other
queue is accounted as a possible memory loss rather than acknowledged delivery.

## Opaque server boundaries

The client cannot infer from a 2xx response whether the service durably stored
an event, how it applies metadata nulls, whether event UUIDs are retained
forever for idempotency, or how long delivery records survive. It also cannot
infer model completion from heartbeat, worker status, event upload, SSE receipt,
or delivery update. An implementation may add a durable outbox or stricter SSE
schema, but must name it as a compatibility extension and define migration,
acknowledgement, and replay behavior explicitly.
