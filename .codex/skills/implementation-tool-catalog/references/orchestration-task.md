# Agent, task, team, scheduling, and planning contracts

## `Agent`

Input requires a short `description` suitable for status display and a full `prompt`. It may select `subagent_type`, model (`sonnet|opus|haiku`), background execution, stable name, team, mode, and isolation. `isolation=worktree` and an explicit remote/current-directory placement are mutually exclusive. Profiles may omit `cwd` or background fields entirely.

- Resolve an agent definition before launch and filter its tools through the agent profile. Do not grant the parent’s entire registry implicitly.
- Treat delegation as concurrency-safe and read-only at the wrapper level; all child side effects pass through the child's capability boundary.
- Foreground execution returns the child’s normalized final result. Background execution returns a stable task identity and launched state; persistence of state, output, and notification follows the selected task subtype rather than the word “background.”
- Resume uses an explicit child identity and preserved transcript/task state; never start a second child while claiming to resume the first.
- Internal teammate and remote-worker variants retain parent/child ownership, authority, output, and cancellation identity.
- Alias `Task` resolves to canonical `Agent`.

## Background task compatibility

`TaskOutput` is a compatibility polling tool with aliases `AgentOutputTool` and `BashOutputTool`. Input identifies a task and accepts timeout from 0 through 600,000 ms, default 30,000, plus `block` default true. It is concurrency-safe, read-only, and deferred. Return current status and new bounded output; timeout without completion is not task failure.

`TaskStop` accepts canonical `task_id` and compatibility `shell_id`, validates that the target exists, is running, and is stoppable by the caller, then requests cancellation and returns identity/type/command plus the resulting state. Alias `KillShell` resolves to it. Cancellation is idempotent; already-terminal and unauthorized targets return distinct results.

## Legacy todos and identity-bearing tasks

Exactly one task surface is active:

- `TodoWrite` replaces the complete legacy todo list. Each item carries content, status, and active form. When all items are complete, persist an empty list so stale completed work does not reappear.
- Task-v2 exposes `TaskCreate`, `TaskGet`, `TaskUpdate`, and `TaskList`.

Task-v2 contracts:

1. `TaskCreate` creates `pending` work with subject, description, active form, and optional metadata; return the stable ID.
2. `TaskGet` returns authoritative fields, status, owner, blockers, dependents, and metadata for one ID.
3. `TaskList` returns bounded summaries in deterministic order with enough relationship state to choose work.
4. `TaskUpdate` may change subject, description, active form, owner, status (`pending|in_progress|completed`), add blockers/dependencies, and merge metadata; null metadata values remove keys. A special deleted transition removes the task under the task-runtime deletion contract.
5. Relationship updates reject self-links, unknown IDs, duplicates, and cycles where the runtime requires an acyclic dependency graph.
6. State transitions, ownership, and updates are atomic and generate task events/notifications.

Reads are concurrency-safe. Mutations are concurrency-safe only because the task store supplies per-record atomicity; this is not permission to race non-atomic implementations.

Task-manager host callbacks may temporarily return the exact task-owned busy sentinel before an operation enters its state transition. Public task adapters retry only that exact sentinel with context-aware bounded backoff; they never inspect or retry an arbitrary error. Preserve the caller's `TaskOutput` timeout across retries instead of restarting it. A cancelled retry performs no mutation, and exhausted contention returns the closed `unavailable` code. Error-bearing checked list APIs distinguish transient contention from an authoritative empty task list.

## Teams and mailboxes

`TeamCreate` creates at most one active team per leader and resolves a unique stable team name. `TeamDelete` refuses while members are active; successful deletion cleans team state only after worker shutdown and output preservation.

`SendMessage` accepts a destination and either plain text or a recognized structured coordination message. Resolve the destination exactly; unknown or ambiguous recipients fail. Plain local mailbox text is classified read-only at the wrapper level, while structured state-changing messages are writes. A bridge/UDS destination is open-world and asks for authority. The compatibility filesystem mailbox record has no stable message/delivery identifier: a retry can append a duplicate, and a legacy successful wrapper response can prove only that delivery was attempted, not that append or read was acknowledged. A stable delivery ID, deduplication ledger, or durable acknowledgement is an intentional safer/versioned divergence.

