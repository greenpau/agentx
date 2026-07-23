# Durable global and project configuration contract

This document specifies the sparse per-user configuration file that also contains path-keyed project state. It is distinct from the validated settings cascade in [settings schema](settings-schema.md): the global/project file is an application-owned compatibility record, has no complete runtime validator, and contains preferences, identities, consent receipts, caches, counters, migration guards, and limited credential fallback. `GCFG-*` identifiers are stable implementation anchors.

## Contents

1. [Responsibility and authority](#responsibility-and-authority)
2. [File and project identity](#file-and-project-identity)
3. [Wire format and compatibility](#wire-format-and-compatibility)
4. [Embedded value grammars](#embedded-value-grammars)
5. [GlobalConfig schema](#globalconfig-schema)
6. [ProjectConfig schema](#projectconfig-schema)
7. [Defaults and sparse serialization](#defaults-and-sparse-serialization)
8. [Bootstrap, reads, and cache coherence](#bootstrap-reads-and-cache-coherence)
9. [Writes, locking, backups, and concurrency](#writes-locking-backups-and-concurrency)
10. [Trust and derived helpers](#trust-and-derived-helpers)
11. [Migrations](#migrations)
12. [Secrets and server-owned data](#secrets-and-server-owned-data)
13. [Failure and crash matrix](#failure-and-crash-matrix)
14. [Acceptance scenarios](#acceptance-scenarios)

## Responsibility and authority

`GCFG-001` — Treat this store as durable application state, not as an ordinary settings source and not as managed policy. Values here never outrank the settings cascade merely because they are persisted. Settings migrations may consume and remove legacy fields from this store.

`GCFG-002` — One physical JSON object contains both `GlobalConfig` fields and `projects`, a map of normalized project identities to `ProjectConfig`. Project state has no independent transaction, lock, or file.

`GCFG-003` — Preserve four ownership classes: user preferences and consent receipts; client-generated counters/identity/migration guards; server-derived caches and account metadata; and secrets or executable configuration. Never send the whole object to telemetry or a server.

`GCFG-004` — The declared schemas below are the writer and consumer contract. The compatibility reader does not enforce them. A rebuild must validate values at the consuming boundary while retaining unknown data for forward and backward compatibility.

## File and project identity

`GCFG-PATH-001` — Resolve the configuration home as the explicitly configured configuration directory, otherwise `<user-home>/.agentx`, and normalize that directory string to Unicode NFC. Freeze path-selection inputs before the first configuration access.

`GCFG-PATH-002` — Select the global file once per process:

1. if `<configuration-home>/.config.json` exists, use that legacy file;
2. otherwise use `<configured-directory-or-user-home>/.agentx<oauth-suffix>.json`;
3. use no suffix for production, `-staging-oauth` for the internal staging identity, `-local-oauth` for the internal local identity, and `-custom-oauth` whenever a custom OAuth URL is selected.

With no override, the ordinary file is therefore `<user-home>/.agentx.json`, while backups live under `<user-home>/.agentx/backups/`.

`GCFG-PATH-003` — Derive the current project key once per process from the original launch directory, not later directory changes. If it is inside a repository, use the canonical main-repository identity so linked worktrees share project state. Validate worktree back-links before adopting a common repository identity; fall back to the worktree root on malformed or suspicious metadata. Outside a repository, use the absolute original launch directory.

`GCFG-PATH-004` — Normalize project keys lexically by resolving `.` and `..` and converting platform path separators to `/`. The canonical repository helper normalizes its returned identity to NFC. Do not invent case folding.

`GCFG-PATH-005` — A later working-directory change does not change the primary project key. Trust lookup additionally examines the live working directory and its lexical ancestors as described by `GCFG-TRUST-002`.

## Wire format and compatibility

`GCFG-WIRE-001` — The file is UTF-8 JSON. Strip one UTF-8 byte-order mark before parsing. A normal write emits an indented object with two-space indentation and no required trailing newline.

`GCFG-WIRE-002` — Load by creating a fresh default object and shallowly overlaying the parsed top-level value. Do not recursively merge defaults. A disk `null`, wrong-typed value, or malformed nested object can therefore override a declared default if the JSON parse itself succeeds.

`GCFG-WIRE-003` — There is deliberately no full schema validation and no format-version discriminator. Preserve unknown top-level and nested fields through a read-modify-write when the updater retains the current object. `migrationVersion` gates a particular migration set; it is not a general wire-format version.

`GCFG-WIRE-004` — Sparse serialization filters top-level entries whose serialized value is exactly equal to the corresponding default's serialized value. JSON omission removes absent-valued fields. It does not prune nested defaults or unknown fields. A changed application default becomes the value supplied when the field is absent on a later read.

`GCFG-WIRE-005` — A valid but wrong-shaped JSON value is compatibility input, not proof of validity. The specified implementation performs one local repair: if `ProjectConfig.allowedTools` is a string, parse it as JSON and replace it in the live cached object with the parsed array or `[]`. Other wrong-typed values reach consumers unless those consumers defend themselves.

`GCFG-WIRE-006` — The global and project key allowlists are metadata subsets, not exhaustive schemas or an authorization mechanism. The global subset is:

```text
apiKeyHelper, installMethod, autoUpdates, autoUpdatesProtectedForNative,
theme, verbose, preferredNotifChannel, shiftEnterKeyBindingInstalled,
editorMode, hasUsedBackslashReturn, autoCompactEnabled, showTurnDuration,
diffTool, env, tipsHistory, todoFeatureEnabled, showExpandedTodos,
messageIdleNotifThresholdMs, autoConnectIde, autoInstallIdeExtension,
fileCheckpointingEnabled, terminalProgressBarEnabled, showStatusInTerminalTab,
taskCompleteNotifEnabled, inputNeededNotifEnabled, agentPushNotifEnabled,
respectGitignore, agentxInChromeDefaultEnabled,
hasCompletedAgentXInChromeOnboarding, lspRecommendationDisabled,
lspRecommendationNeverPlugins, lspRecommendationIgnoredCount,
copyFullResponse, copyOnSelect, permissionExplainerEnabled,
prStatusFooterEnabled, remoteControlAtStartup, remoteDialogSeen
```

The project subset is `allowedTools`, `hasTrustDialogAccepted`, and `hasCompletedProjectOnboarding`.

## Embedded value grammars

`GCFG-TYPE-001` — Enumerations are exact:

- `InstallMethod`: `local | native | global | unknown`;
- `ReleaseChannel`: `stable | latest`;
- `ThemeSetting`: `auto | dark | light | light-daltonized | dark-daltonized | light-ansi | dark-ansi`;
- `NotificationChannel`: `auto | iterm2 | iterm2_with_bell | terminal_bell | kitty | ghostty | notifications_disabled`;
- `EditorMode`: `normal | vim`, plus legacy-readable `emacs`;
- `DiffTool`: `terminal | auto`;
- teammate mode: `auto | tmux | in-process`.

`GCFG-TYPE-002` — `AccountInfo` is `{accountUuid:string, emailAddress:string, organizationUuid?:string, organizationName?:string|null, organizationRole?:string|null, workspaceRole?:string|null, displayName?:string, hasExtraUsageEnabled?:boolean, billingType?:server-defined-billing-value|null, accountCreatedAt?:string, subscriptionCreatedAt?:string}`. It is profile metadata and PII, not an OAuth token.

`GCFG-TYPE-003` — An MCP server map is keyed by server name. Each value is one validated MCP configuration: command transport `{type?:"stdio", command:nonempty-string, args:string[] defaulting empty, env?:string-map}`; SSE/HTTP/WebSocket transport with its exact discriminator, URL, optional headers and header-helper command; IDE SSE/WebSocket with URL and IDE identity; in-process SDK with name; or first-party proxy with URL and server ID. OAuth subconfiguration may contain client ID, positive callback port, HTTPS metadata URL, and cross-app-access flag. Treat environment values, headers, helper output, IDE auth tokens, and OAuth client data as sensitive.

`GCFG-TYPE-004` — `StoredCompanion` is `{name:string, personality:string, hatchedAt:number}`. Appearance and statistics are deterministically regenerated and are not persisted here.

`GCFG-TYPE-005` — A cached model option is `{value:string|null, label:string, description:string, descriptionForModel?:string}`. Values and availability are server/model-catalog input and must be revalidated before selection.

`GCFG-TYPE-006` — A cached referral eligibility entry is an open, server-owned response with required consumer field `eligible:boolean`, optional `remaining_passes:number`, optional `referrer_reward:{amount_minor_units:number,currency:string,...}`, any forward-compatible server fields, and client-added `timestamp:number`. Do not treat unknown server fields as executable configuration.

`GCFG-AUX-001` — Adjacent exported prompt-history shapes are not fields in this file. A pasted item is `{id:number,type:"text"|"image",content:string,mediaType?:string,filename?:string,dimensions?:image-dimensions,sourcePath?:string}`. A serialized structured history entry is `{display:string,pastedContents?:map<number,pasted-item>,pastedText?:string}`; its live form requires the pasted-content map. `OutputStyle` is an unrestricted string identifier. Persist these only in their owning prompt-history store, never under `GlobalConfig` by inference.

## GlobalConfig schema

All fields are top-level. “Optional” means absent is accepted; the reader also tolerates incompatible JSON under `GCFG-WIRE-005`.

`GCFG-GLOBAL-001` — Identity, installation, account, onboarding, and release fields:

| Key | Declared value and meaning |
| --- | --- |
| `apiKeyHelper?` | legacy executable helper string; settings now owns the preference |
| `projects?` | normalized-path to `ProjectConfig` map |
| `numStartups` | startup counter |
| `installMethod?` | `InstallMethod` |
| `autoUpdates?` | legacy update preference |
| `autoUpdatesProtectedForNative?` | distinguishes native protection from user opt-out |
| `doctorShownAtSession?` | startup count at last doctor display |
| `userID?` | installation identity; generated as 32 random bytes encoded to 64 lowercase hexadecimal characters |
| `theme` | `ThemeSetting` |
| `hasCompletedOnboarding?` | global onboarding receipt |
| `lastOnboardingVersion?` | last onboarding-reset product version |
| `lastReleaseNotesSeen?` | product version whose notes were seen |
| `changelogLastFetched?` | epoch-millisecond fetch time |
| `cachedChangelog?` | deprecated cached content, migrated to a cache file |
| `mcpServers?` | user-scoped MCP server map from `GCFG-TYPE-003` |
| `agentxAiMcpEverConnected?` | first-party connector IDs that connected at least once |
| `preferredNotifChannel` | `NotificationChannel` |
| `customNotifyCommand?` | deprecated executable notification command |
| `verbose` | detailed-output preference |
| `customApiKeyResponses?` | `{approved?:string[], rejected?:string[]}` containing normalized/truncated key fingerprints |
| `primaryApiKey?` | plaintext API-key fallback when secure storage is unavailable |
| `hasAcknowledgedCostThreshold?` | one-time cost-consent receipt |
| `hasSeenUndercoverAutoNotice?` | internal automatic-mode notice receipt |
| `hasSeenUltraplanTerms?` | internal remote-control terms receipt |
| `hasResetAutoModeOptInForDefaultOffer?` | one-shot permission migration guard |
| `oauthAccount?` | `AccountInfo` from `GCFG-TYPE-002` |

`GCFG-GLOBAL-002` — Editing, terminal, IDE, prompt, and presentation fields:

| Key | Declared value and meaning |
| --- | --- |
| `iterm2KeyBindingInstalled?` | legacy terminal binding receipt |
| `editorMode?` | `EditorMode` |
| `bypassPermissionsModeAccepted?` | deprecated dangerous-mode consent receipt |
| `hasUsedBackslashReturn?` | input-hint state |
| `autoCompactEnabled` | automatic compaction preference |
| `showTurnDuration` | show completed-turn duration |
| `env` | legacy string-to-string environment map |
| `hasSeenTasksHint?` | task-hint receipt |
| `hasUsedStash?` | prompt-stash usage receipt |
| `hasUsedBackgroundTask?` | background-task usage receipt |
| `queuedCommandUpHintCount?` | queued-command hint counter |
| `diffTool?` | `DiffTool` |
| `iterm2SetupInProgress?` | interrupted-setup marker |
| `iterm2BackupPath?` | terminal preferences backup path |
| `appleTerminalBackupPath?` | terminal preferences backup path |
| `appleTerminalSetupInProgress?` | interrupted-setup marker |
| `shiftEnterKeyBindingInstalled?` | multiline binding receipt |
| `optionAsMetaKeyInstalled?` | terminal meta-key receipt |
| `autoConnectIde?` | connect when exactly one valid IDE is available |
| `autoInstallIdeExtension?` | IDE-extension auto-install preference |
| `hasIdeOnboardingBeenShown?` | terminal-name to boolean map |
| `ideHintShownCount?` | IDE command-hint counter |
| `hasIdeAutoConnectDialogBeenShown?` | auto-connect dialog receipt |
| `tipsHistory` | tip ID to `numStartups` value at last display |
| `lastShownEmergencyTip?` | last emergency-tip identifier |
| `respectGitignore` | file-picker ignore preference; independent `.ignore` handling still applies |
| `copyFullResponse` | whether copy bypasses the response picker |
| `copyOnSelect?` | selection auto-copy; absence resolves to enabled |
| `showExpandedTodos?` | show task view even when empty |
| `showSpinnerTree?` | show teammate tree instead of compact pills |
| `showStatusInTerminalTab?` | terminal-tab status indicator |
| `terminalProgressBarEnabled` | terminal progress protocol preference |

`GCFG-GLOBAL-003` — Companion, survey, memory, usage, and hint fields:

| Key | Declared value and meaning |
| --- | --- |
| `companion?` | `StoredCompanion` |
| `companionMuted?` | companion audio/notification preference |
| `feedbackSurveyState?` | `{lastShownTime?:number}` |
| `transcriptShareDismissed?` | do-not-ask-again receipt |
| `memoryUsageCount` | memory-add counter |
| `promptQueueUseCount` | prompt-queue usage counter |
| `btwUseCount` | auxiliary-command usage counter |
| `lastPlanModeUse?` | epoch-millisecond last-use time |
| `githubActionSetupCount?` | integration setup counter |
| `slackAppInstallCount?` | integration install-click counter |
| `skillUsage?` | skill ID to `{usageCount:number,lastUsedAt:number}` |
| `agentxCodeHints?` | `{plugin?:string[],disabled?:boolean}`; plugin receipts are capped by the consumer at 100 |

`GCFG-GLOBAL-004` — Server-derived entitlement and offer caches:

| Key | Declared value and meaning |
| --- | --- |
| `hasShownS1MWelcomeV2?` | organization ID to welcome receipt |
| `s1mAccessCache?` | organization ID to `{hasAccess:boolean,hasAccessNotAsDefault?:boolean,timestamp:number}` |
| `s1mNonSubscriberAccessCache?` | same shape for pay-as-you-go access |
| `passesEligibilityCache?` | organization ID to referral entry from `GCFG-TYPE-006` |
| `groveConfigCache?` | account ID to `{grove_enabled:boolean,timestamp:number}` |
| `passesUpsellSeenCount?` | offer display counter |
| `hasVisitedPasses?` | passes-surface visit receipt |
| `passesLastSeenRemaining?` | last observed remaining-pass count |
| `overageCreditGrantCache?` | organization ID to `{info:{available:boolean,eligible:boolean,granted:boolean,amount_minor_units:number|null,currency:string|null},timestamp:number}` |
| `overageCreditUpsellSeenCount?` | offer display counter |
| `hasVisitedExtraUsage?` | extra-usage surface visit receipt |
| `subscriptionNoticeCount?` | subscription notice counter |
| `hasAvailableSubscription?` | cached availability |
| `subscriptionUpsellShownCount?` | deprecated counter |
| `recommendedSubscription?` | deprecated server recommendation string |
| `penguinModeOrgEnabled?` | last server-observed fast-mode organization state |
| `cachedExtraUsageDisabledReason?` | absent means no cache, `null` means enabled, string means disabled reason |
| `clientDataCache?` | open server-owned string-keyed object or `null` |
| `additionalModelOptionsCache?` | `ModelOption[]` from `GCFG-TYPE-005` |
| `metricsStatusCache?` | `{enabled:boolean,timestamp:number}`; only successful API responses may populate it |

`GCFG-GLOBAL-005` — Notices, feature exposure, and migration receipts:

| Key | Declared value and meaning |
| --- | --- |
| `voiceNoticeSeenCount?` | voice availability notice count |
| `voiceLangHintShownCount?` | language-hint count |
| `voiceLangHintLastLanguage?` | last resolved speech-language code |
| `voiceFooterHintSeenCount?` | push-to-talk footer count |
| `opus1mMergeNoticeSeenCount?` | model-merge notice count |
| `experimentNoticesSeenCount?` | experiment ID to count |
| `hasShownOpusPlanWelcome?` | organization ID to receipt |
| `firstStartTime?` | ISO-8601 first-start time |
| `agentxCodeFirstTokenDate?` | ISO-8601 first OAuth-token date |
| `modelSwitchCalloutDismissed?` | callout dismissal receipt |
| `modelSwitchCalloutLastShown?` | epoch-millisecond display time |
| `modelSwitchCalloutVersion?` | callout version |
| `effortCalloutDismissed?` | legacy effort callout receipt |
| `effortCalloutV2Dismissed?` | current effort callout receipt |
| `desktopUpsellSeenCount?` | desktop offer count, consumer caps at three |
| `desktopUpsellDismissed?` | do-not-ask receipt |
| `idleReturnDismissed?` | idle-return do-not-ask receipt |
| `opusProMigrationComplete?` | model migration guard |
| `opusProMigrationTimestamp?` | migration notification time |
| `sonnet1m45MigrationComplete?` | model migration guard |
| `legacyOpusMigrationTimestamp?` | migration notification time |
| `sonnet45To46MigrationTimestamp?` | migration notification time |
| `migrationVersion?` | last completed synchronous migration-set number |

`GCFG-GLOBAL-006` — Runtime/UI switches and local feature evidence:

| Key | Declared value and meaning |
| --- | --- |
| `todoFeatureEnabled` | task feature preference |
| `messageIdleNotifThresholdMs` | idle threshold before completion notification |
| `fileCheckpointingEnabled` | file-history checkpoint preference |
| `taskCompleteNotifEnabled?` | explicit opt-in to remote task-complete push |
| `inputNeededNotifEnabled?` | explicit opt-in to remote input-needed push |
| `agentPushNotifEnabled?` | explicit opt-in to agent-selected push |
| `cachedStatsigGates` | gate-name to boolean cache |
| `cachedDynamicConfigs?` | dynamic-config name to open server value |
| `cachedGrowthBookFeatures?` | feature-name to open server value |
| `growthBookOverrides?` | internal local feature-name overrides; evaluated after environment overrides and before resolved server value |
| `permissionExplainerEnabled?` | absence resolves to enabled |
| `prStatusFooterEnabled?` | absence resolves to enabled when the feature exists |
| `autoPermissionsNotificationCount?` | internal notice counter |
| `speculationEnabled?` | internal speculative assistance switch; absence resolves to enabled |

`GCFG-GLOBAL-007` — Integrations, browser, remote, and multi-agent fields:

| Key | Declared value and meaning |
| --- | --- |
| `githubRepoPaths?` | lowercase `owner/repository` to absolute clone-path array |
| `deepLinkTerminal?` | terminal application captured for later headless deep-link launch |
| `iterm2It2SetupComplete?` | split-pane CLI setup receipt |
| `preferTmuxOverIterm2?` | teammate backend preference |
| `officialMarketplaceAutoInstallAttempted?` | marketplace bootstrap receipt |
| `officialMarketplaceAutoInstalled?` | marketplace bootstrap success receipt |
| `officialMarketplaceAutoInstallFailReason?` | `policy_blocked | git_unavailable | gcs_unavailable | unknown` |
| `officialMarketplaceAutoInstallRetryCount?` | retry count |
| `officialMarketplaceAutoInstallLastAttemptTime?` | epoch-millisecond attempt time |
| `officialMarketplaceAutoInstallNextRetryTime?` | earliest retry time |
| `hasCompletedAgentXInChromeOnboarding?` | browser onboarding receipt |
| `agentxInChromeDefaultEnabled?` | explicit browser default; absence delegates to platform default |
| `cachedChromeExtensionInstalled?` | last local installation probe |
| `chromeExtension?` | `{pairedDeviceId?:string,pairedDeviceName?:string}` |
| `lspRecommendationDisabled?` | suppress all recommendations |
| `lspRecommendationNeverPlugins?` | plugin IDs never to recommend |
| `lspRecommendationIgnoredCount?` | ignored recommendation count; consumer stops after five |
| `teammateMode?` | `auto | tmux | in-process` |
| `teammateDefaultModel?` | absent selects the backward-compatible fixed default, `null` inherits leader model, string names a model |
| `tungstenPanelVisible?` | internal live-panel preference |
| `startupPrefetchedAt?` | epoch-millisecond background-prefetch throttle time |
| `remoteControlAtStartup?` | explicit global opt-in/out; absence uses `GCFG-DERIVE-001` |
| `remoteDialogSeen?` | first remote-control explanation receipt |
| `bridgeOauthDeadExpiresAt?` | failed-token expiry identity used for cross-process backoff |
| `bridgeOauthDeadFailCount?` | consecutive failure count; consumer caps persistent writes after three |

## ProjectConfig schema

`GCFG-PROJECT-001` — Tool, context, session-statistics, and example fields:

| Key | Declared value and meaning |
| --- | --- |
| `allowedTools` | tool-name allowlist; empty means no project restriction |
| `mcpContextUris` | retained MCP context URI list |
| `mcpServers?` | local-scoped MCP server map |
| `lastAPIDuration?` | last session API duration |
| `lastAPIDurationWithoutRetries?` | last session API duration excluding retries |
| `lastToolDuration?` | last session tool duration |
| `lastCost?` | last session cost in USD |
| `lastDuration?` | last session total duration |
| `lastLinesAdded?`, `lastLinesRemoved?` | last session line deltas |
| `lastTotalInputTokens?`, `lastTotalOutputTokens?` | last session token totals |
| `lastTotalCacheCreationInputTokens?`, `lastTotalCacheReadInputTokens?` | last session cache token totals |
| `lastTotalWebSearchRequests?` | last session web-search count |
| `lastFpsAverage?`, `lastFpsLow1Pct?` | last terminal rendering metrics |
| `lastSessionId?` | session identity associated with all `last*` usage values |
| `lastModelUsage?` | model ID to `{inputTokens,outputTokens,cacheReadInputTokens,cacheCreationInputTokens,webSearchRequests,costUSD}`, all numeric |
| `lastSessionMetrics?` | metric name to numeric value |
| `exampleFiles?` | generated example-file path list |
| `exampleFilesGeneratedAt?` | epoch-millisecond generation time |

Only restore stored cost state when the requested session ID equals `lastSessionId`. Startup may report the prior session's metrics without clearing them; the next completed session overwrites them.

`GCFG-PROJECT-002` — Consent, onboarding, and MCP fields:

| Key | Declared value and meaning |
| --- | --- |
| `hasTrustDialogAccepted?` | persistent trust receipt for this project identity and lexical descendants |
| `hasCompletedProjectOnboarding?` | project onboarding completion receipt |
| `projectOnboardingSeenCount` | display counter; onboarding suppresses at four |
| `hasAgentXMdExternalIncludesApproved?` | permission to load external instruction includes |
| `hasAgentXMdExternalIncludesWarningShown?` | warning receipt, including a decline |
| `enabledMcpjsonServers?` | legacy approved project server names |
| `disabledMcpjsonServers?` | legacy declined project server names |
| `enableAllProjectMcpServers?` | legacy approve-all flag |
| `disabledMcpServers?` | opt-out list across ordinary MCP scopes |
| `enabledMcpServers?` | opt-in list for built-ins that default disabled |

`GCFG-PROJECT-003` — Worktree and remote-control fields:

| Key | Declared value and meaning |
| --- | --- |
| `activeWorktreeSession?` | `{originalCwd,worktreePath,worktreeName,originalBranch?,sessionId,hookBased?}` plus compatibility-preserved runtime fields `worktreeBranch?`, `originalHeadCommit?`, `tmuxSessionName?`, `creationDurationMs?`, and `usedSparsePaths?` |
| `remoteControlSpawnMode?` | `same-dir | worktree` per-project preference |

The worktree record is a recovery pointer, not proof that paths, branches, hooks, or sessions still exist. Verify external state before restoration. Clearing or keeping a worktree clears this pointer after changing back to the original directory; a crash between external worktree mutation and the config write can leave either an orphaned worktree or a stale pointer.

## Defaults and sparse serialization

`GCFG-DEFAULT-001` — Fresh global defaults are exactly:

```text
numStartups=0; installMethod=absent; autoUpdates=absent; theme=dark;
preferredNotifChannel=auto; verbose=false; editorMode=normal;
autoCompactEnabled=true; showTurnDuration=true; hasSeenTasksHint=false;
hasUsedStash=false; hasUsedBackgroundTask=false; queuedCommandUpHintCount=0;
diffTool=auto; customApiKeyResponses={approved:[],rejected:[]}; env={};
tipsHistory={}; memoryUsageCount=0; promptQueueUseCount=0; btwUseCount=0;
todoFeatureEnabled=true; showExpandedTodos=false;
messageIdleNotifThresholdMs=60000; autoConnectIde=false;
autoInstallIdeExtension=true; fileCheckpointingEnabled=true;
terminalProgressBarEnabled=true; cachedStatsigGates={};
cachedDynamicConfigs={}; cachedGrowthBookFeatures={}; respectGitignore=true;
copyFullResponse=false.
```

Every fresh construction receives new nested containers. The exported default snapshot is not the factory result used by each read.

`GCFG-DEFAULT-002` — Fresh project defaults are `allowedTools=[]`, `mcpContextUris=[]`, `mcpServers={}`, `enabledMcpjsonServers=[]`, `disabledMcpjsonServers=[]`, `hasTrustDialogAccepted=false`, `projectOnboardingSeenCount=0`, `hasAgentXMdExternalIncludesApproved=false`, and `hasAgentXMdExternalIncludesWarningShown=false`.

`GCFG-DEFAULT-003` — A read returns a shared in-process object, not a defensive copy. Updaters and consumers must treat it as immutable even though the legacy `allowedTools` repair mutates the cached project object. Returning the identical object from an updater is the explicit no-write signal.

## Bootstrap, reads, and cache coherence

`GCFG-READ-001` — Configuration access is disabled during module initialization. `enableConfigs` transitions the process gate once, then strictly parses the global file before configuration-dependent startup. A pre-enable non-test read throws.

`GCFG-READ-002` — Strict bootstrap behavior differs from recovery reads: invalid JSON raises a path-bearing parse error and aborts the enabling call. The enabled flag is already set and is not rolled back. Calling `enableConfigs` again is a no-op, so callers must treat the first failure as terminal rather than attempting partial startup.

`GCFG-READ-003` — The first ordinary global read stats then synchronously reads the file, applies defaults and the inline migration, records `(mtime,size)`, publishes the cache, and starts the freshness watcher. Stat-before-read guarantees that a concurrent later replacement has an mtime the watcher can observe.

`GCFG-CACHE-001` — After startup, ordinary reads return the cached object without filesystem access. A nonpersistent file-stat watcher polls every 1,000 ms. On cleanup, stop it and permit a later reinitialization.

`GCFG-CACHE-002` — The watcher accepts only evidence with an mtime strictly greater than the cached mtime. It asynchronously reads, checks again that the cache did not advance, accepts non-null JSON objects (including compatibility shapes), shallowly overlays fresh defaults, runs the inline migration, and updates `(cache,mtime,size)`. Parse/read failures, deletion evidence with mtime zero, equal/backward mtimes, and nonobject JSON leave the prior cache intact without a user-facing corruption workflow.

`GCFG-CACHE-003` — A successful local write publishes its exact in-memory result and sets cache time to the post-write wall clock, intentionally suppressing the watcher's event for that write. It clears last-read stat evidence. This is eventual cross-process coherence, not a linearizable cache: clock skew, coarse mtimes, or a rapid external write whose mtime does not exceed the synthetic cache time can remain unseen until a later advancing write or process restart.

`GCFG-CACHE-004` — Track cache hits, misses, and actual global-file writes for diagnostics. Emit aggregate cache statistics during registered cleanup. A write-rate display threshold of 20 writes is diagnostic only and does not block writes.

## Writes, locking, backups, and concurrency

`GCFG-WRITE-001` — A global or project save accepts a pure updater. Under the normal path: create the parent directory; acquire a synchronous sibling lock at `<file>.lock`; re-read the complete file under the lock; apply the updater to that fresh state; treat identical-object return as no-op; create/rotate a backup; sparsify defaults; flush a temporary file; atomically replace the target; release the lock; then write through the process cache.

`GCFG-WRITE-002` — Lock acquisition uses the lock provider's default stale/refresh behavior and no configured retry policy. A compromised-lock callback logs instead of asynchronously crashing the process. Acquisition taking over 100 ms is diagnosed. `(mtime,size)` mismatch against the last read emits stale-write telemetry but does not reject the update because the under-lock re-read is authoritative.

`GCFG-WRITE-003` — Any exception in the locked path, including contention, enters an unlocked fallback: re-read, apply the updater again, sparsify, and write without backup. Consequently the updater must be pure and repeatable, and the fallback admits lost-update races. Locking is best-effort, not a cross-process transaction guarantee.

`GCFG-WRITE-004` — Before a normal locked replacement, copy the existing file to `<configuration-home>/backups/<basename>.backup.<epoch-ms>` unless the newest timestamped backup is less than 60,000 ms old. Retain the five newest healthy backups; ignore cleanup failures. Missing source or backup failure does not block the primary write. Backup creation is absent on the unlocked fallback.

`GCFG-WRITE-005` — Write through an existing symbolic link to its resolved target while preserving the link. For a new target request owner-only mode (`0600` equivalent). For an existing target, preserve its existing mode, even if permissive. Write and flush `<target>.tmp.<process-id>.<epoch-ms>`, apply the target mode, then rename over the target. No directory flush is promised.

`GCFG-WRITE-006` — If temporary-write or rename fails, attempt to delete the temporary file and then flush a direct, non-atomic replacement. If that also fails, propagate the error. A process death during direct replacement can truncate or corrupt the only current file.

`GCFG-WRITE-007` — The auth-loss guard compares a fresh reread with the last cached object and refuses a write only when the cache has `oauthAccount` but the reread does not, or the cache has `hasCompletedOnboarding=true` but the reread does not. It does not protect `primaryApiKey`, projects, trust, counters, or other fields. On refusal, keep file and cache unchanged and emit a diagnostic event.

`GCFG-WRITE-008` — `saveGlobalConfig` always replaces `projects` with the pre-update project's map after removing each legacy nested `history` field. A global updater must not be used to mutate projects. `saveCurrentProjectConfig` is the only ordinary project updater and merges one project key into the under-lock fresh global map.

`GCFG-WRITE-009` — Creating a user ID is not compare-and-set: concurrent first processes can each generate and return different IDs, with the last completed write becoming durable. First-start time is safer: the under-lock updater preserves a time already written by another process.

## Trust and derived helpers

`GCFG-TRUST-001` — Session trust and durable trust are distinct. Accepting the user home stores session-only trust. Accepting another workspace writes `hasTrustDialogAccepted=true` at the primary project key.

`GCFG-TRUST-002` — Current-session trust is true if session-only trust is true, the primary project key is trusted, or the live working directory or any lexical ancestor is trusted. Latch only `true`; re-evaluate `false` so mid-session acceptance becomes visible. A disk revocation after the latch does not revoke the running process.

`GCFG-TRUST-003` — Arbitrary-path trust starts at the resolved target and walks lexical ancestors. It does not consult session-only trust or the primary project key unless encountered in that walk. Treat a persisted receipt as consent for that identity, not as filesystem integrity evidence.

`GCFG-DERIVE-001` — Effective `remoteControlAtStartup` precedence is explicit stored boolean, then an internal build's feature-gated auto-connect default, then `false`. Explicit `false` always wins.

`GCFG-DERIVE-002` — Custom-key status checks the approved fingerprint list before the rejected list; a fingerprint accidentally present in both is `approved`.

`GCFG-DERIVE-003` — Auto-updater disable reason precedence is development build, truthy `DISABLE_AUTOUPDATER`, essential-traffic-only environment reason, then stored `autoUpdates=false`. Stored false is ignored for a native installation when `autoUpdatesProtectedForNative=true`. Plugin auto-update is skipped for any disable reason unless `FORCE_AUTOUPDATE_PLUGINS` is truthy.

`GCFG-DERIVE-004` — Memory path mapping is: user instructions at `<configuration-home>/AGENTX.md`; local instructions at `<original-cwd>/AGENTX.local.md`; project instructions at `<original-cwd>/AGENTX.md`; managed instructions at `<managed-config-location>/AGENTX.md`; automatic memory at its memory service entrypoint; and team memory at its gated team entrypoint. Managed rules are `<managed-config-location>/.agentx/rules`; user rules are `<configuration-home>/rules`.

## Migrations

`GCFG-MIG-001` — On every read, if `installMethod` is absent, interpret legacy `autoUpdaterStatus`:

| Legacy value | Result |
| --- | --- |
| `migrated` | `installMethod=local` |
| `installed` | `installMethod=native` |
| `enabled`, `no_permissions`, `not_configured` | `installMethod=global` |
| `disabled` | `installMethod=unknown`, `autoUpdates=false` |
| absent/unknown | `installMethod=unknown` |

Initialize `autoUpdates` to its stored value or `true`. This in-memory migration does not delete the unknown legacy key.

`GCFG-MIG-002` — The current synchronous migration-set marker is `11`. When the stored marker differs, run these ordered, idempotent operations, then write marker `11`:

1. move a user `autoUpdates=false` preference (but not native protection) to user settings environment `DISABLE_AUTOUPDATER=1`, apply it immediately, then remove both legacy update fields;
2. move dangerous-mode consent to user setting `skipDangerousModePermissionPrompt=true` when not already effective, then remove its legacy field;
3. merge legacy project MCP approve/deny fields into local settings without duplicates, then remove them from project config;
4. run the eligible first-party Pro default migration and record its guard/optional notice time;
5. pin legacy `sonnet[1m]` user settings and in-memory override to the explicit Sonnet 4.5 one-million-context model, then set its guard;
6. migrate eligible explicit legacy Opus model strings in user settings to the `opus` alias and record a notice time;
7. migrate eligible explicit Sonnet 4.5 user settings to Sonnet 4.6 aliases and optionally record a notice time;
8. migrate eligible user `opus` to `opus[1m]` without changing command-line overrides;
9. rename unknown legacy `replBridgeEnabled` to `remoteControlAtStartup` only when the new field is absent;
10. in classifier builds, clear legacy automatic-mode prompt suppression only for the eligible offer and set its one-shot guard;
11. in internal builds, migrate legacy model aliases in user settings.

`GCFG-MIG-003` — The changelog-content migration is asynchronous and outside marker completion; failure is swallowed and retried later. Synchronous migration steps use independent writes across this file and settings files. A crash can leave a destination written before its source is removed; each step must tolerate retry, and the marker is written only after the ordered synchronous chain returns.

`GCFG-MIG-004` — Removing nested project `history` is a compatibility cleanup performed opportunistically by global saves, not by read and not necessarily by project-only saves. Preserve unknown fields other than explicitly migrated/removed fields.

## Secrets and server-owned data

`GCFG-SEC-001` — Classify `primaryApiKey` as a secret. Prefer platform secure storage; persist it here only when secure storage is unavailable or fails. Removal clears secure storage and this field. Owner-only creation mode reduces exposure but does not encrypt the file.

`GCFG-SEC-002` — Treat legacy `apiKeyHelper`, `customNotifyCommand`, MCP commands, MCP header helpers, and MCP environment as executable or sensitive. Trust and permission checks belong to their consumers. Never execute them merely while parsing or migrating this file.

`GCFG-SEC-003` — Treat `env`, MCP headers/environment/auth tokens, and arbitrary unknown fields as potentially secret. Redact values, full objects, authorization data, email, account IDs, paths where policy requires, and helper output from logs and telemetry. Corrupted and healthy backups may contain all of these values.

`GCFG-SEC-004` — Server-owned, non-authoritative cache fields include account/profile metadata, entitlement caches, offer data, Statsig/GrowthBook/dynamic-config caches, client data, model options, and metrics status. Validate shape, identity key, freshness, feature eligibility, and current policy at use. Stale cache may improve startup but cannot grant durable security authority.

`GCFG-SEC-005` — An existing config retains its filesystem mode rather than being forcibly tightened, and backups receive no independent owner-only permission correction. An implementation must preserve this compatibility behavior or perform an explicit, user-visible permission migration; never silently assume every existing or backup file is owner-only.

## Failure and crash matrix

| Evidence | Required behavior | Residual risk/recovery |
| --- | --- | --- |
| file absent | return fresh defaults; if a healthy backup exists, print its path and a manual restore command | never auto-restore |
| invalid JSON during strict enable | raise parse error and abort startup initialization | repair or restore file; do not continue partially |
| invalid JSON during non-strict recovery read | report corruption, copy unique corrupted bytes to `backups/<basename>.corrupted.<epoch-ms>`, point to newest healthy backup, return fresh defaults | corrupted backups are content-deduplicated but not count-pruned |
| watcher sees invalid/truncated replacement | retain last cache silently | later valid advancing mtime or restart |
| lock contention/lock error | attempt unlocked fallback | lost update possible |
| auth-loss guard triggers | skip write and retain good cache | only OAuth account/onboarding protected |
| backup fails | continue primary write | reduced recovery evidence |
| crash before temp rename | old target normally remains; temp may remain | cleanup on later maintenance |
| crash during non-atomic fallback | target may be empty, truncated, or malformed | strict next startup fails; manual backup restoration |
| crash after rename before cache publication | disk is new, memory may be old until process exits | next process reads disk; current call reports failure only if later step throws |
| crash between settings migration write and source cleanup | both old and new representations may exist | idempotent retry and documented precedence |
| external write with non-advancing mtime | process retains old cache | restart or later advancing write |
| missing/invalid imported server cache | feature uses its cold/default behavior and may refresh | never promote cache to policy authority |

## Acceptance scenarios

`GCFG-A01` — With no override and no legacy file, first enable reads `<home>/.agentx.json`, overlays sparse `{theme:"light"}` on fresh defaults, and returns `theme=light`, `autoCompactEnabled=true`, and fresh empty maps. A later write omits every top-level value equal to a default.

`GCFG-A02` — A linked worktree and its main repository derive the same canonical project key. A nonrepository subdirectory derives the absolute original launch path, while trust lookup can still inherit a separately trusted lexical ancestor.

`GCFG-A03` — Two processes update different counters. Each normal updater re-reads under the lock and preserves the other's field. Force lock acquisition to fail for both: the unlocked fallback admits a last-writer-wins loss, which the conformance test must expose rather than claiming serializability.

`GCFG-A04` — Kill the process after flushed temp creation but before rename: the prior target remains readable. Force atomic replacement failure and kill during direct fallback: the next strict enable rejects the truncated file and leaves a healthy timestamped backup available when the locked path created one.

`GCFG-A05` — Corrupt the file after a valid cache containing OAuth account data is loaded. The watcher retains the valid cache. A later save's fresh read returns defaults; the auth-loss guard refuses to overwrite and keeps the cached account.

`GCFG-A06` — Repeat `GCFG-A05` with no OAuth account and incomplete onboarding but with project trust and a plaintext primary API key. The narrow guard does not protect those fields; a fallback write can lose them. Treat this as an exact compatibility risk, not a guarantee to preserve all sensitive state.

`GCFG-A07` — Put the same normalized key fingerprint in approved and rejected arrays. Status is approved. Remove the API key: secure storage and plaintext fallback are cleared, while approval fingerprints remain unless the caller separately removes them.

`GCFG-A08` — Persist trust on a repository root and query a child path: trust is true. Revoke the disk field after current-session trust latched true: the running process remains trusted, while a new process observes the revocation.

`GCFG-A09` — Supply `allowedTools` as a JSON-encoded string: current project read repairs it in memory to an array. Supply another wrong-typed field and an unknown top-level field: both survive the compatibility overlay; the relevant consumer rejects or ignores the wrong type, and a spreading updater retains the unknown field.

`GCFG-A10` — Crash after a migration writes local MCP settings but before legacy project fields are removed. On restart, the migration merges without duplicates, removes the legacy fields, completes remaining migrations, and only then records marker 11.

`GCFG-A11` — Create the file at a permissive mode, then save. The replacement preserves that mode. Delete it and save anew: the new file requests owner-read/write only. Verify backups may contain credential fallback and receive equivalent protection review.

`GCFG-A12` — Write a valid external replacement immediately after a local write with an mtime not greater than the synthetic cache time. The running process may miss it; restart must load it. The test establishes eventual, mtime-dependent coherence rather than linearizability.

## Non-normative provenance

Reference behavior was specified from the durable configuration record, path/environment resolution, filesystem flush and symbolic-link writer, lock adapter, startup migration chain, trust dialogs, OAuth/account storage, MCP configuration, worktree lifecycle, project onboarding, cost restoration, feature caches, and remote-control callers. These locations and implementation-language symbols are provenance only; every implementation requirement is restated above.
