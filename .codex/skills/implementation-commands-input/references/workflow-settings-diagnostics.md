# Settings, diagnostics, and capability workflow contracts

## Contents

1. [CMD-WF-MODEL-001 — Inspect or change the main model](#cmd-wf-model-001-inspect-or-change-the-main-model)
2. [CMD-WF-EFFORT-001 — Inspect, persist, or clear reasoning effort](#cmd-wf-effort-001-inspect-persist-or-clear-reasoning-effort)
3. [CMD-WF-FAST-001 — Toggle fast mode and reconcile model state](#cmd-wf-fast-001-toggle-fast-mode-and-reconcile-model-state)
4. [CMD-WF-SETTINGS-001 — Shared Status, Config, and Usage settings shell](#cmd-wf-settings-001-shared-status-config-and-usage-settings-shell)
5. [CMD-WF-STATS-001 — Local usage statistics viewer](#cmd-wf-stats-001-local-usage-statistics-viewer)
6. [CMD-WF-EXTRA-USAGE-001 — Extra-usage browser or administrator request](#cmd-wf-extra-usage-001-extra-usage-browser-or-administrator-request)
7. [CMD-WF-DOCTOR-001 — Installation and configuration diagnostics](#cmd-wf-doctor-001-installation-and-configuration-diagnostics)
8. [CMD-WF-ADD-DIR-001 — Add an authorized working directory](#cmd-wf-add-dir-001-add-an-authorized-working-directory)
9. [CMD-WF-AGENTS-001 — Browse, create, and edit agent definitions](#cmd-wf-agents-001-browse-create-and-edit-agent-definitions)
10. [CMD-WF-MEMORY-001 — Select/create and edit a memory file](#cmd-wf-memory-001-selectcreate-and-edit-a-memory-file)
11. [CMD-WF-PERMISSIONS-001 — Permission rule management and denial retry](#cmd-wf-permissions-001-permission-rule-management-and-denial-retry)
12. [CMD-WF-PLAN-001 — Enter or exit plan mode](#cmd-wf-plan-001-enter-or-exit-plan-mode)
13. [CMD-WF-PRIVACY-001 — Privacy settings handoff](#cmd-wf-privacy-001-privacy-settings-handoff)
14. [CMD-WF-HOOKS-001 — Hook configuration menu](#cmd-wf-hooks-001-hook-configuration-menu)
15. [CMD-WF-SANDBOX-001 — Sandbox settings and command exclusions](#cmd-wf-sandbox-001-sandbox-settings-and-command-exclusions)
16. [CMD-WF-TASKS-001 — Inspect and control asynchronous tasks](#cmd-wf-tasks-001-inspect-and-control-asynchronous-tasks)

## CMD-WF-MODEL-001 — Inspect or change the main model

Trim the full `/model [model]` argument. Common info arguments (`current`, `status`, and the shared info aliases) report the effective current model; when plan mode supplies a session override, show both override and base model, plus effort. Common help arguments show usage. Literal `default` means clear the configured model. No argument opens the picker; cancel reports that the current model was kept.

For a direct model value, perform checks before mutation: organization allowlist; account entitlement for Opus/Sonnet 1M variants; then model validation. Known aliases bypass remote validation. Preserve case for a custom provider/model value while validating. Validation rejection and validation transport failure are distinct and leave app state unchanged.

On success set the base main-loop model and clear the plan/session model override. Clear fast-mode cooldown. If fast mode is currently on and the selected model cannot support it, turn fast mode off in session state only; do not rewrite the user's fast-mode setting for this automatic compatibility downgrade. If compatible/available, retain it. Completion states whether the model is billed as extra usage and whether fast mode was turned off. Picker selection may also return effort and applies it through the same state boundary.

There is no cancellation once a direct argument is accepted, but all checks precede mutation. A picker cancel is a no-op. Model entitlement/service details are owned by auth/network; disabled picker surfaces reject locally.

## CMD-WF-EFFORT-001 — Inspect, persist, or clear reasoning effort

Trim arguments. `help`, `-h`, and `--help` show levels. Empty, `current`, or `status` reports effective effort. Accepted setting values are case-insensitive `low`, `medium`, `high`, `max`, `auto`; `unset` is an alias for `auto`. Any other value returns the exact valid-value list without state change.

For `auto`/`unset`, delete `effortLevel` from user settings and set app-state effort to the absent-value sentinel. For a concrete value, convert to a persistable value when supported and update user settings first; values not persistable for the current contract are session-only. A settings write error produces no app-state update. After successful persistence/session decision, update app state and report the level description.

The environment override `AGENTX_EFFORT_LEVEL` wins at effective-resolution time. If it conflicts, still preserve a successfully persistable user preference for future sessions and update app state, but report that the environment controls this session. For a session-only selection, report that it was not effectively applied and nothing was saved. Clearing settings while a concrete environment override exists reports that it remains in control.

## CMD-WF-FAST-001 — Toggle fast mode and reconcile model state

Gate by account/build availability and hide when disabled. Before either direct or picker mutation, await the organization fast-mode status prefetch. Arguments are case-insensitive `on` or `off`; any other/empty argument opens the picker.

Enabling clears cooldown, persists `fastMode=true` in user settings, sets session fast mode true, and switches the base model to the fast-mode model only when the current model is incompatible; that switch clears a session model override. Disabling deletes the persisted key and sets session fast mode false. Direct mode reports unavailable reason without mutation. Picker confirmation performs the same transition and shows premium pricing/extra-usage warning.

Picker cancel normally retains initial state. Exception: if organization status now makes fast mode unavailable while it was initially on, cancel forcibly turns it off and reports that policy result. Cooldown is displayed but does not by itself rewrite the preference. A settings write and app-state update are expected as one logical transition; on write failure the implementation must not advertise success. Disabled command does not prefetch.

## CMD-WF-SETTINGS-001 — Shared Status, Config, and Usage settings shell

`/status`, `/config` (`/settings`), and `/usage` open the same interactive shell with default tabs `Status`, `Config`, and `Usage` respectively. The external specified build has no Gates tab. Start one diagnostics promise when the shell mounts; diagnostic failure projects as an empty diagnostic list, not a crashed settings panel. Ctrl-C and Escape use settings keybinding semantics.

### Status tab

Status is read-only. Display product/version/runtime, working directory and account/model, permission and settings sources, IDE, MCP, sandbox, and asynchronously resolved diagnostic warnings. Closing makes no mutation.

### Usage tab

Fetch utilization on mount. Show current-session, seven-day all-model, and plan-dependent Sonnet-only limits, reset times, extra-usage state, and eligible credit/upgrade affordances. Loading error retains a retry action; `r` refetches. Escape closes. A browser/extra-usage action is a separate `CMD-WF-EXTRA-USAGE-001` handoff. Usage data is observational and failure must not alter account limits.

### Config transaction

At mount, snapshot global config, local/user settings keys touched by the panel, app state, theme-provider state, and brief/message opt-in. Individual toggles write through immediately so live preview works. The specified configurable rows are:

- auto-compact, tips, reduced motion, thinking, fast mode, prompt suggestions, speculative execution, checkpoints, verbose output, terminal progress/status/duration;
- default permission mode and optional automatic mode during plan;
- `.gitignore` file-picker behavior, copy-full-response, copy-on-select;
- update channel, theme, notification channel and task/input/agent push controls;
- output style, default view, response language, editor mode, PR-status footer, model, and diff tool;
- IDE auto-connect/auto-install, browser integration default, optional teammate mode/model, optional remote control at startup;
- external instruction includes and approval of the currently supplied custom API key where applicable.

Settings with policy/build/account/platform prerequisites are omitted or read-only. Source ownership is exact: global UI preferences go to global config; tips/reduced-motion/default-view/output-style use local settings where defined; thinking/fast/prompt suggestions/update/language/permissions use user settings where defined; model/verbose/permission effects also update live app state.

Enter outside search/submenus commits by retaining the already persisted values and returns a human-readable change summary. Escape outside search/submenus actively restores every snapshotted value: restore theme first, overwrite global config from the snapshot, restore/delete each touched local/user key, batch-restore all touched app-state fields, reconcile plan automatic mode, and restore brief opt-in. This is a compensating rollback and can itself fail at a settings boundary; failure must be surfaced rather than claiming dismissal restored everything.

Search mode owns Escape: first clear nonempty search, then leave search. A submenu hides tabs and owns its own Enter/Escape; leaving a submenu returns to Config rather than closing the shell. The outer shell must not intercept those keys. Ordinary outer Escape currently reports `Status dialog dismissed` even when another tab was selected; preserve that observable wording if compatibility is required.

## CMD-WF-STATS-001 — Local usage statistics viewer

On `/stats`, start the all-time local aggregation once. Asynchronous operation failure becomes an error state; zero sessions becomes an empty state. Render Overview and Models tabs. Date filters cycle `all → 7d → 30d → all`; derive each filtered range from locally stored session data and cache range results. When a filter changes, mark the prior asynchronous calculation cancelled so its late result cannot replace the current range. A filtered-load failure ends its spinner while preserving the last valid all-time data.

Ctrl+S copies an ANSI/plain snapshot through the clipboard adapter. Escape or Ctrl-C closes. The command is read-only apart from clipboard output and ephemeral caches. No model/API usage should be inferred from opening it. If local history/stat support is absent, show unavailable/empty rather than synthesizing values.

## CMD-WF-EXTRA-USAGE-001 — Extra-usage browser or administrator request

Gate by explicit disable flag and account overage eligibility. The registry selects exactly one descriptor: interactive UI when the session is interactive, local noninteractive otherwise. Mark the global `hasVisitedExtraUsage` flag on first entry and invalidate the current organization's overage-credit cache.

For Team/Enterprise users without billing access:

1. Fetch utilization. If extra usage is enabled with no monthly cap, terminate `already unlimited`. Fetch failure is nonfatal.
2. Check admin-request eligibility. A definite denial terminates `contact your admin`; check failure continues so the create endpoint remains authoritative.
3. Query existing pending or dismissed limit-increase requests. Any match terminates `already submitted`; query failure continues.
4. Create a `limit_increase` admin request with null details. Report enable versus increase according to current utilization. Create failure falls back to `contact your admin`.

Other eligible users open the account-appropriate browser URL: team/admin usage settings for Team/Enterprise, personal usage settings otherwise. Browser-open failure returns the URL for manual use. The interactive variant can require/login and then rerun; cancel reports interrupted. Server request creation is not rolled back by closing the UI, and a timeout can leave uncertain server state.

## CMD-WF-DOCTOR-001 — Installation and configuration diagnostics

Gate by `DISABLE_DOCTOR_COMMAND`; interactive only. On mount, run independent checks for executable installation/version/path/search, published distribution tags, settings schema excluding MCP-specific validation, environment bounds, agent definitions and failed files, context warnings, MCP/plugin errors, sandbox dependencies/configuration, and process-ID locks. Network/dist-tag failure is a diagnostic item, not a screen crash. Render results as they settle, then allow dismissal.

When PID-lock support is enabled, clean stale locks before listing active lock diagnostics. That cleanup is the command's one material mutation and is best-effort/idempotent. Do not describe the remaining checks as repairs. Cancel/dismiss after load does not reverse stale-lock cleanup. Disabled execution performs neither cleanup nor network checks. Per-check failure is isolated; a catastrophic coordinator failure produces a doctor error screen.

## CMD-WF-ADD-DIR-001 — Add an authorized working directory

Trim the entire optional path. With no path, open an input/confirmation form. With a path, validate and normalize it against the current permission context before showing confirmation. Validation failure renders a bounded help/error and then closes; it does not mutate authority.

On confirmation choose destination `session` or, when “remember” is selected, `localSettings`. Apply an `addDirectories` permission update to the latest session permission context first. Add the path to bootstrap additional-directory state if absent, then refresh sandbox configuration immediately. For remembered paths, persist the same permission update. Persistence failure is partial: the directory remains authorized for this session and the message explicitly says local save failed. Cancel before confirmation is a no-op. There is no rollback of session authorization on persistence failure.

## CMD-WF-AGENTS-001 — Browse, create, and edit agent definitions

Open with the tool registry filtered by the current permission context, then load all agent definitions with source attribution and active-state filtering. The menu can inspect details, create a custom agent, or edit eligible custom/plugin agents. Built-in/managed definitions are read-only unless their source contract says otherwise.

Creation uses a wizard to collect location/scope, name, when-to-use description, system prompt (manual or generated), allowed tools, model, and color; validate name/frontmatter/content and collisions before the first file write. The agent-definition file write is the commit point. Refresh agent definitions/app state after a successful save. Generation is advisory text only and has no filesystem authority.

Editing offers external editor, tools, model, and color. Direct structured edits rewrite the agent file, then update the color manager and live all/active-agent lists. If the file write fails, do not mutate live state. Opening an external editor is a handoff: edits can remain even when the command closes, and the current process may require restart/reload. Escape backs out one nested editor mode before leaving. Delete, if exposed by source policy, requires explicit confirmation and cannot remove built-in/plugin-owned definitions outside the permitted scope.

## CMD-WF-MEMORY-001 — Select/create and edit a memory file

Clear and prime memory-file discovery before rendering. User selects an available user/project/local memory path. For a path under the configuration home, create the parent directory recursively. Create the file exclusively if missing; treat already-exists as success so content is never truncated. Then invoke the editor adapter, preferring `VISUAL`, then `EDITOR`, then the platform default. Completion reports the relative memory path and editor source.

Cancel before selection has no effect. Once selected, the empty file can remain if editor launch fails; do not delete it because another process may have begun using it. External-editor edits are outside rollback. Clear/reload memory caches on the next context assembly so saved content becomes authoritative.

## CMD-WF-PERMISSIONS-001 — Permission rule management and denial retry

Open a rule list over the composed permission context. Allow scoped addition/removal/edit only through typed permission updates and the settings/permission service; managed rules remain read-only. Each persisted rule records its destination scope. Cancel leaves already saved rule changes intact.

The UI may select previously denied commands for retry. On retry, append one deliberate permission-retry message to the conversation with the selected commands; this is model-visible and must be distinguishable from ordinary user text. It does not execute commands directly or bypass the freshly composed permission decision. Persistence failure leaves the old effective rule set and remains in the editor.

## CMD-WF-PLAN-001 — Enter or exit plan mode

Parse only the command's supported enter/exit/toggle intent. Transition the permission/mode context through its plan-mode state machine, preserving the prior mode needed for exit and applying any configured automatic-mode-during-plan rule. A mode change can install a plan-specific model override and prompt sections; exiting clears only those plan-owned overrides. Cancel before confirmation is a no-op. Managed policy denial leaves mode unchanged. Already-in-target mode is an idempotent success. The query/context skills own subsequent plan messages and tool restrictions.

## CMD-WF-PRIVACY-001 — Privacy settings handoff

Gate by account, service availability, and managed policy. Load current privacy/retention controls from the owning service, present only user-changeable fields, and require confirmation for changes that affect data collection. Persist through service/settings authority, then refresh local privacy caches. Cancel has no new effect; server changes committed before a late refresh failure can remain and must be reported as partial. Essential-traffic-only or managed values are read-only. Provider-owned retention semantics are opaque and require the service contract.

## CMD-WF-HOOKS-001 — Hook configuration menu

On entry emit bounded command analytics, snapshot current permission-filtered tool names, and open hook configuration. Manage hook event, matcher/tool scope, command, timeout, and settings destination through hook schemas. Validate before persistence; managed/plugin-owned hooks are read-only where required. Each saved hook commits independently. Cancel leaves saved hooks and discards only unsaved form state. The menu does not execute a hook merely to save it; explicit test actions still cross shell permissions.

## CMD-WF-SANDBOX-001 — Sandbox settings and command exclusions

Before parsing arguments, reject unsupported platform (with a distinct WSL1/WSL2 message), platform excluded by enterprise enabled-platforms, or settings locked by higher-priority policy. Check dependencies and pass warnings into the interactive settings surface.

With no argument, open sandbox settings. With arguments, the only specified subcommand is `exclude <command pattern>`. Preserve all text after `exclude `, trim it, remove one pair of surrounding single/double quotes, and append it to excluded commands in local settings. Missing pattern and unknown subcommand are errors without mutation. Report the local-settings path used. Exclusion writes are immediate and not rolled back by closing the command. A policy lock always wins even for direct arguments.

## CMD-WF-TASKS-001 — Inspect and control asynchronous tasks

Snapshot the task registry and display task identity, type, status, owner, placement, and durable-output location. A task-runtime host callback may temporarily claim the registry; use its error-bearing snapshot API with bounded, context-aware retry and report persistent contention explicitly rather than projecting a false empty registry. Refresh from task events while open. Viewing output uses the task runtime's bounded retrieval contract. Kill/cancel requires explicit selection/confirmation and delegates to the owning task implementation; completion acknowledgement means cancellation requested unless the task has reached a terminal state. Escape closes without affecting tasks. A task can finish while cancellation is pending; preserve its authoritative terminal state. Output/read failure for one task does not hide others.
