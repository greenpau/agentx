# Source registry manifest

## Contents

1. [Purpose](#purpose)
2. [Reconciliation rules](#reconciliation-rules)

## Purpose

This is the independently maintained bridge between the specified registry enumeration and the language-neutral command catalog. It is not generated from `command-catalog.md` or `command-workflow-index.md`. Update it by inspecting the registry arrays and their conditional entries, then let `scripts/audit_command_workflows.rb` compare all three artifacts.

Registry identity count: **112**. Registry descriptor-symbol count: **113**. The counts differ because one canonical identity, `/reset-limits`, has distinct interactive and noninteractive descriptors. An implementation may represent surface variants differently, but it must retain their selection semantics under one stable catalog identity.

`base` means a symbol occurs directly in the built-in command array. `conditional` means the symbol is present in that array only when a build, provider, or account condition contributes it. `internal` means the symbol occurs in the internal-only array, which is appended only for the eligible internal profile. The manifest records registry symbols rather than filenames so renamed/moved descriptors still produce an explicit reconciliation decision.

| Catalog identity | Canonical name | Registry symbol(s) | Registry set |
| --- | --- | --- | --- |
| Registry anchor: CC-001 | `init` | `init` | base |
| Registry anchor: CC-002 | `pr-comments` | `pr_comments` | base |
| Registry anchor: CC-003 | `review` | `review` | base |
| Registry anchor: CC-004 | `security-review` | `securityReview` | base |
| Registry anchor: CC-005 | `statusline` | `statusline` | base |
| Registry anchor: CC-006 | `insights` | `usageReport` | base |
| Registry anchor: CC-007 | `advisor` | `advisor` | base |
| Registry anchor: CC-008 | `clear` | `clear` | base |
| Registry anchor: CC-009 | `compact` | `compact` | base |
| Registry anchor: CC-010 | `context` | `contextNonInteractive` | base |
| Registry anchor: CC-011 | `cost` | `cost` | base |
| Registry anchor: CC-012 | `files` | `files` | base |
| Registry anchor: CC-013 | `heapdump` | `heapDump` | base |
| Registry anchor: CC-014 | `install-slack-app` | `installSlackApp` | base |
| Registry anchor: CC-015 | `keybindings` | `keybindings` | base |
| Registry anchor: CC-016 | `release-notes` | `releaseNotes` | base |
| Registry anchor: CC-017 | `reload-plugins` | `reloadPlugins` | base |
| Registry anchor: CC-018 | `rewind` | `rewind` | base |
| Registry anchor: CC-019 | `stickers` | `stickers` | base |
| Registry anchor: CC-020 | `thinkback-play` | `thinkbackPlay` | base |
| Registry anchor: CC-021 | `vim` | `vim` | base |
| Registry anchor: CC-022 | `voice` | `voiceCommand` | conditional |
| Registry anchor: CC-023 | `extra-usage` | `extraUsageNonInteractive` | base |
| Registry anchor: CC-024 | `add-dir` | `addDir` | base |
| Registry anchor: CC-025 | `agents` | `agents` | base |
| Registry anchor: CC-026 | `branch` | `branch` | base |
| Registry anchor: CC-027 | `btw` | `btw` | base |
| Registry anchor: CC-028 | `chrome` | `chrome` | base |
| Registry anchor: CC-029 | `color` | `color` | base |
| Registry anchor: CC-030 | `config` | `config` | base |
| Registry anchor: CC-031 | `context` | `context` | base |
| Registry anchor: CC-032 | `copy` | `copy` | base |
| Registry anchor: CC-033 | `desktop` | `desktop` | base |
| Registry anchor: CC-034 | `diff` | `diff` | base |
| Registry anchor: CC-035 | `doctor` | `doctor` | base |
| Registry anchor: CC-036 | `effort` | `effort` | base |
| Registry anchor: CC-037 | `exit` | `exit` | base |
| Registry anchor: CC-038 | `fast` | `fast` | base |
| Registry anchor: CC-039 | `help` | `help` | base |
| Registry anchor: CC-040 | `ide` | `ide` | base |
| Registry anchor: CC-041 | `install-github-app` | `installGitHubApp` | base |
| Registry anchor: CC-042 | `mcp` | `mcp` | base |
| Registry anchor: CC-043 | `memory` | `memory` | base |
| Registry anchor: CC-044 | `mobile` | `mobile` | base |
| Registry anchor: CC-045 | `model` | `model` | base |
| Registry anchor: CC-046 | `output-style` | `outputStyle` | base |
| Registry anchor: CC-047 | `remote-env` | `remoteEnv` | base |
| Registry anchor: CC-048 | `plugin` | `plugin` | base |
| Registry anchor: CC-049 | `rename` | `rename` | base |
| Registry anchor: CC-050 | `resume` | `resume` | base |
| Registry anchor: CC-051 | `session` | `session` | base |
| Registry anchor: CC-052 | `skills` | `skills` | base |
| Registry anchor: CC-053 | `stats` | `stats` | base |
| Registry anchor: CC-054 | `status` | `status` | base |
| Registry anchor: CC-055 | `theme` | `theme` | base |
| Registry anchor: CC-056 | `feedback` | `feedback` | base |
| Registry anchor: CC-057 | `ultrareview` | `ultrareview` | base |
| Registry anchor: CC-058 | `terminal-setup` | `terminalSetup` | base |
| Registry anchor: CC-059 | `upgrade` | `upgrade` | base |
| Registry anchor: CC-060 | `extra-usage` | `extraUsage` | base |
| Registry anchor: CC-061 | `rate-limit-options` | `rateLimitOptions` | base |
| Registry anchor: CC-062 | `usage` | `usage` | base |
| Registry anchor: CC-063 | `permissions` | `permissions` | base |
| Registry anchor: CC-064 | `plan` | `plan` | base |
| Registry anchor: CC-065 | `privacy-settings` | `privacySettings` | base |
| Registry anchor: CC-066 | `hooks` | `hooks` | base |
| Registry anchor: CC-067 | `export` | `exportCommand` | base |
| Registry anchor: CC-068 | `sandbox` | `sandboxToggle` | base |
| Registry anchor: CC-069 | `login` | `login` | conditional |
| Registry anchor: CC-070 | `logout` | `logout` | conditional |
| Registry anchor: CC-071 | `passes` | `passes` | base |
| Registry anchor: CC-072 | `tasks` | `tasks` | base |
| Registry anchor: CC-073 | `think-back` | `thinkback` | base |
| Registry anchor: CC-074 | `web-setup` | `webCmd` | conditional |
| Registry anchor: CC-075 | `fork` | `forkCmd` | conditional |
| Registry anchor: CC-076 | `buddy` | `buddy` | conditional |
| Registry anchor: CC-077 | `proactive` | `proactive` | conditional |
| Registry anchor: CC-078 | `brief` | `briefCommand` | conditional |
| Registry anchor: CC-079 | `assistant` | `assistantCommand` | conditional |
| Registry anchor: CC-080 | `remote-control` | `bridge` | conditional |
| Registry anchor: CC-081 | `remote-control-server` | `remoteControlServerCommand` | conditional |
| Registry anchor: CC-082 | `workflows` | `workflowsCmd` | conditional |
| Registry anchor: CC-083 | `torch` | `torch` | conditional |
| Registry anchor: CC-084 | `peers` | `peersCmd` | conditional |
| Registry anchor: CC-085 | `backfill-sessions` | `backfillSessions` | internal |
| Registry anchor: CC-086 | `break-cache` | `breakCache` | internal |
| Registry anchor: CC-087 | `bughunter` | `bughunter` | internal |
| Registry anchor: CC-088 | `commit` | `commit` | internal |
| Registry anchor: CC-089 | `commit-push-pr` | `commitPushPr` | internal |
| Registry anchor: CC-090 | `ctx-viz` | `ctx_viz` | internal |
| Registry anchor: CC-091 | `good-agentx` | `goodAgentX` | internal |
| Registry anchor: CC-092 | `issue` | `issue` | internal |
| Registry anchor: CC-093 | `init-verifiers` | `initVerifiers` | internal |
| Registry anchor: CC-094 | `force-snip` | `forceSnip` | internal |
| Registry anchor: CC-095 | `mock-limits` | `mockLimits` | internal |
| Registry anchor: CC-096 | `bridge-kick` | `bridgeKick` | internal |
| Registry anchor: CC-097 | `version` | `version` | internal |
| Registry anchor: CC-098 | `ultraplan` | `ultraplan` | internal |
| Registry anchor: CC-099 | `subscribe-pr` | `subscribePr` | internal |
| Registry anchor: CC-100 | `reset-limits` | `resetLimits`, `resetLimitsNonInteractive` | internal |
| Registry anchor: CC-101 | `onboarding` | `onboarding` | internal |
| Registry anchor: CC-102 | `share` | `share` | internal |
| Registry anchor: CC-103 | `summary` | `summary` | internal |
| Registry anchor: CC-104 | `teleport` | `teleport` | internal |
| Registry anchor: CC-105 | `ant-trace` | `antTrace` | internal |
| Registry anchor: CC-106 | `perf-issue` | `perfIssue` | internal |
| Registry anchor: CC-107 | `env` | `env` | internal |
| Registry anchor: CC-108 | `oauth-refresh` | `oauthRefresh` | internal |
| Registry anchor: CC-109 | `debug-tool-call` | `debugToolCall` | internal |
| Registry anchor: CC-110 | `agents-platform` | `agentsPlatform` | internal |
| Registry anchor: CC-111 | `autofix-pr` | `autofixPr` | internal |
| Registry anchor: CC-112 | `tag` | `tag` | base; live internal-profile gate |

## Reconciliation rules

1. Parse the registry's built-in and internal-only arrays as the authority for descriptor symbols. The flattened symbol set above must equal that source set exactly; imported-but-unregistered artifacts do not count.
2. Treat conditional array entries as registered identities even when their current build gate is false. Their disabled behavior belongs in the catalog and acceptance tests.
3. Treat multiple surface descriptors for one canonical command as one catalog identity and list every contributing symbol in that row.
4. Keep IDs contiguous from `CC-001` through the declared identity count. A newly registered canonical name receives the next ID; removing a command retains its ID as a documented supported absence rather than silently renumbering history.
5. `/tag` is a required specified registry identity. The audit must fail if its manifest row, catalog row, workflow mapping, registry symbol, or descriptor name disappears.
