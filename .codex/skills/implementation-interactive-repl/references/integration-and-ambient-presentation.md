# Integration and Ambient Presentation

## Contents

1. [Ownership boundary](#ownership-boundary)
2. [Authentication and external handoff](#authentication-and-external-handoff)
3. [IDE and language-service presentation](#ide-and-language-service-presentation)
4. [Remote-control and teleport presentation](#remote-control-and-teleport-presentation)
5. [Read-only extension browsers](#read-only-extension-browsers)
6. [Notifications, notices, tips, and progress](#notifications-notices-tips-and-progress)
7. [Local viewers and presentation failures](#local-viewers-and-presentation-failures)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Ownership boundary

These adapters display and collect intent for external services. Authentication, settings, remote transport, MCP, LSP, extension discovery, transcript persistence, and telemetry remain authoritative in their owning domains. Every operation below is keyed to a mounted workflow or stable integration identity; a late completion may update its owner, but cannot resurrect a closed screen.

**REPL-INT-001 — Non-authoritative integration state.** Connection badges, copied flags, QR text, selected rows, installation hints, timers, spinners, and local errors are presentation state. Only explicitly typed owner operations or transcript messages cross into durable state.

**REPL-INT-002 — Escaped external data.** Treat URLs, repository and branch names, remote identifiers, hook descriptions, skill metadata, IDE names, task output, and external errors as untrusted display data. Remove terminal controls and never interpret them as key hints or markup.

**REPL-INT-003 — Operation fencing.** Fence each asynchronous lookup, browser launch, preview, subscription, retry, clipboard action, or install request by mounted lifetime and current entity or invocation identity. Cancellation is advisory; stale completion is locally rejected.

## Authentication and external handoff

**REPL-INT-AUTH-001 — Console authentication states.** Model console authentication as `choose-method`, `platform-instructions`, `ready`, `waiting-for-browser`, `creating-key`, `retry-delay`, `success`, or `error`. Each state alone determines accepted keys, shown secret material, legal successor, and whether completion is available.

**REPL-INT-AUTH-002 — Method and policy selection.** Offer subscription, API-billing, and third-party-platform routes unless policy preselects subscription or API billing. A preselected method skips the chooser but remains visibly attributed. The platform route shows restart-oriented configuration guidance and returns to the chooser; it does not start an OAuth exchange.

**REPL-INT-AUTH-003 — Browser and manual callback.** Start only one browser exchange for a `ready` state. After 3,000 milliseconds in the waiting state, reveal the URL, a copy action, and manual callback input. Manual input must contain nonempty authorization-code and state parts separated by `#`; invalid input becomes a retryable error tied to the same URL. A retry waits 1,000 milliseconds before its captured successor.

**REPL-INT-AUTH-004 — Login versus token setup.** Ordinary login installs returned credentials, validates any forced organization, emits an operating-system success notification, and requires confirmation to close. Token-setup mode requests the inference-only, one-year credential, never stores it, displays it once with secret-handling guidance, and closes after a 500-millisecond render allowance without clearing the visible terminal output.

**REPL-INT-AUTH-005 — Failure and cleanup.** Normalize exchange, organization, TLS, browser, and manual-input failures into an error with a safe retry target. Prefer an actionable enterprise-TLS hint when available. Closing or superseding the workflow cancels timers and cleans the authentication service; a late exchange cannot install credentials or close a later invocation.

**REPL-INT-AUTH-006 — API-key and provider status prompts.** API-key approval, provider credential status, dangerous-mode consent, channel downgrade, and cost-limit prompts are explicit local decisions. Cancel is never approval; policy or provider ownership is shown, and the owning domain performs every mutation.

**REPL-INT-AUTH-007 — External handoff.** Desktop, browser, editor, clipboard, and operating-system open actions expose `ready`, `launching`, `success`, `not-installed`, and `error` outcomes as applicable. A launch timeout or missing executable retains a copyable fallback target. Closing the screen never implies that the external application completed its work.

## IDE and language-service presentation

**REPL-INT-IDE-001 — Auto-connect decision.** IDE connection is eligible when a saved preference, launch flag, supported integrated terminal, explicit IDE port, requested extension install, or truthy environment override enables it, unless an explicit falsy environment override disables it. Add at most one dynamic `ide` MCP entry; classify a WebSocket URL as `ws-ide` and every other supported URL as `sse-ide`.

**REPL-INT-IDE-002 — Connection projection.** Derive `connected`, `pending`, `disconnected`, or `absent` solely from the authoritative IDE client. Preserve the owner-supplied IDE name. A client replacement resets selection and notification-handler registration before the new client may publish data.

**REPL-INT-IDE-003 — Selection notifications.** Validate `selection_changed` input before use. For a real range, compute inclusive line count and subtract the ending line when its ending character is zero; retain start line, text, and file path. Empty-text updates are legitimate selection clearing. Display selected-line count before file basename, and display neither unless the IDE is connected.

**REPL-INT-IDE-004 — Mention and event notifications.** Validate IDE `at_mentioned` and logging envelopes. Convert mention line bounds from zero-based to one-based without changing the path. Ignore a handler belonging to a replaced client. Prefix forwarded IDE analytics with the IDE namespace and accept only the bounded scalar metadata contract.

**REPL-INT-IDE-005 — Onboarding and preferences.** Record onboarding once per terminal identity. Auto-connect opt-in defaults to yes and stores both choice and shown marker; disable confirmation defaults to no and changes the preference only on explicit yes. Onboarding explains active integration and closes on either confirmation gesture without creating transcript content.

**REPL-INT-IDE-006 — File-review handoff.** An IDE file-review prompt identifies the actual file and any symlink target, warns when the target is outside the workspace, and preserves accept/reject feedback by decision type. Opening a diff in an IDE is not approval; the user must still return an explicit permission decision.

**REPL-INT-IDE-007 — LSP recommendation.** Present plugin identity, description, and triggering file extension with exactly four results: install, not now, never for this plugin, or disable all recommendations. Escape and a 30,000-millisecond unattended timeout both mean `not now`. Timer cleanup and a live response reference prevent duplicate or stale settlement.

**REPL-INT-IDE-008 — Integration notifications.** Suppress local IDE/LSP notifications in remote mode. Delay an IDE discovery hint 3,000 milliseconds and show it at most five persisted times. Key disconnected, install-failure, and LSP-failure notices by identity; remove them when the condition clears, deduplicate LSP errors by source plus message, and stop LSP polling after manager initialization fails.

## Remote-control and teleport presentation

**REPL-INT-REM-001 — Common remote surface.** Remote-control, direct-connect, SSH, and teleport adapters expose send, interrupt, disconnect, loading, normalized message delivery, and correlated permission decisions through the shared session interface. A displayed remote tool request is never executed locally.

**REPL-INT-REM-002 — Remote-control dialog.** Derive status from connected, session-active, reconnecting, and error owner state. Show repository and branch only as context, and environment/session identifiers only in verbose mode. Space toggles a QR representation of the current connect/session URL; QR failure hides the code while retaining the URL. Enter or Escape closes presentation.

**REPL-INT-REM-003 — Explicit disconnect.** The raw disconnect key is active only in the remote-control dialog. It disables the live adapter; when remote control was explicitly enabled at startup, it also clears that persisted startup preference. It then settles the dialog once. Merely closing the dialog leaves the connection unchanged.

**REPL-INT-REM-004 — Environment selection.** Environment selection has `loading`, `content`, `updating`, `empty`, and `error` states. Fence the initial lookup against unmount. One available environment is informational; multiple environments show the selected value and settings source. A confirmed different value writes only the local default-environment setting after identity revalidation.

**REPL-INT-REM-005 — Teleport workflow.** Teleport presentation distinguishes eligibility failure, authentication need, repository mismatch, local-stash preparation, transfer progress, remote-session choice, resume-in-progress, operation failure, and success handoff. Keep the selected session identity with errors. Cancellation aborts only the current operation and never reports adoption.

**REPL-INT-REM-006 — Remote session selection.** List sessions by stable identifier, preserve repository scope, show bounded loading/error guidance, and classify load failure as network, authentication, API, or other only for remediation text. Retry creates a new attempt identity. Selecting a row is not resume completion.

**REPL-INT-REM-007 — Message conversion.** Convert each remote SDK event once, suppress duplicate per-turn initialization records, append normalized messages in arrival order, and stop loading on an explicit session-end event. Unknown events remain diagnosable and cannot fabricate semantic messages.

**REPL-INT-REM-008 — Permission correlation.** Turn a remote permission request into one local decision row keyed by remote request and tool-use identities. Allow may carry updated input; reject and abort carry explicit denial. Every terminal decision removes the row once and sends one correlated response.

**REPL-INT-REM-009 — Disconnect and loss window.** A transient SSH drop clears loading, emits a reconnecting warning with bounded attempt counters, and documents that the in-flight request is lost even when history later reloads. Exhausted or pre-connect failure includes bounded stderr only when useful, restores resources, and exits through orderly shutdown.

**REPL-INT-REM-010 — Adapter cleanup.** Disconnect/unmount removes listeners, closes transport or child-process ownership, stops proxy resources, clears permission callbacks and transient remote state, and fences all pending completions. Already received transcript events remain intact.

## Read-only extension browsers

**REPL-INT-EXT-001 — Hook browser states.** The hook browser is read-only and moves through event list, matcher list when supported, hook list, and hook detail. Back follows that exact hierarchy. Group hooks from the current combined built-in and MCP tool-name snapshot, and display counts without editing settings.

**REPL-INT-EXT-002 — Hook policy projection.** When all hooks are disabled, show configured count, whether managed policy caused it, and which runtime effects are absent. When managed-only policy restricts hooks, retain visible source attribution and disabled explanation. Presentation never offers a mutation that policy forbids.

**REPL-INT-EXT-003 — Hook prompt settlement.** A runtime hook prompt displays owner-supplied title, safe tool-input summary, message, and keyed options. Interrupt aborts; selecting an option returns its key exactly once. Closing or supersession cannot silently choose an option.

**REPL-INT-EXT-004 — Skill browser filtering.** Include only current skills discovered from the trusted active repository's root `.codex/skills`. Sort rows by canonical command name.

**REPL-INT-EXT-005 — Skill attribution.** File-backed groups show their effective skill path and include the legacy command path when applicable. MCP groups show distinct server-name prefixes. Each row displays canonical name, plugin identity when applicable, and an approximate frontmatter-description token count. Empty state gives creation locations.

**REPL-INT-EXT-006 — Read-only settlement.** Hook and skill browsers close to one local system result and do not append their listings to semantic history. Live source changes may refresh the owner snapshot, but selection is repaired by stable identity rather than row position.

## Notifications, notices, tips, and progress

**REPL-INT-AMB-001 — Notification record.** A notification has a stable key, one text or structured presentation, priority, optional color, and optional timeout. Adding an existing key replaces its current observation; removal is idempotent. Highest eligible priority renders first without rewriting transcript state.

**REPL-INT-AMB-002 — Once-per-session computation.** A startup-notification producer runs at most once per mounted session and never in remote mode. It may asynchronously return zero, one, or several records. Rejection is logged locally; it neither retries unboundedly nor fails startup.

**REPL-INT-AMB-003 — Reactive cleanup.** Condition-backed notifications use a stable key and remove it as soon as their condition clears. Timer callbacks recheck identity and mounted state. A dismissed or replaced notice cannot be re-added by an older async lookup.

**REPL-INT-AMB-004 — Static status-notice registry.** Evaluate each startup notice's eligibility against one authoritative context snapshot, retain definition order, and render only active entries. Large memory/agent-description warnings expose thresholds and remediation; authentication conflicts identify competing credential sources; IDE installation guidance is informational. Notice rendering never changes those sources.

**REPL-INT-AMB-005 — Tip eligibility.** Disable tips when the setting is explicitly false. Evaluate built-in relevance independently, then enforce each stable tip identity's session cooldown. When custom tips exist with `excludeDefault`, use only custom tips; otherwise append custom tips after eligible built-ins. A relevance failure excludes only that tip.

**REPL-INT-AMB-006 — Tip fairness.** Select the eligible tip with the greatest number of sessions since last shown, preserving source order on equal values. A never-shown tip has infinite age. Record the current startup counter only when shown, make same-session re-recording idempotent, and emit one sanitized shown event.

**REPL-INT-AMB-007 — Progress projection.** Spinners, agent trees, shell progress, context indicators, update status, and footer badges derive from current owner state. Animation may pause offscreen or under terminal pressure. Stalled or failed animation falls back to legible static status and never changes task/query lifecycle.

**REPL-INT-AMB-008 — Hint suppression.** Informational recommendations may be gated by build, account, platform, persisted count, cooldown, prompt activity, remote mode, or current connection. Security and failure messages outrank hints. Suppression removes the focus target and stale key guide as well as the visible row.

## Local viewers and presentation failures

**REPL-INT-VIEW-001 — Local viewer state.** Help, logs, diagnostics, export, context visualization, session preview, and status/usage panels are local workflows with explicit loading, content, empty, error, and closed states where applicable. Their filters, tabs, previews, and errors are not semantic messages.

**REPL-INT-VIEW-002 — Search and preview fencing.** Debounce or defer expensive local search, abort the prior operation, and fence results and previews to query plus selected identity. A failed preview keeps the result list usable. Closing cancels pending work and restores prior focus.

**REPL-INT-VIEW-003 — Export boundary.** Export uses an immutable message snapshot and explicit destination or clipboard action. It reports success only after the platform owner confirms the write/copy. Cancellation leaves no claimed export and does not mutate the source transcript.

**REPL-INT-VIEW-004 — Safe diagnostics.** Diagnostic and status views aggregate owner-provided facts, redact secrets, bound path/error detail, and distinguish unavailable data from a healthy empty value. Refresh failure preserves the last safe snapshot when possible.

**REPL-INT-ERR-001 — Row isolation.** A row, rich-content, or optional panel failure produces a local fallback and diagnostic while preserving the authoritative event and the rest of the screen.

**REPL-INT-ERR-002 — Safety-dialog failure.** If a decision dialog cannot render or settles during teardown, choose its explicit deny/cancel outcome. Presentation failure is never permission.

**REPL-INT-ERR-003 — Root failure.** A root presentation failure renders a bounded safe error when possible, settles callbacks, unmounts, restores terminal modes, and delegates exit status to orderly shutdown. It cannot erase durable history.

## Acceptance scenarios

**REPL-INT-A01 — Authentication retry and supersession.** Enter a malformed manual code, retry, then close and open a new login before the old exchange returns. Only the new invocation can install credentials or close; all old timers and the old service are cleaned.

**REPL-INT-A02 — IDE client replacement.** Receive a selection from IDE A, replace it with IDE B, then deliver a late A notification. Selection clears on replacement, the late event is ignored, B registers once, and no transcript entry is created.

**REPL-INT-A03 — Remote drop and recovery.** Drop an SSH session during a turn. Loading clears, the bounded reconnect warning states that in-flight work was lost, history received before the drop remains, and terminal failure cleans the manager, child process, and proxy exactly once.

**REPL-INT-A04 — Read-only extension policy.** Open hooks with managed disablement and repository-local skills. The hook view explains disablement and offers no edit; skill rows remain deterministic; closing produces only the local dismissal result.

**REPL-INT-A05 — Stale ambient completion.** Schedule an IDE hint and an asynchronous startup notice, then enter remote mode or clear the condition before completion. Neither stale result appears; existing same-key records are removed idempotently.

**REPL-INT-A06 — Tip cooldown and tie.** Provide two equally old eligible built-ins, one cooling-down tip, and one custom tip. Preserve source order for the tie, exclude the cooling tip, apply custom override policy exactly, and record only the displayed identity at the current startup count.

**REPL-INT-A07 — Local viewer cancellation.** Start log search, change query twice, open a preview, and close before completion. Every stale result is discarded, focus returns, no export or transcript mutation occurs, and reopening begins with a new invocation identity.

**REPL-INT-A08 — Presentation fault isolation.** Force rich-row, diagnostic, and safety-dialog render failures. The first two fall back locally while retaining source events; the safety request settles deny/cancel; root cleanup restores terminal state without deleting durable history.

## Non-normative provenance

Evidence was specified from authentication and external-handoff views; IDE/LSP connection, notification, and selection adapters; bridge, direct-connect, SSH, teleport, and remote-environment views; hook and skill browsers; notification and tip registries; spinner/status components; local log/export/help/diagnostic viewers; and presentation error boundaries. Current file names, component structure, implementation framework, and implementation language are provenance only.
