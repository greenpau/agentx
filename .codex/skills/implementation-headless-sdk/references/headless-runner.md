# Headless Runner State Machine

## Contents

1. [State and concurrency model](#state-and-concurrency-model)
2. [Initialization](#initialization)
3. [Input reader](#input-reader)
4. [Serialized run loop](#serialized-run-loop)
5. [Queue and batching](#queue-and-batching)
6. [Tasks, result holdback, and idle](#tasks-result-holdback-and-idle)
7. [EOF, teams, and shutdown](#eof-teams-and-shutdown)
8. [Persistence and replay classes](#persistence-and-replay-classes)
9. [Flush domains](#flush-domains)
10. [Failure and cancellation](#failure-and-cancellation)
11. [Acceptance scenarios](#acceptance-scenarios)
12. [Non-normative provenance](#non-normative-provenance)

## State and concurrency model

Run one concurrent input reader and one serialized semantic runner.

Core state:

- initialization state: not initialized, initializing, initialized, failed
- `running` mutex/flag and diagnostic phase
- input-open/input-closed
- abort controller for the active query
- priority command queue
- transcript and runtime UUID deduplication sets
- pending control and permission requests
- held-back terminal result
- pending prompt suggestion
- finite background tasks, long-lived teammates and task notifications
- model/tool/MCP/plugin/session settings refreshed at turn boundaries
- last retained ordinary result, which determines the eventual normal EOF exit status

- **RUN-001 — Reader responsiveness.** The reader remains able to process interrupt, permission and callback control messages while a query is streaming.
- **RUN-002 — Single semantic turn.** Only one invocation may dequeue and run semantic work at a time.
- **RUN-003 — Race-closing recheck.** After releasing the run mutex, immediately recheck the queue because an enqueue may have observed `running=true` just before release.
- **RUN-004 — Proactive yield.** Before a proactive/timer tick claims an idle runner, give already-readable external input one scheduling opportunity. If stdin/control input is ready, enqueue it ahead of timer-generated work; the contract is this priority relationship, not a particular event-loop or zero-delay-timer primitive.

## Initialization

Explicit `initialize` is allowed once. The first user record implicitly initializes if no explicit initialize arrived.

Initialization may configure:

- hooks and callback routing
- SDK-owned MCP server names/configuration
- structured-output schema
- replacement and appended system prompts
- agent definitions
- prompt suggestions and agent-progress summaries

Return an initialization inventory containing commands, agents, output style and available styles, models, account information, optional process ID, and optional fast-mode state.

- **RUN-005 — Initialize-before-drain.** Apply explicit initialize configuration before prequeued auto-resume or user work drains.
- **RUN-006 — Duplicate initialize.** Reject a second initialize with a correlated control error that also reports any still-pending permission requests needed for recovery.
- **RUN-007 — Implicit parity.** Implicit initialization uses defaults and emits the same session initialization event/inventory required by the protocol.

## Input reader

The reader processes records in arrival order:

- Control requests execute through the control dispatcher.
- Control responses settle pending outbound requests.
- User records enter the priority queue and trigger the runner.
- Replayed assistant/system records become prior model/session context; assistant echo follows the replay-output policy.
- Keepalive records do nothing semantically.
- Environment updates apply immediately to the process environment after validation.

User UUID handling:

- If a UUID already exists in restored transcript or the current process, do not execute it again.
- Emit replay acknowledgement when configured.
- Complete any associated command lifecycle as historical/duplicate.
- Record a new UUID before execution can race with another copy.

- **RUN-008 — Record ordering.** Prepended/internal user records are inserted before the next external record and rechecked between records from the same input chunk.
- **RUN-009 — Replay boundary.** Replayed messages implement context but cannot silently execute their old tools or commands.
- **RUN-025 — Reader callback isolation.** A local duplex reader must either be a finite in-memory source or expose `Close` as its interruption contract. Run callback-owned `Read` and `Close` outside the decoder and protocol-pump join path. Do not format, unwrap, or classify a reader-owned error: exact EOF is the only distinguished callback result, and every other error or callback panic becomes one fixed input failure. Discard bytes returned alongside a non-EOF error so an already-observed source failure cannot race semantic admission. Cancellation closes the owned pipe, decoder queue, and pump even when a broken callback panics or ignores `Close`; such a callback may be abandoned without session references rather than keeping structured shutdown open.

## Serialized run loop

Runner sequence:

1. Acquire the run mutex or return if another runner owns it.
2. Publish `running` session state.
3. Refresh dynamic SDK MCP configuration and session components.
4. Await configured synchronous plugin installation, optionally bounded by its configured timeout.
5. Select and remove the next workload.
6. Recompute current tools and connected clients so late arrivals are visible.
7. Run the shared query stream and enqueue normalized output events.
8. Hold the terminal result when finite background work remains.
9. Poll/drain background notifications back into model context where required.
10. Emit held result only after finite work finishes.
11. Emit pending prompt suggestion after the result.
12. Flush the active adapter's remote internal/resume-event queue, if it has one.
13. Publish authoritative `idle` after result and task terminal events are queued.
14. Release the mutex and recheck the queue.

- **RUN-010 — Fresh registry.** Tool/client snapshots are per turn, not per process.
- **RUN-011 — Terminal result identity.** Lifecycle events emitted after semantic result creation cannot replace which object aggregate/text mode treats as the result.
- **RUN-012 — Error terminality.** An uncaught runner failure emits a structured execution-error result and initiates status 1 shutdown.

## Queue and batching

Priority is `now`, `next`, `later`, preserving FIFO order within a priority.

- `now` may abort active work.
- Slash/local and shell commands execute separately.
- Ordinary prompt commands may batch only when they are compatible model workloads, share the same mode, and are either all metacommands under the allowed rule or all ordinary prompts.
- Every original UUID receives its own replay/acceptance acknowledgement even if semantic messages are merged into one workload.
- Task notification prompts remain separate from ordinary human prompts.

- **RUN-013 — Main-thread filtering.** The headless main runner dequeues only commands owned by the main session; task/subagent queues use their own lifecycle.
- **RUN-014 — Cancellation window.** `cancel_async_message` succeeds only while the target UUID is still queued. After dequeue it returns `cancelled=false` and does not interrupt unless a separate interrupt is requested.

## Tasks, result holdback, and idle

Finite local agents and workflows may outlive the model stream that launched them.

- Poll finite background work every 100 ms while holding a result.
- Exclude deliberately long-lived teammates from result holdback.
- Drain task notifications into normalized events and, where the session contract requires, into a continuation prompt for the model.
- Emit task progress before terminal task notification.
- Emit held result after all finite background work is terminal.
- Wait up to 5,000 ms for an in-flight prompt suggestion before final stream shutdown; suggestion absence is not an error.
- Publish `idle` only after the held result is enqueued and the adapter's distinct remote internal/resume-event flush has completed.

Observable order:

```text
running
assistant/tool/hook stream
task_started and task_progress
task_notification(s)
result
prompt_suggestion (if available)
remote internal/resume-event flush
idle
```

- **RUN-015 — Requires-action accuracy.** Publish `requires_action` while one or more host decisions are pending. Return to `running` only when the last concurrent permission request settles.
- **RUN-016 — No early idle.** A created result object is not proof that the turn is over; held tasks and ordered output must finish first.

## EOF, teams, and shutdown

On input EOF:

1. Parse and dispatch a nonempty unterminated tail as one final record.
2. Mark input closed and reject only control waiters still pending so new outbound requests fail immediately.
3. Finish the current queued/running workload.
4. If an active team remains, wait without a deadline. A team-lead path polls active task/roster state and the lead mailbox every 500 ms, injects the shutdown prompt at most once through its process-local latch, processes correlated approvals, and continues polling until no teammate remains. Before that path, waiting for already-working in-process teammates to become idle is callback-based and also has no deadline.
5. Await a pending suggestion for at most 5,000 ms.
6. Unsubscribe auth/rate-limit/settings/task listeners and finalize hooks.
7. End the output adapter and run cleanup under the adapter-specific flush guarantees described below.

- **RUN-017 — EOF settlement.** Parse and dispatch any final unterminated input record first; then mark input closed and reject only control requests still pending with an input-stream-closed error.
- **RUN-018 — Team close gate and unbounded compatibility wait.** Do not close while a team remains registered. The specified EOF path has no maximum wait, maximum poll count, response deadline, or automatic force-kill escalation: a silent, rejecting, non-idling, or stale registered teammate can keep stdout/output open indefinitely. The lead polls every 500 ms and uses one process-local `shutdownPromptInjected` latch to avoid duplicate prompt injection in the normal lead branch. A process signal or external shutdown may separately enter global cleanup, which best-effort kills owned pane workers before deleting team data; that is not a deadline escalation from this EOF loop. A port that adds a close deadline must expose it as an intentional behavioral divergence and define the resulting terminal status and cleanup evidence.
- **RUN-019 — Long-lived exclusion.** Perpetual teammate/session tasks are handled by explicit shutdown policy, not ordinary finite result holdback.

### Persistence and replay classes

Do not infer durability from an SDK event's presence on the wire. Preserve these independent storage classes:

| Information | Live authority | Durable/restorable form | SDK projection |
| --- | --- | --- | --- |
| `running`, `idle`, `requires_action` | process-memory session state | not the semantic transcript; CCR v2 may separately store remote worker/external state and pending-action metadata | bounded, ephemeral state event when enabled |
| Permission request, response, cancellation | correlated in-process waiters | none in the semantic transcript; CCR v2 may separately retain pending-action worker/external metadata | control request, response, or cancel record |
| Permission mode | session application state | CCR v2 external metadata may restore it | status/init projection where enabled |
| Authorized always-allow update | effective permission settings | settings storage only when the chosen target is editable and the update is authorized | part of the permission response, not a transcript event by itself |
| Permission denial | permission pipeline plus tool result | the semantic error tool result is transcript-visible when persistence is enabled | request/response traffic and derived final denial summary are ephemeral |
| Initialization and MCP status | current registry/application state | connection configuration or enablement may persist through its own settings contract; the projection does not | ephemeral inventory/status projection |
| MCP connection state | application state | configuration enablement may persist separately | current-status event only |
| Task started/progress/notification | bounded SDK event queue | emission alone is not transcript persistence | ephemeral lifecycle events |
| Task authority and output | task application state and output file | task-owned output plus selected remote sidecars | lifecycle projection |
| Model-facing task notification | semantic internal user message once processed | semantic transcript when persistence is enabled | may also produce task lifecycle events |
| Direct foreground task terminal event | task/event path | not transcript merely because the SDK event was emitted | ephemeral terminal task event |
| Accepted provider usage | provider event plus engine accounting | model-hidden `usage` event carrying the owning turn ID when persistence is enabled and append succeeds | normalized usage fields where the selected output contract exposes them |
| Terminal `turn_result` | engine terminal state after semantic finalization | exactly one model-hidden `turn_result` event per finalized model-backed turn when persistence is enabled and append plus flush succeed | source evidence for, but not identical to, the ephemeral final `result` |
| Final `result` | derived from the turn's terminal semantic state | never a transcript record itself | ephemeral terminal projection |
| Underlying user, assistant, and tool-result messages | conversation history | semantic transcript when enabled | normalized user/assistant events |

- **RUN-022 — Storage-class independence.** Optional state-event emission, task support, or MCP support does not change an event's storage class. With local persistence disabled, no local semantic transcript is required. CCR v2 adds remote internal transcript events and restorable external metadata; it does not make every visible SDK event durable.
- **RUN-024 — Replay by owning class.** Restore semantic user, assistant, and tool-result messages from the semantic transcript. Restore durable usage and terminal `turn_result` events only as model-hidden accounting and terminal evidence; they neither enter model context nor recreate a visible SDK `result`. Inbound bridge replay of assistant/system records appends them to in-memory context without re-recording them through the input path; echo only assistant records and only when replay output is enabled. A duplicate user UUID is acknowledged as replay when configured and is never executed again. Do not implement final results, control traffic, state events, or raw task lifecycle projections from the semantic transcript; restore their owning state or task store when that contract supports it.

### Flush domains

Treat these operations as four separate milestones:

1. `recordTranscript` submission places semantic records into an ordered write path. Awaiting submission does not necessarily mean bytes reached disk; the observed writer may defer serialization/write for approximately 100 ms locally or 10 ms in remote mode.
2. A local session-storage flush drains the JSONL transcript queue. Eager-flush and cowork profiles await it before terminal result emission; ordinary profiles need not. Graceful shutdown attempts this local drain first, bounded by its shutdown deadline, before later cleanup.
3. A remote internal-event flush drains CCR resume/internal events. It is a no-op for stdio and does not drain visible client events.
4. A visible-output drain concerns stdout or the selected remote client transport. Local stdout writes ignore backpressure. A hybrid transport awaits its uploader for nonstream records, while CCR v2 queues visible client events without a per-turn client-event flush; closing can abandon those queues.

- **RUN-023 — No universal visible acknowledgement.** The runner enqueues the final result, flushes remote internal/resume events, then enqueues `idle`, preserving FIFO result-before-idle. This sequence does not prove that a remote client durably received either record. Do not advertise result, idle, or shutdown as a universal visible-delivery acknowledgement.

## Failure and cancellation

- SIGINT in print/headless mode aborts the active query and requests graceful shutdown with status 0. It may complete shutdown without waiting for an ordinary terminal result.
- Interrupt control immediately returns correlated success and leaves every accepted tool-use identifier with an explicit terminal result. It does not exit the process.
- Permission/control errors deny or cancel safely; they never imply approval.
- Output write failure makes the protocol unavailable, aborts work and performs cleanup; do not continue producing unobservable side effects indefinitely.
- Plugin synchronization timeout logs/reports the timeout and continues with the prior available plugin set when policy permits.
- Task failure is a task terminal event and may still allow the parent semantic result to report success or error according to session continuation policy.

- **RUN-020 — Two-phase SDK interrupt.** On an SDK `interrupt`, abort the active turn controller if present, abort and clear prompt-suggestion work, and enqueue a success control response immediately. If a semantic turn was active, its ordinary terminal projection arrives later as `result/error_during_execution` with `is_error=true` and the normal accounting, usage, denial, diagnostic, identity, and optional fast-mode fields. There is no `cancelled` result subtype and no dedicated cancellation-reason field.
- **RUN-021 — Exit and interruption distinctions.** Preserve the following outcomes exactly:

  | Trigger | Immediate response | Semantic result | Process effect |
  | --- | --- | --- | --- |
  | SDK control `interrupt` | correlated success with absent operation payload | active turn later emits `error_during_execution`; idle interrupt emits no turn result | remains alive for later input |
  | Input closes after SDK interrupt | none beyond EOF processing | no new result; use the last retained ordinary result | status 1 if interrupted error remains last; a later successful turn makes normal EOF status 0 |
  | Print-mode SIGINT | no SDK control response | ordinary result is not guaranteed before cleanup | abort plus graceful shutdown status 0 |
  | `cancel_async_message` | correlated success `{cancelled: boolean}` | none | removes only a still-queued UUID; does not interrupt or exit |
  | Submit-interrupt user workload | no control acknowledgement; the new workload remains queued | suppress synthetic interruption text; if only tool-result blocks remain, the interrupted turn may yield an empty successful result with `is_error=false` before the queued prompt runs | process remains alive and serializes the queued turn next |

When a hard SDK interrupt races a pending host permission, synchronous abort listeners enqueue one cancellation record per pending permission before the interrupt success response enters the same FIFO. The permission path then fails closed and pairs every accepted tool ID. Finite background tasks may delay the ordinary result; long-lived teammates do not.

## Acceptance scenarios

1. Send two user records and an interrupt while the first streams; verify the reader handles interrupt immediately and the second runs only after serialization.
2. Queue user input before explicit initialize; verify initialize configuration applies before the input drains.
3. Send initialize twice; verify one success and one correlated error with no duplicate session initialization.
4. Replay a previously executed user UUID; verify acknowledgement without query/tool execution.
5. Connect a tool provider between turns; verify the second turn sees it.
6. Launch a finite background task; verify progress and notification precede the held result, then idle follows.
7. Launch a perpetual teammate; verify ordinary result is not held forever, but EOF enters the team close gate. Withhold idle/shutdown completion and verify 500 ms polling continues with no built-in deadline or repeated normal-lead shutdown prompt; then deliver correlated completion and verify close proceeds. Separately verify a process signal uses global cleanup rather than masquerading as an EOF timeout.
8. Enqueue during the mutex-release boundary; verify the post-release recheck runs it without another external wakeup.
9. Close stdin with a pending permission request; verify the request rejects and the tool receives denial/cancellation.
10. Delay prompt suggestion beyond five seconds at shutdown; verify clean close without suggestion.
11. Interrupt an active SDK turn; verify immediate correlated success, then an ordinary `error_during_execution` result with no cancelled subtype, and verify the process accepts a later turn.
12. Let the interrupted result remain last and close input; verify status 1. Repeat with a successful later turn and verify normal EOF status follows that later result.
13. Send print-mode SIGINT during a turn; verify the active scope aborts and graceful shutdown requests status 0 without requiring an ordinary result first.
14. Cancel one queued UUID and one already-dequeued UUID; verify `{cancelled:true}` then `{cancelled:false}`, no semantic cancellation result, and no active-turn interruption.
15. Submit a priority-now prompt during tools; verify submit-interrupt semantics, no synthetic interruption text, and the queued prompt runs after the current turn's pairing/result.
16. Emit a result through CCR v2, then close before the visible client-event uploader drains; verify transcript/internal-event flush and FIFO ordering do not masquerade as durable client delivery.
17. Replay assistant, system and duplicate user records; verify all rebuild context as applicable, only the assistant/user acknowledgement is echoed under replay mode, no old tool executes, and ephemeral result/control/state events are not invented from transcript history.
18. Return an uncomparable reader error whose `Error`, `Is`, and `Unwrap` methods panic, panic from `Read` and `Close`, and separately block cooperative and noncooperative reader callbacks. Verify the raw error methods are never invoked, an initialized stream emits exactly one fixed terminal input-error result, cancellation closes the decoder queue and protocol pump, and shutdown does not wait behind a broken callback.
19. Run the same persistent successful model-backed turn at INFO and DEBUG through text, aggregate JSON, and stream JSON. Normalize IDs and timestamps; stdout remains semantically identical and protocol-clean, while diagnostics use stderr only. The transcript contains the accepted user message, completed provider usage, and exactly one model-hidden terminal `turn_result` at both levels. The visible final `result` remains an ephemeral projection rather than a duplicate transcript record.

## Non-normative provenance

Evidence was specified from the reference headless print runner, command queue, structured I/O adapter, task/team integration, prompt-suggestion service, plugin synchronization and state subscriptions. Names and paths are non-normative.
