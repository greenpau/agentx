# Task Runtime Implementation Contract

This document defines asynchronous work independently of the tool that launched it or the surface that displays it. Durability is stated per artifact: a task may outlive a model call without its live state or completion notification surviving a process crash.

## Contents

- [Task model and registry](#task-model-and-registry)
- [Durability classes and crash boundary](#durability-classes-and-crash-boundary)
- [Lifecycle and state updates](#lifecycle-and-state-updates)
- [Output storage and security](#output-storage-and-security)
- [Polling, notification, and eviction](#polling-notification-and-eviction)
- [Concrete task families](#concrete-task-families)
- [Cancellation, shutdown, and disabled behavior](#cancellation-shutdown-and-disabled-behavior)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Task model and registry

**TR-001 — Task meaning.** A task is work with identity and lifecycle that can outlive the initiating model call. A task is not the initiating tool request, its tool result, a process handle, or a UI row, even when one feature uses all of them.

**TR-002 — Common state.** Every task state contains:

- stable `id` and `type`;
- `status`;
- human description and optional initiating tool-use ID;
- start time and optional end time;
- optional accumulated paused duration;
- output-file path and last delivered byte offset;
- `notified`, a live-state latch recording that one completion-notification path has claimed this task in the current state generation.

Concrete task state may additionally carry abort controllers, process/remote IDs, messages, progress, result/error, foreground/background state, owner agent, retention, and cleanup operations.

**TR-003 — Types.** Support the registered task types that are included in the build: local shell, local agent, remote agent, in-process teammate, local workflow, MCP monitor, and memory-consolidation/dream. Treat missing feature-gated types as unavailable registry entries, not failed tasks.

**TR-004 — Status machine.** Use:

```text
pending -> running -> completed
                   -> failed
                   -> killed
```

`completed`, `failed`, and `killed` are terminal. A terminal task never returns to running; resumption creates or replaces a running task under explicit resume semantics.

**TR-005 — Identifier format.** Generate a type prefix plus eight cryptographically random characters from lowercase base-36. Observed prefixes are `b` local shell, `a` local agent, `r` remote agent, `t` teammate, `w` workflow, `m` monitor, and `d` dream. The roughly 36^8 space also makes precreated symlink targeting impractical.

**TR-006 — Registry dispatch.** A task registry resolves type to lifecycle implementation, at minimum kill. Shell, local agent, remote agent, and dream implementations are ordinarily registered; workflow and monitor registration depends on build inclusion.

## Durability classes and crash boundary

**TR-007 — Artifact lifetimes.** Keep these stores distinct:

| Artifact | Normal owner and lifetime | Crash/restart meaning |
| --- | --- | --- |
| application task map, status, offsets, handles, and `notified` | live process/application state | ordinarily lost; it is not a general task journal |
| task output file or agent-transcript symlink | project-temporary file storage | may remain as partial inspectable evidence; it does not prove terminal status or notification delivery |
| model-facing completion queue | process-global in-memory queue | lost on process exit; diagnostic queue-operation transcript records are not replayed into it |
| ordinary conversation or child sidechain transcript | transcript persistence contract | recoverable according to transcript rules, not because it is task state |
| remote-task sidecar | best-effort per-session metadata file | a recovery hint for a still-live remote session, not an atomic task/notification record |

**TR-008 — No inferred durability.** Do not infer a terminal task state from the existence or final bytes of an output file. Do not infer that a completion was consumed from `notified`, a queue-operation diagnostic, a removed sidecar, or a terminal remote response. Each is evidence for only its own contract.

**TR-009 — Safer divergence.** An implementation may deliberately add a transactional task journal, durable outbox, delivery identifier, and consumption acknowledgement. Record that as a compatibility-strengthening divergence. It must not be described as behavior of the documented runtime, whose state transition, queue enqueue, transcript append, and sidecar mutation are separate operations.

## Lifecycle and state updates

**TR-010 — Registration.** Register a task by immutable insertion into the live application task map and emit one semantic `task_started` event with ID, tool-use ID, description, type, optional workflow name, and optional prompt. “One” applies while the ID is already present in that live map; it is not a persisted start-event ledger.

**TR-011 — Live replacement.** Re-registering an ID already present in the current application task map is replacement, not a new start. Preserve UI-held retention, original start time, already displayed messages, disk-loaded state, and queued user messages when those fields exist. Do not emit a second start event. A fresh process normally has no such map entry; remote-sidecar implementation therefore registers a fresh live task and may emit another `task_started` event for the same stable task ID.

**TR-012 — Typed update.** A task update reads the current task from the newest application snapshot. If absent or the updater returns the same object, publish no state change. Never apply a stale whole-task snapshot after an asynchronous read.

**TR-013 — Completion race guard.** Each concrete completion/failure callback first verifies the current task is still running. If kill already won, completion must not overwrite `killed`, enqueue another notification, or restore the abort/process handle.

**TR-014 — Foreground/background.** Foreground is a task presentation/execution mode, not a separate task identity. Backgrounding an existing foreground shell or agent retains ID, start time, output, process/query, and callbacks and flips `isBackgrounded` atomically. Unregistering an unbackgrounded foreground task removes it and releases its resources.

**TR-015 — Background visibility.** A task appears in the general background indicator only while pending/running and not explicitly marked foreground. Terminal history may remain in a specialized panel during a grace/retain interval.

## Output storage and security

**TR-020 — Output directory identity.** Place output in a project-temporary session directory ending in `tasks`. Capture that directory at the first task-output use. A later session clear/ID regeneration must not strand files for tasks that survived the clear.

**TR-021 — Output filename.** Derive the filename only from the generated task ID and a fixed extension. Do not accept a caller-provided path.

**TR-022 — Secure creation.** Create a new output file exclusively and reject an existing path. On systems supporting it, prohibit symlink following when opening. Create parent directories recursively. Platform-specific absence of no-follow support may be tolerated only where the sandbox attack vector is absent or separately mitigated.

**TR-023 — Ordered writer.** Maintain a flat string/chunk queue per task and one active ordered asynchronous writer. Combine the currently queued chunks into one byte buffer for each write so completed chunks can be reclaimed immediately; do not retain an ever-growing dependency history of prior write operations. Close the file handle after the queue drains, then recheck for data appended during close.

**TR-024 — Disk cap.** Cap output at 5 GiB. Sanitize first, then apply the cap to the projected stream. Once crossed, ignore later chunks after appending one set-safe terminal marker; if no such marker exists, append nothing. File-backed process modes may enforce the same cap with a size watchdog; pipe-backed modes enforce it in the output queue.

**TR-025 — Write failure.** Log a drain failure and retry once when queued data remains. Resolve flush waiters even after the retry fails so shutdown cannot hang forever. Record `output_incomplete=true` plus one bounded credential-safe presentation warning independently from status, exit code, and process error; never copy the external write/sanitizer error payload. A failed output write affects inspectability, not the already-determined process result. These omitted JSON fields are additive to the current state version: older records decode as false/empty without migration.

**TR-094 — Credential-safe durable output.** When the session owns credential material, sanitize the durable/display command and description before creating any task artifact while retaining the raw authorized command only in process-local launch state. Filter each task's ordered stdout/stderr stream before any bytes enter its output file. Use fresh per-task state and retain the minimum bounded suffix that could complete a configured literal in a later chunk, including across stdout/stderr write boundaries. Flush only the final safe suffix before syncing and closing the file. Apply disk caps and poll offsets to the sanitized byte stream. Before atomically replacing task state, validate a private copy of the complete encoded JSON document against the bounded session/provider union so structural framing cannot reconstruct a literal across individually safe fields and a hostile validator cannot mutate live state. Use the same panic-isolated fixed-failure helper for startup, preflight, and persistence. This final seam rejects but never rewrites the in-memory task/work/todo identities or their durable representation; terminal quarantine retains stable task/tool/owner correlation and the last validated durable record. A nonempty union without a safe streaming terminal marker is a session/task-manager construction error; it cannot create pre-closed per-task sanitizers that discard safe output while preserving successful task status. Sanitizer creation, record projection, whole-state validation, or stream processing failure fails closed: do not create or replace a task artifact, persist raw pending bytes, expose panic payloads, lose the accepted task identity, or skip bounded process reap, output close, mutex release, and completion notification. A profile with no session credential material may use the identity transform.

**TR-095 — Opaque callback failures.** Persistence, signal, task, and completion callback failures return through fixed host-owned projections. Classify them only from exact sentinels, task-owned context state, detached values, and package-sealed snapshots; never invoke callback-owned `Error`, `Is`, `As`, or `Unwrap` behavior. Unknown failures receive fixed diagnostics, and a blocking error method cannot delay stop, close, notification, or durable cleanup.

**TR-096 — Reentrant host-callback liveness.** Bracket manager-owned clock, identifier randomness, whole-state validation, persistence fault seam, record projection, command construction, and output-sanitizer factory/marker/write/flush callbacks with one manager-scoped callback claim whenever they can execute inside a serialized state transition or while the ordered output lock is held. Every error-returning public manager entrypoint, including checked context-bearing snapshots, checks that claim before acquiring a task lock or invoking another host callback and returns the exact task-busy classification while it is active. A checked snapshot abandons contended lock acquisition when its context is cancelled and returns no partial snapshot. Legacy snapshot methods that cannot report an error retain their compatibility shape and return an empty snapshot during the claim. Panic, rejection, or ordinary return always releases the claim, and nested clock acquisition degrades to the trusted wall clock. This fail-fast boundary preserves the atomic visibility of the state transition and prevents a callback from deadlocking the manager, stalling its own output close, or recursively invoking its own error sanitizer.

**TR-097 — Opaque incidental formatting.** Incidental formatting of task
records, work items and patches, todos, poll results, launch options, shell
specifications, and managers reports only a fixed type/shape marker. Commands,
output, descriptions, paths, metadata, and error fields remain available only
through deliberate task protocol, persistence, and output-retrieval channels;
`%v`, `%+v`, `%#v`, `%s`, and `%q` are not alternate task-output surfaces.

**TR-039 — Crash evidence.** Output writes are asynchronous and are not an fsync-backed task journal. After a crash, a file may be absent, truncated, or contain only a prefix even when the underlying work emitted more data. Preserve it for inspection, but require independent state or remote evidence before claiming completion.

**TR-026 — Flush and eviction.** `flush` waits for the current drain. In-memory eviction waits for flush and retains the disk file. Cleanup cancels queued chunks, removes the in-memory writer, and deletes the file; missing files are a successful cleanup.

**TR-027 — Delta read.** Read output from a byte offset, not a character index, with an 8 MiB default maximum. Return content plus `old offset + actual bytes read`. Missing/unreadable output returns empty content and preserves the offset; non-missing errors are logged.

**TR-028 — Tail read.** Full-output inspection reads only the last 8 MiB by default. If earlier bytes exist, prepend a notice stating approximately how much output was omitted.

**TR-029 — Symlink output.** An agent task may expose another durable transcript as its output through a symlink. Attempt symlink replacement, but if creation fails, log and fall back to an ordinary exclusively created output file.

## Polling, notification, and eviction

**TR-030 — Poll interval.** The generic task framework polls every 1,000 milliseconds while active.

**TR-031 — Poll responsibility.** Generic polling reads deltas for running tasks and computes offset patches. Concrete task implementations own terminal notification. The generic framework must not independently notify terminal tasks because that races concrete callbacks.

**TR-032 — Fresh patch application.** After asynchronous output reads, merge only byte-offset patches against the newest task map and only if each task remains running. Recheck terminal/notified state before eviction. This prevents a stale read from resurrecting or de-notifying a completed task.

**TR-033 — Same-generation notification latch.** Against the newest live task snapshot, test `notified` and change it from false to true in one application-state update. Only the caller that won that transition may proceed with its completion-notification path. This suppresses duplicate enqueue attempts from concurrent completion, kill, and stop paths while the same task-state generation remains registered. It does not persist across process restart and does not prove enqueue, transcript persistence, delivery, or consumption.

**TR-034 — Notification shape.** A model-facing task notification identifies task ID, optional tool-use ID, task type, output file, terminal status, and a concise summary. It is an internal queued event even if encoded as a user-role message; it is not a new human request.

**TR-035 — Terminal eviction.** Evict only a terminal task with `notified=true`. Killed tasks may remain visible for 3 seconds. Local-agent tasks remain through a 30-second panel grace unless explicitly retained; retained tasks have no automatic deadline.

**TR-036 — Speculation interaction.** Abort any active prompt speculation before enqueueing a completion notification that changes the model-visible queue.

**TR-037 — Notification transaction boundary.** The terminal-state update/latch flip and the later model-queue enqueue are separate operations. A crash after the flip but before enqueue loses that completion notification. A crash after in-memory enqueue but before the queue is consumed also loses it. Queue-operation transcript entries are diagnostics and are filtered during resume; they do not implement the queue.

**TR-038 — Duplicate recovery window.** A remote completion can become model-visible or durable in the conversation while its sidecar cleanup remains incomplete. On a later recovery, the remaining sidecar and remote service may cause the completion path to run again because the prior live `notified` latch is gone. Consumers that require stronger behavior may deduplicate by stable task/tool-use identity, but the documented runtime has no durable delivery ledger guaranteeing this.

## Concrete task families

### Local shell

**TR-040 — Shell state.** Track command, process/shell handle, result code/interruption, foreground/background state, optional owner agent, output and cleanup registrations, last observed output size/time, and optional monitor display kind.

**TR-041 — Shell completion.** Start in running state after output initialization. On process completion, flush output and release process cleanup before changing state. Exit code zero completes; other codes fail. If killed already, retain killed.

**TR-042 — Shell notification.** After terminal transition, use the live latch in TR-033 before enqueueing. A stopped local shell may emit a direct SDK termination and set `notified` without a model-queue completion, suppressing a noisy secondary exit-137 notification. Monitor-kind notifications may use higher queue priority than ordinary background work.

**TR-043 — Shell stall watchdog.** Every 5 seconds, detect no output growth for at least 45 seconds. Read the last 1,024 bytes and, if the final text resembles an interactive prompt, enqueue one warning/attention notification. Do not attach a terminal status to this warning. Reset or suppress repeated warnings until new output establishes a new stall.

**TR-044 — Agent shell cleanup.** When an owning agent exits, kill agent-scoped shells and remove their queued notifications so stale work cannot address a dead agent.

### Local agent and main session

**TR-050 — Local-agent state.** Track agent identity, abort controller, result/error, progress/token/tool counts, recent activities (cap 5), messages, pending user messages, foreground/background state, retention, and optional panel-eviction deadline.

**TR-051 — Pending messages.** Queue messages while an agent is unavailable to consume them, then drain atomically. Appending a displayed agent message must preserve bounded presentation history without changing the durable transcript contract.

**TR-052 — Local-agent terminal transition.** Complete or fail only from running, remove the active abort controller, set end time/result or error, establish the panel grace unless retained, then claim the live notification latch and enqueue completion. Killing aborts and uses the same live-state gate. The queue operation is not atomic with the state transition.

**TR-053 — Main-session background task.** Backgrounding the main query reuses its actual abort controller and query generator. Stopping the task cancels the real query. If completion occurs while still backgrounded, enqueue a task notification; if foregrounded again, emit the direct terminal event and mark notified without queuing duplicate model input.

### Remote agent

**TR-060 — Remote metadata.** Best-effort sidecar metadata records the local task ID, remote session identity/type, description, tool-use association, spawn time, and remote-specific metadata needed to query a remote session later. Writes and removals are independent fire-and-forget file operations: they are not temp-file/rename commits, are not fsync-backed, and log rather than escalate many failures.

**TR-061 — Remote restore.** On startup, enumerate parseable sidecars independently and query each remote session. Skip a malformed sidecar without blocking the others. For archived/not-found sessions, remove the sidecar and do not synthesize a local terminal task or completion notification. For recoverable authentication/network failures, leave the sidecar and skip restoration so a later session can retry. Implement only a still-live remote session with its stable local task ID and original start time. Because the new process has an empty live task map, registration may emit a new `task_started` event; output polling cursor and `notified` state begin fresh.

**TR-062 — Remote polling.** Poll every second. Update only if the current local state is still running so local kill wins a race. Certain remote task types require a specialized completion checker rather than treating the first remote result as terminal.

**TR-063 — Remote stability and timeout.** Remote review completion may require five consecutive idle polls with no new log data and has a 30-minute review timeout. Timeout fails the task explicitly. Plan-oriented states such as needs-input and plan-ready remain nonterminal until their contract is satisfied.

**TR-064 — Remote kill.** In live state, mark killed, `notified=true`, and end time together so late polls cannot overwrite killed. Stop polling and emit the direct structured termination event. Remote archival and sidecar removal are best-effort independent operations. This path does not imply a model-facing completion was queued.

**TR-065 — Remote recovery limits.** Restoring a remote sidecar recovers placement identity, not a local process snapshot: in-memory log fragments, delivered byte offset, notification claim, abort handles, and queued completion state are not restored. If the remote service reports the session already archived, the runtime deliberately drops the sidecar without retroactively inventing an outcome.

### In-process teammate

**TR-070 — Teammate identity.** Track teammate name/team/agent identity, status, independent whole-teammate and current-work abort scopes, permission mode, progress, plan approval, messages, and pending injected user messages.

**TR-071 — Teammate messages.** Cap UI-held teammate messages at 50 while keeping durable transcript ownership separate. Prefer a running task when old terminal tasks share an agent identity.

**TR-072 — Shutdown and injection.** A shutdown request is valid only for a live teammate not already shutting down and is expressed as an explicit special prompt. User-message injection is rejected for dead teammates and wakes/continues eligible running or idle teammate work through its own queue.

### Dream, workflow, and monitor

**TR-080 — Dream task.** Memory consolidation is UI-only task work with phases such as starting/updating, an abort controller, and at most 30 retained turn records. Complete, fail, and kill set `notified=true` immediately because there is no model-facing completion path.

**TR-081 — Optional task families.** Workflow and MCP-monitor tasks exist only when included and enabled. They still use the common ID, status, output, notification, and kill contracts. An absent implementation is reported as unavailable at registry construction, never as a fabricated running task.

## Cancellation, shutdown, and disabled behavior

**TR-090 — Kill precondition.** Generic stop accepts only a currently running task. Unknown, pending when not stoppable, or already terminal tasks return a clear non-destructive error.

**TR-091 — Resource registry.** Register process handles, abort controllers, polling timers, output writers, and transport subscriptions with cleanup ownership when the task starts. Remove them idempotently on every terminal path and process shutdown.

**TR-092 — Kill-surface differences.** A local-agent stop path may enqueue a partial-result completion for its live parent. Local-shell and remote kill paths may instead set `notified` and emit a direct structured termination event without model-queue input. Bulk owner cleanup can also remove queued notifications addressed to a dead agent. None of these process-local queues survives a crash.

**TR-093 — Unsupported output security.** If required exclusive/no-follow protection cannot be provided on a platform where hostile symlink creation is in scope, reject task output initialization rather than opening a broad writable path.

## Acceptance scenarios

**TR-A01 — Live replacement.** Re-registering an agent under an ID already present in the same live task map preserves its retained flag, original start time, messages, pending prompts, and disk-loaded state and emits no second start event.

**TR-A02 — Completion versus offset read.** A poll starts a disk read while the task is running; the process completes before the read returns. Offset application sees terminal fresh state and does not overwrite it.

**TR-A03 — Kill race.** Kill marks a shell killed before its exit callback runs. The callback flushes/cleans resources but leaves status killed and sends no duplicate notification.

**TR-A04 — Clear during background work.** A session clear changes the session ID while a background shell runs. Its captured output path remains readable and completion notification points to that path.

**TR-A05 — Symlink attack.** A path with the generated task filename already exists as a symlink. Exclusive/no-follow creation fails without modifying the target.

**TR-A06 — Output cap.** A pipe task crosses 5 GiB. It appends one truncation marker, drops later chunks, flushes, and remains killable/inspectable.

**TR-A07 — Remote kill versus poll.** A poll response arrives after local kill. Fresh-state guard discards the response; terminal state remains killed and remote cleanup continues.

**TR-A08 — Stall prompt.** A background shell emits `(y/n)` and stops growing for 45 seconds. One nonterminal attention notification appears; the task remains running.

**TR-A09 — Dream completion.** Dream work completes and becomes immediately evictable without injecting a model-facing task message.

**TR-A10 — Crash after output prefix.** Kill the process while output is draining. On restart, any surviving file prefix remains inspectable, but no local running/terminal task is fabricated from that file and no completion is inferred.

**TR-A11 — Crash between latch and enqueue.** Pause after the terminal live-state update sets `notified=true` and before queue enqueue, then terminate the process. Resume produces no implemented completion from the live latch or diagnostic queue-operation records. Mark this as a known loss window, not a failed exactly-once test.

**TR-A12 — Remote recovery classification.** Given live, archived, transient-auth-failing, and not-found remote sidecars, restore the live task with the same ID and a fresh live start event; preserve the transient sidecar; remove archived/not-found sidecars without synthetic completion.

**TR-A13 — Corrupt sidecar isolation.** One sidecar contains truncated JSON while a second is valid. Recovery skips/logs the corrupt record and still restores the valid remote task.

**TR-A14 — Completion/sidecar crash window.** Persist or consume a remote completion, terminate before sidecar deletion, and resume. Demonstrate that the reference topology can re-enter completion with a fresh latch; if the implementation suppresses it with a durable delivery ledger, identify that mechanism as the safer divergence in TR-009.

**TR-A15 — Split credential output.** Configure a synthetic session credential, then have a background shell emit that literal across two adjacent process writes. Polling, the raw output file, and a reopened task manager expose one redaction marker and no credential bytes; byte offsets advance over the sanitized representation. Repeat with a credential equal to the final JSON separator between two safe task-state fields; whole-state validation rejects the replacement and the prior state file remains intact.

**TR-A16 — Output sanitizer failure and schema compatibility.** Make record projection, whole-state validation, per-task sanitizer construction, or stream processing fail while a shell emits output. Include a bounded credential union with no safe terminal marker and verify manager construction fails rather than creating a task whose safe output disappears behind successful status. Record/construction failure creates no artifact and validation failure does not replace the prior state; stream failure writes no raw chunk or retained suffix, persists a safe `output_incomplete` warning across reopen, keeps the successful process status/exit code unchanged, and omits the panic payload. Reopen a current-version legacy record lacking both additive fields and verify false/empty defaults without migration.

**TR-A17 — Host callback reentry.** From each configured clock, random reader, state validator, persistence seam, record sanitizer, command factory, and output-sanitizer factory/marker/write/flush callback, call every public manager entrypoint before returning normally. Error-returning operations, including the context-bearing work snapshot, immediately receive the exact task-busy result; slice-only snapshots are empty; no nested callback runs; the outer state transition finishes; and a later ordinary mutation, checked snapshot, poll, and close still succeed. Hold the task-state write lock while cancelling a checked snapshot and verify it returns cancellation without waiting for lock release or exposing a partial result. Repeat under the race detector and shuffled execution.

**TR-A18 — Opaque task formatting.** Put a unique secret in every command,
output, description, path, metadata, error, launch, and patch field of each
public task value, then render each value with `%v`, `%+v`, `%#v`, `%s`, and
`%q`. Every rendering contains only its fixed opaque shape and no secret;
explicit JSON, accessor, and bounded output-retrieval contracts retain their
ordinary typed values.

## Non-normative provenance

Behavior was specified primarily from `Task.ts`, `tasks.ts`, `tasks/LocalShellTask`, `tasks/LocalAgentTask`, `tasks/LocalMainSessionTask.ts`, `tasks/RemoteAgentTask`, `tasks/InProcessTeammateTask`, `tasks/DreamTask`, `tasks/stopTask.ts`, `utils/task/framework.ts`, and `utils/task/diskOutput.ts`.
