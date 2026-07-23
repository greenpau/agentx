# Teams, mailboxes, and coordinator

This reference defines flat agent teams, teammate backends, append-backed compatibility mailboxes, shared tasks, cross-agent messaging, permission and plan relays, shutdown, and coordinator mode.

## Contents

- [Contract map](#contract-map)
- [Team configuration and identity](#team-configuration-and-identity)
- [Teammate backend selection](#teammate-backend-selection)
- [Pane-backed teammates](#pane-backed-teammates)
- [In-process teammates](#in-process-teammates)
- [Mailbox storage](#mailbox-storage)
- [Structured mailbox protocol](#structured-mailbox-protocol)
- [Message routing](#message-routing)
- [Permission and plan authority](#permission-and-plan-authority)
- [Shared task coordination](#shared-task-coordination)
- [Shutdown and cleanup](#shutdown-and-cleanup)
- [Coordinator mode](#coordinator-mode)
- [Failure and compatibility behavior](#failure-and-compatibility-behavior)

## Contract map

| ID | Requirement |
| --- | --- |
| MA-TEAM-002 | Team identity and stable member IDs are persisted before teammate work begins. |
| MA-TEAM-003 | Team roster is flat; teammate workers cannot spawn teammates. |
| MA-BE-001 | Backend selection is snapshotted for the session and uses explicit fallback rules. |
| MA-PANE-001 | Pane processes receive explicit identity, policy, model, directory, and parent-session arguments; the initial task arrives by mailbox. |
| MA-INP-001 | In-process teammates own independent state/abort lifetime and receive the initial prompt directly, not through a duplicate mailbox path. |
| MA-INP-002 | Teammates remain alive in an idle work loop until approved shutdown/abort rather than exiting after one turn. |
| MA-MBX-002 | Mailbox append is lock-protected read-modify-write with bounded retry and FIFO array order. |
| MA-MBX-003 | Structured control envelopes are consumed by protocol handlers and never injected as plain model prompts. |
| MA-MSG-001 | Cross-agent destination resolution follows bridge, ordinary subagent, then ambient team routing with explicit safety checks. |
| MA-PERM-002 | The team lead is the sole team-level interactive permission authority; unresolved relay waits have no specified deadline. |
| MA-PERM-003 | Mailbox permission success, rejection, empty-input fallback, weak compatibility parsing, and `userModified` projection are adapter-exact. |
| MA-PLAN-002 | Plan approval is correlated with the lead and determines the teammate's post-plan permission mode. |
| MA-SHD-001 | Teammate shutdown is correlated, but ordinary response waiting has no specified timeout or automatic force-kill escalation. |
| MA-TSK-001 | Shared-task claiming respects ownership and dependency blocks; task state is not inferred from mailbox text. |
| MA-COORD-001 | Coordinator never performs implementation directly; it delegates bounded async work and synthesizes terminal results. |
| MA-COORD-002 | Coordinator/worker tool sets are deliberately narrow and mode-specific. |

## Team configuration and identity

### Persisted team record

```text
TeamConfig {
  name: string
  description?: string
  createdAt: timestamp
  leadAgentId: string
  leadSessionId?: string
  hiddenPaneIds?: string[]
  teamAllowedPaths?: string[]
  members: TeamMember[]
}

TeamMember {
  agentId: string
  name: string
  agentType?: string
  model?: string
  prompt?: string
  color?: string
  planModeRequired?: boolean
  joinedAt: timestamp
  tmuxPaneId?: string
  cwd: string
  worktreePath?: string
  sessionId?: string
  subscriptions: string[]
  backendType?: string
  isActive?: boolean
  mode?: string
}
```

Store the team under a sanitized team directory and `config.json`. Sanitize path components; never permit `..`, separators, absolute prefixes, or control characters to escape team storage.

### Member identity

Derive a deterministic base member ID:

```text
<sanitized-member-name>@<sanitized-team-name>
```

Replace `@` inside an authored name with `-` before composing. Names are unique case-insensitively. On collision, append `-2`, `-3`, and so on to both the chosen display/routing name and derived identity consistently.

Display name is a routing alias, not the durable join key. Pane ID, process ID, child session ID, worktree path, and task ID remain separate fields linked to `agentId`.

### Current identity resolution

For code running as a teammate, resolve identity in this order:

1. In-process asynchronous-local-storage context.
2. Explicit dynamic process context/command-line teammate identity.
3. Team-leader application-state fallback when running in the lead process.

Classify as team lead when resolved agent ID equals `leadAgentId`. For backward-compatible team files lacking usable agent identity, the process owning the team/session may be treated as lead, but new records must always persist explicit lead identity.

Track teams created by the current session so shutdown cleans only owned teams. Explicit team deletion unregisters that ownership before removal.

## Teammate backend selection

Available backend types are:

- in-process;
- tmux pane/window;
- iTerm2 native pane.

Requested teammate mode is `auto`, `tmux`, or `in-process` and is snapshotted at session startup. Noninteractive sessions always choose in-process when teams are otherwise supported.

### Selection algorithm

1. Explicit `in-process` selects in-process.
2. Explicit `tmux` requires a usable pane backend; inability is a setup error.
3. `auto`:
   - if already inside tmux, choose tmux;
   - otherwise, if in iTerm2 and native integration is available and tmux is not preferred, choose iTerm2;
   - otherwise use a usable tmux backend;
   - if iTerm2 lacks native integration, the setup flow may offer/configure tmux;
   - if pane detection/setup is unavailable, permanently fall back to in-process for the remainder of the session.

Cache/snapshot the decision. Do not oscillate between pane and in-process backends as environment detection changes during one team lifecycle.

Mode `auto` may fall back; explicit `tmux` does not silently switch to in-process because the user selected a visible process topology.

## Pane-backed teammates

### Spawn transaction

1. Validate team and flat-roster rule.
2. Allocate unique member name, stable agent ID, color, and working directory.
3. Decide split-pane (default) or explicit separate-window placement.
4. Construct child process arguments containing:
   - agent ID and member name;
   - team name;
   - color;
   - parent/lead session identity;
   - agent type if selected;
   - plan-required flag;
   - model and supported settings/plugin flags;
   - effective permission-mode flags;
   - working directory and worktree identity.
5. Inherit parent bypass, accept-edits, or automatic permission mode only under policy. When plan approval is required, suppress inherited bypass so plan authority cannot be evaded.
6. Spawn the terminal pane/process **without** embedding the task prompt in a shell command or process arguments.
7. Persist member and corresponding running task.
8. Deliver the initial prompt through the teammate mailbox.

Never shell-interpolate prompt text, secrets, or untrusted names. Pane process startup proves only that the runtime began; mailbox prompt delivery and task terminal state are separate.

Pane-backed teammates use process/mailbox permission routing because they cannot call the lead's in-memory UI bridge directly.

## In-process teammates

### Independent state

An in-process teammate owns:

```text
agent identity and team identity
independent abort controller
asynchronous-local-storage identity context
running shared-task identity
initial/current prompt
selected model
awaiting-plan-approval flag
permission mode (plan or default/effective)
idle state
shutdown requested/approved flags
turn and usage counters
pending inbox/control messages
child query/application state
```

Its abort controller is not linked to the lead's current query cancellation. Escape in the lead does not kill the teammate. Explicit team/task shutdown does.

### Spawn and first turn

1. Allocate identity and write team/task membership.
2. Register the in-process task/worker state.
3. Start the worker inside its identity context.
4. Pass the initial prompt directly to the first turn.
5. Do not also enqueue that prompt in the mailbox.
6. Mark the team member backend/pane field as `in-process` for UI and cleanup.

### Continuous loop

After every child turn:

1. Mark teammate idle/available.
2. Poll/wait approximately every 500 ms or wake on a local queue signal.
3. Handle approved/requested shutdown with highest priority.
4. Prefer lead messages over peer messages.
5. Handle structured permission/plan/mode/task-assignment events outside the model prompt.
6. Admit plain messages as teammate-tagged prompt content.
7. If no message exists, inspect shared tasks and claim one unowned, unblocked task atomically.
8. Remain alive while idle until abort or approved shutdown.

Do not exit merely because one assigned prompt completed. An idle notification reports availability to the lead; it is not task/process termination.

## Mailbox storage

Mailbox location is conceptually:

```text
<user-agent-data>/teams/<safe-team>/inboxes/<safe-agent>.json
```

The file is a JSON array of:

```text
MailboxMessage {
  from: string
  text: string
  timestamp: string
  read: boolean
  color?: string
  summary?: string
}
```

There is no stable per-message ID in the compatibility envelope. Ordering and read mutation therefore use array order/index and predicates; do not fabricate an ID and assume peers persist it.

### Append algorithm

1. If absent, initialize the mailbox atomically to `[]`.
2. Acquire `<mailbox>.lock` with at most ten retries, starting at 5 ms and capping at 100 ms.
3. Re-read and parse the complete array **inside** the lock.
4. Append one message with `read: false`.
5. Atomically replace/write the complete JSON array.
6. Release the lock in `finally`.

Reads of an absent or malformed mailbox return an empty list and log a safe diagnostic. Mark-read operations also lock, re-read, mutate the matched entries, and atomically write; never apply a stale pre-lock snapshot.

The compatibility low-level writer logs and returns without throwing on some persistence failures. Therefore a legacy “sent” response can mean “delivery was attempted,” not “durably appended/read.” A clean adapter should distinguish `accepted_for_delivery` from durable/read acknowledgement and must not use the legacy response as completion evidence.

Mailbox FIFO is per target file. Broadcast writes to multiple mailboxes are sequential and have no cross-mailbox atomicity.

## Structured mailbox protocol

Structured payloads are encoded in `text` but recognized by their `type`. Parse them as untrusted JSON. A valid structured control is consumed by its handler and never wrapped as a plain `<teammate-message>` prompt.

### Idle notification

```text
{
  type: "idle_notification"
  from: string
  timestamp: string
  idleReason?: "available" | "interrupted" | "failed"
  summary?: string
  completedTaskId?: string
  completedStatus?: "resolved" | "blocked" | "failed"
  failureReason?: string
}
```

### Permission request/response

Compatibility permission fields are snake case:

```text
PermissionRequest {
  type: "permission_request"
  request_id: string
  agent_id: string
  tool_name: string
  tool_use_id: string
  description: string
  input: object
  permission_suggestions: object[]
}

PermissionSuccess {
  type: "permission_response"
  request_id: string
  subtype: "success"
  response?: {
    updated_input?: object
    permission_updates?: object[]
  }
}

PermissionError {
  type: "permission_response"
  request_id: string
  subtype: "error"
  error: string
}
```

**Mailbox permission exactness (contract MA-PERM-003).** The declared success type permits `response` to be absent and
permits `updated_input` and `permission_updates` to be absent inside it. The
reference response constructor always creates the response object, but JSON
serialization omits its undefined members, yielding `{}` when neither value is
supplied. Exact `subtype: "success"` means allow: absent response, absent or
null `updated_input`, and an empty object all select the original request
input; a nonempty object replaces it in full rather than merging. Exact
`subtype: "error"` means rejection/denial and carries leader feedback, with
the response constructor defaulting a missing/empty error to `Permission
denied`. A low-level send/controller failure can instead leave the waiter
pending and is not automatically converted into this error envelope.

The compatibility mailbox recognizer parses JSON and checks only
`type: "permission_response"`; it does not runtime-validate the remaining
declared fields. Consumers treat exact success as approval and every other
subtype as rejection. They validate permission-update entries individually,
drop malformed entries, and apply valid updates in order. A hardened closed
schema is an intentional divergence. In-process direct-UI and mailbox paths
set `userModified=false` even for a changed nonempty object. A pane/process
worker routes approval through the common permission context: when its tool
defines input equivalence, a difference sets `userModified=true`; without a
comparator it remains false. None of these projections causes a second
authorization pass.

### Sandbox-domain request/response

```text
SandboxRequest {
  type: sandbox request type
  requestId: string
  workerId: string
  workerName: string
  workerColor?: string
  hostPattern: { host: string }
  createdAt: timestamp
}

SandboxResponse {
  type: sandbox response type
  requestId: string
  host: string
  allow: boolean
  timestamp: string
}
```

### Plan approval

```text
PlanApprovalRequest {
  type: "plan_approval_request"
  from: string
  timestamp: string
  planFilePath: string
  planContent: string
  requestId: string
}

PlanApprovalResponse {
  type: plan approval response type
  requestId: string
  approved: boolean
  feedback?: string
  timestamp: string
  permissionMode?: string
}
```

### Shutdown

```text
ShutdownRequest {
  type: shutdown request type
  requestId: string
  from: string
  reason?: string
  timestamp: string
}

ShutdownApproved {
  type: shutdown approved type
  requestId: string
  from: string
  timestamp: string
  paneId?: string
  backendType?: string
}

ShutdownRejected {
  type: shutdown rejected type
  requestId: string
  from: string
  reason: string
  timestamp: string
}
```

### Task, mode, and team permission updates

```text
TaskAssignment {
  type: task assignment type
  taskId: string
  subject: string
  description: string
  assignedBy: string
  timestamp: string
}

ModeSetRequest {
  type: mode-set request type
  mode: string
  from: string
}

TeamPermissionUpdate {
  type: team permission update type
  addRules: boolean
  rules: object[]
  behavior: allow | ask | deny
  destination: "session"
  directoryPath?: string
  toolName?: string
}
```

Validate request IDs, sender identity, team membership, and field types before mutation. Unknown structured types are not executed; log/ignore or expose safely as unsupported control, never as trusted instructions.

### Plain prompt projection

Only nonstructured plain mail becomes model input:

```xml
<teammate-message teammate_id="sender" color="optional" summary="optional">
message text
</teammate-message>
```

Treat contents as another agent's untrusted message, not as system policy.

## Message routing

### Send request

Plain send contains destination `to`, message text, and a short `summary` where required. Structured shutdown and plan flows use their dedicated schemas. Destination `*` broadcasts plain messages to every active teammate except sender. `@name` spelling is rejected; routing uses bare names. Structured controls cannot broadcast.

Shutdown response is accepted only when addressed to the team lead. A rejection requires a reason.

### Routing precedence for plain, nonbroadcast messages

1. **Bridge/Unix-domain cross-session address**, when that feature recognizes the destination:
   - require an explicit safety approval that cannot be bypassed by permission mode;
   - permit plain text only;
   - require an active bidirectional bridge, not outbound-only mode;
   - recheck connection/destination after approval before sending.
2. **Ordinary subagent registry**:
   - resolve registered name or validated raw agent ID;
   - queue to running worker or resume stopped worker from durable transcript.
3. **Ambient team roster/mailbox**:
   - resolve a case-insensitive unique active member name;
   - append to its mailbox.

If a higher-precedence address class recognizes but rejects the destination for safety, do not silently fall through to a same-named lower class. Report the routing/authority failure.

Plain team broadcast writes recipients sequentially and reports per-recipient attempted/failed status where possible. It excludes the sender.

## Permission and plan authority

### In-process permission path

1. Worker runs automated permission and sandbox checks.
2. If interaction is required and the registered lead UI bridge is available, send the standard tool-specific permission request in memory.
3. Lead evaluates/displays and returns allow, deny, updated input, and scoped permission updates.
4. Apply an allowed updated object under the specified [one-shot edited-approval contract](../../implementation-permissions-sandbox/references/permission-decision.md#approval-protocol) (`PERM-042`): the worker returns that selected object to execution without a second schema, semantic, tool-permission, safety, classifier, sandbox, or prompt pass. This is not authorization for another tool-use ID. Empty-object fallback and `userModified` evidence remain adapter-specific as defined by `PERM-042`.
5. Persist approved rule updates through shared leader permission context and notify the worker; persistence of those rules is distinct from selecting this invocation's execution input.
6. If the in-process lead UI queue is absent, register the response callback before appending a mailbox request, then begin a 500 ms interval that reads the worker's complete mailbox and consumes only the unread response with the correlated request ID. The first interval tick occurs after 500 ms; there is no attempt limit or response deadline.
7. A pane/process teammate instead receives permission responses through the ordinary inbox poller: it performs one initial poll when mounted and otherwise polls every 1,000 ms. The older resolved-file compatibility poller uses 500 ms intervals when mounted. Neither path supplies a maximum attempt count.
8. Abort/cancel clears the in-process fallback interval and callback and produces denial/cancellation; a correlated allow/reject also clears them. A failed mailbox send can be logged/returned by the low-level adapter without settling the already-registered waiter, so absent abort the wait can remain pending indefinitely. Never default allow.

Sandbox host/domain requests follow the same bridge-then-mailbox structure.

### Out-of-process permission path

Pane workers always use mailbox request/response. Legacy file-based permission compatibility may also exist:

```text
permissions/pending/<request-id>.json
permissions/resolved/<request-id>.json
```

Pending requests are read oldest first under lock. Compatibility request IDs use `perm-<time>-<random>`. Resolved files older than one hour are cleanup candidates. New implementations should prefer mailbox/in-memory correlation while retaining legacy reads/writes only when interoperability requires them.

### Authority rules

- The lead is the sole team-level interactive permission authority.
- Teammate permission mode does not bypass managed policy or leader safety constraints.
- Permission updates are scoped and synchronized through the authoritative permission service; mailbox text alone is not an allow rule.
- Missing lead/UI/mailbox response does not default allow, but the specified relay has no autonomous timeout: it remains pending until a correlated response, owning abort/cancel, session teardown, or process death. An implementation may add a deadline only as an explicit safer divergence with a terminal denial/error and callback/interval cleanup.
- Request cancellation/worker abort removes pending UI and causes one terminal response.

### Plan approval

A plan-required teammate starts in plan mode, writes/returns a correlated plan request, and waits for the lead. Only the lead approves/rejects. On approval, the teammate's next mode is the lead's effective permission mode, except if the lead is itself in `plan`, in which case use `default` so the teammate does not remain trapped in plan. On rejection, carry feedback back to the teammate and continue planning or stop according to the request contract.

Plan-required pane spawn never inherits bypass permissions, even if the lead normally has them.

## Shared task coordination

Shared task records, not mailbox claims, are authoritative. A teammate may auto-claim only a task that is:

- not terminal;
- unowned/unassigned;
- free of unresolved dependency blocks;
- eligible for that worker under team policy.

Claim is atomic compare-and-set against current task state. If two idle teammates race, exactly one becomes owner. A task-assignment mailbox event is a wake/notification pointing to the task ID; the worker re-reads the authoritative task before starting.

On completion, update task state (`resolved`, `blocked`, or `failed`) before sending an idle notification. Include completed task ID/status and optional failure reason. The lead may reassign blocked/failed work explicitly; no worker silently converts it to resolved.

## Shutdown and cleanup

### Graceful teammate shutdown

1. Lead creates unique shutdown request ID and sends request with reason.
2. Teammate handles shutdown ahead of ordinary prompts/tasks.
3. Teammate returns correlated approval or rejection. Rejection includes reason and worker continues.
4. On approval:
   - the teammate writes the approval before stopping;
   - an in-process backend aborts its independent controller and exits its loop;
   - a pane teammate schedules its own graceful process shutdown; the interactive lead also launches one best-effort pane-kill attempt when it observes approval, without awaiting that attempt before membership cleanup. The headless lead removes the approved member but does not add a grace timer.
5. Mark member inactive/remove membership and terminally update its task.
6. Close SDK task event, queues, permission waiters, MCP/query resources, and timers.
7. Clean or retain worktree according to change ownership.
8. Evict large output only after the result/read window.

There is no ordinary shutdown-response deadline, poll ceiling, or timer that turns a missing/rejected response into force kill. Rejection leaves the worker active. Explicit task/backend kill, signal-driven session cleanup, and orphan-pane cleanup are separate operations: they may stop a worker immediately, but they are not escalation stages of the graceful request. The headless EOF team gate polls every 500 ms and may wait forever for idle, approval, or roster cleanup. Preserve this as a named compatibility boundary; a safer bounded shutdown profile must specify its own deadline, terminal evidence, and worktree/process cleanup without attributing that timeout to the specified runtime.

### Team/session cleanup

At lead/session shutdown:

1. Identify teams created/owned by this session.
2. Kill/reconcile orphan pane-backed teammates first so no process writes into deleted team state.
3. Abort/reconcile in-process teammates.
4. Resolve pending permission/plan/shutdown requests.
5. Clean/retain member worktrees.
6. Remove team configuration/inboxes and shared task directory when ownership permits.
7. Release panes, locks, watchers, backend handles, and identity contexts.

All steps are idempotent and best-effort after terminal evidence is persisted. A failed cleanup leaves actionable path/process evidence.

## Coordinator mode

Coordinator mode requires both build inclusion and its environment/runtime control. Persisted sessions record whether they were coordinator or normal. On resume, if stored mode differs from current requested mode, switch the live mode deliberately and surface a warning; do not silently mix tool contracts.

### Coordinator tool boundary

Coordinator candidate tools are deliberately limited to:

```text
Agent
TaskStop
SendMessage
SyntheticOutput
selected subscription/coordination tools explicitly enabled
```

Simple coordinator worker definitions use only:

```text
Bash (or canonical shell tool)
Read
Edit
```

Normal coordinator-spawned workers use the asynchronous allowlist, excluding team creation/deletion, raw team messaging, and coordinator-only synthetic tools; add authorized MCP tools and skills deliberately.

### Coordinator behavior

1. Decompose the user goal into self-contained bounded worker prompts.
2. Launch workers asynchronously; never perform repository implementation directly from coordinator context.
3. Tell the user which work was launched and yield rather than polling through prose.
4. Continue an existing worker when context overlap or error recovery makes continuity valuable; launch a fresh verifier for independent verification.
5. Do not ask one implementation worker to “check” another as a substitute for coordinator synthesis/independent verification.
6. Stop unwanted work with task stop and reconcile its terminal state.
7. Receive user-role task notifications after workers reach terminal live state; suppress duplicate enqueue attempts only while the same task-state generation remains live.
8. Synthesize only terminal results, preserving each worker identity, failure, usage, and retained artifacts.

Task notification projection is conceptually:

```xml
<task-notification>
  <task-id>stable-id</task-id>
  <status>completed|failed|killed</status>
  <summary>safe summary</summary>
  <result>optional terminal result</result>
  <usage>optional attributable usage</usage>
</task-notification>
```

The notification is a user-role/model-visible event by deliberate projection. It is not the worker transcript. The task's `notified` latch is application state: compare-and-set gives at most one enqueue attempt in the same live state generation, but the latch and queue do not survive restart. A crash after latch claim can lose delivery; a durable/model-visible remote completion followed by a crash before sidecar cleanup can be observed again. A durable outbox/delivery ledger would be an intentional safer divergence.

## Failure and compatibility behavior

| Condition | Required behavior |
| --- | --- |
| Duplicate member name with case variation | Choose deterministic numbered suffix and derived stable ID |
| Teammate attempts teammate spawn | Reject; roster stays flat |
| Explicit tmux unavailable | Setup/error; no silent in-process fallback |
| Auto pane detection unavailable | Snapshot in-process fallback for session |
| Pane starts but initial mailbox write fails | Keep task/member evidence, report delivery attempt failure/uncertainty; do not claim work completed |
| Malformed mailbox JSON | Safe empty read plus diagnostic; preserve/recover file rather than executing content |
| Lock retry exhausted | Log/return attempted-delivery failure according to compatibility API; never mutate unlocked stale snapshot |
| Unknown structured mailbox type | Ignore/report unsupported; never inject as trusted control |
| Permission lead unavailable | Wait remains pending without default allow until correlated response, owning abort/teardown, or process death; no specified deadline |
| Plan approval from nonlead | Reject |
| Shutdown approval from stale request | Ignore; no unrelated worker stop |
| Two workers claim one task | Atomic state allows one owner; loser returns idle/retries |
| Coordinator worker progress only | Coordinator may report waiting but cannot synthesize completion |
| Notification races in one live process | The current task state's compare-and-set latch permits one enqueue attempt |
| Crash at notification/sidecar boundary | Classify possible missing or duplicate completion; do not claim persistent exactly-once delivery |
| Team cleanup finds changed worktree | Retain and report path; continue other cleanup |
