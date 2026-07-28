# System Contract

## Contents

1. Product boundary
2. State and identity model
3. Canonical events
4. End-to-end lifecycle
5. Concurrency and ordering
6. Failure containment
7. Security boundary
8. Portability rules
9. Cross-domain acceptance scenarios

## Product boundary

The product is an orchestration runtime between a human or external caller, a model service, the caller's workstation or remote environment, and extension providers. It is not the language model, terminal emulator, operating system, source-control system, or extension server.

The semantic core accepts normalized external events and emits canonical session events. Interactive terminal, one-shot text, structured streams, SDK control, bridge, remote viewer, and standalone capability-host surfaces translate to and from those events. The standalone capability host omits the conversation loop but still reuses capability validation and result contracts. Provider-free native session-management entrypoints also omit the conversation loop: after the shared application-home, authentication-presence, option, and workspace gates, they call the runtime-owned continuity service without constructing a semantic session, transcript writer, model/provider client, extension registry, or project memory.

### Required ports

| Port | Input | Output | Failure scope |
| --- | --- | --- | --- |
| Model | effective prompt, messages, tools, model options, abort | streamed blocks, usage, stop metadata, request identity | current request or bounded retry chain |
| Capability | canonical name, validated input, call identity, context | progress and exactly one live-turn terminal result; explicit unresolved-call recovery after crash | one call unless policy declares sibling cancellation |
| Persistence | append records, flush, load, branch, mutation | durable evidence or explicit failure | session continuity, never silent success |
| Attachment storage | explicit file import or correlated upload, stable attachment identity, bounded resolve/copy/collect | immutable verified media plus safe manifest | one import/message/session; never filesystem authority |
| Presentation | canonical events and UI commands | user/external events | adapter only; core remains coherent |
| Extensions | manifests, registrations, hooks, remote resources | attributed registry entries and lifecycle events | extension entry or provider, not entire session by default |
| Platform | files, processes, terminal, notifications, credentials | bounded results and capability status | integration-specific degradation |

## State and identity model

`ARCH-STATE-001` — Give every session, turn, API request, message, tool use, task, agent, team, hook request, control request, and remote delivery a stable identity appropriate to its lifetime.

`ARCH-STATE-002` — Keep these stores conceptually separate:

| Lifetime | Owns | Must not own |
| --- | --- | --- |
| Process bootstrap | entrypoint, original environment, early feature latches, initial identity, process services | mutable conversation truth |
| Session application | current model/mode, tool registry, permission context, connections, tasks, UI coordination | immutable historical evidence |
| Turn | abort tree, request depth, queued input snapshot, tool-use set, retry counters | cross-turn settings without an explicit update |
| Capability call | validated and observable input, decision provenance, progress, result | unrelated sibling state |
| Background task | lifecycle, owner, output location/offset, cancellation, notification | anonymous fire-and-forget work |
| Durable transcript | ordered/linked semantic records and significant metadata | transient rendering or progress-only events |
| Session attachment store | owner-private manifests, content-addressed immutable blobs, upload reservations and temporary files | source paths, inline transcript bytes, permission authority |
| Presentation | focus, scroll, overlays, animation, viewport, ephemeral notifications | permission or transcript authority |

`ARCH-STATE-003` — A durable record is immutable after append unless the format explicitly defines a separately recorded tombstone, replacement, relink, or snapshot event. Recovery replays those events; it does not rewrite history invisibly.

`ARCH-STATE-004` — Switching or forking identity updates all dependent routing and persistence state in one owned transition where the active storage profile supports it. If the reference profile has no durable cross-store transaction, record the exact process-local update order and crash window and reconcile partial evidence on restart. A fork copies allowed semantic history but receives fresh ownership and delivery identities.

## Canonical events

Use a tagged event envelope with at least: schema version, event identity, session identity, timestamp, semantic type, payload, visibility class, persistence class, and optional parent/correlation identities. Preserve unknown extension fields when forwarding if the wire contract permits it.

Visibility classes are:

- model-visible and durable;
- durable metadata but not model-visible;
- surface-visible but not durable;
- internal operational evidence;
- secret, which must not be emitted or persisted outside its protected owner.

The core event family must represent user input, assistant blocks, tool use and result, local command result, progress, status, compaction boundary, task lifecycle, permission request/decision, retry/rate limit, connection status, hook lifecycle, usage, final result, cancellation, and error.

`ARCH-EVENT-001` — A surface-specific renderer may coalesce events for responsiveness but may not change semantic order or omit durable terminal evidence.

`ARCH-EVENT-002` — If delivery supports replay, consumers deduplicate by event or request identity, not by payload equality.

## End-to-end lifecycle

Preserve this dependency order:

1. Determine entrypoint and output contract before general logging or terminal initialization.
2. Load syntactically valid configuration, perform compatible migrations, establish identity, trust, policy, authentication, and initial permission state.
3. Discover registries, then filter each entry by build, gate, identity, platform, policy, configuration, and health.
4. Create or restore session, transcript cursor, file and attribution snapshots, task registry, background ownership, and presentation adapter. Repair interrupted call pairs before accepting another turn.
5. Normalize input, commit supported caller-selected attachments into session-owned storage, atomically validate the ordered typed message, expand other supported references, route local commands, run input hooks, and persist accepted model-bound input.
6. Compose stable and volatile context in declared order, invoke the model, and publish streamed semantic events.
7. Resolve every tool use, validate it, decide permission, select isolation, execute safely, normalize output, and append paired results.
8. Continue the model loop until successful completion, cancellation, non-recoverable error, policy/turn/cost/token limit, or another explicit terminal condition.
9. Fan out final events, flush durable state, notify background completions, and release registered resources in reverse ownership order.

`ARCH-LIFE-001` — Setup work may overlap only where no later decision depends on its result. Trust-dependent code, environment application, project executables, hooks, and servers cannot run early merely to reduce latency.

`ARCH-LIFE-002` — Recovery runs before new user work and converts every accepted but incomplete operation into explicitly authorized resumed work, an interruption/error projection, omission of an incomplete specified branch under the documented compatibility rule, or a documented irrecoverable error. Recovery never infers success from a later filesystem state.

`ARCH-LIFE-003` — A provider-free session-management mode branches before semantic session initialization. It derives the workspace partition only from the frozen application-home capability and normalized absolute workspace, shares one continuity authority with resume, continue, fork, and explicit creation, and exposes only bounded versioned inventory or deletion outcomes. A caller cannot supply an application-home path, workspace key, session path, or transcript path.

`ARCH-LIFE-004` — Attachment admission is a transaction over references, not
over provider transport. Import may commit immutable session-owned blobs before
the user message is admitted, but every reference in one message must resolve
and match its manifest before queue insertion or active-turn interruption.
Failed/unsubmitted imports are collected as orphans; an accepted message is
durable before provider access.

## Concurrency and ordering

- Preserve input order within a session unless a documented priority preempts active work.
- Serialize unsafe capabilities. Safe capabilities may overlap within declared barriers.
- Publish progress opportunistically, but publish terminal tool results in the order required to maintain valid message pairing and context modification.
- Use one writer or an ordered append queue per durable log.
- Bound or backpressure every producer where the owning adapter supports it: model deltas, process output, remote uploads, task files, and UI rendering. Where a source-compatible adapter writes without backpressure or closes with a memory-only queue, name that loss window and never reinterpret queue admission as durable delivery.
- Cancellation flows from owner to children. Child failure reaches parents only according to the owning protocol.
- Await or synthesize terminal evidence before releasing a turn for every accepted tool-use identifier and finite turn-owned operation. Work deliberately handed off to a longer-lived task may outlive that turn only after its stable identity, owner, lifecycle/cancellation path, output location or retrieval path, and completion-notification contract are registered. Process death and named lossy transport windows may erase process-local waiters/queues; recovery classifies only what durable evidence supports and never fabricates a terminal callback.

`ARCH-ORDER-001` — Registry merging is deterministic. Each registry declares source precedence, canonical-name and alias collision policy, stable ordering, filtering, and provenance.

