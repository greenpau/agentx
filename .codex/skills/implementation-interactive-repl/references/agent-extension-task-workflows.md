# Agent, Extension, MCP, Task, and Team Workflows

## Contents

1. [Purpose and ownership](#purpose-and-ownership)
2. [Agent discovery, creation, viewing, editing, and deletion](#agent-discovery-creation-viewing-editing-and-deletion)
3. [Plugin discovery, installation, configuration, and removal](#plugin-discovery-installation-configuration-and-removal)
4. [MCP management, authentication, and elicitation](#mcp-management-authentication-and-elicitation)
5. [Background task dialog](#background-task-dialog)
6. [Team and teammate presentation](#team-and-teammate-presentation)
7. [Cross-domain asynchronous safety](#cross-domain-asynchronous-safety)
8. [Disabled and fault-state matrix](#disabled-and-fault-state-matrix)
9. [Normal and fault-path acceptance](#normal-and-fault-path-acceptance)
10. [Non-normative provenance](#non-normative-provenance)

## Purpose and ownership

This reference specifies interactive presentation state machines for managing agents, plugins, MCP servers, background tasks, and teams. It defines selection, confirmation, progress, refresh, cancellation, and failure behavior. It does not define the schemas or execution semantics of those domains.

Read it with the [agent, extension, and task workflow diagram](../assets/agent-extension-task-workflows.drawio). The plugin domain owns manifests, installation, dependency checks, and cache lifecycle; the MCP domain owns configuration, transport, authentication, and protocol validation; the task and multi-agent domains own lifecycle, authority, persistence profile, and cancellation; the settings domain owns storage precedence.

The shared workflow contracts `REPL-WF-001` through `REPL-WF-012` apply throughout this reference.

## Agent discovery, creation, viewing, editing, and deletion

### Agent menu

**REPL-WF-AGT-001 — Agent menu states.** The agent workflow has `list`, `create`, `actions`, `view`, `edit`, `delete-confirm`, `busy`, and `error` states. Returning from a subordinate state preserves the selected agent by stable `(agent identity, source identity)` rather than its display name or row position.

**REPL-WF-AGT-002 — Source attribution.** Group same-named agent definitions by their authoritative source and expose precedence/source labels. A deterministic presentation order is built-in, user, project, local, managed policy, launch flag, then plugin, unless the agent registry supplies a stronger ordering. Never merge different sources into an apparently editable single row.

**REPL-WF-AGT-003 — Editability.** Built-in, launch-provided, plugin-provided, and managed immutable agents can be viewed but not locally edited or deleted. Show the controlling source and an explanation. Editable sources delegate file location and authorization to the agent/settings owners.

**REPL-WF-AGT-004 — Live reconciliation.** Subscribe to the agent registry while open. If the selected agent changes, refresh its view without losing the source identity. If it disappears, close the subordinate state to the repaired list selection and display a bounded notice.

**REPL-WF-AGT-005 — Change summary.** Accumulate only successfully committed changes for the invocation. On exit, return a concise typed summary to the caller so the REPL can present or act on changes. Cancelled previews and failed writes are not changes.

### Creation wizard

The wizard is a forward/back state machine with explicit validation per step. Supported steps are location, creation method, optional generation, type/name, prompt, description, tools, model, color, optional memory, and confirmation. Availability may skip steps, but every transition records the data and validation version that justified it.

**REPL-WF-AGT-006 — Location and method.** First select an allowed definition source/location, then choose manual authoring or assisted generation. Disable locations blocked by policy or filesystem authority. Generation is optional and may prefill later steps but never commits an agent.

**REPL-WF-AGT-007 — Name validation.** Require an agent type/name between 3 and 50 characters, beginning and ending with an alphanumeric character and otherwise containing only the supported alphanumeric/hyphen form. Validate collisions against the selected source and the current registry immediately before save, not only when leaving the step.

**REPL-WF-AGT-008 — Content validation.** Require a useful prompt with a minimum of 20 characters; show a nonblocking quality warning for unusually long or weak content. Validate description and tool references through their owners. Distinguish a blocking schema error from a quality warning the user may explicitly accept.

**REPL-WF-AGT-009 — Assisted generation.** Generation has `idle`, `generating`, `generated`, `cancelled`, and `error` states. Capture invocation plus generation identity and own an abort signal. Restarting invalidates the previous attempt before launching the next; an old completion or `finally` action cannot clear the new spinner, replace fields, or advance steps.

**REPL-WF-AGT-010 — Generated-data review.** Treat generated name, description, prompt, tools, model, and other fields as untrusted draft input. Run the same validation and confirmation as manual input. A generation shortcut may jump to the first incomplete review step, never directly to persistence.

**REPL-WF-AGT-011 — Atomic save boundary.** Confirmation submits one complete normalized definition to the agent owner. The owner writes atomically or returns failure. Update the live agent registry and report success only after durable save succeeds; partial files and optimistic list entries are not accepted outcomes.

**REPL-WF-AGT-012 — Save-and-edit handoff.** If offered, `save and edit externally` first performs the same successful save, then requests the platform owner to open the exact created definition. Editor-launch failure keeps the saved result visible and offers path copy/retry. Explain when runtime reload or restart is required.

### Editing and deletion

**REPL-WF-AGT-013 — Structured edit.** Editing may enter tool, color, model, prompt/file, or other typed sub-editors. Each captures the definition revision it opened. Save performs compare-and-apply through the owner; concurrent changes require reload, overwrite confirmation when safe, or cancel. Failed save remains in edit with user input intact.

**REPL-WF-AGT-014 — Destructive delete.** Deletion always identifies name and source, requires an explicit confirmation, and targets the captured stable identity. After confirmation, disable repeats until authoritative completion. Failure restores actions with the agent still selected; success refreshes the registry and selects a deterministic neighbor. Never delete a newly shadowing same-named definition.

### Agent acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| Generation A is cancelled and B starts immediately | A cannot clear B's busy state or populate B's fields. |
| Same agent name exists in user and project sources | View/edit/delete targets the explicitly selected source. |
| Definition changes externally during edit | Save detects revision conflict and never silently overwrites it. |
| Save succeeds but external editor launch fails | Agent remains created; path and retry/copy actions are visible. |
| Delete fails | Agent remains in registry and the dialog reports failure rather than closing as success. |

## Plugin discovery, installation, configuration, and removal

### Plugin settings shell

**REPL-WF-PLG-001 — Plugin tabs.** The plugin shell exposes discover, installed, marketplaces, and errors views when available. Initial arguments may deep-link to a tab or stable plugin identity. A child search/editor owns cancel before the shell closes.

**REPL-WF-PLG-002 — Plugin identity.** Address a plugin by canonical identity plus marketplace/source and installed version, never display name alone. Rows distinguish discoverable, installed, disabled, update-available, incompatible, blocked, configuration-required, and broken states.

**REPL-WF-PLG-003 — Initial load and refresh.** Initial discovery has loading, content, empty, and error states. If the installation registry changes on disk after runtime registries were assembled, mark the view `refresh required` and offer the supported reload action. Do not pretend that commands, agents, hooks, MCP, or language servers changed live when the extension owner requires restart/reload.

**REPL-WF-PLG-004 — Search and empty reasons.** Search owns printable input and cancel while active. Empty results identify whether no marketplace is configured, discovery failed, filters matched nothing, policy hid entries, or all results are already installed. Selection follows `REPL-WF-007`.

### Operations

**REPL-WF-PLG-005 — Details before mutation.** Details present source, version, capabilities, trust/policy status, requested configuration, and the exact available actions supplied by the plugin owner. Remote metadata is untrusted display input and cannot inject key hints or terminal controls.

**REPL-WF-PLG-006 — Install transaction.** Installation moves through `review`, optional `configure`, `installing`, and `result`. Confirm exactly one captured identity/version/source. Progress reflects owner events. On failure, retain details and safe retry; on success, report whether reload is needed.

**REPL-WF-PLG-007 — Configuration flow.** Model configuration as an ordered list of typed required/optional fields supplied by the owner. Skip the flow immediately if no fields exist. Each step validates before advance; back preserves prior answers; cancel submits nothing. Use a current callback/operation identity so a stale completion cannot call a callback from an earlier opening.

**REPL-WF-PLG-008 — Enable and disable.** Treat enable/disable as pending until authoritative completion. A pending toggle is nonrepeatable and visually distinct from the committed state. Failure snaps back to authoritative state with reason. Explain the activation boundary and reload requirement.

**REPL-WF-PLG-009 — Uninstall confirmation.** Uninstall identifies plugin, source, scope, version, and affected project configuration. Project-scoped removal requires explicit scope confirmation. Do not remove user data merely because executable plugin content is removed.

**REPL-WF-PLG-010 — Data-cleanup confirmation.** If optional plugin data or configuration may be deleted, present a second, separate destructive confirmation enumerating exact stores and recovery implications. Declining cleanup still permits a successfully completed ordinary uninstall when supported.

**REPL-WF-PLG-011 — Capability inspection.** Plugin-contributed MCP servers and tools may be inspected through nested details, but their enablement and protocol state are delegated to the extension and MCP owners. Returning preserves the plugin identity and scroll anchor.

**REPL-WF-PLG-012 — Error routing.** Show plugin-specific process errors inline. Also expose persistent discovery/load errors through the diagnostic workflow. Sanitized error text includes source and remediation without rendering secrets, raw control characters, or arbitrary markup.

### Plugin acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| Installation registry changes externally | View marks refresh required and does not claim new runtime capabilities are active. |
| Toggle fails after optimistic key press | Row returns to authoritative state and reports reason. |
| Configuration has zero fields | Flow proceeds directly to the operation without a blank modal. |
| Project uninstall chosen | Scope and affected configuration are confirmed before mutation. |
| Cleanup declined after uninstall | Plugin removal result is preserved; data remains and is reported. |

## MCP management, authentication, and elicitation

### Server management

**REPL-WF-MCP-001 — MCP management states.** Management uses `server-list`, `server-actions`, `server-tools`, `tool-detail`, `agent-server-actions`, `authenticating`, `auth-callback`, `reconnecting`, `busy`, and `error` states. Stable server identity includes configured scope and owner-supplied key.

**REPL-WF-MCP-002 — Scope grouping.** Group configured servers by project, local, user, enterprise/managed, and dynamic scope when supplied. Preserve unavailable and disabled servers with status reason when they remain actionable; never merge equal display names across scopes.

**REPL-WF-MCP-003 — Status projection.** Present connecting, connected, disconnected, failed, authentication-required, disabled, policy-blocked, and unavailable states from the MCP owner. UI does not infer health solely from tool discovery or transport process existence.

**REPL-WF-MCP-004 — Reconnect and toggle.** A stdio/local server may expose reconnect or toggle actions; a remote server may expose authenticate, reconnect, or clear-auth actions. Submit captured server identity and disable repeats. Completion after close updates the owner but not the closed screen.

**REPL-WF-MCP-005 — Remote authentication.** Authentication owns an abort signal and progresses through starting, browser URL, optional manual callback input, exchanging, and result. Cancel first aborts the current authentication before navigating back. Never display credentials, authorization codes after exchange, or raw tokens.

**REPL-WF-MCP-006 — Clear authentication.** Clearing stored authentication is a destructive credential operation with explicit confirmation and target scope. After success, refresh server state and never retain secret material in local dialog history.

**REPL-WF-MCP-007 — Tool inspection.** Server tool lists and tool details are asynchronously owner-supplied, untrusted descriptions. Loading, empty, unavailable, and error states remain navigable. Tool detail completion is fenced to captured server/tool identity.

### Setup approvals

**REPL-WF-MCP-008 — Setup approval default.** Requests to add or enable an MCP server are explicit approval dialogs whose cancel/escape result is rejection. Display source, transport class, scope, command or destination in a safely escaped form, and declared capabilities before confirmation.

**REPL-WF-MCP-009 — Multi-server setup.** A multi-select setup request resolves every requested server identity exactly once: approved selections as approved and all unselected/cancelled entries as rejected. A disappearing row cannot be implicitly approved.

**REPL-WF-MCP-010 — Import flow.** Desktop or external configuration import previews normalized servers, conflicts, target scopes, and unsupported entries. Import commits only selected valid entries through the MCP configuration owner and reports per-entry success/failure.

### Runtime elicitation

MCP elicitation is a high-priority correlated runtime request governed by the dialog-arbitration contract. It is not an ordinary server-management screen and must remain available even when management UI is never opened.

**REPL-WF-MCP-011 — Elicitation correlation.** Identify every elicitation by request identity and server key. Render and settle only the matching request. Validate all returned values against the request schema immediately before response.

**REPL-WF-MCP-012 — Form elicitation.** Form mode supports typed fields, focusable action buttons, validation errors, expandable help, and asynchronous resolution for URL-backed options. A field's late resolution is fenced to request, field, and input identities. Invalid or unresolved required fields prevent acceptance, not cancellation.

**REPL-WF-MCP-013 — URL elicitation.** URL mode progresses through prompt, browser-open result, waiting, retry, dismiss, or cancel. Opening a URL never implies consent or completion. Retry is a new operation identity; dismiss/cancel returns the protocol result defined by the MCP owner.

**REPL-WF-MCP-014 — Elicitation fault closure.** If the server disconnects, request expires, schema becomes invalid, or the session cancels, close the visible request with an explicit correlated terminal result. Do not leave focus captured or send a response to a replacement request.

### MCP acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| Authentication cancelled during callback exchange | Abort is signalled; late exchange cannot show connected or expose credentials. |
| Two same-named servers exist in different scopes | Actions target the selected owner-supplied key and scope. |
| Elicitation option lookup finishes after request expiry | Result is discarded; no replacement request is mutated. |
| Multi-server approval is cancelled | Every still-pending server receives explicit rejection. |
| Tool description contains terminal escapes | Content is neutralized and cannot control rendering/input. |

## Background task dialog

### List and detail transitions

**REPL-WF-TSK-001 — Task dialog states.** The background task workflow uses `list`, typed `detail`, `stopping`, `foregrounding`, `empty`, and `error`. It may open directly to detail when given a task identity or when exactly one eligible task exists; record whether the list was skipped for correct back behavior.

**REPL-WF-TSK-002 — Task eligibility.** Display background-capable tasks returned by the task owner. Exclude a foreground local agent from the background list. A task's persistence/recovery class is owner metadata; the dialog does not label every identity-bearing task as crash durable.

**REPL-WF-TSK-003 — Stable task order.** Sort running tasks before terminal tasks, then by descending start time within the owner's declared class ordering. Present groups in this order when available: teammates including leader, shells, monitors, remote agents, local agents, workflows, and memory-consolidation work. Preserve selection by task identity.

**REPL-WF-TSK-004 — Task type dispatch.** Detail view is selected by task kind and receives only normalized owner state. Unknown/new kinds render a generic safe detail rather than crashing or silently disappearing.

**REPL-WF-TSK-005 — Direct-open back behavior.** From a direct-open detail, back closes when zero or one eligible task remains. If a second task appeared while open, back reveals the list. From ordinary navigation, back always returns to list first.

**REPL-WF-TSK-006 — Disappearance and completion.** If the viewed task disappears or stops qualifying as background, close a direct-open dialog or return to list otherwise. A workflow may retain a short owner-declared terminal grace view so final results can be read; grace expiry is identity fenced.

### Actions and live updates

**REPL-WF-TSK-007 — Stop action.** Offer stop/kill only for running kinds whose owner declares it supported. Confirmation or the documented direct key captures task identity, enters stopping, and reports terminal completion/failure. A row status change before dispatch forces revalidation.

**REPL-WF-TSK-008 — Foreground action.** Foreground is available only for leader/teammate or other owner-approved task kinds. It changes the presentation attachment, not task identity or authority. Failure leaves the task backgrounded and selected.

**REPL-WF-TSK-009 — Empty state.** Zero eligible tasks produces an explicit empty state with close action. It must not manufacture placeholder task identities or retain stale details.

**REPL-WF-TSK-010 — Update coalescing.** Subscribe to task state and coalesce bursts before rendering. If subscription reliability is not guaranteed, use a bounded fallback poll only while incomplete tasks exist. Compare task identity and meaningful status/revision so cosmetic object replacement does not reset selection or timers.

**REPL-WF-TSK-011 — Leader-only aggregate.** Show shared aggregate task progress only in the leader presentation unless the team contract explicitly delegates it. When all items complete, retain the aggregate for a short readable interval, then collapse/hide. New work cancels the hide timer and reopens without stale completion animation.

**REPL-WF-TSK-012 — Compact aggregate.** When hidden or terminal space is constrained, provide a compact summary with counts by state and a route back to the task dialog. Compact state remains presentation-only and cannot alter task visibility or lifecycle.

**REPL-WF-TSK-013 — Output and errors.** Detail may stream bounded output, status, timing, and owner-provided result. Keep output cursor/scroll independent per task identity. Sanitize terminal controls and distinguish operation failure from failure to retrieve output.

**REPL-WF-TSK-014 — Task cancellation distinction.** Cancelling the dialog closes presentation. Stopping a task invokes the owner. Interrupting the currently viewed teammate turn is separate from killing the teammate. The UI must label and route these as different actions.

**REPL-WF-TSK-015 — Notification reconciliation.** A task-completion notification may update or open a detail only when user policy and focus arbitration permit it. Deduplicate by task identity plus terminal revision; never append the notification as ordinary prompt text.

### Task acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| Dialog direct-opens the only task, then another starts | Back reveals the two-item list. |
| Selected task finishes during stop key press | Revalidation prevents a second stop; terminal result remains readable. |
| Viewed task is removed | Direct-open closes or ordinary view returns to repaired list, never a blank captured modal. |
| Poll result arrives after subscription newer revision | Older result is discarded by task revision/generation. |
| User closes task dialog | Tasks keep owner-defined lifecycle; closing alone never kills them. |

## Team and teammate presentation

**REPL-WF-TEAM-001 — Team navigation model.** Represent leader as a stable special identity and teammates by owner-supplied identity. Expanded navigation traverses leader, teammates, and a hide/collapse row with deterministic wrapping. Selection and viewing are distinct states.

**REPL-WF-TEAM-002 — Teammate detail.** Detail projects status, current task, recent bounded messages/output, permission mode, and owner-declared actions. Missing or malformed optional data degrades individual fields, not the whole screen.

**REPL-WF-TEAM-003 — Foreground/view action.** The view/foreground key attaches presentation to the captured leader or teammate identity. Auto-exit detail when that identity becomes missing, killed, failed, or irrecoverably errored. A completed teammate may remain visible until the user exits.

**REPL-WF-TEAM-004 — Interrupt versus terminate.** While a teammate is running, cancel in its attached view may abort only the current turn when the owner supports that operation. Terminating the teammate is a separate destructive action and confirmation. A completed teammate's cancel simply exits view.

**REPL-WF-TEAM-005 — Team browser states.** Team management has `team-list`, `team-detail`, `teammate-detail`, `changing-permission`, and `error`. Poll or subscribe to team/task/mailbox state with generation fencing and preserve identities across refresh.

**REPL-WF-TEAM-006 — Permission-mode change.** Permission modes and legal transitions are supplied by the multi-agent/permission owners. The UI cycles or selects among supplied choices, sends a correlated mailbox/control request, and keeps pending state until acknowledged. Failure restores the previous authoritative mode.

**REPL-WF-TEAM-007 — Shared-state absence.** If team storage, mailbox, worker backend, or permission relay is unavailable, render the precise degraded state and disable dependent actions. Local session controls remain usable.

**REPL-WF-TEAM-008 — Team focus safety.** Team status refresh never steals focus, expands a collapsed panel, or moves the user's selected identity unless that identity vanished. Completion notifications obey global dialog arbitration.

## Cross-domain asynchronous safety

**REPL-WF-ASY-001 — Composite operation key.** Every asynchronous UI operation is keyed by workflow invocation, state, stable entity identity, and attempt number. All four must match before state mutation.

**REPL-WF-ASY-002 — Abort is advisory.** Signal cancellation when supported, but also reject stale completion locally because external processes and protocol calls may ignore abort.

**REPL-WF-ASY-003 — Finalizer guard.** `finally`-equivalent cleanup checks the composite operation key. It cannot clear a spinner, error, callback, or abort handle belonging to a newer attempt.

**REPL-WF-ASY-004 — Subscription revision.** Live domain updates carry or receive a monotonically comparable revision per stable entity. Ignore older observations and reconcile equal revisions idempotently.

**REPL-WF-ASY-005 — Timer ownership.** Debounce, grace, auto-hide, and polling timers belong to an invocation/entity identity. Cancel them on transition, close, identity change, and unmount; callbacks recheck ownership.

**REPL-WF-ASY-006 — Callback currency.** Long-lived child workflows invoke the currently registered settlement callback for their invocation, exactly once. Re-rendering or reconfiguration cannot cause an old closure to receive a new result.

## Disabled and fault-state matrix

| Domain condition | Required presentation | Forbidden implication |
| --- | --- | --- |
| Agent source read-only | View plus source/reason; edit/delete disabled | That another same-named source will be edited |
| Assisted generation unavailable | Manual path remains; generation hidden/disabled with reason | Agent creation itself is unavailable |
| Marketplace offline | Installed plugins remain inspectable; discovery error/retry | Installed runtime registry is empty |
| Plugin installed but reload required | Pending-refresh badge and reload action | Contributions are already active |
| MCP server policy blocked | Status and policy source; connection actions disabled | Authentication would bypass policy |
| MCP transport disconnected | Reconnect/auth actions as supplied | Discovered tools are currently callable |
| Task output missing | Lifecycle status remains; output-unavailable reason | Task never existed or must be killed |
| Team relay unavailable | Readable cached status when safe; mutations disabled | Local permissions changed |

## Normal and fault-path acceptance

**REPL-WF-A06 — Agent lifecycle path.** Create, validate, save, view, edit, and delete an editable agent through stable source identity; every registry change follows authoritative persistence and every failure retains a recoverable state.

**REPL-WF-A07 — Plugin lifecycle path.** Discover, inspect, configure, install, enable/disable, uninstall, optionally clean data, and acknowledge reload without conflating on-disk installation with active runtime contributions.

**REPL-WF-A08 — MCP lifecycle path.** Inspect scoped servers and tools, authenticate or reconnect, approve setup, and answer correlated elicitation while cancellation and server failure settle each request exactly once.

**REPL-WF-A09 — Task lifecycle path.** Observe task updates, open typed detail, foreground or stop only supported identities, survive completion/removal races, and close presentation without changing task lifecycle.

**REPL-WF-A10 — Team lifecycle path.** Navigate stable teammate identities, view status, interrupt a turn separately from termination, and change permission mode only after owner acknowledgement.

**REPL-WF-A11 — Stale completion path.** Repeat every asynchronous action, close its screen, or change selection before completion; no old completion, rejection, finalizer, subscription, or timer mutates the current state.

**REPL-WF-A12 — Extension fault isolation.** A malformed agent, broken plugin, failed MCP server, missing task output, or unavailable team relay degrades only its owning row/workflow and never corrupts the prompt, transcript, or unrelated extension state.

## Non-normative provenance

Evidence was specified from the reference agent menu and authoring wizard; plugin discovery, installed-plugin management, marketplace, configuration, and error screens; MCP settings, authentication, setup approvals, and elicitation forms; background task list/detail views; task aggregate controller; and team/teammate views. Current component names, source paths, and implementation-language mechanisms are provenance only.
