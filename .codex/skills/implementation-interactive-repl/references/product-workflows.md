# Interactive Product Workflows

## Contents

1. [Purpose and ownership](#purpose-and-ownership)
2. [Shared workflow contract](#shared-workflow-contract)
3. [Dialog arbitration and focus](#dialog-arbitration-and-focus)
4. [Settings, status, usage, and selectors](#settings-status-usage-and-selectors)
5. [Startup, onboarding, trust, and invalid configuration](#startup-onboarding-trust-and-invalid-configuration)
6. [Statistics, insights, and diagnostics presentation](#statistics-insights-and-diagnostics-presentation)
7. [Feedback and survey workflows](#feedback-and-survey-workflows)
8. [Conversation resume, fork, rename, preview, and search](#conversation-resume-fork-rename-preview-and-search)
9. [Fault matrix](#fault-matrix)
10. [Normal and fault-path acceptance](#normal-and-fault-path-acceptance)
11. [Non-normative provenance](#non-normative-provenance)

## Purpose and ownership

This reference specifies the presentation state machines for product-level interactive workflows that run above the shared session runtime. It covers dialog arbitration, settings/status/usage, startup gates, diagnostics and statistics, feedback, and conversation selection. These workflows may request authoritative operations, but they never become the authority for configuration, authentication, models, telemetry, transcripts, or session recovery.

Read this reference with the [dialog arbitration diagram](../assets/dialog-arbitration.drawio) and the [product workflow state-machine diagram](../assets/product-workflow-state-machines.drawio).

## Shared workflow contract

**REPL-WF-001 — Typed local workflow.** Represent every workflow as an explicit discriminated state, not as a collection of loosely related booleans. Each state declares its visible content, accepted inputs, outstanding operation, cancellation behavior, and legal successors.

**REPL-WF-002 — Stable invocation identity.** Assign every opening of a workflow a monotonically changing invocation identity. Asynchronous completion may mutate presentation state only when its captured invocation identity, operation identity, and mounted lifetime still match the current workflow.

**REPL-WF-003 — Single settlement.** A workflow invocation resolves or rejects its caller exactly once. Closing, unmounting, process exit, query cancellation, and supersession settle any pending callback with the workflow's documented cancellation result.

**REPL-WF-004 — Authority delegation.** A local workflow collects intent and presents results. The owning domain validates and performs mutations. The workflow must not reproduce settings precedence, provider authentication, model eligibility, transcript recovery, plugin installation, MCP transport, task lifecycle, or telemetry rules.

**REPL-WF-005 — Explicit busy state.** While an accepted operation is pending, show the operation and disable conflicting actions. Keep a safe cancellation action when the delegated operation is cancellable. Repeated confirm keys never launch duplicate work.

**REPL-WF-006 — Recoverable failure.** A failed delegated operation returns to the nearest state in which the user can correct input, retry, or cancel. Preserve valid input and selection. Never display success before authoritative completion.

**REPL-WF-007 — Stable selection.** Track selected entities by stable identity rather than row index. After filtering, refresh, deletion, or pagination, retain the same identity when present; otherwise select the nearest deterministic neighbor or an explicit empty state.

**REPL-WF-008 — Local-only projection.** Dialog text, search queries, previews, warnings, focus, progress spinners, and workflow errors are presentation-only. They enter the transcript or model context only through an explicitly typed domain event.

**REPL-WF-009 — Input ownership.** Exactly one visible control owns ordinary keyboard input. Global emergency cancellation and terminal-exit gestures may preempt it; all other keys are routed through the focused workflow before reaching the prompt or transcript.

**REPL-WF-010 — Disabled explanation.** If policy, build features, platform support, authentication, account eligibility, or current availability disables an action, retain a non-activatable row when useful and explain the controlling reason. Absence and disablement are distinct states.

**REPL-WF-011 — Bounded live refresh.** Polling, subscription, and deferred loading stop on close or supersession, tolerate transient failure, and use bounded intervals or event coalescing. A late refresh cannot reopen a closed workflow or replace newer state.

**REPL-WF-012 — Accessible terminal semantics.** Every action reachable by pointer input is reachable by keys; focus, selected row, active tab, busy state, validation failure, and destructive confirmation remain distinguishable without color or animation.

## Dialog arbitration and focus

### Arbitration model

The REPL may have several pending requests, but it exposes only the highest-priority eligible focus owner. Pending state is not discarded merely because a request is suppressed. Recompute eligibility after every focus, prompt-activity, tool-animation, exit, or request-queue transition.

**REPL-WF-DLG-001 — Exit suppression.** Once an exit-confirmation or terminal-exit state owns the screen, suppress all ordinary workflow dialogs. New requests remain queued or receive an explicit cancellation result according to their domain contract.

**REPL-WF-DLG-002 — Message-selection priority.** Transcript message selection may remain focused even while prompt text exists because it is an intentional modal editing state. It is the highest ordinary dialog focus owner below exit handling.

**REPL-WF-DLG-003 — Active-prompt shield.** While the prompt is actively editing or within its short activity grace interval, suppress every remaining dialog. This prevents an approval or suggestion from stealing characters intended for the prompt.

**REPL-WF-DLG-004 — Sandbox precedence.** A pending sandbox approval is the first eligible dialog after the prompt shield. It may be rendered while other tool presentation is active because it controls whether that execution may begin.

**REPL-WF-DLG-005 — Tool-animation gate.** Ordinary tool permission, hook prompt, worker sandbox, elicitation, cost, idle-return, recommendation, onboarding hint, and promotional dialogs are eligible only when no foreground tool presentation exists or that presentation has declared its animation complete.

**REPL-WF-DLG-006 — Eligible priority order.** Below the tool-animation gate, choose the first pending request in this order: tool permission; hook/user prompt; worker sandbox; MCP elicitation; cost warning; idle return; enabled plan choice and launch; IDE onboarding; internal model switch; internal compatibility notice; effort callout; remote callout; language-server recommendation; plugin hint; desktop upsell. Feature- or build-gated entries simply do not participate.

**REPL-WF-DLG-007 — Suppression indicator.** Treat sandbox permission, tool permission, hook prompt, worker sandbox, elicitation, and cost requests as materially suppressed while prompt input blocks them. Presentation may show a neutral pending indicator without exposing sensitive request content.

**REPL-WF-DLG-008 — Local overlay ownership.** Local command content is stored in a dedicated overlay slot and can be replaced or cleared only by its owning local workflow. Tool presentation never overwrites it. An overlay is considered active only when both its ownership flag and rendered content exist, preventing an invisible modal deadlock.

**REPL-WF-DLG-009 — Full-screen composition.** In full-screen mode, local workflow content is a centered modal over the transcript; tool approval is a separate approval layer. Transcript scrolling may continue behind a centered modal when the active workflow does not consume the scroll key, but prompt editing is hidden while another focus owner is active.

**REPL-WF-DLG-010 — Timing exclusion.** Time spent awaiting a permission response is excluded from active-operation elapsed time. Opening an approval restores its relevant transcript/tool position so the decision has context.

**REPL-WF-DLG-011 — Queue correlation.** Requests with protocol identities, including tool-use, elicitation, and hook-prompt identities, settle the matching request even if their visible order changes. Never resolve a request by screen position.

**REPL-WF-DLG-012 — Focus restoration.** Closing the active dialog selects the next eligible pending request. If none exists, restore the previous safe prompt/transcript focus and its cursor or scroll anchor without replaying the closing key.

### Dialog arbitration acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| User types while tool approval arrives | Prompt retains every character; approval remains pending and appears after the activity grace interval. |
| Sandbox and tool permission arrive together | Sandbox approval appears first; settling it exposes the still-pending tool permission when eligible. |
| Animated tool view is active | Cost, elicitation, and hints wait; sandbox approval may still appear. |
| Local command modal closes with another request queued | Local ownership clears; exactly one next eligible dialog receives focus. |
| A correlated request disappears remotely | Its visible dialog closes with an explicit stale/cancelled result and does not settle a different request. |

## Settings, status, usage, and selectors

### Settings shell

**REPL-WF-SET-001 — Settings tab model.** The settings shell has `Status`, `Config`, and `Usage` tabs, plus build-gated internal tabs. Preserve the selected tab while nested content is active. Hide tab chrome when a nested selector owns focus, and restore the originating row and tab when it closes.

**REPL-WF-SET-002 — Invocation snapshot.** On entry to editable configuration, capture the effective editable values and all preview-sensitive presentation values. Immediate edits may be delegated to the settings service, but top-level cancellation restores the invocation snapshot through that service.

**REPL-WF-SET-003 — Commit and revert.** Top-level confirm closes while retaining authoritative changes and emits a concise change summary. Top-level cancel reverts the snapshot and closes. Cancel inside search, a nested selector, a confirmation, or a header first exits that subordinate state and must not trigger the top-level revert.

**REPL-WF-SET-004 — Search ownership.** Configuration opens with search ready. Printable input edits the query; cancel first clears a nonempty query, then leaves search; confirm or downward navigation transfers focus to results. Navigation above the first result re-enters search. Empty results are explicit and do not retain an unreachable selection.

**REPL-WF-SET-005 — Immediate item behavior.** Boolean and enumerated changes take effect only after the settings owner accepts them. Mark the invocation dirty only on success. A managed, policy-fixed, unsupported, or invalid setting is read-only with source/reason attribution.

**REPL-WF-SET-006 — Nested selector behavior.** Theme, model, teammate model, external-instruction inclusion, output style, language, update channel, and similar compound choices open a typed nested state. A nested cancel restores any preview and returns without dirtying the setting; completion returns the authoritative choice and marks dirty only if changed.

**REPL-WF-SET-007 — Theme preview transaction.** Moving focus may preview a theme without persisting it. Confirm delegates persistence and keeps the preview; cancel or failure restores the previously committed theme. Syntax-highlighting or color-support options follow the same preview boundary and provide a legible fallback.

**REPL-WF-SET-008 — Model selector boundary.** Present eligible models, effort options, inherited/default state, and policy explanations supplied by the model/settings domains. The selector does not infer eligibility or provider routing. A mid-conversation behavior change that can invalidate expectations requires a dedicated warning and confirm state.

**REPL-WF-SET-009 — Async selector fallback.** Output styles, languages, models, or other asynchronously discovered options show loading, success, empty, and recoverable-error states. A documented built-in fallback may be offered when discovery fails, but it is labeled as fallback and cannot overwrite a later invocation.

**REPL-WF-SET-010 — Status snapshot.** Status renders synchronous session and environment facts immediately, then incorporates a single shared diagnostic result for that invocation. Include session identity, product version, working context, account/provider, selected model, IDE, MCP, sandbox, and settings-source attribution when supplied. Diagnostic failure produces an explicit unavailable section or bounded empty result rather than blocking the shell.

**REPL-WF-SET-011 — Usage state.** Usage has distinct loading, available, unavailable/error, and no-data states. Render utilization windows and extra-usage information exactly as returned by the usage service; do not derive billing conclusions locally. Retry creates a new operation identity.

**REPL-WF-SET-012 — External settings changes.** If a watched settings source changes while the workflow is open, ask the owner for a reconciled view. Preserve user focus where possible, identify conflicts with the invocation snapshot, and never silently revert external authoritative changes during local cancellation.

### Settings acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| Preview theme then cancel selector | Original committed theme is restored; invocation remains clean. |
| Change two settings then cancel top level | Both are reverted through the settings service or conflicts are explicitly reported. |
| Policy changes while model selector is open | Ineligible row becomes disabled or disappears under deterministic focus repair; stale confirm is rejected. |
| Usage request fails, then user retries | Error remains actionable; only the latest request may populate the tab. |
| Diagnostics fail after Status is visible | Settings shell remains usable and the diagnostic subsection reports unavailability. |

## Startup, onboarding, trust, and invalid configuration

These states refine the setup ordering in the session-controller reference. They do not change the rule that project-controlled integrations remain disabled until trust is resolved.

**REPL-WF-BOOT-001 — Dynamic onboarding sequence.** Build a sequential step list from runtime facts: optional account preflight; theme; environment-key approval when a new key is detected; account sign-in unless an accepted key makes it unnecessary; security explanation; optional terminal integration. Omitted steps leave no empty screen and do not alter subsequent ordering.

**REPL-WF-BOOT-002 — Forward-only onboarding.** Onboarding advances only after a step returns success or an explicitly supported skip. General back navigation is unavailable because earlier steps may already have delegated irreversible account or terminal operations.

**REPL-WF-BOOT-003 — Step-specific skip and failure.** Terminal integration may be skipped and its installation failure is presented/logged without invalidating completed onboarding. Sign-in may be skipped only when the account contract permits it. Required security acknowledgement cannot be silently bypassed.

**REPL-WF-BOOT-004 — Exit gesture.** A single terminal interrupt warns or arms exit while onboarding owns the terminal; the documented repeated interrupt/EOF gesture exits. Exiting settles onboarding as incomplete and does not mark trust or setup complete.

**REPL-WF-BOOT-005 — Trust before project activation.** Compute dangerous project-controlled surfaces before loading or invoking them. If the working location is already accepted, complete the gate without visible interaction. Otherwise show location and consequences; accept records session-only trust for the user-home special case and durable project trust for other eligible locations. Reject or cancel exits the session.

**REPL-WF-BOOT-006 — Trust idempotence.** Trust completion is idempotent and generation fenced. A late persistence callback cannot enter the REPL after rejection, exit, working-directory change, or a newer setup attempt.

**REPL-WF-BOOT-007 — Invalid configuration choice.** Invalid settings or configuration blocks normal startup and presents explicit choices appropriate to the fault: inspect details, continue with a clearly defined safe subset when permitted, reset the exact invalid source after confirmation, or exit. Never silently discard or rewrite user configuration.

**REPL-WF-BOOT-008 — First-paint safety.** Optional checks may finish after the shell paints, but trust, mandatory migrations, required authentication decisions, and required startup hooks finish before the first model request or project extension activation.

## Statistics, insights, and diagnostics presentation

**REPL-WF-OBS-001 — Statistics states.** Statistics has loading, success, no-data, and recoverable-error states. Range choices are all time, recent seven days, and recent thirty days; preserve per-range cached results and identify any fallback data as such.

**REPL-WF-OBS-002 — Statistics tabs and actions.** Provide overview and model-oriented projections supplied by the analytics owner. Tab cycles tabs, the range action cycles ranges, close keys exit, and export/copy serializes the currently rendered report without control sequences. Presentation never alters source statistics.

**REPL-WF-OBS-003 — Statistics generation fence.** Filtering or loading a range captures invocation and range identities. Until fresh data resolves, the screen may retain a labeled previous/all-time result. Late completion cannot replace a newer range or a closed screen.

**REPL-WF-OBS-004 — Doctor aggregation.** Doctor starts required installation diagnostics and may run independent agent, context, lock, plugin, and environment checks concurrently. Show a bounded loading shell until the minimum primary result exists, then progressively add categorized findings.

**REPL-WF-OBS-005 — Doctor ownership.** Doctor presents installation, executable search, update channel, sandbox, MCP, keybinding, environment, settings, lock, agent, plugin, and context findings returned by their owners. Any cleanup, including stale-lock recovery, is an explicit diagnostic-service operation rather than a UI side effect. Confirm and cancel both dismiss after pending operations are safely detached or cancelled.

**REPL-WF-OBS-006 — Diagnostic severity and absence.** Distinguish healthy, informational, warning, error, unsupported, and check-failed states. Failure of an optional check does not convert unrelated categories into healthy or crash the dialog.

**REPL-WF-OBS-007 — Insights boundary.** Insights generation is a command-driven long-running workflow, not a second local transcript. Present its progress and terminal semantic result through normal command/query projection. If an external report is produced, expose its path or open action only after successful generation and keep browser launch optional.

## Feedback and survey workflows

### Explicit feedback

**REPL-WF-FBK-001 — Feedback states.** The feedback workflow advances through `edit`, `consent`, `submitting`, and `done`. Edit retains the user's text; consent lists every data class to be attached; submitting accepts no duplicate confirmation; done reports the authoritative result.

**REPL-WF-FBK-002 — Informed consent.** Before submission, enumerate the description, bounded environment/repository metadata, transcript or diagnostic excerpt, and any other included material. Transcript inclusion is explicit. Sanitization and subagent/transcript collection are delegated to privacy and transcript owners.

**REPL-WF-FBK-003 — Failure recovery.** Submission failure returns to edit with text intact and a sanitized actionable error. Cancellation during a cancellable submission signals the delegated operation and waits for or detaches its terminal result without later showing success.

**REPL-WF-FBK-004 — Done behavior.** From done, confirm may launch an optional prefilled issue/report URL and then close; other close keys simply close. Browser launch failure is reported without resubmitting feedback.

### Lightweight survey

**REPL-WF-FBK-005 — Survey states.** A survey uses `closed`, `question`, `thanks`, `transcript-consent`, `submitting`, and `submitted`. Every opening receives a new appearance identity for analytics correlation and stale-timer rejection.

**REPL-WF-FBK-006 — Noninterference.** Do not surface a survey while a prompt, query, permission, modal, or other focus-critical state is active. If eligibility changes before display, defer or discard according to the survey policy rather than stealing focus.

**REPL-WF-FBK-007 — Optional transcript sharing.** Record the primary response before requesting optional transcript sharing. Offer explicit share, decline, and decline-future-prompts choices. Failure to submit optional transcript data still reaches a bounded thanks state.

**REPL-WF-FBK-008 — Timer fencing.** Auto-close and thanks timers capture appearance identity. Closing or reopening cancels old timers; a stale timer cannot close a newer survey or emit duplicate analytics.

## Conversation resume, fork, rename, preview, and search

The transcript/recovery domain owns log validation, graph repair, adopt-versus-fork semantics, session identity, and durable changes. This workflow owns discovery, filtering, preview, intent collection, and progress presentation.

**REPL-WF-RES-001 — Resume shell states.** The screen has `loading`, `list`, `search`, `rename`, `preview`, `resuming`, `cross-project handoff`, `empty`, and `error` states. It may progressively append pages while remaining navigable.

**REPL-WF-RES-002 — Eligible conversations.** Request same-project top-level conversations by default and exclude subagent sidechains unless the owning recovery domain explicitly exposes them as resumable roots. Optional pull-request or launch filters are owner-supplied predicates, not filename guesses.

**REPL-WF-RES-003 — Stable conversation identity.** Selection uses durable session/log identity. Sorting, paging, title changes, branch filtering, worktree filtering, or refreshed metadata must not cause an action to target a different row.

**REPL-WF-RES-004 — Progressive pagination.** Load the next page near the list tail with a single in-flight page request. Deduplicate by stable identity, retain ordering rules, and show an inline loading/error sentinel. Retry only the failed page and ignore late results from an older filter generation.

**REPL-WF-RES-005 — Scope filters.** Support current branch, current worktree, tagged or launch-supplied scope, and all-project views when available. Toggling all projects invalidates outstanding discovery, shows new loading state, and deterministically repairs selection.

**REPL-WF-RES-006 — Text search.** Printable input may enter search; query changes are debounced and generation fenced. Cancel first exits or clears search according to the visible control. Search results preserve stable identity and never reuse a stale page cursor from another query.

**REPL-WF-RES-007 — Agent-assisted search.** If enabled, agent-assisted search is a separately labeled cancellable operation with its own result/error state. Abort it on query change, close, or supersession. Its ranking cannot silently mutate transcript metadata.

**REPL-WF-RES-008 — Preview.** Preview loads the selected conversation through the transcript owner and shows loading, content, missing/corrupt, and error states. Confirm selects the same captured identity; a list refresh while preview is open cannot redirect confirm to another conversation.

**REPL-WF-RES-009 — Rename.** Rename edits title metadata only through the transcript owner. Keep the editor open on validation or persistence failure. On success, update every visible projection of the same stable identity without changing its ordering unless the declared sort requires it.

**REPL-WF-RES-010 — Resume transaction.** Confirm enters `resuming`, disables conflicting actions, requests validation/recovery, and mounts the REPL only after authoritative restoration succeeds. Failure returns to list or error with the original selection and a retry action; partial state is never treated as an active session.

**REPL-WF-RES-011 — Fork versus adopt.** Present fork/adopt intent when the entrypoint supports both, but pass that intent to the recovery owner. A fork receives the owner's new identity while retaining declared ancestry; adopt resumes the original identity. UI caches are re-keyed from the returned identity.

**REPL-WF-RES-012 — Cross-project handoff.** When a selected conversation belongs to another project and direct safe resume is unavailable, do not mutate the current session. Produce a quoted/escaped handoff command or equivalent launch instruction, copy only with user intent, and show the target location and next action. Handoff failure remains recoverable.

### Resume acceptance scenarios

| Scenario | Required observation |
| --- | --- |
| Filter changes during page load | Old page result is discarded; no cross-filter rows or cursor leak into the new list. |
| Selected log is deleted before confirm | Recovery reports missing; no adjacent row is resumed accidentally. |
| Preview completes after selection changes | Preview is discarded or remains explicitly pinned to its captured identity. |
| Rename persistence fails | Original title remains authoritative; editor retains proposed text and shows retry/cancel. |
| Restore detects interrupted tool calls | Recovery owner reconciles them before REPL mount; local workflow remains in resuming/progress. |
| Cross-project item selected | Current project session is untouched; explicit handoff guidance is shown. |

## Fault matrix

| Fault | Local response | Delegated authority |
| --- | --- | --- |
| Workflow unmounted with request pending | Cancel/detach, settle once, invalidate invocation | Operation owner defines whether work can continue |
| Policy changes mid-selector | Disable stale action, refresh choices, repair focus | Settings/model/policy domain |
| Authentication expires | Present reauthentication or unavailable state | Authentication/network domain |
| Diagnostics partially fail | Preserve successful categories and label failed checks | Each diagnostic owner |
| Transcript discovery returns corrupt entry | Quarantine row from confirm, allow details/reporting | Transcript/recovery domain |
| Terminal too small | Render compact scrollable fallback; preserve focus/action | Terminal renderer |
| External browser/editor cannot launch | Keep result/path visible and offer retry/copy | Platform lifecycle domain |

## Normal and fault-path acceptance

**REPL-WF-A01 — Normal product-workflow path.** A user can enter each workflow, navigate entirely by keyboard, complete or cancel it, return to the prior focus and scroll anchor, and observe no unintended transcript entry.

**REPL-WF-A02 — Async supersession path.** Opening, closing, and reopening any asynchronous workflow before its first request completes leaves only the newest invocation capable of changing the screen.

**REPL-WF-A03 — Delegated rejection path.** When an owner rejects a mutation because of validation, policy, authentication, or concurrent change, the workflow reports the exact safe reason, preserves recoverable input, and does not claim success.

**REPL-WF-A04 — Cancellation path.** Interrupting onboarding, feedback, diagnostics, selector discovery, or resume settles callbacks once, aborts cancellable work, and prevents late navigation or modal resurrection.

**REPL-WF-A05 — Disabled-build path.** Omitting internal, account-gated, platform-gated, or remotely unavailable features does not leave an empty tab, unreachable focus target, dangling key hint, or unsatisfied caller.

## Non-normative provenance

Evidence was specified from the reference interactive screen shell; settings, status, usage, model/theme/output-style selectors; onboarding and trust screens; statistics and doctor screens; feedback and survey controllers; conversation selectors; and resume launch flow. Current component names, source paths, and implementation-language mechanisms are provenance only.