`ARCH-ORDER-002` — Async callbacks apply patches to fresh state or use compare-and-swap/version checks. They never overwrite a newer snapshot by spreading stale pre-await state.

## Failure containment

| Failure | Default containment |
| --- | --- |
| Invalid configuration | reject initialization before partially trusted execution |
| Authentication unavailable | block dependent remote/model operation; allow unrelated local inspection where valid |
| Model request failure | bounded retry/fallback; preserve accepted input and partial evidence |
| One tool failure | explicit error result; siblings continue unless the scheduler contract cancels them |
| Permission denial | terminal denial result with provenance; no side effect |
| Extension failure | disable or mark that entry; preserve other registries |
| Telemetry/update failure | log locally if safe; never fail semantic work |
| Remote disconnect | reconnect/replay within bounds or surface explicit terminal status |
| Persistence failure | surface loss-of-durability; never claim resumability |
| Native session cleanup failure | retain validated deletion staging and return an explicit cleanup-pending outcome; never report hidden retained data as deleted |
| Shutdown interruption | best-effort bounded flush followed by idempotent cleanup |

All retries define eligible errors, maximum attempts or bounded persistent mode, delay and jitter, reset rules, user-visible progress, abort behavior, and idempotency assumptions.

## Security boundary

`ARCH-SEC-001` — Treat model, user attachments, repository contents, hooks, plugins, MCP/LSP servers, remote peers, and restored transcripts as untrusted inputs.

`ARCH-SEC-002` — Validate the model-originated/evaluated input's syntax and semantic preconditions before permission, and evaluate permission before side effects. Run lifecycle hooks at their specified points without granting them undeclared authority. The specified edited-approval compatibility path in `ARCH-013`/`PERM-042` is deliberately one-shot: after a winning approver selects a replacement input object, that selected object reaches execution without a second schema, semantic, permission, safety, classifier, sandbox, or prompt pass. An implementation that closes this gap must label the bounded reauthorization as an intentional hardening divergence rather than silently changing the reference contract.

`ARCH-SEC-003` — Resolve paths against explicit roots, analyze shell structure rather than raw prefixes alone, protect sensitive internal locations, reject dangerous broad deletion targets, and fail closed when sandbox guarantees are required but unavailable.

`ARCH-SEC-004` — Credentials remain behind the authentication/network port. Redact them from prompts, logs, telemetry, process listings, environment forwarding, and persisted records.

`ARCH-SEC-005` — Native session inventory and deletion accept only a normalized workspace, a grammar-valid opaque session identifier, and runtime-issued opaque continuation or revision tokens. Enumeration fails closed on a safe-looking unsafe identity. Deletion revalidates workspace parent, target, transcript, lock, and token identity at the mutation boundary; it holds the existing nonblocking session lock through a same-parent atomic detach into a reserved non-session name, then releases the lock before descriptor-rooted recursive cleanup.

`ARCH-SEC-006` — Attachment content is untrusted model input, never a tool,
filesystem, command, or instruction grant. Reject unsupported media and unsafe
source identity before commit. Neither public events nor diagnostics may expose
attachment bytes, base64, provider request bodies, original paths, or
runtime-owned paths.

## Portability rules

- Represent schemas using language-neutral tagged records and validation predicates.
- Represent cancellation with an owned propagation token or equivalent, not a language-specific exception convention.
- Represent async streams with a pullable or backpressured event sequence.
- Replace UI frameworks freely while preserving focus, keyboard, rendering, and timing behavior.
- Replace filesystem and process libraries freely while preserving atomicity, permissions, symlink handling, output bounds, exit interpretation, and signal propagation.
- Replace prompt implementation details freely only when section order, content visibility, cache boundaries, and dynamic invalidation stay observable-equivalent.

## Cross-domain acceptance scenarios

### `ARCH-A01` — Crash during a write tool

