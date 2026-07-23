# Multi-agent acceptance and provenance

Use these scenarios as executable conformance specifications. Each test should distinguish live task state from durable transcript, file, mailbox, worktree, and sidecar evidence as well as returned text. Inject crashes at ownership boundaries; do not mock them away.

## Contents

- [Definition and filtering scenarios](#definition-and-filtering-scenarios)
- [Spawn and context scenarios](#spawn-and-context-scenarios)
- [Background, result, and cleanup scenarios](#background-result-and-cleanup-scenarios)
- [Resume and continuation scenarios](#resume-and-continuation-scenarios)
- [Team and mailbox scenarios](#team-and-mailbox-scenarios)
- [Permission and coordinator scenarios](#permission-and-coordinator-scenarios)
- [Traceability checklist](#traceability-checklist)
- [Non-normative provenance](#non-normative-provenance)

## Definition and filtering scenarios

### MA-D01 — Required definition fields

**Contracts:** MA-DEF-002, MA-DEF-004

Given a custom definition has an empty prompt or description, when discovery runs, then that candidate is reported invalid and cannot shadow a valid lower-precedence definition.

### MA-D02 — Exact source precedence

**Contracts:** MA-DEF-003, MA-DEF-001

Given valid same-name built-in, plugin, user, project, flag, and policy definitions, when reduction runs, then policy wins and all candidates remain available for explanation in their attributed sources.

### MA-D03 — Local-settings compatibility quirk

**Contracts:** MA-DEF-005

Given the only custom definition of a type is from local settings, when active registry is reduced, then it is discoverable/displayable but not invocable; inserting it into active precedence fails conformance.

### MA-D04 — Malformed high-precedence candidate does not erase winner

**Contracts:** MA-DEF-002, MA-DEF-004

Given a valid project definition and malformed policy definition with the same type, when discovery completes, then project remains active and policy failure is reported.

### MA-D05 — Simple mode uses built-ins only

**Contracts:** MA-BLT-001, MA-OFF-001

Given plugin and user definitions exist, when simple/constrained mode starts, then only its supported built-ins are active.

### MA-D06 — Registry refresh does not mutate running child

**Contracts:** MA-PLAN-001, MA-DEF-001

Given a child starts under one definition fingerprint and the registry refreshes with a new prompt/model, when the child takes another turn, then it retains its frozen invocation plan; only later invocations use the new winner.

### MA-D07 — Explicit unknown type does not fall back

**Contracts:** MA-SEL-001, MA-INV-001

Given `subagent_type` names no active definition, when invoked, then a typed error lists available types and no ID/task/worktree is allocated.

### MA-D08 — Denied type cites source

**Contracts:** MA-FLT-002, MA-AUTH-001

Given the selected type exists but an applicable Agent rule permits only another set, when invoked, then it fails with the denying rule/source and does not substitute `general-purpose`.

### MA-D09 — Required MCP waits and succeeds

**Contracts:** MA-MCP-001

Given one required MCP server is connecting and exposes tools after 1.5 seconds, when invocation waits in 500 ms increments, then launch proceeds with the resolved tool registry before the 30-second deadline.

### MA-D10 — Required MCP permanent failure exits early

**Contracts:** MA-MCP-001, MA-OFF-001

Given a required MCP connection reaches permanent authentication failure, when readiness observes it, then invocation fails immediately with a redacted snapshot and allocates no child resources.

### MA-D11 — Wildcard is not an authority wildcard

**Contracts:** MA-FLT-001, MA-AUTH-001

Given `tools: ['*']`, when child registry is built, then global/backend exclusions and permission policy still remove or deny unsafe capabilities.

### MA-D12 — Async allowlist excludes parent-only tools

**Contracts:** MA-FLT-001, MA-TOOL-001

Given the parent has `AskUserQuestion`, `TaskOutput`, and `Agent`, when an ordinary async child pool is built, then those tools are absent unless a narrowly documented backend exception applies.

### MA-D13 — Explicit deny wins authored allow

**Contracts:** MA-FLT-001

Given `tools` allows `Read` and `Edit` while `disallowedTools` removes `Edit`, when resolved, then only `Read` remains.

### MA-D14 — Invalid tool rule does not widen

**Contracts:** MA-FLT-001, MA-OFF-001

Given an explicit tool list contains only an unresolved name, when filtered, then the error identifies it and the implementation does not fall back to wildcard.

### MA-D15 — MCP name remains permission-controlled

**Contracts:** MA-FLT-001, MA-POL-001

Given a syntactically valid `mcp__` tool passes async filtering, when its call violates managed/network policy, then it is denied through normal authorization.

### MA-D16 — Memory tools are added but still constrained

**Contracts:** MA-DEF-002, MA-POL-001

Given a memory-enabled definition has an explicit tool list without file tools, when planned, then required Read/Write/Edit memory capabilities are considered, but sandbox and permission policy still control each call.

## Spawn and context scenarios

### MA-S01 — Stable identity precedes worktree

**Contracts:** MA-ID-001, MA-WT-001

Given worktree isolation, when launch is traced, then stable agent ID exists before the `agent-<first-eight>` path is created and task metadata joins both.

### MA-S02 — Explicit type beats fork default

**Contracts:** MA-SEL-001, MA-FRK-001

Given fork experiment is active and request explicitly selects `Explore`, when invoked, then `Explore` is selected subject to authorization; no synthetic fork is created.

### MA-S03 — Recursive fork rejected

**Contracts:** MA-FRK-001, MA-CTX-001

Given current query source or prefix proves it is already a fork, when an implicit fork spawn is attempted, then it is rejected or uses the documented nonfork path without nesting a fork.

### MA-S04 — Fork pairs every tool use

**Contracts:** MA-FRK-001, MA-CTX-001

Given the current assistant message has three tool-use blocks including the Agent call, when the fork prefix is formed, then one following user message contains three matching placeholder tool results in original order.

### MA-S05 — Fork prefix remains exact

**Contracts:** MA-FRK-001

Given parent system prompt, tools, thinking setting, and context bytes are captured, when fork starts, then those prefix sections are identical; fork-specific direction appears only after the paired placeholder results.

### MA-S06 — Normal child gets selected context, not parent internals

**Contracts:** MA-CTX-001, MA-TRN-001

Given the parent has UI state, unrelated task output, and another child sidechain, when a normal child starts, then those are absent while selected repository/environment instructions and delegated prompt are present with parent attribution.

### MA-S07 — Worktree context names effective directory

**Contracts:** MA-WT-001, MA-CTX-001

Given the parent prompt mentions original checkout paths, when the child runs in a worktree, then it receives an explicit effective-directory/path-translation notice and does not blindly rewrite arbitrary text.

### MA-S08 — Mandatory background obeys disable control

**Contracts:** MA-BGD-001, MA-OFF-001

Given a remote/fork/coordinator invocation requires background and global background disable is set, when invoked, then it fails before launch instead of silently running foreground.

### MA-S09 — Definition background selects async

**Contracts:** MA-BGD-001

Given an otherwise foreground-compatible definition has `background: true`, when invoked with no override, then it returns an async launch acknowledgement tied to the registered task.

### MA-S10 — Auto-background preserves identity

**Contracts:** MA-BGD-001, MA-BGD-002

Given foreground auto-background is active and the same child runs past 120 seconds, when it detaches, then agent/transcript identity remains unchanged and no second model query begins.

### MA-S11 — Remote preconditions are complete

**Contracts:** MA-REM-001, MA-OFF-001

Given remote isolation lacks login, remote environment, repository remote/integration, or policy approval, when checked, then all actionable preconditions are returned and no remote session/task is created.

### MA-S12 — Remote launch is always asynchronous

**Contracts:** MA-REM-001, MA-TASK-001

Given all remote preconditions pass, when remote session creation succeeds, then a remote task with linked session identity and metadata is registered before a remote launch acknowledgement returns.

## Background, result, and cleanup scenarios

### MA-B01 — Output and task precede worker progress

**Contracts:** MA-BGD-002, MA-TASK-001

Given a child emits progress immediately on start, when launch is traced, then the output location/writer and live task registration already exist and the event has a stable owner.

### MA-B02 — Failure after registration remains visible

**Contracts:** MA-BGD-002, MA-CAN-001

Given task registration succeeds but process/worktree initialization fails, when launch unwinds in one live process, then the task becomes failed with recorded error and one notification-enqueue attempt rather than disappearing.

### MA-B03 — Escape does not kill background worker

**Contracts:** MA-TASK-001, MA-KIL-001

Given a running background child and a parent prompt cancellation, when Escape is handled, then the child continues; explicit task stop is required.

### MA-B04 — Task stop preserves partial output

**Contracts:** MA-KIL-001, MA-OUT-002

Given a background child has emitted useful text then is stopped, when cancellation completes, then task status is `killed`, partial text is retained/labeled partial, and no completed result is synthesized.

### MA-B05 — Tool-only last assistant falls back to text

**Contracts:** MA-OUT-002

Given the final assistant message contains only tool-use blocks and an earlier assistant message contains its explanation, when result is built, then the latest earlier assistant text becomes content.

### MA-B06 — Terminal state precedes notification

**Contracts:** MA-OUT-001, MA-TASK-001

Given a background child completes, when the notification becomes model-visible, then terminal live task state is readable and the configured output-flush attempt has finished. A write failure/partial file remains explicit rather than being mislabeled durable completion proof.

### MA-B07 — Notification crash windows are explicit

**Contracts:** MA-OUT-001

Given crashes after terminal/latch update, after in-memory enqueue, and after a remote completion becomes durable but before sidecar removal, when session recovers, then tests demonstrate the reference's possible missing completion in the first two cases and possible repeated remote completion in the third. If an implementation produces exactly-once delivery, the test must name its added durable outbox/delivery ledger as an intentional safer divergence.

### MA-B08 — Changed worktree is retained

**Contracts:** MA-WT-001, MA-ISO-001

Given the child commits or leaves file changes, when cleanup runs, then the worktree remains and result includes path/branch; cleanup never deletes the changes.

### MA-B09 — Unchanged worktree is removed idempotently

**Contracts:** MA-WT-001, MA-CLN-001

Given no head advance or changes and no hook ownership, when cleanup is called twice, then the worktree is removed once, metadata is cleared, and the second call succeeds harmlessly.

### MA-B10 — Worker finally releases all owned resources

**Contracts:** MA-CLN-001, MA-ISO-001

Given failures are injected after each partial initialization stage, when `finally` settles, then initialized MCP, hooks, timers, queues, child processes, cache/context, skills, transcript mapping, and isolation resources are closed or retain explicit owner evidence.

### MA-B11 — Parent synthesis preserves mixed outcomes

**Contracts:** MA-SYN-001, MA-OUT-002

Given two children complete and one fails, when parent synthesizes, then it identifies all three stable IDs and statuses and does not describe the overall work as uniformly successful.

### MA-B12 — One-shot built-ins omit continuation trailer

**Contracts:** MA-OUT-002

Given Explore or Plan completes, when its result is formatted, then it omits the resumable Agent ID/SendMessage/usage trailer while preserving substantive report content.

### MA-B13 — Remote intermediate result does not end long task

**Contracts:** MA-REM-001, MA-OUT-001

Given a long-running remote task emits an intermediate result, when poller processes it, then the task remains live until its declared semantic completion condition/checker succeeds.

### MA-B14 — Remote terminal metadata is not resurrected

**Contracts:** MA-REM-001, MA-CLN-001

Given a remote task reaches completed/killed, when terminal cleanup runs and the parent later resumes, then live remote metadata has been removed and the finished task is not restarted.

## Resume and continuation scenarios

### MA-R01 — No transcript means no fake resume

**Contracts:** MA-RSM-001

Given an old agent ID but no durable transcript, when continuation is requested, then resume fails explicitly and does not start a fresh agent under that identity.

### MA-R02 — Transcript repair removes invalid structures

**Contracts:** MA-RSM-001, MA-TRN-001

Given whitespace assistant records, orphan thinking, and dangling tool use, when resume implements, then it removes/repairs them and validates message/tool-result pairing before model invocation.

### MA-R03 — Fork resume does not duplicate parent prefix

**Contracts:** MA-FRK-002, MA-RSM-001

Given a persisted fork transcript already contains the parent prefix and placeholder results, when resumed, then only continuation is appended; no prior tool-use ID appears twice.

### MA-R04 — Original authorization is not re-gated by type deny

**Contracts:** MA-RSM-002, MA-AUTH-001

Given a child was validly spawned and a later Agent-type rule would deny new spawn, when its transcript is resumed, then resume proceeds without spawn re-gating, while every new tool call still obeys current managed/permission policy.

### MA-R05 — Missing worktree reports isolation loss

**Contracts:** MA-RSM-001, MA-WT-001

Given resume metadata points to a deleted worktree, when resumed, then documented cwd fallback is explicit in result/context and no replacement tree is claimed to be the original.

### MA-R06 — Existing worktree recency is refreshed

**Contracts:** MA-RSM-001, MA-WT-001

Given recorded worktree exists, when resume validates it, then child uses it and refreshes its recency/ownership marker.

### MA-R07 — Running worker queues continuation safely

**Contracts:** MA-RSM-001, MA-MSG-001

Given `SendMessage` targets a running ordinary subagent mid-stream, when routed, then message waits for the next safe query/tool-round boundary and never mutates an in-flight model request.

### MA-R08 — Evicted task resumes from disk

**Contracts:** MA-RSM-001

Given in-memory task state was evicted but transcript/metadata remain on disk, when its raw ID or registered name is messaged, then disk-backed resume starts in background with the same identity mapping.

### MA-R09 — Missing original type uses explicit compatibility fallback

**Contracts:** MA-RSM-001, MA-DEF-001

Given metadata names a removed agent type, when resume cannot resolve it, then it uses `general-purpose` only under the compatibility rule and reports that semantic change.

## Team and mailbox scenarios

### MA-T01 — Member names are case-insensitively unique

**Contracts:** MA-TEAM-002

Given members `Reviewer` and `reviewer`, when the second joins, then it receives deterministic suffix `-2` and a distinct derived `name@team` identity.

### MA-T02 — Roster stays flat

**Contracts:** MA-TEAM-003

Given a teammate calls Agent with team name/member name, when validated, then the nested teammate spawn is rejected and no roster/task entry appears.

### MA-T03 — Auto backend fallback is sticky

**Contracts:** MA-BE-001

Given auto detection cannot establish pane integration and selects in-process, when pane capability later appears, then existing session remains in-process rather than changing topology.

### MA-T04 — Explicit tmux does not silently fall back

**Contracts:** MA-BE-001

Given explicit tmux mode and no usable tmux backend, when spawn runs, then it returns a setup/error and does not launch an in-process teammate.

### MA-T05 — Pane prompt is not process argument

**Contracts:** MA-PANE-001, MA-SEC-001

Given a prompt contains shell metacharacters/secrets, when pane teammate spawns, then process arguments contain only validated identity/configuration and the prompt travels through mailbox data.

### MA-T06 — In-process prompt delivered once

**Contracts:** MA-INP-001

Given an in-process teammate starts, when its first turn and mailbox are inspected, then initial prompt appears directly once and no duplicate initial mailbox message exists.

### MA-T07 — Lead Escape leaves teammate alive

**Contracts:** MA-INP-001, MA-TASK-001

Given in-process teammate is running and lead cancels its own query, when cancellation settles, then teammate's independent abort controller remains active.

### MA-T08 — Idle teammate remains available

**Contracts:** MA-INP-002

Given teammate completes a turn with no next message/task, when idle loop runs, then it emits/records availability and remains alive until approved shutdown/abort.

### MA-T09 — Mailbox concurrent append preserves both messages

**Contracts:** MA-MBX-002

Given two writers append concurrently, when lock-protected read-modify-write completes, then the mailbox contains both records in serialized FIFO order without lost update.

### MA-T10 — Mailbox lock retry is bounded

**Contracts:** MA-MBX-002, MA-CAN-001

Given lock cannot be acquired after ten retries from 5 ms up to 100 ms, when append fails, then it returns/logs delivery uncertainty and does not write an unlocked stale array.

### MA-T11 — Structured control never becomes prompt

**Contracts:** MA-MBX-003, MA-CTX-001

Given a valid permission/shutdown/plan envelope in mailbox, when polled, then its handler consumes it and the model never sees it as plain `<teammate-message>` content.

### MA-T12 — Plain message is tagged and untrusted

**Contracts:** MA-MBX-003

Given plain teammate mail, when admitted, then it is wrapped with sender/color/summary metadata as user content, not system instructions.

### MA-T13 — Broadcast excludes sender

**Contracts:** MA-MSG-001, MA-MBX-002

Given three-member team and sender broadcasts to `*`, when writes finish, then the other two mailboxes receive one message each, sender receives none, and per-target failures do not imply cross-mailbox atomicity.

### MA-T14 — Address class does not silently fall through

**Contracts:** MA-MSG-001, MA-AUTH-001

Given a destination is recognized as a bridge address but bridge safety approval is denied, when routing runs, then it reports denial and does not send to a same-named ambient teammate.

### MA-T15 — Outbound-only bridge cannot receive cross-session message

**Contracts:** MA-MSG-001, MA-AUTH-001

Given destination is a bridge address on an outbound-only connection, when approved send is rechecked, then delivery fails safely before transport write.

### MA-T16 — Shared-task claim is atomic

**Contracts:** MA-TSK-001

Given two idle teammates race for one unowned unblocked task, when both compare-and-set, then exactly one becomes owner and the other remains idle/retries.

## Permission and coordinator scenarios

### MA-P01 — Lead is sole interactive authority

**Contracts:** MA-PERM-002, MA-AUTH-001

Given a teammate tool needs approval, when another peer attempts to answer it, then the response is rejected; only the correlated lead decision is admitted.

### MA-P02 — In-process UI bridge falls back to mailbox

**Contracts:** MA-PERM-002

Given no registered in-memory lead UI bridge, when an in-process worker requests permission, then it registers correlation before send and polls its response mailbox every 500 ms beginning after the first interval; there is no attempt limit or deadline. Withhold the response and verify it remains pending without default allow until abort clears the interval/callback and returns denial/cancellation. For a pane worker, verify the ordinary inbox poll is initially invoked and then runs every 1,000 ms.

### MA-P03 — Updated permission input is one-shot

**Contracts:** MA-PERM-002, MA-AUTH-001

Given the lead approves with `updated_input`, when the worker receives it in the exact compatibility profile, then that selected object reaches execution for the same tool-use ID without a second schema, semantic, tool-permission, rule, safety, classifier, sandbox, or prompt pass. The original request remains audit/transcript evidence and the edit grants no authority to a future ID. Inject an invalid and a newly protected selected path so the test proves the specified gap; test any safer revalidation/reprompt profile separately as an intentional divergence.

### MA-P04 — Aborted permission cannot later allow

**Contracts:** MA-PERM-002, MA-CAN-001

Given worker aborts while permission UI is pending, when lead later clicks allow, then abort/deny is terminal and the late response is discarded.

### MA-P05 — Plan-required spawn suppresses bypass

**Contracts:** MA-PLAN-002, MA-AUTH-001

Given lead uses bypass mode but teammate requires plan approval, when pane arguments/effective mode are built, then bypass is absent and teammate waits for lead plan decision.

### MA-P06 — Approved plan exits lead plan to default

**Contracts:** MA-PLAN-002

Given lead is in `plan` and approves teammate plan, when response is applied, then teammate continues in `default`, not permanently in plan and not bypass.

### MA-P07 — Stale shutdown approval is ignored

**Contracts:** MA-SHD-001, MA-CAN-001

Given an old shutdown request ID and a new pending request, when old approval arrives, then it does not stop the teammate or terminally resolve the new request.

### MA-P08 — Shutdown rejection continues worker

**Contracts:** MA-SHD-001

Given teammate rejects shutdown with reason, when lead processes response, then the worker remains active and the reason is surfaced.

### MA-P09 — Graceful shutdown has no deadline

**Contracts:** MA-SHD-001

Given a correlated shutdown request whose teammate neither approves nor exits, when the lead waits normally, then no response timeout or automatic force-kill occurs. Headless EOF continues its 500 ms close-gate poll and stays open; only an explicit kill or separate signal/session-cleanup path may force termination.

### `MA-PERM-A01` — Mailbox permission selection and weak parsing

**Contracts:** MA-PERM-002, MA-PERM-003

Send exact success with absent response, empty response, absent `updated_input`, null `updated_input`, an empty object, and a nonempty object. The first five select the original input; the nonempty object replaces it in full. In-process UI/mailbox paths report `userModified=false`; a pane path reports true only when its tool supplies an equivalence comparator that detects the difference. Exact error rejects with feedback/default text, and an unknown subtype follows the compatibility rejection branch. Malformed permission-update entries are dropped without widening authority. A failed send leaves the registered waiter pending until response or abort rather than fabricating an error response.

### MA-C01 — Coordinator has no direct edit capability

**Contracts:** MA-COORD-001, MA-COORD-002

Given coordinator mode, when its registry is inspected, then direct implementation tools such as Edit/Bash are absent from coordinator while worker definitions retain their deliberate narrow sets.

### MA-C02 — Coordinator launches async bounded prompts

**Contracts:** MA-COORD-001, MA-BGD-001

Given a decomposable goal, when coordinator acts, then it launches self-contained background worker prompts, tells the user, and yields rather than doing the work itself.

### MA-C03 — Progress is not completion evidence

**Contracts:** MA-COORD-001, MA-SYN-001

Given workers have emitted progress but no terminal task states, when coordinator responds, then it may report waiting but cannot synthesize success.

### MA-C04 — Coordinator preserves worker failure

**Contracts:** MA-COORD-001, MA-SYN-001

Given one worker completes and one fails, when terminal notifications arrive, then synthesis identifies both outcomes and includes actionable failure rather than omitting it.

### MA-C05 — Resume mode mismatch is explicit

**Contracts:** MA-COORD-001, MA-OFF-001

Given session stored coordinator mode but resumes in normal mode, when initialized, then live mode changes deliberately and warning is surfaced; tool registries do not blend.

### MA-C06 — Completion notification follows live terminal state

**Contracts:** MA-COORD-001, MA-OUT-001

Given a worker finishes, when coordinator receives its user-role task notification in the same process, then reading the live task/output by ID already returns the same terminal status/result. A separate crash test must permit the documented missing/duplicate windows rather than assuming the live latch is persisted.

## Traceability checklist

For every implementation, record evidence for:

- schema, source precedence, built-ins, and immutable plans (`MA-DEF-*`, `MA-BLT-*`, `MA-PLAN-001`);
- type selection, tool pools, MCP readiness, and permission composition (`MA-SEL-*`, `MA-FLT-*`, `MA-MCP-*`, `MA-POL-*`, `MA-TOOL-*`, `MA-AUTH-*`);
- invocation, backend/background selection, identity, context, fork, and transcript (`MA-INV-*`, `MA-BKD-*`, `MA-BGD-*`, `MA-ID-*`, `MA-CTX-*`, `MA-FRK-*`, `MA-TRN-*`);
- worktree/remote ownership and task lifecycle (`MA-WT-*`, `MA-REM-*`, `MA-ISO-*`, `MA-TASK-*`, `MA-OUT-*`, `MA-KIL-*`, `MA-CLN-*`);
- resume and continuation (`MA-RSM-*`, `MA-MSG-*`);
- teams, backends, mailboxes, permissions, plans, shared tasks, and shutdown (`MA-TEAM-*`, `MA-BE-*`, `MA-PANE-*`, `MA-INP-*`, `MA-MBX-*`, `MA-PERM-*`, `MA-PLAN-002`, `MA-TSK-*`, `MA-SHD-*`);
- coordinator and synthesis (`MA-COORD-*`, `MA-SYN-*`);
- disabled/failure behavior (`MA-OFF-*`, `MA-CAN-*`).

At least one test for each active backend must inject cancellation, process crash, transcript interruption, cleanup failure, and notification replay. Every accepted agent/task/request ID must have either provable terminal evidence, an explicitly registered still-live handoff, or a loss/orphan classification bounded by the evidence that actually survived; no test may manufacture a terminal callback from erased process-local state.

## Non-normative provenance

The contracts above are normative. These paths are source-audit provenance only and must not be required by a standalone implementation:

- Agent schema, discovery, built-ins, registry, prompt, and invocation: `tools/AgentTool/AgentTool.tsx`, `tools/AgentTool/loadAgentsDir.ts`, `tools/AgentTool/builtInAgents.ts`, `tools/AgentTool/agentToolUtils.ts`, `tools/AgentTool/prompt.ts`, `tools/AgentTool/runAgent.ts`, `tools/AgentTool/constants.ts`.
- Fork and resume: `tools/AgentTool/forkSubagent.ts`, `tools/AgentTool/resumeAgent.ts`.
- Agent memory/context helpers: `tools/AgentTool/agentMemory.ts`, `tools/AgentTool/agentMemorySnapshot.ts`.
- Local, remote, and in-process task implementations: `tasks/LocalAgentTask/LocalAgentTask.tsx`, `tasks/RemoteAgentTask/RemoteAgentTask.tsx`, `tasks/InProcessTeammateTask/InProcessTeammateTask.tsx`, `tasks/InProcessTeammateTask/types.ts`.
- Task framework, output files, and remote sidecars: `utils/task/framework.ts`, `utils/task/diskOutput.ts`, `utils/task/TaskOutput.ts`, `utils/task/sdkProgress.ts`, `utils/sessionStorage.ts`.
- Team identities, mailbox, and teammate context: `utils/teammate.ts`, `utils/teammateMailbox.ts`, `utils/teammateContext.ts`, `utils/inProcessTeammateHelpers.ts`.
- In-process and pane worker orchestration: `utils/swarm/inProcessRunner.ts`, `utils/swarm/spawnInProcess.ts`, `utils/swarm/spawnUtils.ts`, `utils/swarm/teammateInit.ts`, `utils/swarm/teamHelpers.ts`, `utils/swarm/reconnection.ts`.
- Backend selection and implementations: `utils/swarm/backends/detection.ts`, `utils/swarm/backends/registry.ts`, `utils/swarm/backends/teammateModeSnapshot.ts`, `utils/swarm/backends/InProcessBackend.ts`, `utils/swarm/backends/TmuxBackend.ts`, `utils/swarm/backends/ITermBackend.ts`, `utils/swarm/backends/PaneBackendExecutor.ts`.
- Team permission routing and synchronization: `utils/swarm/leaderPermissionBridge.ts`, `utils/swarm/permissionSync.ts`, `hooks/toolPermission/handlers/swarmWorkerHandler.ts`.
- Team, message, and shared-task tools: `tools/TeamCreateTool/TeamCreateTool.ts`, `tools/TeamDeleteTool/TeamDeleteTool.ts`, `tools/SendMessageTool/SendMessageTool.ts`, `tools/TaskCreateTool/TaskCreateTool.ts`, `tools/TaskGetTool/TaskGetTool.ts`, `tools/TaskListTool/TaskListTool.ts`, `tools/TaskUpdateTool/TaskUpdateTool.ts`.
- Coordinator mode: `coordinator/coordinatorMode.ts`.
