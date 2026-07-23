# Command workflow index

## Contents

1. [Purpose and normative interpretation](#purpose-and-normative-interpretation)
2. [Mechanical catalog reconciliation](#mechanical-catalog-reconciliation)
3. [CC-155 — Workflow coverage maintenance](#cc-155-workflow-coverage-maintenance)

## Purpose and normative interpretation

This index closes the gap between a command descriptor and the behavior of the command after dispatch. The catalog says *what can be invoked*; this index says whether invocation is prompt expansion, an atomic local operation, a implementable multi-step workflow, or a supported absence.

Use these classifications exactly:

- `workflow`: the command owns or enters a multi-step operation. The Primary workflow column names exactly one defined `CMD-WF-*` contract.
- `atomic`: the command performs one bounded local observation or state transition and returns. Its complete result semantics remain in the command catalog and descriptor contract.
- `prompt`: dispatch produces a model-bound prompt; the query engine, tools, and permissions own subsequent work. The command must not secretly perform that work locally.
- `profile`: the name belongs to a build/profile contribution whose implementation is unavailable unless that profile is included. Absence is correct; do not invent a base implementation.
- `stub`: the specified external build contains no reachable command under the intended name. A disabled descriptor named `stub` is absence, not an implementation.

For a `workflow` row, the named workflow is normative for inputs, states, authority handoff, effects, cancellation, partial failure, gates, and terminal outcomes. The workflow can route to another skill for the internals of an owned subsystem, but this skill still owns command entry and completion.

## Mechanical catalog reconciliation

| Catalog ID | Command | Classification | Primary workflow | Notes |
| --- | --- | --- | --- | --- |
| Catalog anchor: CC-001 | `init` | prompt | — | Model-bound project initialization prompt. |
| Catalog anchor: CC-002 | `pr-comments` | prompt | — | Integration authority is requested through the model/tool loop. |
| Catalog anchor: CC-003 | `review` | prompt | — | Model-bound review prompt. |
| Catalog anchor: CC-004 | `security-review` | prompt | — | Model-bound security review prompt. |
| Catalog anchor: CC-005 | `statusline` | prompt | — | Guided model prompt; noninteractive filtering remains descriptor-owned. |
| Catalog anchor: CC-006 | `insights` | workflow | CMD-WF-INSIGHTS-001 | Local collection, analysis, HTML report, optional internal upload. |
| Catalog anchor: CC-007 | `advisor` | atomic | — | Feature-owned local response with live enablement. |
| Catalog anchor: CC-008 | `clear` | workflow | CMD-WF-CLEAR-001 | Session teardown and regeneration. |
| Catalog anchor: CC-009 | `compact` | workflow | CMD-WF-COMPACT-001 | Manual compaction with memory/reactive/legacy paths. |
| Catalog anchor: CC-010 | `context` (local) | atomic | — | Noninteractive context observation. |
| Catalog anchor: CC-011 | `cost` | atomic | — | Read-only session accounting. |
| Catalog anchor: CC-012 | `files` | atomic | — | Read-only tracked-file projection. |
| Catalog anchor: CC-013 | `heapdump` | atomic | — | One diagnostic artifact write; runtime support gates it. |
| Catalog anchor: CC-014 | `install-slack-app` | workflow | CMD-WF-INTEGRATION-HANDOFF-001 | First-party browser/service handoff; server-owned completion. |
| Catalog anchor: CC-015 | `keybindings` | workflow | CMD-WF-KEYBINDINGS-001 | Local configuration editor handoff. |
| Catalog anchor: CC-016 | `release-notes` | atomic | — | Read-only bundled/retrieved release-note display. |
| Catalog anchor: CC-017 | `reload-plugins` | workflow | CMD-WF-PLUGIN-RELOAD-001 | Cache invalidation and MCP/plugin reconnect. |
| Catalog anchor: CC-018 | `rewind` | workflow | CMD-WF-REWIND-001 | Opens checkpoint selector; selector owns restore transaction. |
| Catalog anchor: CC-019 | `stickers` | workflow | CMD-WF-PRODUCT-EXPERIENCE-001 | Feature-owned experience handoff. |
| Catalog anchor: CC-020 | `thinkback-play` | workflow | CMD-WF-THINKBACK-001 | Hidden installed-plugin playback route; shares the playback contract. |
| Catalog anchor: CC-021 | `vim` | atomic | — | Toggle interactive editing mode. |
| Catalog anchor: CC-022 | `voice` | workflow | CMD-WF-PRODUCT-EXPERIENCE-001 | Voice-feature settings/handoff. |
| Catalog anchor: CC-023 | `extra-usage` (local) | workflow | CMD-WF-EXTRA-USAGE-001 | Noninteractive account/admin/browser decision flow. |
| Catalog anchor: CC-024 | `add-dir` | workflow | CMD-WF-ADD-DIR-001 | Validate, authorize, optionally persist, refresh sandbox. |
| Catalog anchor: CC-025 | `agents` | workflow | CMD-WF-AGENTS-001 | Agent configuration menu with tool-aware validation. |
| Catalog anchor: CC-026 | `branch` | workflow | CMD-WF-BRANCH-001 | Copy transcript and resume into new session. |
| Catalog anchor: CC-027 | `btw` | atomic | — | Opens an immediate side-input surface. |
| Catalog anchor: CC-028 | `chrome` | workflow | CMD-WF-BROWSER-INTEGRATION-001 | Browser integration status/setup handoff. |
| Catalog anchor: CC-029 | `color` | atomic | — | Session/UI identity mutation. |
| Catalog anchor: CC-030 | `config` | workflow | CMD-WF-SETTINGS-001 | Settings shell, default Config tab. |
| Catalog anchor: CC-031 | `context` (UI) | atomic | — | Read-only context breakdown dialog. |
| Catalog anchor: CC-032 | `copy` | atomic | — | Clipboard/terminal escape handoff for last response. |
| Catalog anchor: CC-033 | `desktop` | workflow | CMD-WF-PRODUCT-EXPERIENCE-001 | Platform-dependent desktop open/install handoff. |
| Catalog anchor: CC-034 | `diff` | workflow | CMD-WF-DIFF-001 | File-history/repository diff viewer. |
| Catalog anchor: CC-035 | `doctor` | workflow | CMD-WF-DOCTOR-001 | Parallel diagnostics plus stale-lock cleanup. |
| Catalog anchor: CC-036 | `effort` | workflow | CMD-WF-EFFORT-001 | Validate, persist where allowed, update live state. |
| Catalog anchor: CC-037 | `exit` | atomic | — | Clean interactive shutdown request. |
| Catalog anchor: CC-038 | `fast` | workflow | CMD-WF-FAST-001 | Eligibility check and coupled model/fast-state transition. |
| Catalog anchor: CC-039 | `help` | atomic | — | Read-only command/help surface. |
| Catalog anchor: CC-040 | `ide` | workflow | CMD-WF-IDE-INTEGRATION-001 | IDE discovery/connect/status handoff. |
| Catalog anchor: CC-041 | `install-github-app` | workflow | CMD-WF-GITHUB-APP-001 | Repository, app, secret, workflow, branch, PR sequence. |
| Catalog anchor: CC-042 | `mcp` | workflow | CMD-WF-MCP-001 | Settings/reconnect/enable/disable routing. |
| Catalog anchor: CC-043 | `memory` | workflow | CMD-WF-MEMORY-001 | Select/create memory file and external-editor handoff. |
| Catalog anchor: CC-044 | `mobile` | workflow | CMD-WF-REMOTE-SESSION-001 | Remote/mobile connection status and handoff. |
| Catalog anchor: CC-045 | `model` | workflow | CMD-WF-MODEL-001 | Inspect, validate, select, and reconcile fast mode. |
| Catalog anchor: CC-046 | `output-style` | workflow | CMD-WF-OUTPUT-STYLE-001 | Legacy style selection and persisted setting update. |
| Catalog anchor: CC-047 | `remote-env` | workflow | CMD-WF-REMOTE-ENV-001 | Remote environment selector/configuration handoff. |
| Catalog anchor: CC-048 | `plugin` | workflow | CMD-WF-PLUGIN-001 | Plugin, marketplace, validation, and option workflows. |
| Catalog anchor: CC-049 | `rename` | atomic | — | Session title/identity mutation. |
| Catalog anchor: CC-050 | `resume` | workflow | CMD-WF-RESUME-001 | Resolve picker/ID/title and hand session ownership to resume. |
| Catalog anchor: CC-051 | `session` | workflow | CMD-WF-REMOTE-SESSION-001 | Remote URL/QR/status display and browser handoff. |
| Catalog anchor: CC-052 | `skills` | workflow | CMD-WF-SKILLS-001 | Read-only skill discovery/browse flow. |
| Catalog anchor: CC-053 | `stats` | workflow | CMD-WF-STATS-001 | Async local aggregation, filtering, and snapshot copy. |
| Catalog anchor: CC-054 | `status` | workflow | CMD-WF-SETTINGS-001 | Settings shell, default Status tab. |
| Catalog anchor: CC-055 | `theme` | atomic | — | Interactive theme selection/persistence surface. |
| Catalog anchor: CC-056 | `feedback` | workflow | CMD-WF-FEEDBACK-001 | Consent, report upload, optional public issue draft. |
| Catalog anchor: CC-057 | `ultrareview` | workflow | CMD-WF-ULTRAREVIEW-001 | Metering, PR/branch preparation, teleport, and remote task registration. |
| Catalog anchor: CC-058 | `terminal-setup` | workflow | CMD-WF-TERMINAL-SETUP-001 | Terminal capability detection and settings/instructions flow. |
| Catalog anchor: CC-059 | `upgrade` | workflow | CMD-WF-PRODUCT-EXPERIENCE-001 | Account/browser upgrade handoff. |
| Catalog anchor: CC-060 | `extra-usage` (UI) | workflow | CMD-WF-EXTRA-USAGE-001 | Interactive variant of the same account/admin decision flow. |
| Catalog anchor: CC-061 | `rate-limit-options` | workflow | CMD-WF-RATE-LIMIT-OPTIONS-001 | Hidden dynamic menu delegates to upgrade or extra-usage authority. |
| Catalog anchor: CC-062 | `usage` | workflow | CMD-WF-SETTINGS-001 | Settings shell, default Usage tab. |
| Catalog anchor: CC-063 | `permissions` | workflow | CMD-WF-PERMISSIONS-001 | Rule management and optional retry-message append. |
| Catalog anchor: CC-064 | `plan` | workflow | CMD-WF-PLAN-001 | Permission/mode transition with model-context consequences. |
| Catalog anchor: CC-065 | `privacy-settings` | workflow | CMD-WF-PRIVACY-001 | Policy-gated privacy settings handoff. |
| Catalog anchor: CC-066 | `hooks` | workflow | CMD-WF-HOOKS-001 | Hook configuration using live tool names. |
| Catalog anchor: CC-067 | `export` | workflow | CMD-WF-EXPORT-001 | Snapshot render, clipboard or synchronous file write. |
| Catalog anchor: CC-068 | `sandbox` | workflow | CMD-WF-SANDBOX-001 | Platform/policy checks and config mutation. |
| Catalog anchor: CC-069 | `login` | workflow | CMD-WF-LOGIN-001 | OAuth, signature cleanup, auth-dependent refreshes. |
| Catalog anchor: CC-070 | `logout` | workflow | CMD-WF-LOGOUT-001 | Telemetry flush, credential wipe, cache reset, shutdown. |
| Catalog anchor: CC-071 | `passes` | workflow | CMD-WF-PRODUCT-EXPERIENCE-001 | Entitlement/account flow; service owns granting. |
| Catalog anchor: CC-072 | `tasks` | workflow | CMD-WF-TASKS-001 | Inspect/cancel/retrieve task-runtime work. |
| Catalog anchor: CC-073 | `think-back` | workflow | CMD-WF-THINKBACK-001 | Marketplace/plugin bootstrap, artifact menu, generation prompt, and playback. |
| Catalog anchor: CC-074 | `web-setup` | workflow | CMD-WF-WEB-SETUP-001 | Feature-gated remote-web setup handoff. |
| Catalog anchor: CC-075 | `fork` | profile | — | Dedicated fork feature replaces `branch` alias. |
| Catalog anchor: CC-076 | `buddy` | profile | — | Companion build contribution. |
| Catalog anchor: CC-077 | `proactive` | profile | — | Proactive/persistent build contribution. |
| Catalog anchor: CC-078 | `brief` | workflow | CMD-WF-BRIEF-001 | Immediate coupled output-channel/tool-list/app-state transition. |
| Catalog anchor: CC-079 | `assistant` | profile | — | Persistent-assistant build contribution. |
| Catalog anchor: CC-080 | `remote-control` | profile | — | Bridge-mode contribution. |
| Catalog anchor: CC-081 | `remote-control-server` | profile | — | Daemon plus bridge-mode contribution. |
| Catalog anchor: CC-082 | `workflows` | profile | — | Workflow-scripts contribution. |
| Catalog anchor: CC-083 | `torch` | profile | — | Torch contribution. |
| Catalog anchor: CC-084 | `peers` | profile | — | UDS inbox contribution. |
| Catalog anchor: CC-085 | `backfill-sessions` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-086 | `break-cache` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-087 | `bughunter` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-088 | `commit` | prompt | — | Internal prompt command when included. |
| Catalog anchor: CC-089 | `commit-push-pr` | prompt | — | Internal prompt command; tools own side effects. |
| Catalog anchor: CC-090 | `ctx-viz` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-091 | `good-agentx` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-092 | `issue` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-093 | `init-verifiers` | prompt | — | Internal prompt command when included. |
| Catalog anchor: CC-094 | `force-snip` | profile | — | History-snip feature contribution. |
| Catalog anchor: CC-095 | `mock-limits` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-096 | `bridge-kick` | workflow | CMD-WF-BRIDGE-KICK-001 | Internal live-handle fault injection and forced recovery trigger. |
| Catalog anchor: CC-097 | `version` | atomic | — | Internal local version observation. |
| Catalog anchor: CC-098 | `ultraplan` | workflow | CMD-WF-ULTRAPLAN-001 | Detached remote task, bounded poll, disposition, stop, archive, and race guards. |
| Catalog anchor: CC-099 | `subscribe-pr` | profile | — | GitHub-webhook contribution. |
| Catalog anchor: CC-100 | `reset-limits` | profile | — | Internal limit-test contribution. |
| Catalog anchor: CC-101 | `onboarding` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-102 | `share` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-103 | `summary` | stub | — | External specified build lacks the bridge-safe implementation. |
| Catalog anchor: CC-104 | `teleport` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-105 | `ant-trace` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-106 | `perf-issue` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-107 | `env` | profile | — | Internal environment-report contribution. |
| Catalog anchor: CC-108 | `oauth-refresh` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-109 | `debug-tool-call` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-110 | `agents-platform` | profile | — | Internal-profile import only. |
| Catalog anchor: CC-111 | `autofix-pr` | stub | — | External specified build exposes no such command. |
| Catalog anchor: CC-112 | `tag` | workflow | CMD-WF-TAG-001 | Sanitized append-only transcript metadata toggle with confirmed removal. |

## CC-155 — Workflow coverage maintenance

`CC-155` is a maintenance contract, not an invocable command and not an extension of the manifest-declared observable catalog.

The following conditions are mandatory after any command change:

1. The source registry arrays, [source registry manifest](source-registry-manifest.md), catalog, and reconciliation table agree. IDs are contiguous from `CC-001` through the manifest-declared identity count, with no duplicate; every registered descriptor symbol is represented, including multiple surface descriptors for one canonical identity.
2. Every row classified `workflow` has exactly one non-dash Primary workflow identifier.
3. Every non-`workflow` row has `—` in Primary workflow; no hidden state machine may be smuggled into an `atomic`, `prompt`, `profile`, or `stub` row.
4. Every mapped Primary workflow has exactly one heading definition in a directly linked workflow reference.
5. A new complex command first receives a catalog ID and workflow definition, then is added to this mapping. A formerly complex command can be reclassified only after its local side effects and cancellation paths have actually been removed.
6. `profile` and `stub` rows are tested for absence under base/external gates. Presence of a source artifact or disabled descriptor does not satisfy availability. A specified descriptor with an actual state machine cannot be downgraded to `profile` merely because a gate can hide it.
7. `/tag` remains a required specified identity and the actual registry count is reconciled. Any omission or count drift fails the mechanical audit before semantic acceptance runs.

Run `ruby scripts/audit_command_workflows.rb` from this skill directory, or pass the skill directory as its first argument. The audit is intentionally structural: semantic acceptance remains the responsibility of `workflow-acceptance.md`.
