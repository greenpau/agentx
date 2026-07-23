# Bridge architectures

This reference defines how a local runtime becomes a remote-controlled worker. It covers both environment-backed and environment-less architectures. Transport mechanics shared by those architectures are specified in [transport protocols](transport-protocols.md).

## Contents

- [Contract map](#contract-map)
- [Surface selection](#surface-selection)
- [Identity and authority ledger](#identity-and-authority-ledger)
- [Environment-backed architecture](#environment-backed-architecture)
- [Environment-less REPL architecture](#environment-less-repl-architecture)
- [Spawn modes and capacity](#spawn-modes-and-capacity)
- [Polling, leases, and backoff](#polling-leases-and-backoff)
- [State machines](#state-machines)
- [Teardown and recovery](#teardown-and-recovery)
- [Disabled and failure behavior](#disabled-and-failure-behavior)

## Contract map

| ID | Requirement |
| --- | --- |
| RB-ARCH-001 | Keep environment-backed work dispatch and environment-less session registration as separate architectures. |
| RB-ARCH-002 | Select bridge architecture independently from transport protocol version. |
| RB-ARCH-003 | The shared bridge-topology selector chooses environment-less only when the environment-less REPL gate is enabled and perpetual operation is false; every perpetual selection is environment-backed. The executing selector, not an entrypoint label or nearby comment, is authoritative. |
| RB-ARCH-004 | Select event transport after topology: environment-less always uses CCR version 2; environment-backed uses CCR version 2 when `use_code_sessions` or the development override selects it and otherwise uses Hybrid. The generic SDK transport has its own ordered override chain: SSE, then Hybrid, then WebSocket. |
| RB-ID-002 | Local transcript UUID, remote logical session ID, control request ID, worker epoch, and delivery cursor are independent identities. The documented runtime has no general durable record that binds them together. |
| RB-RST-001 | Same-process replacement may carry an in-memory cursor, queue, and logical session into a new worker epoch, but this is not a crash-safe transaction. Process restart follows only the explicitly persisted pointer or resume contract and never infers missing cursor, epoch, transcript, or pending-control state. |
| RB-REG-001 | Environment registration is idempotent by client environment identity and yields the server environment identity used thereafter. |
| RB-WORK-001 | A work item is not owned until its secret validates and acknowledgement succeeds. |
| RB-WORK-002 | Reclaim age allows a server to redeliver stale unacknowledged work. Workers therefore tolerate duplicate polls. |
| RB-SPN-001 | Spawn mode determines bridge lifetime, working-directory isolation, and capacity semantics. |
| RB-LSE-002 | Work heartbeat extends a lease but does not constitute session output delivery. |
| RB-CAP-001 | Capacity transitions wake the poll loop immediately; they do not wait for a long at-capacity interval. |
| RB-V2-001 | Environment-less initialization creates a session, then registers a worker and receives an epoch-fenced JWT. |
| RB-V2-002 | Re-registering the worker intentionally advances its epoch and fences the old transport. |
| RB-V2-003 | Credential recovery is serialized so concurrent triggers cannot advance the epoch twice. |
| RB-END-001 | Teardown is idempotent and orders terminal result, archive, transport close, and local resource release. |

## Surface selection

Evaluate dimensions independently. “Included” does not imply “eligible,” eligibility does not imply “enabled,” topology does not imply transport, and a human-facing mode label does not override the code path that actually invokes a selector.

### Topology selection

| Executing path | Topology rule | Work placement | Unsupported behavior |
| --- | --- | --- | --- |
| Shared bridge initializer used by an interactive or SDK-control executable | If the `tengu_bridge_repl_v2` environment-less gate is enabled and perpetual operation is false, choose environment-less; otherwise choose environment-backed | Current interactive/SDK-controlled session for environment-less, or environment work/session ownership for environment-backed | Fail or leave the local surface usable according to that caller's lifecycle; never choose from a comment or display label |
| Perpetual bridge operation | Force environment-backed regardless of the environment-less gate | Repeated environment work items and child sessions | Refuse startup if environment registration cannot become active; never create a fresh environment-less session as a perpetual fallback |
| Dedicated legacy environment-work dispatcher | Environment-backed | One or many acknowledged work items according to spawn mode | Exit or drain with explicit work reconciliation; never partially claim an item |
| Outbound mirror | Neither work-dispatch topology | Local events only; remote inbound mutation disabled | Respond safely to inbound control attempts; never reinterpret mirror as bidirectional remote control |

Consequently, a daemon-like or print-like SDK control executable can select environment-less when it actually calls the shared selector with the gate enabled and perpetual false. Do not encode “daemon” or “print” as an unconditional environment-backed rule unless that concrete executable uses the dedicated environment-work dispatcher.

### Transport selection after topology

| Context | Ordered decision | Result |
| --- | --- | --- |
| Environment-less bridge | No secondary choice | CCR version 2 |
| Environment-backed bridge | `use_code_sessions` true or development override active | CCR version 2 |
| Environment-backed bridge | Neither CCR selector active | Hybrid WebSocket-read/HTTP-write transport |
| Generic SDK transport factory | SSE override first; otherwise Hybrid override; otherwise default | SSE, Hybrid, or WebSocket respectively |

The generic SDK override chain is a separate adapter decision and must not be used to infer bridge topology.

### Eligibility order

Use this order to produce a stable actionable reason:

1. Verify remote-control code is included in the build.
2. Verify the session uses a supported first-party subscriber OAuth identity, not an API key or an incompatible cloud/provider/gateway identity.
3. Verify the OAuth profile has the required scope.
4. Verify an organization identifier is available.
5. Verify the primary remote-control runtime gate.
6. Verify the selected architecture's minimum client version.
7. Verify mode-specific gates, such as environment-less REPL or auto-connect.
8. Apply managed policy and locally disabled state.

An outbound mirror uses its own gate and environment switch. Auto-connect similarly uses a separate gate. Neither is evidence that inbound bridge control is authorized.

## Identity and authority ledger

| Identity or credential | Issuer | Scope and comparison | Lifetime and invalidation |
| --- | --- | --- | --- |
| Bridge instance ID | Local client | UUID for one installed/running bridge identity | Stable for the configured bridge identity |
| Local transcript UUID | Local session runtime | Exact identity of one local transcript graph; never compare to a remote session ID | A URL/remote-history resume may hydrate into a newly allocated local UUID; no general durable remote association exists |
| Client environment ID | Local client | Idempotency key used when registering | Stable across environment registration retries |
| Server environment ID | Environment service | Exact opaque ID for poll, reconnect, heartbeat, stop, and deregistration | Until deregistered, expired, or invalidated by service |
| Work ID | Work service | Exact opaque ID; validate as URL-safe before path interpolation | Until acknowledged and terminally stopped/completed |
| Work secret | Work service | Versioned encrypted/encoded payload for one work item | Reject unknown version, missing ingress token, or invalid API base URL |
| Logical session ID | Session service | Exact opaque ID; compatibility spellings may compare only by normalized suffix | Until archived/expired; default requested timeout is 24 hours |
| Worker JWT | Worker registration service | Opaque bearer credential for one logical session and worker epoch | Expires by `expires_in`; replace before refresh buffer or on 401 |
| Worker epoch | Worker registration service | Validated integer carried on epoch-scoped worker writes, heartbeats, event uploads, and delivery reports; exact match | A later worker registration fences all earlier epochs; authenticated SSE and state reads use their cataloged cursor/query form |
| Ingress token | Environment work secret | Session-ingress writes for the assigned work | Bound to work/session; do not reuse for unrelated sessions |
| Event sequence/cursor | Transport service and local transport | Monotonic observed-frame position, not a message identity or durable processing checkpoint | Retained only across same-process replacement; no cross-process persistence format exists in the documented runtime |
| Message UUID | Message producer | Exact UUID for echo/replay deduplication | Bounded recent cache; initial-history set may have longer scope |
| Control request ID | Control producer | Correlates one request, cancellation, and response; never substitute the local transcript or remote session ID | Pending correlation is process-local and terminal after success, error, denial, cancellation, or supersession |
| Tool-use ID | Query engine | Correlates permission request and tool result | Terminal under shared tool protocol |

### Reference persistence boundary

The reference behavior persists less than the full identity ledger:

- Its general session persistence does not store a durable tuple relating local transcript UUID, remote logical session ID, surface instance, worker epoch, delivery cursor, and pending control requests.
- An environment-backed bridge pointer contains only remote logical session ID, environment ID, and source. It contains no local transcript UUID, cursor, worker epoch, deduplication window, or pending-control state.
- Resuming remote history by URL allocates a fresh local transcript UUID and hydrates the history; it does not restore the earlier local UUID by association.
- A fresh environment-less process creates a new remote logical session and begins with cursor zero. Same-process JWT/epoch replacement is the path that retains the existing logical session and in-memory cursor.
- A perpetual environment-backed process may reuse its persisted bridge pointer, but implements cursor and deduplication state independently. Pointer updates are asynchronous and are not a commit record for transport recovery.

An implementation may add a durable association record such as `{local_transcript_uuid, remote_session_id, environment_id?, surface_generation, worker_epoch?, cursor?}`. Treat that as an intentional hardening extension with migration, validation, and stale-generation rules; do not claim it reproduces reference persistence.

Session-ID compatibility is a named shim:

- Compare two supported session spellings by the suffix after the final underscore only when that suffix has at least four characters.
- Convert a native session ID to the compatibility spelling only when the dedicated compatibility gate permits it.
- Never apply suffix comparison to environment IDs, work IDs, request IDs, or epochs.

## Environment-backed architecture

### Configuration contract

An environment-backed bridge configuration contains:

| Field | Requirement |
| --- | --- |
| Working directory | Existing local directory used directly or as the parent of worktrees |
| Machine name | Human-readable environment label |
| Branch | Current source-control branch when available |
| Repository URL | Normalized repository identity or null |
| Maximum sessions | Positive capacity limit |
| Spawn mode | `single-session`, `worktree`, or `same-dir` |
| Verbose flag | Diagnostic presentation only; never changes protocol semantics |
| Sandbox setting | Local execution isolation policy |
| Bridge instance ID | Stable local UUID |
| Worker type | Opaque string sent to the service; known values include coding and assistant workers |
| Client environment ID | Stable registration idempotency identity |
| Reused environment ID | Optional server identity used for reconnect/reuse |
| API base URL | Valid absolute service base URL |
| Session-ingress URL | Valid absolute event-write endpoint/base |
| Debug file | Optional diagnostics sink; must exclude secrets |
| Session timeout | Optional; default 24 hours |

### Registration and work API

All dynamic path identifiers must match `^[a-zA-Z0-9_-]+$` before URL construction. Fail locally if not.

| Operation | Method and route | Important semantics |
| --- | --- | --- |
| Register bridge environment | `POST /v1/environments/bridge` | Use environments beta version `environments-2025-11-01`; retry with the same client environment ID |
| Poll work | `GET /v1/environments/{environment_id}/work/poll` | Include capacity/reclaim inputs; no work is a normal outcome |
| Acknowledge work | `POST /v1/environments/{environment_id}/work/{work_id}/ack` | Ownership begins only after valid secret and successful acknowledgement |
| Stop work | `POST /v1/environments/{environment_id}/work/{work_id}/stop` | Publish terminal state; repeated terminal stop is treated idempotently when the service permits |
| Heartbeat work | `POST /v1/environments/{environment_id}/work/{work_id}/heartbeat` | Response reports whether lease was extended and current state |
| Reconnect bridge | `POST /v1/environments/{environment_id}/bridge/reconnect` | Reassert a known environment after local reconnect |
| Deregister bridge | `DELETE /v1/environments/bridge/{environment_id}` | Best-effort on orderly daemon shutdown |
| Archive session | `POST /v1/sessions/{session_id}/archive` | HTTP 409 means already archived and is terminal success for cleanup |
| Send control response | `POST /v1/sessions/{session_id}/events` | Correlated response envelope; transport protocol may provide an alternate writer |

The polled work envelope is:

```text
WorkResponse {
  id: string
  type: "work"
  environment_id: string
  state: service-defined work state
  data: WorkData
  secret: encoded versioned work secret
  created_at: timestamp
}

WorkData {
  type: "session" | "healthcheck"
  id: string
}
```

Decode and validate the secret before acknowledgement:

1. Secret schema version equals `1`.
2. `session_ingress_token` is a nonempty string.
3. `api_base_url` is a valid string URL.
4. Optional source, authentication, arguments, MCP configuration, environment, and `use_code_sessions` fields have expected types.
5. Unknown optional fields are ignored for forward compatibility; unknown required-version semantics fail closed.

A health-check work item exercises claim/liveness behavior but must not be mistaken for a user conversation.

### Environment-backed dispatch sequence

```text
local startup
  -> validate eligibility and configuration
  -> register or reconnect environment
  -> poll while capacity permits
  -> validate returned identifiers and decode secret
  -> acknowledge work
  -> allocate session owner and spawn worker
  -> heartbeat lease while work remains live
  -> report activity and terminal work state
  -> archive logical session when required
  -> release worktree/process/task capacity
  -> poll again, or deregister on shutdown
```

If secret validation or acknowledgement fails, do not spawn. The server may reclaim and redeliver after `reclaim_older_than_ms`. If spawn fails after acknowledgement, report a failed terminal work outcome so the item is not silently leased forever.

## Environment-less REPL architecture

This architecture controls the already-running interactive session. It does not create an environment resource and never calls environment register, poll, work ack/stop, environment heartbeat, or deregister.

### Initialization wire sequence

1. Create the remote logical session:

   ```text
   POST /v1/code/sessions
   body {
     title: string
     bridge: {}
     tags?: string[]
   }
   -> { session identity beginning with "cse_", ... }
   ```

2. Register the local worker for that session using the OAuth or trusted-device credential:

   ```text
   POST /v1/code/sessions/{session_id}/bridge
   -> {
     worker_jwt: string
     expires_in: seconds
     api_base_url: string
     worker_epoch: epoch
   }
   ```

3. Construct CCR event transport with the session identity, worker JWT, worker epoch, and current sequence high-water mark.
4. Connect the server-to-worker SSE stream and the worker-to-server HTTP uploader.
5. Activate the initial-history flush gate, send eligible history, release queued live events, and begin heartbeat/credential scheduling.

Calling `/bridge` is worker registration, not a token-only refresh. It advances the epoch. Therefore proactive refresh and reactive 401 recovery share one serialized `authRecoveryInFlight` operation.

### Environment-less configuration

The entire configuration is validated as a unit. Any malformed value causes the full default configuration to be used, avoiding unsafe mixtures of independently defaulted timing values.

| Field | Default | Valid range or rule |
| --- | ---: | --- |
| Initialization retry attempts | 3 | Integer 1–10 |
| Initialization base delay | 500 ms | At least 100 ms |
| Initialization jitter fraction | 0.25 | 0–1 |
| Initialization maximum delay | 4,000 ms | At least 500 ms |
| HTTP request timeout | 10,000 ms | At least 2,000 ms |
| UUID deduplication buffer | 2,000 | Integer 100–50,000 |
| Worker heartbeat interval | 20,000 ms | 5,000–30,000 ms |
| Heartbeat jitter fraction | 0.1 | 0–0.5 |
| Token refresh buffer | 300,000 ms | 30,000–1,800,000 ms |
| Teardown archive timeout | 1,500 ms | 500–2,000 ms |
| Connection timeout | 15,000 ms | 5,000–60,000 ms |
| Minimum version | `0.0.0` | Valid semantic version |
| Show application-upgrade message | false | Boolean |

Initialization retry delay for attempt `n`, starting at one, is:

```text
min(max_delay, base_delay * 2^(n-1)) adjusted by symmetric configured jitter
```

Do not retry schema, eligibility, or permanent authorization errors as transient network failures.

The worker heartbeat interval of 20 seconds is chosen beneath a 60-second server worker TTL. Heartbeat is worker presence, not event-delivery acknowledgement.

### Credential refresh and transport replacement

Use the credential's `expires_in` and the configured refresh buffer. If expiration metadata cannot schedule normally, use a bounded fallback refresh (30 minutes is the compatibility default). Token-refresh failures use a 60-second retry and stop after three consecutive failures unless a higher-level reconnect policy implements the bridge.

Replacement sequence:

1. Acquire the single recovery operation; concurrent proactive or 401 triggers await it.
2. Stop admitting new transport writes into the old connection and retain the flush queue.
3. Capture the current inbound sequence high-water mark.
4. Close the old transport.
5. Register the worker again to receive a new JWT and epoch.
6. Build a transport with the same logical session and captured sequence.
7. Wire handlers, connect, and perform/reperform initial history as required.
8. Release queued live writes in FIFO order.
9. If teardown began during any awaited step, close the newly created transport without attaching it.

A 401 recovery resets the “initial flush done” latch so history is re-projected under the in-memory deduplication rules. An epoch conflict follows the same implementation boundary; never keep uploading under a stale epoch.

This replacement is serialized only inside the live process. The captured cursor, retained flush queue, deduplication rings, and recovery promise are not durably committed with the new epoch. A process death at any awaited step therefore cannot resume the algorithm as a transaction; restart follows `RB-RST-001`.

## Spawn modes and capacity

| Mode | Bridge lifetime | Directory semantics | Capacity and collision behavior |
| --- | --- | --- | --- |
| `single-session` | Ends when its one child session ends | Uses configured directory directly | Exactly one owned work item; no persistent dispatch loop |
| `worktree` | Persistent | Creates one isolated source-control worktree per accepted session | Up to `maxSessions`; cleanup/retention follows child outcome and modifications |
| `same-dir` | Persistent | Every child uses the same configured directory | Up to `maxSessions`; concurrent writes may conflict and must be disclosed |

Session state reports `completed`, `failed`, or `interrupted`. Activity is a small presentation buffer, not authoritative history; retain approximately the last ten activity and stderr records. Activity categories are `tool_start`, `text`, `result`, and `error`.

When active count reaches maximum capacity:

- Continue liveness using the configured heartbeat and/or at-capacity poll.
- Do not claim work beyond capacity.
- Install a capacity-change wake signal. A child terminal transition aborts the long wait and immediately re-evaluates capacity.
- Treat the wake as a hint; re-read authoritative active state before polling.

Worktree pointers retained for reconnect are valid for four hours. Bound broad pointer discovery to at most fifty candidates per scan.

## Polling, leases, and backoff

### Default poll configuration

| Field | Default |
| --- | ---: |
| Single-session poll interval when not at capacity | 2,000 ms |
| Single-session poll interval at capacity | 600,000 ms |
| Non-exclusive heartbeat interval | 0 ms |
| Multisession poll interval when empty/not at capacity | 2,000 ms |
| Multisession poll interval at partial capacity | 2,000 ms |
| Multisession poll interval at capacity | 600,000 ms |
| Reclaim work older than | 5,000 ms |
| Version-2 session keepalive interval | 120,000 ms |

Poll configuration is fetched/cached for five minutes. Validate it atomically:

- Non-capacity and partial-capacity poll intervals are integers of at least 100 ms.
- At-capacity poll intervals are either zero or at least 100 ms.
- Heartbeat and keepalive intervals are nonnegative.
- Reclaim age is at least 1 ms.
- At least one at-capacity liveness route is enabled for each applicable loop: heartbeat greater than zero or its at-capacity poll interval greater than zero.
- If any field or cross-field invariant fails, use the entire default set.

At capacity, heartbeat and polling are independent, non-exclusive operations. A heartbeat maintains the approximately 300-second environment-side liveness lease; a sparse poll is also a liveness backstop and a way to observe server state. The work heartbeat separately extends an accepted work item's lease.

### Poll result handling

| Outcome | Action |
| --- | --- |
| No work | Wait the interval selected by capacity and retry |
| Valid unclaimed work | Validate, acknowledge, allocate capacity, then spawn |
| Duplicate/reclaimed work already owned locally | Reconcile by work ID; do not launch a second owner |
| Transient network/5xx/429 | Apply bounded transport retry/backoff without changing work ownership |
| 401 | Refresh the appropriate credential, then retry only if the operation is safe |
| 403 or account/policy denial | Distinguish explicitly suppressible absence from fatal eligibility loss; do not spin |
| Unsafe identifier or malformed secret | Fail closed before URL/spawn; report diagnostic without secret content |
| Service says work terminal | Stop heartbeat and reconcile local child; do not continue writing as live work |

## State machines

### Environment lifecycle

```text
unconfigured
  -> validating
  -> registering | reconnecting
  -> active
  -> draining
  -> deregistering
  -> closed

validating/registering/reconnecting --permanent error--> failed
active --eligibility revoked--> draining
active --transient disconnect--> reconnecting
```

Only `active` polls new work. `draining` accepts no new work but lets owned work settle within policy.

### Work ownership lifecycle

```text
observed
  -> secret_validated
  -> acknowledging
  -> owned
  -> spawning
  -> running
  -> completing | failing | interrupting
  -> terminal_reported
  -> released

observed/secret_validated/acknowledging --not acknowledged--> unowned
owned/spawning --spawn failure--> failing
running --lease lost--> interrupting
```

Persist or otherwise make recoverable the transition to `owned` before launching an anonymous child. Every state at or after `owned` requires terminal reconciliation.

### Environment-less worker lifecycle

```text
local_only
  -> creating_session
  -> registering_worker
  -> connecting
  -> flushing_history
  -> active
  -> recovering_credentials
  -> connecting (new epoch)
  -> draining
  -> archiving
  -> closed
```

Initialization failure returns to `local_only` when the interactive session can continue safely. A permanent eligibility failure must not retry indefinitely.

## Teardown and recovery

Teardown uses one atomic/idempotent latch. Repeated callers await or observe the same teardown; they do not publish multiple results or archives.

Environment-less order:

1. Set teardown latch and cancel initialization, heartbeat, retry, and refresh timers.
2. Stop new writes and drop or explicitly account for queued nonterminal traffic.
3. Report worker idle where the protocol supports it.
4. Enqueue the final result before archive.
5. Archive with the configured timeout (default 1.5 seconds). On 401, refresh OAuth once and retry archive once. Treat already-archived conflict as success.
6. Close the transport only after the archive attempt, giving the serial uploader a bounded drain opportunity.
7. Release handlers, queues, credentials, and local bridge state.

Environment-backed order:

1. Stop polling and mark the bridge draining.
2. Stop or await owned work according to shutdown policy.
3. Report a terminal state for each acknowledged work ID.
4. Stop work heartbeat and archive applicable sessions.
5. Close child processes/transports and release worktrees.
6. Deregister the environment best-effort.
7. Release local locks, timers, capacity signals, and diagnostics handles.

Crash recovery implements environment/work ownership from whatever durable environment pointer, work/session identifiers, and local child/task evidence actually exist. It does not implement an environment-less cursor, epoch, pending control map, or local-transcript association that was never persisted. Never assume “process absent” means “work unowned”; query/reconcile service state or terminally fail it.

## Disabled and failure behavior

| Condition | Required behavior |
| --- | --- |
| Remote-control build absent | Present the build-unavailable explanation; do not expose a nonfunctional command |
| Unsupported credential/provider | Explain the supported account class; never exchange an API key for a worker credential implicitly |
| Missing profile scope or organization | Request the specific missing identity step |
| Runtime gate off | Leave local operation unchanged; no environment/session side effect |
| Client below minimum version | Offer upgrade guidance only when configured; do not attempt a protocol the client cannot honor |
| Environment-less setup fails in REPL | Tear down partial remote state and keep the local session usable |
| Daemon setup fails | Exit nonzero after releasing registration/locks; do not masquerade as an idle bridge |
| Duplicate work delivery | Reconcile by stable work ID; at most one local owner |
| Lost work lease | Stop or fence the child before any further remote writes, then publish/reconcile terminal state |
| Token expiry during operation | Serialize refresh/re-registration, preserve cursor and queues, implement transport |
| Stale worker epoch | Stop writes immediately and implement or exit; never retry under the stale epoch |
| Archive unavailable | Bound the attempt, retain terminal local evidence, and close; archive is best-effort cleanup rather than permission to finish locally |
| Shutdown during initialization/recovery | The teardown latch wins; any late-created transport is immediately closed and never attached |
