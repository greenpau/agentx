# Specialized specified command workflow contracts

## Contents

1. [Scope and interpretation](#scope-and-interpretation)
2. [CMD-WF-THINKBACK-001 — Thinkback installation, generation, and playback](#cmd-wf-thinkback-001-thinkback-installation-generation-and-playback)
3. [CMD-WF-ULTRAPLAN-001 — Detached remote planning and local/remote disposition](#cmd-wf-ultraplan-001-detached-remote-planning-and-localremote-disposition)
4. [CMD-WF-RATE-LIMIT-OPTIONS-001 — Hidden rate-limit recovery menu](#cmd-wf-rate-limit-options-001-hidden-rate-limit-recovery-menu)
5. [CMD-WF-BRIDGE-KICK-001 — Internal bridge fault injection](#cmd-wf-bridge-kick-001-internal-bridge-fault-injection)
6. [CMD-WF-BRIEF-001 — Immediate brief-only output-channel toggle](#cmd-wf-brief-001-immediate-brief-only-output-channel-toggle)
7. [CMD-WF-ULTRAREVIEW-001 — Metered detached cloud bug review](#cmd-wf-ultrareview-001-metered-detached-cloud-bug-review)
8. [CMD-WF-TAG-001 — Sanitized transcript-backed session tag toggle](#cmd-wf-tag-001-sanitized-transcript-backed-session-tag-toggle)

## Scope and interpretation

These commands have complete specified implementations and therefore are not opaque profile handoffs. Build, account, feature, or internal-user gates can make them absent, but an enabled descriptor must follow the state and effect contracts below. Preserve the distinction between command completion, a delegated model query, a registered background task, and an external authority that may continue after the local UI closes.

## CMD-WF-THINKBACK-001 — Thinkback installation, generation, and playback

### Descriptors, gates, and identities

`/think-back` is an interactive local-UI command and `/thinkback-play` is a hidden local text command. Both re-evaluate the same `tengu_thinkback` feature gate. Playback explicitly rejects noninteractive use. The public command installs/enables the feature and offers a menu; the hidden command never installs and is intended for the generated skill to invoke after generation.

Select marketplace identity by product profile. The internal profile uses marketplace `agentx-code-marketplace`, repository `agentxs/agentx-code-marketplace`, while the external profile uses the official marketplace name and repository `agentxs/agentx-plugins-official`. The plugin identity is `thinkback@<marketplace>` and its expected skill directory is `<enabled-plugin-path>/skills/thinkback`.

### Installer state machine

| State | Transition, authority, and durable effect |
| --- | --- |
| `T0 checking` | Read known marketplaces and installed-plugin metadata. Snapshot whether the marketplace and plugin are already installed. No mutation yet. |
| `T1 install-or-refresh-marketplace` | If the marketplace is absent, add its GitHub repository and clear all plugin caches. If the marketplace exists but the plugin is absent, refresh that marketplace, then clear marketplace and plugin caches. Do not refresh merely to play an already installed plugin. Progress text can replace the generic phase label. |
| `T2 install-plugin` | When the initial installed-plugin snapshot says absent, install exactly the computed plugin identity. Aggregate every failed install entry into the error. On success clear all plugin caches. |
| `T3 enable-plugin` | When the initial snapshot says installed, load enabled/disabled plugins. If the matching name/source is disabled, call the plugin-enable authority; a non-success result is an error. Clear all plugin caches after successful enablement. |
| `T4 locate-skill` | Reload enabled plugins, find by name or source identity, append `skills/thinkback`, and require that directory to exist. Missing plugin/directory is a command error even if installation previously reported success. |
| `T5 inspect-artifact` | Test `<skill-dir>/year_in_review.js`. `present` opens the four-action menu; `absent` opens a one-action generation menu. |
| `T6 menu` | With an artifact, actions are `play`, `edit`, `fix`, `regenerate`; without one, the only action is `regenerate` labelled as the initial generation path. Once selected, hide the menu so input cannot commit twice. Escape completes with `display=skip`. |

Installation is not transactional. A marketplace can remain added/refreshed when plugin install fails, and an installed plugin can remain enabled when later skill-directory discovery fails. Report the failing stage and suggest the ordinary plugin manager; never claim rollback.

### Generative action contract

`edit`, `fix`, and `regenerate` do not modify the artifact locally. They complete the command with a displayed user message and `shouldQuery=true`, handing work to the model/Skill tool loop:

- `edit` requests the `thinkback` skill with `mode=edit`, tells it to ask what should change, and asks it to direct the user back to `/think-back` when ready.
- `fix` requests `mode=fix`, requires validation/error identification and repair, then the same return instruction.
- `regenerate` requests `mode=regenerate`, requires deletion/replacement of the existing animation and creation from scratch, then the same return instruction.

This is a prompt handoff, not proof that generation succeeded. Skill discovery, tool permission, and model failure remain visible through the normal query path.

### Playback contract

Playback computes `year_in_review.js` and `player.js` beneath the skill directory and reads each before terminal takeover. Missing data returns `No animation found. Run /think-back first to generate one.`; missing player returns its dedicated message; other read errors are logged and returned as access failures. Require a live renderer instance for standard output.

On success, enter the terminal alternate screen, execute `node <player.js>` with inherited standard streams and the skill directory as working directory, and always exit the alternate screen in a `finally` equivalent. Subprocess nonzero exit and interruption are tolerated because execution is non-rejecting/caught. If `year_in_review.html` exists, asynchronously invoke the platform opener (`open`, `start`, or `xdg-open`) without waiting for browser success. Then return `Year in review animation complete!`.

The `/thinkback-play` path reads installed-plugin v2 metadata, chooses the first installation, requires its install path, delegates to playback, and returns the playback message. It does not repair installation. The `/think-back` menu's `play` branch intentionally ignores the playback result and completes with display skipped after the promise resolves; preserve that observable compatibility quirk unless deliberately versioning the behavior. A rejected promise on that branch has no local catch. Alternate-screen cleanup remains mandatory even when execution is interrupted.

### Cancellation, failure, and absence

Cancel at the menu has no new effect but does not undo installer effects already committed. There is no interactive cancel transaction during marketplace/plugin operations. Installer errors call command completion with a system message and also render an error/remediation view; completion must be idempotent. Both descriptors are absent when their live feature gate is false. Exact hidden invocation while disabled must not load metadata, touch marketplaces, or take over the terminal.

## CMD-WF-ULTRAPLAN-001 — Detached remote planning and local/remote disposition

### Entry paths and launch gate

`/ultraplan <prompt>` is an internal-profile local-UI descriptor included only by the `ULTRAPLAN` build gate. It is also reachable from an ordinary interactive prompt containing a triggerable `ultraplan` word and from the plan-approval dialog with a seed plan. Headless/noninteractive input never keyword-routes it.

Keyword detection uses the pre-expansion input so pasted content cannot trigger launch. It ignores slash-prefixed input, paired quoted/tag/bracket/brace/parenthesis ranges, path/identifier adjacency (`/`, `\\`, `-`, or a file-extension dot), and a following question mark. Replace only the first triggerable word with its case-preserved `plan` suffix before forwarding expanded input as `/ultraplan ...`. Do not keyword-route while `ultraplanLaunching` or `ultraplanSessionUrl` is set.

A bare command with neither prompt nor seed returns usage and terms without analytics or launch dialog. A nonempty slash argument first rejects an already-launching/polling attempt; otherwise it stores `ultraplanLaunchPending` and completes with display skipped. The focused pre-launch dialog owns cancellation, terms/bridge choice, command echo, and calling the shared launcher. Cancel clears the pending field and creates no remote session.

### Launch and registration state machine

| State | Transition and effect |
| --- | --- |
| `U0 idle` | Require neither active URL nor launch latch. Set `ultraplanLaunching=true` synchronously before detaching; this is the duplicate-launch lock. Return the immediate “Starting AgentX on the web…” message, optionally noting that Remote Control was disconnected. |
| `U1 eligibility` | Check remote-agent eligibility. Any blocker becomes a task notification with formatted reasons and clears the launch latch in finalization. |
| `U2 prompt` | At call time select the configured first-party planning model. Build the remote message as optional `Here is a draft plan to refine`, seed plan, hidden planning instructions, then optional user blurb. Keep seed/blurb browser-visible and scaffolding in the hidden reminder. |
| `U3 create` | Call remote teleport with plan permission mode, the planning marker, default environment, abort signal, and a bundle-failure callback. A null result produces a specific session-creation/bundle task notification. |
| `U4 registered` | After creation, save session ID, derive URL, atomically set URL and clear the launch latch, append/defer the monitor message, register a `RemoteAgentTask` of type `ultraplan`, and start a detached approval poll. The task receives its own cancellation controller. |
| `U5 polling` | Poll every three seconds for up to thirty minutes. Maintain event cursor, scanner state, rejection count, and UI phase. Retry only transient network faults and fail on the fifth consecutive fault. Stop when task status is no longer `running`. |

The event scanner records ExitPlanMode tool uses/results and non-success terminal result events. For a batch, precedence is approved/teleport over terminated, then rejection, pending, unchanged. A normal rejection increments the distinct rejected-ID count and searches an older/new target on the next scan. A tool result with the teleport sentinel plus following plan chooses local execution; a non-error result must contain either approved-plan marker and chooses remote execution. Missing approved marker is an extraction failure. Phase is `plan_ready` while a plan call lacks a result, `needs_input` only for quiet idle/requires-action with no events, otherwise `running`.

### Terminal dispositions

- **Execute remotely:** Guard that the task is still running, mark it completed, clear the URL only if it still equals this poll's URL, do not archive the running remote session, and enqueue a task notification that execution continues remotely and results will arrive as a pull request.
- **Teleport locally:** Guard the running task and install `ultraplanPendingChoice={plan, sessionId, taskId}`. Leave the task running so its phase/detail remains visible. The focused choice dialog owns plan disposition, remote archive, matching URL clear, and terminal task completion.
- **Poll failure:** If still running, log reason/rejection count, notify with error and session URL, best-effort archive, clear only the matching URL, and mark the task failed. Archive failure is diagnostic and does not replace the poll failure.
- **Stop:** Delegate to `RemoteAgentTask.kill`, whose remote-task contract archives the session. Then clear URL, pending choice, and launch latch, notify that the session stopped, and enqueue a separate meta notification telling the model not to answer the stop notice. A poll awakened after stop sees non-running status and produces no duplicate failure/choice notification.

### Race and recovery invariants

Every late poll transition is conditional on task status. Every URL clear caused by a particular poll compares the captured URL so it cannot clear a newer relaunch. The launch latch is cleared in finalization, including eligibility/null/error paths. If an exception occurs after remote creation, best-effort archive the hoisted session ID and clear any active URL; do not leave an unpolled thirty-minute orphan silently. A monitor transcript message deferred behind an active query is dropped if the URL was cleared before the query becomes idle.

Cancellation before pre-launch confirmation is effect-free. Abort during remote creation is an error/abort path and may require orphan archival if the service created a session before observing it. After registration, use the explicit stop path; closing task details alone does not stop anything. Build/profile-disabled invocation performs no eligibility, teleport, task, poll, archive, or notification effect.

## CMD-WF-RATE-LIMIT-OPTIONS-001 — Hidden rate-limit recovery menu

`/rate-limit-options` is a hidden local-UI command enabled only for a first-party subscription. It is an internal routing surface shown when a rate limit is reached; exact invocation still follows normal enabled-command lookup.

On mount, read subscription type, rate-limit tier, OAuth extra-usage flag, current limit/overage status, billing access, and the `tengu_jade_anvil_4` ordering flag. Construct action options as follows:

1. If the ordinary extra-usage command is enabled, consider an extra-usage action. For Team/Enterprise users without billing access, hide it when the organization spend cap is depleted for `out_of_credits`, `org_level_disabled_until`, or `org_service_zero_credit_limit`. Otherwise label it `Request more` in rejected/warning overage state, `Request extra usage` before that state, `Add funds to continue with extra usage` when a billing-capable account already enabled extra usage, or `Switch to extra usage` otherwise.
2. Offer upgrade only when the upgrade command is enabled, the account is neither Team/Enterprise nor Max 20x.
3. Always offer `Stop and wait for limit to reset`. Put it last when the ordering flag is true and first otherwise.

Selecting upgrade or extra usage records the matching event, invokes that command with the same completion callback/context, and replaces this menu with any returned child UI. Selecting stop, Escape, or child-level cancel records cancellation and completes with display skipped. A delegated async rejection currently has no local catch and can leave the menu unsettled; do not claim rollback or guaranteed closure. A safer implementation may settle it explicitly while retaining the no-success outcome. Disabled lookup performs no account reads beyond ordinary enablement and no delegated command/browser/admin effect.

## CMD-WF-BRIDGE-KICK-001 — Internal bridge fault injection

`/bridge-kick` is an internal-profile local command, rejects noninteractive use, and reaches only a live registered bridge debug handle. Without a handle it returns a text instruction to connect Remote Control; it never implicitly starts a bridge. Split trimmed arguments on whitespace into subcommand plus two operands and apply exactly one route:

| Route | Debug-handle operation and result |
| --- | --- |
| `close <code>` | Require a finite numeric code, call `fireClose(code)`, and report that transport-close recovery was fired. |
| `poll transient` | Queue one transient `pollForWork` fault with status 503, wake the poll loop, and report it. |
| `poll <status> [type]` | Require finite numeric status. Queue one fatal poll fault; default type is `not_found_error` for 404 and `authentication_error` otherwise. Wake the poll loop. |
| `register fatal` | Queue one fatal 403 `permission_error` for `registerBridgeEnvironment`; do not trigger reconnect automatically. |
| `register <anything> [N]` | Queue transient 503 register faults; `N` is numeric-or-default-one according to the source coercion. The user must trigger close/reconnect. |
| `reconnect-session ...` | Queue two fatal 404 `not_found_error` faults for `reconnectSession`, causing reconnect strategy one to fall through to strategy two when triggered. |
| `heartbeat [status]` | Queue one fatal heartbeat fault; invalid/zero status defaults to 401. Error type is authentication for 401 and not-found otherwise. |
| `reconnect` | Call forced environment-with-session reconnect immediately. |
| `status` | Return the debug handle's current description without mutation. |
| missing/unknown | Return the complete usage text. |

This command acknowledges fault injection or trigger dispatch, not recovery success. Queued faults persist in the debug handle until consumed or the bridge owner tears down. Composite sequences deliberately accumulate faults. There is no post-dispatch cancel or rollback; cancellation must occur before command dispatch. Handle methods can throw, in which case ordinary local-command error handling owns the failure and already queued faults may remain. Internal-profile absence guarantees no debug-handle call.

## CMD-WF-BRIEF-001 — Immediate brief-only output-channel toggle

`/brief` is conditionally included by `KAIROS` or `KAIROS_BRIEF`. Its live visibility reads `tengu_kairos_brief_config`; the entire object must validate with boolean `enable_slash_command`, otherwise the all-default disabled config wins. This visibility cache may update once in the background, so re-evaluate enablement on registry reads.

The command is immediate. Read `isBriefOnly`, compute its inverse, and follow this order:

1. When turning on, check brief entitlement. Failure logs a gated toggle, completes `Brief tool is not enabled for your account`, and changes neither message opt-in nor app state. Turning off is always permitted so a gate change cannot trap the user.
2. Set user-message opt-in to the new value. This changes the model tool list and invalidates prompt caching.
3. Idempotently set app-state `isBriefOnly` to the same value.
4. Log the successful toggle.
5. Unless Kairos is active, attach one hidden system reminder to the next turn: enabled requires all user-facing output through the brief-message tool because ordinary text is hidden; disabled says the tool is unavailable and ordinary text resumes. Kairos omits the reminder because its prompt/tool availability already enforces the channel.
6. Complete with a system result saying brief-only mode enabled or disabled.

There is no UI cancellation after dispatch and no compensating rollback. A failure after message opt-in but before app-state/completion can be partial and must not be reported as success. Cancel before dispatch is a no-op. A missing build contribution, disabled config, or failed live entitlement on the on-transition must perform none of the forbidden downstream state changes.

## CMD-WF-ULTRAREVIEW-001 — Metered detached cloud bug review

### Visibility and billing gate

`/ultrareview [PR-number]` is a local-UI command whose live visibility requires `enabled=true` in the `tengu_review_bughunter_config` object. It is the sole remote bughunter entry; `/review` remains a separate local model prompt. Merely typing the word can show a prompt-input nudge, but does not launch review.

Before launch, evaluate billing:

- Team/Enterprise proceeds without consumer quota or billing note.
- Other accounts fetch quota and utilization concurrently; utilization failure becomes unknown, while an uncaught quota-fetch rejection fails command dispatch. Missing quota proceeds and defers billing to the server.
- Remaining free quota proceeds with `free ultrareview <used+1> of <limit>` in the launch note.
- Exhausted quota plus unavailable utilization proceeds without a note. With utilization, disabled Extra Usage blocks with its settings URL; available balance below $10 blocks with exact available amount; otherwise first use in the process session opens the billing confirmation dialog.
- After one confirmed, non-aborted attempt, later invocations proceed with an Extra Usage note. This is process-session state, not durable account truth.

The billing dialog offers proceed/cancel. Cancel aborts its dialog-local signal and completes `Ultrareview cancelled.` Proceed renders launching. The dialog-local signal suppresses late command completion and confirmation, but the remote launcher uses the command context's abort signal rather than this local signal. Therefore Escape during launch can hide completion while a remote session still gets created and registered; preserve this race as a documented compatibility fault or version it with an explicit remote cancel.

### Target resolution and remote launch

Run remote eligibility and ignore only `no_remote_environment`, because the synthetic code-review environment supplies placement. Any other blockers return a model-bound error block. Configure bounded positive bughunter values from live config, falling back on wrong type/range: fleet 5 max 20, duration 10 max 25 minutes, agent timeout 600 max 1800 seconds, total wall clock 22 max 27 minutes. Always set dry-run and use the synthetic code-review environment; an internal development bundle may be forwarded only under its explicit development override.

If the trimmed argument is digits only, use PR mode: require a detected `github.com` repository, target `refs/pull/<N>/head`, set PR/repository environment, and create a remote session without an initial model message. A non-GitHub repository or null teleport returns the generic launch failure.

Otherwise use branch mode: resolve default branch or `main`; compute `git merge-base <base> HEAD`; reject missing merge base; run `git diff --shortstat <merge-base>` and reject a successful empty diff. Teleport a working-tree bundle with the merge-base SHA, not a remote branch name. A null bundle/teleport returns the explicit repo-too-large instruction to push a PR.

After any successful session creation, register a `RemoteAgentTask` with type `ultrareview`, command identity/target, and remote-review marker. Return a model-bound text block containing target, approximate duration, URL, billing note, and task-notification promise; request one brief acknowledgement without repeating target/URL. Task runtime owns polling, findings, cancellation, and terminal notification.

Recoverable precondition blocks complete with `shouldQuery=true` so the model can present them. A null/unclassified launch result completes with a system failure and no query. After a non-aborted billing-dialog attempt, the session confirmation flag is set even if the launcher returned a recoverable/null failure, because confirmation follows resolved launch handling rather than proven remote creation. No branch/PR upload is rolled back after the remote service accepts it. Disabled command performs no quota, utilization, git, teleport, or task effect.

## CMD-WF-TAG-001 — Sanitized transcript-backed session tag toggle

`/tag <tag-name>` is a registered internal-profile local-UI command. Empty arguments and common help/info arguments show usage and examples; they do not attempt an empty tag. For a nonempty raw argument, recursively sanitize Unicode and trim again. Sanitization applies NFKC repeatedly and removes format, private-use, unassigned, zero-width, directional, BOM, and private-use ranges until stable, with a bounded iteration limit. If sanitation makes the value empty, return `Tag name cannot be empty`.

Require an active session ID. Read only the current session's cached tag. Then:

- If the cached tag differs, append a transcript `tag` entry with the new normalized value and session ID through `saveTag`, using the active transcript path; update the current-session tag cache; log add with `is_replacing` according to whether a prior value existed; and report `Tagged session with #<tag>`. Replacement does not require confirmation.
- If the cached tag is identical, show `Remove tag?` with `Yes, remove tag` and `No, keep tag`. Confirm appends a transcript tag entry whose value is the empty string, clears the current-session cache through the same save authority, logs confirmation, and reports removal. No/Escape logs cancellation, reports that the tag was kept, and writes nothing.

`saveTag` is append-only metadata, not an in-place transcript rewrite. Compaction/resume readers must treat the latest applicable tag entry/cache projection as authoritative. Add/replace and confirmed remove await `saveTag` inside callbacks with no local catch/finally in the specified implementation. A rejection does not call command completion and can leave the local command/UI lifecycle unsettled; it does not prove rollback of a possibly appended entry. Preserve this as a compatibility fault or make a safer implementation catch, report, and settle explicitly. Disabled lookup performs no session read or transcript append.
