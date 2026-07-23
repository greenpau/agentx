# Prompt Dispatch and Queue Scheduling

## Contents

1. [Input forms](#input-forms)
2. [Dispatch guard](#dispatch-guard)
3. [Direct submission](#direct-submission)
4. [Local commands](#local-commands)
5. [Queue state and scheduling](#queue-state-and-scheduling)
6. [Attachments, paste, and history](#attachments-paste-and-history)
7. [Cancellation and races](#cancellation-and-races)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Input forms

The controller accepts:

- Direct human prompt text with prompt mode, cursor/paste state and turn-level attachments.
- A prevalidated queued command.
- Slash/local commands that may return model messages, an immediate local view, or both.
- Shell-mode commands.
- Model-visible system-generated prompts that are intentionally hidden from the terminal transcript.
- Task/team notifications queued for the model.
- Remote-injected input with explicit provenance.

Each queued command records value, mode, optional priority, pasted content, pre-expansion input, attachments/image IDs, workload identity, provenance, model-visibility flags and settlement callbacks as applicable.

- **REPL-DIS-001 — Expansion before execution.** Resolve supported references and paste placeholders before dispatch. Preserve the original unexpanded string for keyword/mode detection that must ignore pasted contents.
- **REPL-DIS-002 — Orphan image filtering.** Include an image block only when a nonempty image paste record exists; an orphaned visible marker is ordinary text and cannot create an empty image block.
- **REPL-DIS-003 — Command distinction.** Keep user commands, model-callable tools, and identity-bearing or long-lived tasks as separate contracts even when one feature uses all three. The task-runtime owner defines each task kind's persistence and crash-recovery profile; identity alone does not imply durability.

## Dispatch guard

The guard state machine is:

```text
idle --reserve--> dispatching --start--> running --end(generation)--> idle
  \---------------------start-direct--------------------/
dispatching --cancel-reservation--> idle
any --force-end/increment-generation--> idle
```

- **REPL-DIS-004 — Synchronous reservation.** `reserve` changes the synchronously readable state before asynchronous input processing begins. This prevents two submissions in one UI batch from both observing idle.
- **REPL-DIS-005 — Generation ownership.** Starting a query captures the current generation. `end` succeeds only for that generation.
- **REPL-DIS-006 — Force invalidation.** Cancellation increments generation before returning to idle so stale completion cannot affect later work.
- **REPL-DIS-007 — Local release.** If preprocessing produces no model messages, cancel the reservation before removing the local view/loading placeholder to avoid a one-frame spinner flash.

## Direct submission

Submission workflow:

1. Reject an empty trimmed value unless its command/mode contract permits emptiness.
2. Expand references and validate paste/image records.
3. Route recognized exit aliases through the ordinary exit command unless input was explicitly remote-injected.
4. If query work is active, queue only supported prompt/shell work and optionally interrupt a tool whose behavior permits interruption.
5. Otherwise create an abort controller and reserve dispatch.
6. Normalize each command separately into semantic messages and local effects.
7. Attach turn-level context only to the first normalized command in a batch.
8. Clear local views before starting the semantic query.
9. Start the guard, run the query, and end the matching generation.
10. Reset editor/history state according to submission outcome.

- **REPL-DIS-008 — Per-command normalization.** Never concatenate raw queued commands before command parsing. Normalize each, then combine only the resulting model-message workload allowed by batching rules.
- **REPL-DIS-009 — First-command attachments.** IDE context, selected files, paste statistics and ordinary turn attachments apply only to the first command in a batch.
- **REPL-DIS-010 — Workload scope.** Establish a shared workload identity only when every batched command has the same nonempty workload tag.
- **REPL-DIS-011 — File checkpoints.** Record file-history snapshots for selectable user messages before changes associated with the turn can occur.
- **REPL-DIS-012 — History timing.** Add a human prompt to prompt history before query dispatch. Do not add task notifications or synthetic hidden prompts.

## Local commands

A local command result may be:

- Immediate text or typed local message.
- A local interactive view with an `onDone` callback.
- One or more model-visible messages.
- A prompt replacement/prefill or queued next input.
- A request to change model, effort, permission mode or tools.

- **REPL-DIS-013 — Concurrent local view.** An immediate local view may run while external loading/query work continues if the command declares that behavior safe.
- **REPL-DIS-014 — View ownership.** While a local view owns input, the ordinary prompt editor is inactive. Completion clears the view, may publish a notification, and applies queued/prefilled next input.
- **REPL-DIS-015 — Transcript policy.** Fullscreen local views are not automatically appended to transcript because doing so would duplicate presentation. Only explicit meta/model messages enter semantic history.
- **REPL-DIS-016 — Exit routing.** Exit aliases share the command's cleanup and confirmation behavior; they are not hard process exits hidden inside text handling.

## Queue state and scheduling

The queue is coordination state readable outside the component render cycle. A scheduler reacts to both queue mutations and guard state.

Priority order is `now`, then `next`, then `later`. When priority is omitted, derive it from prompt mode according to the current interaction policy.

Dispatch preconditions:

- Guard is idle.
- Queue is nonempty.
- No local interactive view owns input.
- The command belongs to the main thread; delegated task queues are owned elsewhere.

Batching rules:

- Slash commands execute one at a time.
- Shell commands execute one at a time.
- Ordinary prompts may batch when mode, workload and metadata compatibility permit.
- Task notifications and system-generated notification prompts never batch with ordinary human prompts.
- Preserve FIFO order within the same priority.

- **REPL-QUE-001 — Now behavior.** Enqueueing `now` may abort the active query, then runs at the next safe idle boundary.
- **REPL-QUE-002 — One scheduler.** Multiple state reactions may request scheduling, but only one dispatch reservation can win.
- **REPL-QUE-003 — No reentry.** The scheduler does not recursively dispatch while a guard transition is incomplete.
- **REPL-QUE-004 — Post-idle wake.** Returning to idle triggers a queue check even when no component re-render is otherwise needed.
- **REPL-QUE-005 — Explicit settlement.** Removing, cancelling or failing a queued item invokes its completion/rejection contract exactly once.

## Attachments, paste, and history

When submitting:

- Expand text paste placeholders into model-visible text by saved offsets.
- Convert valid image paste records to image content blocks and retain their user-visible markers.
- Include attachments and IDE selection according to turn-level ownership.
- Preserve the raw pre-expansion value for modes or keywords whose detection must not inspect pasted content.
- Clear the editor paste map after successful handoff.
- On interrupt auto-restore, restore text, cursor and paste records and semantically undo the last prompt-history insertion.

- **REPL-DIS-017 — Visible/model pairing.** Every image marker passed to the model has one valid image block; deleting the marker removes its orphaned paste record on the next consistency pass.
- **REPL-DIS-018 — Queued image restore.** Editing or recalling a queued command restores only image records still referenced by that command.

## Cancellation and races

- Submission may be cancelled while still `dispatching`; cancel the reservation and all asynchronous preprocessing.
- If a queue enqueue observes `running`, the active loop may become idle before enqueue completes. The idle transition and enqueue operation must both trigger scheduler checks.
- State setters may commit after an input handler returns. Maintain synchronous refs for guard, input, cursor, queue index and messages where same-tick correctness matters.
- A queued `now` interruption and explicit user cancellation share the same abort primitive but retain different prompt-restoration and notification behavior.
- Reject stale suggestion/autocomplete results using the input value or generation for which they were computed.

## Acceptance scenarios

**REPL-DIS-A01 — Same-batch submission.** Submit twice in one event-loop batch; verify one reservation wins and the second input queues or is rejected according to policy.

**REPL-DIS-A02 — Local-only command.** Run a local command that yields no model messages; verify no model request and no transient running frame.

**REPL-DIS-A03 — Compatible prompt batch.** Batch three compatible prompts with attachments; verify only the first carries turn-level attachments and all are normalized independently.

**REPL-DIS-A04 — Mixed workload.** Mix a slash command, prompt and task notification; verify three correctly ordered workloads rather than one concatenated prompt.

**REPL-DIS-A05 — Immediate queue priority.** Enqueue `now` during a stream; verify active work is interrupted, its identifiers settle, then the queued item runs once.

**REPL-DIS-A06 — Removed image marker.** Delete an image marker before submit; verify no empty image block reaches the model.

**REPL-DIS-A07 — Interrupt restoration.** Interrupt before any response and restore the prompt; verify text/paste/cursor return and prompt history has no duplicate.

**REPL-DIS-A08 — Stale query completion.** Complete cancelled query A after query B begins; verify A cannot clear B's loading state.

## Non-normative provenance

Evidence was specified from the reference query guard, prompt-submit handler, queued-command module/hook, prompt component, input types, command execution helpers, file history integration and cancellation hooks. These names and paths are non-normative.