Persist the user message and assistant tool use. If the process dies after permission but before a result is appended, the specified runtime has no transaction marker proving whether the write committed. It never reruns the mutation or claims success. The reference-compatible projection either removes a fully unresolved assistant tool-use record from the resumed live chain or injects an explicitly synthetic error for an unresolved member of a partially resolved group; strict pairing mode fails instead. Raw on-disk evidence remains available, and the UI must not imply success or failure of the external effect without evidence.

### `ARCH-A02` — Parallel safe reads around an unsafe edit

Two adjacent safe reads may overlap and publish their terminal results in completion order. Each result retains its tool-use identifier and source-assistant parent, while context modifiers are applied in accepted request order after the safe group completes. The edit is a serialization barrier, and later reads do not begin until it finishes. Every accepted ID is paired exactly once despite the permitted result interleaving.

### `ARCH-A03` — Headless permission race

A policy result requires approval. A `PermissionRequest` hook and SDK host are asked concurrently; `PreToolUse` has already completed. In this trace the decisive hook denial wins, cancels the host request, emits one denial result, returns session state from requires-action to running when no other prompt remains, and ignores a late duplicate host response.

### `ARCH-A04` — Optional integration failure

An MCP server, IDE, telemetry sink, update service, and notification service all fail while a local edit turn runs. Their entries show unavailable/failed state and clean up owned resources. Local tools, transcript, permission decisions, and final result remain coherent.

### `ARCH-A05` — Context pressure with active background work

Compaction transforms only the foreground model projection, preserves authoritative transcript history, retains required task identities and invoked-skill context, does not clear subagent-owned or main-thread-owned caches incorrectly, and resumes without duplicating background completion notices.

### `ARCH-A06` — Remote reconnect and replay

Disconnect at each remote delivery milestone: observed frame identifier, parsed envelope, local dispatch, `received`, `processed`, transport-queue admission, and server acknowledgement. Reconnect starts from only the cursor and identity state that the selected adapter actually retains. The reference SSE cursor is an in-process observed-frame high-water mark, not a durable processing checkpoint, and one bridge adapter reports `received` and `processed` immediately after its local callback; a crash can therefore lose locally uncommitted work that the server considers processed. Deduplicate where identities remain available, never repeat a tool side effect merely to fill missing evidence, and expose loss, fresh-session, or stale-epoch outcomes instead of claiming universal replay recovery. An implementation may add a durable processing cursor or control-generation fence only as a documented safer divergence.

### `ARCH-A07` — Crash-safe native session deletion

List two workspaces that contain the same session ID and obtain distinct runtime-issued revisions. Deletion of one revision acquires that workspace's existing session lock, persists matching intent, revalidates the workspace parent, target, transcript, lock, and revision, and atomically detaches only that target to a reserved invalid-session staging name. Resume, continue, fork, ordinary inventory, and explicit recreation cannot select either live intent or detached staging. A crash or cleanup failure returns `delete_incomplete`; retry by the original session ID and revision validates the bounded staging record and completes descriptor-rooted removal. `deleted` is returned only after the detached owned directory is absent. The other workspace, fork descendants, project memory, worktrees, configuration, authentication, backups, remote copies, and presentation caches are unchanged.

### `ARCH-A08` — Native attachment end-to-end continuity

Negotiate one qualified surface, import ordered PNG, JPEG, and conservative PDF
content through explicit paths and correlated stream uploads, and bind every
committed reference to one stable prompt UUID. Fully validate and reserve the
typed set before a priority-`now` interruption, append the manifest-only user
event before provider access, and materialize bytes only in the qualified
provider adapter. Verify exact provider order, one byte-identical bounded
payload across transport retries, and one terminal turn outcome. Compact,
resume after removing the source paths, and fork while the source is locked;
the transcript retains every manifest and each destination owns independently
verified immutable blobs. Induce missing/tampered durable media and a closed
provider media rejection: local damage fails before transport, while the
rejected projected attachment set is durably quarantined and never
automatically resent. Throughout import, queueing, provider failure,
observation, replay, shutdown, orphan collection, and native session deletion,
emit no bytes, base64, source/runtime paths, or provider body, and never treat
attachment content as permission or instruction authority.
