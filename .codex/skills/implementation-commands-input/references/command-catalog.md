# Observable command catalog

## Contents

1. [Catalog notation](#catalog-notation)
2. [Built-in prompt commands](#built-in-prompt-commands)
3. [Built-in local commands](#built-in-local-commands)
4. [Built-in local UI commands](#built-in-local-ui-commands)
5. [Optional public/profile commands](#optional-publicprofile-commands)
6. [Internal-only catalog](#internal-only-catalog)
7. [Additional registered gated command](#additional-registered-gated-command)
8. [Present but not base-registered compatibility surface](#present-but-not-base-registered-compatibility-surface)
9. [Catalog maintenance rule](#catalog-maintenance-rule)

## Catalog notation

Types are `P` prompt expansion, `L` local text/state operation, and `UI` local interactive flow. Availability is universal unless shown. “Gate” may combine build inclusion and live `isEnabled`; absence is a supported state. Interactive/noninteractive variants with the same name are separate descriptors whose surface filtering leaves one applicable behavior.

## Built-in prompt commands

| ID | Name (aliases) | Type | Observable purpose | Availability/gate |
| --- | --- | --- | --- | --- |
| CC-001 | `init` | P | Analyze the project and create/update project guidance. | Universal |
| CC-002 | `pr-comments` | P | Fetch/address pull-request comments through the migrated integration path. | Integration dependent |
| CC-003 | `review` | P | Review current changes or a specified scope. | Universal |
| CC-004 | `security-review` | P | Perform a security-focused review and produce actionable findings. | Universal |
| CC-005 | `statusline` | P | Configure a terminal status line through guided model instructions. | Disabled in unsupported noninteractive paths |
| CC-006 | `insights` | P | Analyze stored sessions and generate a usage/work-style report. | Local history/data eligibility |

Prompt commands may set argument hints, allowed tools, model, effort, and progress text. Their exact prose is replaceable; their routing, scoped authority, and output purpose are not.

## Built-in local commands

| ID | Name (aliases) | Type | NI | Observable purpose | Availability/gate |
| --- | --- | --- | --- | --- | --- |
| CC-007 | `advisor` | L | yes | Return advice/status from the configured advisor feature. | Feature/live eligibility |
| CC-008 | `clear` (`reset`, `new`) | L | no | Clear/start fresh conversation state. | Universal; remote/bridge safe |
| CC-009 | `compact` | L | yes | Compact conversation, optionally using a custom summary instruction. | Compact not disabled; bridge safe |
| CC-010 | `context` | L | yes | Emit context usage/details in noninteractive mode. | Surface-selected twin |
| CC-011 | `cost` | L | yes | Show session usage/cost accounting. | Universal; remote/bridge safe |
| CC-012 | `files` | L | yes | List tracked/touched files. | Internal profile in specified build; bridge safe |
| CC-013 | `heapdump` | L | yes | Write diagnostic heap information. Hidden. | Runtime diagnostic support |
| CC-014 | `install-slack-app` | L | no | Start Slack integration installation. | `agentx-ai` |
| CC-015 | `keybindings` | L | no | Open/edit keybinding configuration through local text flow. | Keybinding feature; remote safe |
| CC-016 | `release-notes` | L | yes | Show product release notes. | Universal; bridge safe |
| CC-017 | `reload-plugins` | L | no | Clear/reload plugin discovery caches. | Plugin subsystem |
| CC-018 | `rewind` (`checkpoint`) | L | no | Rewind conversation/files to a recoverable checkpoint. | Checkpoint support |
| CC-019 | `stickers` | L | no | Manage/show the stickers experience. | Feature/profile; remote safe |
| CC-020 | `thinkback-play` | L | no | Resolve the installed thinkback skill and replay its captured animation in the alternate screen. Hidden. | Live `tengu_thinkback` gate; installed plugin required |
| CC-021 | `vim` | L | no | Toggle modal editing. | Interactive terminal; remote safe |
| CC-022 | `voice` | L | no | Toggle/configure voice input. | `agentx-ai` plus `VOICE_MODE` |
| CC-023 | `extra-usage` | L | yes | Report/manage extra usage in noninteractive mode. | Surface-selected twin and account eligibility |

## Built-in local UI commands

| ID | Name (aliases) | Observable purpose | Availability/gate/notes |
| --- | --- | --- | --- |
| CC-024 | `add-dir` | Add an authorized working directory. | Interactive filesystem context |
| CC-025 | `agents` | Browse/configure available agents. | Agent subsystem |
| CC-026 | `branch` (`fork` only when not reserved by fork feature) | Branch/fork the current conversation. | Session branching; alias collision gate |
| CC-027 | `btw` | Open quick-note/side input. | Immediate; remote safe |
| CC-028 | `chrome` | Configure browser/Chrome integration. | `agentx-ai`, platform/feature |
| CC-029 | `color` | Change agent/UI color. | Immediate; remote safe |
| CC-030 | `config` (`settings`) | Open settings UI. | Interactive |
| CC-031 | `context` | Show interactive context breakdown. | Surface-selected twin |
| CC-032 | `copy` | Copy last relevant response. | Clipboard support; remote safe |
| CC-033 | `desktop` (`app`) | Open/install desktop application flow. | `agentx-ai`, supported platform |
| CC-034 | `diff` | Show session/source diff UI. | Repository/file-history context |
| CC-035 | `doctor` | Run diagnostics and remedies. | Absent when doctor disabled |
| CC-036 | `effort` | Select reasoning effort. | Current model supports setting |
| CC-037 | `exit` (`quit`) | Exit the interactive client cleanly. | Immediate; remote safe |
| CC-038 | `fast` | Inspect/toggle fast mode. | `agentx-ai` OR `console`, feature/model eligibility |
| CC-039 | `help` | Show the current surface-eligible command catalog from dispatch descriptors, including canonical names, argument hints, descriptions, and aliases. | Universal; remote safe |
| CC-040 | `ide` | Inspect/connect IDE integration. | Interactive integration context |
| CC-041 | `install-github-app` | Guide GitHub App installation. | `agentx-ai` OR `console` |
| CC-042 | `mcp` | Manage MCP connections/servers. | Immediate; MCP subsystem |
| CC-043 | `memory` | Inspect/edit memory sources. | Memory subsystem |
| CC-044 | `mobile` (`ios`, `android`) | Show mobile/remote connection flow. | Remote/mobile feature; remote safe |
| CC-045 | `model` | Inspect/select model, optionally by argument. | Immediate where picker supported |
| CC-046 | `output-style` | Select legacy output style. | Hidden/deprecated compatibility |
| CC-047 | `remote-env` | Configure/select remote environment. | Remote execution feature |
| CC-048 | `plugin` (`plugins`, `marketplace`) | Manage installed plugins/marketplaces. | Immediate; plugin subsystem |
| CC-049 | `rename` | Rename current session. | Immediate |
| CC-050 | `resume` (`continue`) | Select a prior session by picker/ID/title. | Resume subsystem |
| CC-051 | `session` (`remote`) | Show current remote session URL/QR/status. | Remote mode; remote safe |
| CC-052 | `skills` | Browse available skills. | Skill subsystem |
| CC-053 | `stats` | Show session/product statistics. | Data availability |
| CC-054 | `status` | Show current session/model/account/integration status. | Immediate |
| CC-055 | `theme` | Select terminal theme. | Interactive terminal; remote safe |
| CC-056 | `feedback` (`bug`) | Submit product feedback/bug report. | Integration/config dependent; remote safe |
| CC-057 | `ultrareview` | Gate billing, launch a cloud bug review for a PR or branch bundle, and register its remote task. | Live bughunter config; account/repository/remote eligibility |
| CC-058 | `terminal-setup` | Configure terminal key/shift-enter support. | Hidden on terminals with native support |
| CC-059 | `upgrade` | Open upgrade flow. | `agentx-ai` |
| CC-060 | `extra-usage` | Manage extra usage interactively. | Account eligibility; surface-selected twin |
| CC-061 | `rate-limit-options` | Choose stop, upgrade, or extra-usage recovery after a rate limit. | Hidden; first-party subscriber only; delegated actions have their own gates |
| CC-062 | `usage` | Show plan/usage UI. | `agentx-ai`; remote safe |
| CC-063 | `permissions` (`allowed-tools`) | Inspect/change session permission mode/rules. | Interactive |
| CC-064 | `plan` | Enter/exit plan mode. | Interactive; remote safe |
| CC-065 | `privacy-settings` | Open privacy settings. | Account/policy gate |
| CC-066 | `hooks` | Inspect/configure hooks. | Immediate; hooks subsystem |
| CC-067 | `export` | Export conversation/session artifact. | Interactive filesystem destination |
| CC-068 | `sandbox` | Inspect/toggle sandbox configuration. | Immediate; platform/policy support |
| CC-069 | `login` | Authenticate/change account. | Not registered for third-party providers |
| CC-070 | `logout` | End first-party authentication. | Not registered for third-party providers |
| CC-071 | `passes` | Manage product passes/entitlements. | Account eligibility |
| CC-072 | `tasks` (`bashes`) | Inspect running/background tasks. | Task runtime |
| CC-073 | `think-back` | Install/enable the thinkback plugin, then play, edit, fix, or regenerate its animation. | Live `tengu_thinkback` gate; interactive |

## Optional public/profile commands

| ID | Name (aliases) | Type | Observable purpose | Inclusion |
| --- | --- | --- | --- | --- |
| CC-074 | `web-setup` | UI | Configure remote web execution. | `CCR_REMOTE_SETUP`, `agentx-ai` |
| CC-075 | `fork` | profile-defined | Fork through the dedicated subagent/session experience. | `FORK_SUBAGENT`; changes `branch` alias behavior |
| CC-076 | `buddy` | profile-defined | Launch/manage terminal companion experience. | `BUDDY` |
| CC-077 | `proactive` | profile-defined | Toggle proactive/persistent operation. | `PROACTIVE` or `KAIROS` |
| CC-078 | `brief` | UI | Immediately toggle brief-only output, synchronizing message-tool opt-in, app state, and next-turn reminder. | `KAIROS` or `KAIROS_BRIEF`; live config and on-transition entitlement |
| CC-079 | `assistant` | profile-defined | Enter persistent assistant experience. | `KAIROS` |
| CC-080 | `remote-control` (`rc`) | UI | Start/manage Remote Control bridge. | `BRIDGE_MODE`; immediate |
| CC-081 | `remote-control-server` | profile-defined | Manage daemon bridge server. | `DAEMON` and `BRIDGE_MODE` |
| CC-082 | `workflows` | profile-defined | Browse/run workflow scripts. | `WORKFLOW_SCRIPTS` |
| CC-083 | `torch` | profile-defined | Feature-specific torch experience. | `TORCH` |
| CC-084 | `peers` | profile-defined | Browse connected peers. | `UDS_INBOX` |

When a profile-defined command is shipped, define its exact P/L/UI descriptor and result semantics; absence of its build gate is correct, a guessed stub is not.

## Internal-only catalog

Internal commands are included only when `USER_TYPE=ant` and `IS_DEMO` is absent. Specified external artifacts for several are hidden disabled descriptors named `stub`; such stubs are not user-visible commands. Internal builds may provide these intended identities:

| ID | Intended name | Known type/role or compatibility note |
| --- | --- | --- |
| CC-085 | `backfill-sessions` | Maintenance; external stub |
| CC-086 | `break-cache` | Cache diagnostic; external stub |
| CC-087 | `bughunter` | Diagnostic; external stub |
| CC-088 | `commit` | Prompt command for commit preparation |
| CC-089 | `commit-push-pr` | Prompt command for commit/push/PR workflow |
| CC-090 | `ctx-viz` | Context visualization; external stub spelling must be descriptor-defined |
| CC-091 | `good-agentx` | Internal feedback/diagnostic; external stub |
| CC-092 | `issue` | Internal issue flow; external stub |
| CC-093 | `init-verifiers` | Prompt command creating verifier guidance |
| CC-094 | `force-snip` | History maintenance under `HISTORY_SNIP` |
| CC-095 | `mock-limits` | Limit-test flow; external stub |
| CC-096 | `bridge-kick` | Local bridge fault injection/status command; noninteractive unsupported; requires a live debug handle |
| CC-097 | `version` | Local, noninteractive supported version details |
| CC-098 | `ultraplan` | Detached remote planning task under `ULTRAPLAN`; slash, keyword, and seed-plan entries; polling/stop/archive lifecycle |
| CC-099 | `subscribe-pr` | GitHub-webhook subscription under its feature gate |
| CC-100 | `reset-limits` | Interactive and noninteractive variants |
| CC-101 | `onboarding` | Internal onboarding; external stub |
| CC-102 | `share` | Internal sharing; external stub |
| CC-103 | `summary` | Local summary; external stub in this build; bridge-safe when present |
| CC-104 | `teleport` | Session transfer; external stub |
| CC-105 | `ant-trace` | Internal trace diagnostics; external stub |
| CC-106 | `perf-issue` | Performance issue diagnostics; external stub |
| CC-107 | `env` | Internal environment report |
| CC-108 | `oauth-refresh` | Authentication maintenance; external stub |
| CC-109 | `debug-tool-call` | Tool protocol diagnostic; external stub |
| CC-110 | `agents-platform` | Internal agent-platform flow; internal-profile import only |
| CC-111 | `autofix-pr` | Pull-request autofix flow; external stub |

## Additional registered gated command

Unlike the internal-array commands above, this descriptor is always enumerated in the base registry and then filtered by its live profile gate.

| ID | Name | Type | Observable purpose | Availability/gate |
| --- | --- | --- | --- | --- |
| CC-112 | `tag` | UI | Sanitize and append-add/replace/remove a searchable tag for the active transcript; removal is confirmed. | `USER_TYPE=ant`; exact invocation remains local and interactive |

## Present but not base-registered compatibility surface

An `install` local UI descriptor exists in specified artifacts but is not in the base registry enumeration. Do not expose it merely because code is present; assign it a registry route and acceptance tests first, or retain it as unreachable compatibility code. Likewise, moved-to-plugin prompt shims are observable only when explicitly included by a registry source.

## Catalog maintenance rule

Every newly exposed canonical command receives one stable CC ID, type, aliases, availability, enablement, visibility, surface filters, argument contract, result contract, source attribution, and enabled/disabled tests. Never infer observability from a source file alone.