Teammates may use task-v2 tools, `SendMessage`, and their own cron jobs. Messages and scheduled events are routed to the addressed agent’s pending-input queue, not injected into unrelated transcripts.

## Plan-mode transitions

`EnterPlanMode` is main-session-only. It changes effective permission mode and context while returning a normalized transition result. Although descriptor-classified read-only, it is an explicit session-state transition and must be durable/recoverable.

`ExitPlanMode` requires an active plan. It reads the current plan artifact, optionally carries allowed prompt patterns, and either:

- requests user approval and applies the resulting permission/mode transition; or
- for a teammate, sends the plan to the leader without opening a user prompt.

The structured SDK may backfill the plan into observable input for replay. Approval, rejection, amendment, missing plan, cancellation, and teammate handoff are distinct results.

## Worktree isolation

`EnterWorktree` accepts an optional validated name of at most 64 characters with safe path segments. Create a session-owned isolated worktree, record source repository and ownership, switch the session working directory, and invalidate path/repository caches atomically. Failure leaves the original session paths intact.

`ExitWorktree` accepts `keep|remove`. Removing is destructive; dirty or unmerged work requires explicit force. Operate only on the current worktree created and owned by this session. Restore the original path/cache state before optional cleanup, and preserve enough recovery evidence if cleanup fails.

## Cron and sleep

Cron exposure requires the build gate, runtime gate `tengu_kairos_cron` (default true, refresh about every five minutes), and the `AGENTX_DISABLE_CRON` kill switch to be off.

- `CronCreate` accepts a local-time five-field cron expression and work payload. Recurring defaults true; durable defaults false and is honored only when durable scheduling is enabled. Apply a bounded default/max lifetime to recurring jobs. Limit one owner/team to 50 jobs.
- `CronList` returns only caller-visible jobs; teammates see their own.
- `CronDelete` validates stable ID and ownership, and is idempotent for an already-removed owned job where the protocol permits.
- Scheduled delivery creates a pending input/event with agent identity and does not mutate another session directly.

`Sleep` is an interruptible (`cancel`) wait for proactive/assistant loops. A `next` or `later` queued user input wakes it. Each wake produces one explicit continuation decision; do not emit repetitive “still waiting” model turns.

## Orchestration acceptance cases

- **OT-A01:** A background `Agent` call returns a task ID before completion; resume and stop operate on that same identity.
- **OT-A02:** An async agent cannot recursively call `Agent`, use main-thread plan tools, or stop a root task unless an explicit privileged profile says otherwise.
- **OT-A03:** A task-v2 build never exposes `TodoWrite`; a legacy build never exposes the four task-v2 tools.
- **OT-A04:** Two atomic updates to different tasks can overlap; conflicting updates to one task serialize or use version checks without losing fields.
- **OT-A05:** Team deletion with one running member fails and preserves team/mailbox/task state.
- **OT-A06:** Exiting a dirty worktree with `remove` and no force leaves it intact; `keep` restores session paths but preserves the worktree.
- **OT-A07:** A queued user message wakes `Sleep`, retains its priority/identity, and is consumed once.
- **OT-A08:** A teammate-created cron event reaches that teammate's queue, not the leader's or a sibling's.
- **OT-A09:** Exercise `TaskCreate`, `TaskGet`, `TaskList`, `TaskUpdate`, `TaskOutput`, and `TaskStop` through the public core registry, common executor, and real permission evaluator. Preserve canonical names across compatibility aliases, relationship and metadata updates, destructive classification, background-shell output, stop state, exact tool-use correlation, and state/output recovery after manager reopen. A dependency cycle is `semantic_invalid` and leaves the graph unchanged. A task-v2 registry excludes `TodoWrite`; a legacy registry excludes task-v2 and clears completed-only todos durably.
- **OT-A10:** Hold a task-state host callback while public task tools run. Transient contention succeeds after bounded exact-sentinel retry; exhausted semantic lookup returns `unavailable`; context cancellation interrupts a mutation retry without creating work; release exposes an authoritative deterministic list rather than a spurious empty snapshot.
