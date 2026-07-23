# Delegation lifecycle

This reference defines the Agent capability from validated request through foreground/background execution, context construction, worktree or remote placement, persisted child transcript/result, resume, cancellation, and orphan cleanup.

## Contents

- [Contract map](#contract-map)
- [Invocation wire contract](#invocation-wire-contract)
- [Selection and backend decision](#selection-and-backend-decision)
- [Identity and resource ownership](#identity-and-resource-ownership)
- [Context construction](#context-construction)
- [Normal and forked workers](#normal-and-forked-workers)
- [Worktree and remote isolation](#worktree-and-remote-isolation)
- [Foreground execution](#foreground-execution)
- [Background task execution](#background-task-execution)
- [Result construction and synthesis](#result-construction-and-synthesis)
- [Cancellation and cleanup](#cancellation-and-cleanup)
- [Resume and message continuation](#resume-and-message-continuation)
- [Failure and disabled behavior](#failure-and-disabled-behavior)

## Contract map

| ID | Requirement |
| --- | --- |
| MA-INV-001 | Agent input is schema-validated and agent-type-authorized before resource allocation. |
| MA-BKD-001 | Foreground, local async, in-process teammate, pane, remote, fork, and worktree are explicit execution dimensions, not inferred from one boolean. |
| MA-BGD-001 | Definition, invocation, coordinator, fork, product surface, and global disable controls resolve background behavior deterministically. |
| MA-BGD-002 | A background task is registered and output initialized before the worker can emit unowned progress. |
| MA-FRK-001 | A fork reuses an exact parent prompt/tool/cache prefix plus paired placeholder tool results and never recursively forks. |
| MA-FRK-002 | A resumed fork does not append parent context again. |
| MA-WT-001 | Worktree identity derives from the stable agent ID and cleanup retains any modified tree with an actionable result. |
| MA-REM-001 | Remote agents pass remote eligibility, register live remote tasks plus best-effort recovery sidecars, and are always asynchronous. |
| MA-OUT-001 | Progress is advisory; terminal live status and the configured output-flush attempt precede a completion enqueue attempt. Same-generation suppression is not crash-durable delivery. |
| MA-OUT-002 | Final result carries stable agent identity, actual terminal status, text fallback, and attributable usage/counts. |
| MA-KIL-001 | Abort marks the task killed before cleanup and preserves latest partial text without presenting it as completion. |
| MA-RSM-001 | Resume implements a coherent transcript, execution class, and isolation state from durable evidence. |
| MA-RSM-002 | A resumed invocation is not re-gated by current Agent-type deny rules after the original authorized spawn, but every new tool call still obeys current policy. |
| MA-CLN-001 | Worker cleanup is idempotent and covers MCP, hooks, task children, transcript links, cache/context, worktree, and notifications. |

## Invocation wire contract

### Input

```text
AgentRequest {
  description: short nonempty string
  prompt: nonempty string
  subagent_type?: string
  model?: "sonnet" | "opus" | "haiku"
  run_in_background?: boolean
  name?: string                    # teammate-capable builds only
  team_name?: string               # teammate-capable builds only
  mode?: teammate mode             # team gates only
  isolation?: "worktree" | supported remote value
  cwd?: string                     # selected builds only
}
```

The invocation-level model, when supplied and valid, overrides the definition model. It does not change the agent type, permissions, tools, or effort automatically.

Hide or reject schema fields that the current build/surface cannot honor:

- `run_in_background` is absent when background work is globally unavailable or incompatible fork behavior owns the decision;
- team fields are absent when team/swarm capability is unavailable;
- remote isolation is absent outside the builds that explicitly include it;
- `cwd` is present only in the supported product surface.

Unknown fields do not become prompts or shell arguments.

### Output variants

Foreground completion:

```text
{
  status: "completed"
  prompt: original prompt
  ...AgentResult
}
```

Asynchronous launch:

```text
{
  status: "async_launched"
  agentId: stable agent/task ID
  description: original description
  prompt: original prompt
  outputFile: file-backed output path
  canReadOutputFile?: boolean
}
```

Internal team and remote adapters may use typed `teammate_spawned` or `remote_launched` launch results, but these remain launch acknowledgements, not completion evidence.

Do not return `completed` because a process was successfully started. A launch result must tell the caller how later terminal completion is observed.

## Selection and backend decision

### Agent type

Resolve according to [agent definitions](agent-definitions.md): explicit type, eligible fork default, then `general-purpose`; apply allowed-agent-type and definition availability checks before allocation.

If `team_name` resolves and `name` is present, use teammate spawning. If a team name is supplied while team capability is disabled, return an unavailable-team error. A teammate cannot spawn another teammate.

An in-process teammate cannot launch a background subagent, and a definition marked `background` cannot be nested beneath that in-process teammate. This prevents an untracked second asynchronous hierarchy inside an already asynchronous teammate lifecycle.

### Background resolution

Evaluate in this order:

1. If the global background-disable control is set, background execution is unavailable.
2. Remote isolation always requires background because the remote task outlives the call.
3. Teammate backend follows teammate lifecycle rather than ordinary subagent foreground/background.
4. Fork experiment may require all fork spawns to be background.
5. Coordinator mode requires worker spawns to be background.
6. Product-specific assistant/proactive modes may require background.
7. Explicit `run_in_background: true` requests background.
8. Definition `background: true` requests background.
9. Otherwise run foreground, subject to the optional foreground auto-background transition.

If background is globally disabled and the invocation semantically requires it (remote, coordinator, mandatory fork), fail before launch. If it was merely a preference and foreground is safe, use the documented foreground fallback and report it.

Foreground work may automatically detach after 120,000 ms only when the auto-background feature is active. Transition the same stable task/agent identity; do not restart the child or duplicate its transcript.

### Isolation resolution

1. Explicit invocation `isolation` overrides definition isolation.
2. Explicit supported `cwd` overrides the directory chosen by worktree isolation for actual working directory only where the product contract permits this combination; keep ownership metadata explicit.
3. Remote isolation selects the remote-agent backend.
4. Worktree isolation selects a local worker rooted at the allocated worktree.
5. Otherwise use the inherited/selected local working directory.

Backend and isolation are orthogonal: a local asynchronous child may use a worktree; a remote child has remote placement; a foreground child may run in the current directory.

## Identity and resource ownership

Allocate the stable agent ID before creating other resources. For worktrees, use slug:

```text
agent-<first eight characters of stable agent ID>
```

Resource ledger:

| Resource | Owner | Join key | Terminal cleanup |
| --- | --- | --- | --- |
| Agent identity | Parent session/task registry | Agent ID | Retain as result/resume provenance |
| Task state | Task framework | Agent/task ID | Terminal state then eventual eviction |
| File-backed output evidence | Task framework | Agent/task ID | Best-effort flush; retain through notification/read window; a surviving prefix is not terminal proof |
| Child transcript sidechain | Session persistence | Child session/agent ID plus parent UUID | Retain for resume/audit |
| Abort controller | Foreground call or background task | Agent ID | Abort once; detach listeners |
| Child query engine state | Worker | Agent ID/session ID | Flush then dispose |
| MCP clients | Worker invocation | Invocation ID | Close in `finally` |
| Hooks/memory/skill state | Worker invocation | Invocation ID | Final hooks, clear invoked skills, release locks |
| Worktree | Isolation owner | Agent ID/path/branch | Remove only if unchanged and allowed; otherwise retain/report |
| Remote session | Remote task | Remote session ID linked to agent ID | Archive/stop and remove live metadata at terminal |
| Child shell/monitor tasks | Worker/task registry | Parent agent ID | Kill/reconcile or transfer explicit ownership |
| Completion notification | Owning parent session | Agent/task ID and live `notified` latch | At most one enqueue attempt in one live state generation after terminal state/output-flush attempt; queue is not crash durable |

The optional human-readable name maps to the stable ID only after task registration succeeds. Never route by a name whose target registration did not succeed; the mapping's restart behavior follows its own storage contract rather than the live task map.

## Context construction

### Normal child

A normal child receives:

- the selected definition's system prompt and critical reminder;
- shared runtime safety/tool protocol sections appropriate to its capabilities;
- selected environment and repository facts for its working directory;
- the user's delegated task prompt and optional definition `initialPrompt`;
- explicitly configured skills, memory, hooks, and child MCP tools;
- child-specific model, effort, maximum turns, and permission context;
- attribution linking the sidechain to the parent message/tool-use ID.

It does not receive the parent's full hidden reasoning, transient UI state, anonymous task registry, or unrelated child transcripts. Project instructions may be omitted only when the definition explicitly opts out under policy.

Persist child messages incrementally as a sidechain with the parent UUID. Incremental persistence is required even for foreground work because cancellation/crash can occur before final result.

### Context path translation

When a child works in a different directory/worktree, tell it the effective path and translate only path-bearing context whose mapping is known. Do not blindly string-replace arbitrary prompt text. A fork receives a clear worktree notice so cached parent context referring to the original directory is not mistaken for current filesystem placement.

## Normal and forked workers

### Normal worker loop

The worker uses the shared query engine with the frozen invocation plan:

```text
prepare child context and registry
  -> append initial child user prompt
  -> stream model events
  -> execute authorized tools
  -> append paired tool results
  -> continue until completion, limit, error, or abort
  -> persist normalized child result
```

Maximum turns, model transport retry, compaction, usage, stop hooks, and tool-result pairing retain shared semantics.

### Fork eligibility

Fork is available only when:

- included in the build and runtime experiment;
- coordinator mode is off;
- the current surface is interactive/supported;
- the current query is not already a fork;
- fork boilerplate/context inspection does not detect recursive fork;
- background execution required by fork remains available.

The synthetic fork definition has compatibility defaults:

```text
agent type: fork
tools: wildcard before child/backend filtering
maximum turns: 200
model: inherit
permission behavior: bubble through parent authority
```

### Fork prefix construction

The fork maximizes an exact cacheable parent prefix:

1. Reuse the already rendered parent system prompt exactly.
2. Reuse the exact parent tool definitions and thinking/noninteractive configuration.
3. Copy the selected parent conversation context in original order.
4. Include the current assistant message containing all tool-use blocks.
5. Append one user message containing one tool-result placeholder for every tool-use ID in that assistant message. The placeholder text is exactly equivalent to `Fork started — processing in background`.
6. Append the child-only directive/task without altering the preceding cached prefix.

Every copied tool-use ID has exactly one placeholder result. Do not include a subset, because the model API requires coherent pairing. Do not expose hidden chain-of-thought unavailable in the parent message projection.

On fork resume, implement the fork system prompt and exact tools from durable metadata/transcript, but do **not** append the parent context again. Re-adding it duplicates tool IDs and breaks both cache and conversation validity.

## Worktree and remote isolation

### Worktree creation

1. Allocate stable agent ID.
2. Validate source-control repository and worktree support.
3. Create the `agent-<id-prefix>` worktree and record path/branch ownership in task metadata.
4. Set child working directory to the worktree unless an explicitly supported `cwd` rule says otherwise.
5. Add path-translation/isolation notice to child context.
6. On resume, validate the recorded path; touch/update its recency if valid.

If resume metadata names a missing worktree path, fall back to the parent/current directory only under the compatibility rule and report the lost isolation. Do not recreate a different worktree silently while claiming continuity.

### Worktree cleanup

Cleanup is idempotent:

1. Run required worktree lifecycle hooks.
2. Inspect head/revision and working-tree changes.
3. If head has not advanced and there are no changes, remove the worktree and clear path metadata.
4. If commits or changes exist, retain the tree and include its path and branch in the result/notification.
5. Hook-managed worktrees are retained according to hook ownership rather than unconditionally removed.
6. Cleanup failure is diagnostic and preserves path evidence; never destroy potentially useful changes to make cleanup appear successful.

### Remote isolation

Remote agent execution is available only in builds that declare it. Before creation, check:

- supported first-party login;
- available remote environment;
- source-control repository;
- usable remote/repository transfer path;
- installed repository integration where required;
- managed policy.

The remote path uses the teleport/session-placement contract to create a remote session, then registers a live remote-agent task and best-effort recovery sidecar containing local task ID, original remote session ID, command/title, spawn time, tool association, and task subtype/metadata where applicable. Poll/log/todo cursors remain live state and are rebuilt empty after restart. It always returns a background launch result.

Remote task polling appends session events and file-backed output, derives progress/todo state, and recognizes the semantic result or subtype-specific completion checker. Long-running remote tasks do not terminally complete merely because an intermediate `result` appears. On terminal completion/kill, request removal of live remote metadata, request archival or stop best-effort, mark the live task terminal, and claim its same-generation notification latch. These independent steps admit the task-runtime crash loss/duplication windows.

Remote event/service errors are task failures only after the bounded polling/reconnect policy is exhausted. A temporarily unreachable remote session remains running/pending with observable retry state.

## Foreground execution

Foreground execution uses the parent call's abort signal. Its lifecycle:

```text
validated
  -> identity_allocated
  -> resources_ready
  -> running
  -> completed | failed | killed
  -> result_returned
  -> cleaned
```

If ordinary prompt cancellation aborts the parent, the foreground child transitions to killed and returns/records a killed result according to the tool protocol. If auto-background activates at 120 seconds, detach the abort relationship, register/confirm live task state and output ownership, return an async launch result, and let the same worker continue.

Even foreground work persists a sidechain and performs full cleanup. A foreground tool result is not a raw transcript dump; it is the normalized Agent result.

## Background task execution

### Launch transaction

Order matters:

1. Validate request, definition, tools, permissions, MCP readiness, and backend.
2. Allocate stable agent ID.
3. Initialize the bounded file-backed output writer for that ID.
4. Register task in `pending`/`running` state with cancellation callback and metadata.
5. Register name-to-ID mapping if requested.
6. Allocate worktree/remote/process resources and record them.
7. Start the child query asynchronously with an abort controller not linked to ordinary parent Escape cancellation.
8. Return the launch acknowledgement and output location.

If a step after task registration fails, mark the registered task failed and run cleanup; never delete evidence and return a generic pre-launch error as though no task existed.

### Progress

As the child runs:

- append retained child messages/summary output to durable storage;
- update task progress and latest assistant text;
- emit task-progress events on meaningful tool-use/activity boundaries;
- keep usage and turn counters attributable to the child;
- avoid placing every transient progress record into the parent's model-visible context.

### Completion ordering

On normal completion:

1. Persist the normalized Agent result/output.
2. Mark task state `completed` before any potentially slow classifier or worktree embellishment so a blocking waiter can observe terminal state.
3. Run optional handoff/safety classification and append its attributable result.
4. Run worktree cleanup/retention and append retained-path details.
5. Set terminal output metadata.
6. Atomically claim the live one-time notification latch, then separately enqueue a completion notification. A crash between these operations loses the notification; neither operation is a persistent delivery acknowledgement.
7. Release worker-only resources in `finally`.

The implementation must ensure later embellishments cannot change `completed` to a contradictory status. If a required step can fail the task, it belongs before terminal marking; post-terminal steps are explicitly best-effort annotations/cleanup.

On error, record the error, mark `failed`, then use the same live enqueue gate. On abort, mark `killed` first, preserve latest partial text, then use the applicable live enqueue or direct structured-termination surface and clean up.

Background workers survive ordinary Escape/query cancellation because their abort controller is unlinked. Explicit task stop owns their cancellation.

## Result construction and synthesis

### Agent result

```text
AgentResult {
  agentId: string
  agentType?: string
  content: normalized text/content
  totalTurns: nonnegative integer
  durationMs: nonnegative duration
  usage: attributable usage object
  tokenCounts: attributable token counts
  terminalStatus: completed | failed | killed
  worktree?: { path, branch, retained }
  error?: safe structured error
}
```

If the last assistant message contains only tool-use blocks, scan backward for the latest assistant text rather than returning empty output despite an earlier final explanation. Never use tool input or hidden reasoning as result text.

For one-shot built-ins `Explore` and `Plan`, omit the continuation trailer that advertises agent ID, `SendMessage`, and usage. Other resumable agents include enough stable identity guidance for continuation.

### Parent synthesis

The parent may synthesize only:

- terminal normalized child result;
- terminal task status;
- file-backed output evidence attributable to that ID, interpreted with its flush/error state;
- worktree/remote location retained by cleanup;
- child usage and error/denial metadata.

Do not treat launch acknowledgement, task progress, a notification received before output, or stale prior output as evidence of task success. If multiple children finish, identify each result by stable ID/type and preserve failures rather than averaging them away.

## Cancellation and cleanup

### Cancellation sources

| Source | Foreground | Background | Remote/team |
| --- | --- | --- | --- |
| Parent prompt/Escape | Abort linked child | Does not abort | Does not abort independent worker |
| Explicit task stop | Abort/terminally pair tool if applicable | Abort and mark killed | Send stop/shutdown through backend, then reconcile |
| Process/session shutdown | Abort and await bounded cleanup | Kill/reconcile owned tasks | Stop panes/processes/remote pollers and reconcile |
| Limit/model/tool fatal error | Fail child | Mark task failed | Backend-specific failed terminal state |

### Worker `finally` responsibilities

Execute idempotently even after partial initialization:

1. Close agent-specific MCP connections.
2. Run allowed terminal hooks.
3. Flush prompt-cache/performance tracking and child transcript mapping.
4. Flush child file cache/context/todo state as required.
5. Clear invoked-skill state and memory locks.
6. Stop or transfer ownership of child shell/monitor tasks.
7. Dispose abort listeners, queues, timers, and trace resources.
8. Reconcile worktree/remote resources.
9. Ensure terminal live task status exists and finish the configured output-flush attempt, preserving any write failure as an explicit diagnostic.
10. If asynchronous and that task family uses model-queue completion, claim the same-generation latch and make one enqueue attempt; direct structured-termination paths may mark notified without queueing model input.

At parent/session shutdown, scan for tasks still owned by dead workers. Kill/reconcile orphans; never leave anonymous processes because their task record was evicted from UI state.

## Resume and message continuation

### Durable resume input

Resume concurrently loads:

- child transcript/sidechain;
- agent metadata/invocation fingerprint;
- task and name mapping when present;
- worktree path/branch metadata;
- remote session metadata for remote agents.

Fail explicitly if no usable transcript exists. Do not create a fresh agent under the old ID with only the new prompt.

### Transcript reconciliation

Before continuing:

1. Remove assistant messages containing only whitespace.
2. Remove orphan thinking-only records that cannot form a valid API message.
3. Find unresolved/dangling tool-use blocks and filter or synthesize the explicit interrupted result required by the transcript protocol.
4. Apply content-replacement/compaction state in recorded order.
5. Validate message alternation and tool-use/result pairing.
6. Preserve parent-sidechain links and original tool-use IDs.

### Execution-class recovery

Select type:

- metadata type `fork` resumes as fork;
- otherwise, a currently available matching original type resumes with that type;
- absent/obsolete metadata falls back to `general-purpose` only as the documented compatibility path and reports that fallback.

Resume is always background. It does not re-run current Agent-type deny gating because the original spawn already crossed that authorization boundary (`MA-RSM-002`). However:

- current managed policy and tool-call authorization still govern every new operation;
- missing/disabled infrastructure can make resume fail;
- resumed fork implements exact fork prompt/tools and never re-adds parent context;
- valid worktree resumes in the recorded tree and refreshes its recency; missing worktree follows the reported compatibility fallback.

The original name-to-ID mapping remains; do not rewrite it to a new task ID during resume.

### `SendMessage` continuation routing

For an ordinary subagent target:

1. Resolve a registered name to stable ID, or validate an explicit raw agent ID.
2. If worker is running, queue the message for admission at the next safe child query/tool-round boundary.
3. If worker stopped but durable transcript exists, resume it in background with the message.
4. If task state was evicted, attempt disk-backed transcript/metadata recovery.
5. If evidence is gone or cleaned, return an explicit no-resumable-transcript error.

Queued continuation messages are ordered and attributable; do not inject into a model call already streaming in a way that changes its prompt mid-request.

## Failure and disabled behavior

| Condition | Required behavior |
| --- | --- |
| Invalid request/type/rule | Fail before ID/resource allocation |
| Background mandatory but disabled | Typed unavailable error; no hidden foreground substitute |
| Worktree creation fails after task registration | Mark failed, persist error, clean partial tree, and make one same-generation notification-enqueue attempt |
| Remote precondition fails | Return all actionable preconditions; create no remote session/task |
| Child model/tool fatal error | Preserve transcript/error, mark failed, cleanup, and use the live notification gate |
| Abort with partial text | Mark killed; retain partial text as partial, never completed |
| Crash after terminal/latch update but before queue enqueue or consumption | Process-local task/queue state is lost; local work is not implemented from output alone, so completion may be missing |
| Remote completion became durable/model-visible but sidecar removal did not | A later restore has a fresh latch and can re-enter completion, so duplicate model-visible completion is possible |
| Stronger delivery required | Add a durable outbox, stable delivery ID, and consumption acknowledgement as an intentional safer divergence; do not attribute it to the documented runtime |
| Worktree changed | Retain and report path/branch |
| Worktree path missing on resume | Report isolation loss and use only documented cwd fallback |
| Resume transcript malformed beyond repair | Fail with coherent diagnostic; do not send malformed conversation to model |
| Name resolves to cleaned/evicted agent | Attempt durable recovery, then explicit failure |
| Shutdown finds live orphan child | Stop/reconcile using task ownership and preserve terminal evidence |
