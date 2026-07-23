# Persisted settings schema contract

This document defines the exact persisted settings grammar consumed by the configuration cascade. It complements the source/merge contract: this file says which values are valid; [settings resolution](settings-resolution.md) says where they come from and how valid sources combine. `SETS-*` identifiers are stable implementation anchors.

## Contents

1. [Validation and compatibility model](#validation-and-compatibility-model)
2. [Core, model, and presentation fields](#core-model-and-presentation-fields)
3. [Permissions, MCP, and hooks](#permissions-mcp-and-hooks)
4. [Sandbox schema](#sandbox-schema)
5. [Plugins and marketplaces](#plugins-and-marketplaces)
6. [Remote, memory, and feature-gated fields](#remote-memory-and-feature-gated-fields)
7. [Failure and evolution rules](#failure-and-evolution-rules)
8. [Acceptance scenarios](#acceptance-scenarios)

## Validation and compatibility model

`SETS-001` — A settings source is a JSON object. The outer object is forward-compatible: preserve unknown top-level keys in the validated effective object and in raw round trips. Most declared nested objects are closed for effective use: unknown nested keys are removed from the validated view, although the raw source remains available for safe editing. The `permissions` and `sandbox` objects explicitly preserve unknown nested keys.

`SETS-002` — If any ordinary declared field has the wrong type or violates a constraint, validation of that complete source fails and none of that source becomes effective. Preserve its parseable raw object and report field provenance. Two fields deliberately recover locally instead: invalid `effortLevel` becomes absent, and invalid `strictPluginOnlyCustomization` becomes absent according to `SETS-041`.

`SETS-003` — All fields are optional unless a nested grammar says otherwise. Absence means “no contribution from this source,” not a language-specific null/undefined value. JSON `null` is invalid except where an explicitly documented union permits it; this schema currently defines no general nullable top-level settings.

`SETS-004` — `$schema`, when present, must equal exactly `https://json.schemastore.org/agentx-code-settings.json`. Environment map values are coerced to strings; this is the only broad scalar coercion. Numeric strings do not satisfy numeric settings unless a specific command-line parser constructs a number before settings validation.

`SETS-005` — Feature-gated keys exist in the effective schema only when their build/runtime gate is active. Because the outer object preserves unknown fields, a gated-off key survives round trips but remains unvalidated and behaviorally inactive. Enabling the gate in a later run validates the retained raw value before use.

## Core, model, and presentation fields

`SETS-010` — Core and integration fields have this exact grammar:

| Key | Value |
| --- | --- |
| `$schema` | exact literal from `SETS-004` |
| `apiKeyHelper` | string command/path |
| `awsCredentialExport` | string command/path |
| `awsAuthRefresh` | string command/path |
| `gcpAuthRefresh` | string command |
| `fileSuggestion` | `{type:"command", command:string}` |
| `respectGitignore` | boolean |
| `cleanupPeriodDays` | nonnegative integer; zero disables transcript persistence and triggers startup cleanup under the persistence contract |
| `env` | map from string key to value coerced to string |
| `attribution` | `{commit?:string, pr?:string}`; empty strings deliberately suppress the corresponding default |
| `includeCoAuthoredBy` | deprecated boolean retained for compatibility |
| `includeGitInstructions` | boolean |
| `defaultShell` | `bash` or `powershell`; absence does not platform-auto-flip the default |
| `otelHeadersHelper` | string command/path |
| `language` | string |
| `skipWebFetchPreflight` | boolean |
| `plansDirectory` | string path |
| `agentxMdExcludes` | string array |
| `pluginTrustMessage` | string; runtime honors it only from managed policy |

`SETS-011` — Model, agent, and update fields:

| Key | Value |
| --- | --- |
| `model` | string |
| `availableModels` | string array; absent permits all, empty permits only the runtime default |
| `modelOverrides` | string-to-string map |
| `alwaysThinkingEnabled` | boolean |
| `effortLevel` | external builds: `low`, `medium`, `high`; internal build also `max`; invalid value is caught and treated as absent |
| `advisorModel` | string |
| `fastMode` | boolean |
| `fastModePerSessionOptIn` | boolean |
| `agent` | string agent name |
| `autoUpdatesChannel` | `latest` or `stable` |
| `minimumVersion` | string |

`SETS-012` — Presentation and assistance fields:

| Key | Value |
| --- | --- |
| `statusLine` | `{type:"command", command:string, padding?:number}` |
| `outputStyle` | string |
| `feedbackSurveyRate` | number in inclusive range 0 through 1 |
| `spinnerTipsEnabled` | boolean |
| `spinnerVerbs` | `{mode:"append"|"replace", verbs:string[]}` |
| `spinnerTipsOverride` | `{excludeDefault?:boolean, tips:string[]}` |
| `syntaxHighlightingDisabled` | boolean |
| `terminalTitleFromRename` | boolean |
| `promptSuggestionEnabled` | boolean |
| `showClearContextOnPlanAccept` | boolean |
| `companyAnnouncements` | string array |
| `prefersReducedMotion` | boolean |
| `showThinkingSummaries` | boolean |

`SETS-013` — Worktree configuration is `{symlinkDirectories?:string[], sparsePaths?:string[]}`. Remote configuration is `{defaultEnvironmentId?:string}`. SSH configuration is an array of records `{id:string, name:string, sshHost:string, sshPort?:integer, sshIdentityFile?:string, startDirectory?:string}`. The schema does not impose a positive/range constraint on `sshPort`; connection validation may do so later.

## Permissions, MCP, and hooks

`SETS-020` — `permissions` preserves unknown keys and accepts:

| Key | Value |
| --- | --- |
| `allow` | permission-rule string array |
| `deny` | permission-rule string array |
| `ask` | permission-rule string array |
| `defaultMode` | external: `acceptEdits`, `bypassPermissions`, `default`, `dontAsk`, `plan`; classifier-enabled builds also `auto` |
| `disableBypassPermissionsMode` | exact literal `disable` |
| `disableAutoMode` | exact literal `disable`, classifier-enabled builds only |
| `additionalDirectories` | string array |

`bubble` is an internal transition state and is never valid persisted user input. Permission arrays receive the item-level recovery described in `SET-016`: a malformed rule is reported and removed before full-source validation, without discarding valid siblings.

`SETS-021` — Related top-level permission/policy fields are booleans unless noted: `allowManagedPermissionRulesOnly`, `skipDangerousModePermissionPrompt`, and top-level `disableAutoMode:"disable"`. Classifier-enabled builds additionally accept `skipAutoPermissionPrompt:boolean`, `useAutoModeDuringPlan:boolean`, and `autoMode:{allow?:string[], soft_deny?:string[], environment?:string[]}`. Internal classifier builds also accept `autoMode.deny:string[]` and `classifierPermissionsEnabled:boolean`.

`SETS-022` — MCP selection fields are:

| Key | Value |
| --- | --- |
| `enableAllProjectMcpServers` | boolean |
| `enabledMcpjsonServers` | string array |
| `disabledMcpjsonServers` | string array |
| `allowedMcpServers` | MCP match-entry array |
| `deniedMcpServers` | MCP match-entry array |
| `allowManagedMcpServersOnly` | boolean |

One MCP match entry has exactly one of: `{serverName:string}` where the name matches `[A-Za-z0-9_-]+`; `{serverCommand:[string, ...string[]]}` with at least one element; or `{serverUrl:string}` interpreted later as a URL/wildcard pattern. Deny takes precedence over allow. An absent allowlist permits all otherwise eligible servers; an empty allowlist permits none.

`SETS-023` — `hooks` is a partial map whose only recognized event keys are:

```text
PreToolUse, PostToolUse, PostToolUseFailure, Notification,
UserPromptSubmit, SessionStart, SessionEnd, Stop, StopFailure,
SubagentStart, SubagentStop, PreCompact, PostCompact,
PermissionRequest, PermissionDenied, Setup, TeammateIdle,
TaskCreated, TaskCompleted, Elicitation, ElicitationResult,
ConfigChange, WorktreeCreate, WorktreeRemove, InstructionsLoaded,
CwdChanged, FileChanged
```

Each value is an array of `{matcher?:string, hooks:Hook[]}`. Unknown event keys are not effective.

`SETS-024` — A persisted `Hook` is exactly one of:

| Type | Required fields | Optional fields |
| --- | --- | --- |
| command | `type:"command"`, `command:string` | `if?:string`, `shell?:supported-shell`, `timeout?:positive number` in seconds, `statusMessage?:string`, `once?:boolean`, `async?:boolean`, `asyncRewake?:boolean` |
| prompt | `type:"prompt"`, `prompt:string` | `if?:string`, `timeout?:positive number`, `model?:string`, `statusMessage?:string`, `once?:boolean` |
| http | `type:"http"`, `url:valid URL` | `if?:string`, `timeout?:positive number`, `headers?:string map`, `allowedEnvVars?:string[]`, `statusMessage?:string`, `once?:boolean` |
| agent | `type:"agent"`, `prompt:string` | `if?:string`, `timeout?:positive number`, `model?:string`, `statusMessage?:string`, `once?:boolean` |

The `if` string uses permission-rule matching syntax. A persisted hook never contains an executable function. Preserve prompt strings as strings during parse/write; transforming one into an in-memory callback before round trip would delete it during JSON serialization.

`SETS-025` — Hook policy fields are `disableAllHooks:boolean`, `allowManagedHooksOnly:boolean`, `allowedHttpHookUrls:string[]`, and `httpHookAllowedEnvVars:string[]`. URL and environment allowlists merge under ordinary settings array rules, but the enforcement domain must apply managed-only behavior when configured.

## Sandbox schema

`SETS-030` — `sandbox` preserves unknown top-level sandbox keys and accepts:

| Key | Value |
| --- | --- |
| `enabled` | boolean |
| `failIfUnavailable` | boolean |
| `autoAllowBashIfSandboxed` | boolean |
| `allowUnsandboxedCommands` | boolean |
| `network` | network object from `SETS-031` |
| `filesystem` | filesystem object from `SETS-032` |
| `ignoreViolations` | map from string to string array |
| `enableWeakerNestedSandbox` | boolean |
| `enableWeakerNetworkIsolation` | boolean |
| `excludedCommands` | string array |
| `ripgrep` | `{command:string, args?:string[]}` |

An undocumented `enabledPlatforms` value is read through sandbox's unknown-key preservation. A compatible implementation should treat it as a string platform-name array when applying platform restriction, diagnose other shapes, and keep it out of generated public schema until deliberately promoted.

`SETS-031` — `sandbox.network` is `{allowedDomains?:string[], allowManagedDomainsOnly?:boolean, allowUnixSockets?:string[], allowAllUnixSockets?:boolean, allowLocalBinding?:boolean, httpProxyPort?:number, socksProxyPort?:number}`. No integer/range refinement is imposed on proxy ports by this persisted schema; transport startup validates feasibility.

`SETS-032` — `sandbox.filesystem` is `{allowWrite?:string[], denyWrite?:string[], denyRead?:string[], allowRead?:string[], allowManagedReadPathsOnly?:boolean}`. `allowRead` is the explicit exception set inside denied-read regions; it is not a general authorization bypass.

## Plugins and marketplaces

`SETS-040` — Plugin fields:

| Key | Value |
| --- | --- |
| `enabledPlugins` | map from plugin identifier to `boolean`, `string[]`, or absent value |
| `pluginConfigs` | map from plugin identifier to plugin configuration |
| `extraKnownMarketplaces` | map from marketplace key to `{source, installLocation?:string, autoUpdate?:boolean}` |
| `strictKnownMarketplaces` | marketplace-source array |
| `blockedMarketplaces` | marketplace-source array |

One `pluginConfigs` value is `{mcpServers?:map<server,map<option,ConfigValue>>, options?:map<option,ConfigValue>}` where `ConfigValue` is string, number, boolean, or string array. Sensitive plugin option values are stored in the secure store instead, never in this object.

`SETS-041` — `strictPluginOnlyCustomization` is boolean or an array drawn from exactly `skills`, `agents`, `hooks`, and `mcp`. `true` locks all four surfaces, `false` is an explicit no-op, and an array locks only listed surfaces. For forward compatibility, discard unknown strings from an input array before validating known values. A non-array invalid value is caught and becomes absent rather than invalidating the complete settings source.

`SETS-042` — One marketplace source is the discriminated union:

| `source` | Remaining fields |
| --- | --- |
| `url` | `url:valid URL`, `headers?:string map` |
| `github` | `repo:string`, `ref?:string`, `path?:string`, `sparsePaths?:string[]` |
| `git` | `url:string`, `ref?:string`, `path?:string`, `sparsePaths?:string[]`; do not require `.git` suffix |
| `npm` | `package:valid package name` |
| `file` | `path:string` |
| `directory` | `path:string` |
| `hostPattern` | `hostPattern:string` |
| `pathPattern` | `pathPattern:string` |
| `settings` | `name:validated marketplace name`, `plugins:settings-plugin[]`, `owner?:{name:string,email?:string}` |

For `extraKnownMarketplaces`, a `settings` source's `name` must exactly equal its map key. Settings-sourced names reject empty values, spaces, path separators, `..`, `.`, non-ASCII/official impersonation patterns, reserved `inline`/`builtin`, and every reserved official marketplace name.

`SETS-043` — A settings-sourced marketplace plugin is `{name:string, source:remote-plugin-source, description?:string, version?:string, strict?:boolean}`. Name is nonempty and contains no spaces. A relative string source is forbidden because no marketplace repository exists to resolve it against. Remote plugin sources are:

- `{source:"npm", package:string, version?:string, registry?:valid URL}`;
- `{source:"pip", package:string, version?:string, registry?:valid URL}`;
- `{source:"url", url:string, ref?:string, sha?:40 lowercase hexadecimal characters}`;
- `{source:"github", repo:string, ref?:string, sha?:40 lowercase hexadecimal characters}`;
- `{source:"git-subdir", url:string, path:nonempty string, ref?:string, sha?:40 lowercase hexadecimal characters}`.

`SETS-044` — `allowManagedHooksOnly`, `allowManagedPermissionRulesOnly`, `allowManagedMcpServersOnly`, strict marketplace lists, plugin-only customization, and `pluginTrustMessage` are only authoritative when supplied by the policy source. Schema validity alone does not grant a user or project source managed authority.

## Remote, memory, and feature-gated fields

`SETS-050` — Always-recognized remote, channel, and memory fields are:

| Key | Value |
| --- | --- |
| `channelsEnabled` | boolean; default behavior remains off |
| `allowedChannelPlugins` | array of `{marketplace:string, plugin:string}` |
| `autoMemoryEnabled` | boolean |
| `autoMemoryDirectory` | string; runtime ignores project-settings contribution for security |
| `autoDreamEnabled` | boolean |

`allowedChannelPlugins` replaces the product ledger when set; it is not merged with that ledger. It becomes useful only when channel capability, identity, policy, session selection, and `channelsEnabled` all pass.

`SETS-051` — Build/runtime-gated keys:

| Gate | Keys and grammar |
| --- | --- |
| XAA environment enabled | `xaaIdp:{issuer:valid URL, clientId:string, callbackPort?:positive integer}` |
| deep-link/LODestone feature | `disableDeepLinkRegistration:"disable"` |
| proactive or assistant feature | `minSleepDurationMs?:nonnegative integer`, `maxSleepDurationMs?:integer >= -1` |
| voice feature | `voiceEnabled:boolean` |
| assistant feature | `assistant:boolean`, `assistantName:string` |
| assistant or brief feature | `defaultView:"chat"|"transcript"` |
| transcript classifier | fields in `SETS-020` and `SETS-021` marked classifier-only |

`maxSleepDurationMs = -1` represents indefinite wait. It is not a generic negative-duration allowance.

## Failure and evolution rules

`SETS-060` — Evolve this persisted schema only compatibly: add optional fields, add enum values while retaining old values, add optional nested properties, loosen validation, or use an old/new union for migration. Do not remove a field, remove an enum member, rename without preserving the old name, make an optional field required, or narrow a previously valid type.

`SETS-061` — A schema-invalid source is ineffective as a whole, except for the two caught fields in `SETS-002`; do not partially apply convenient siblings. Item-level permission recovery is an explicit prevalidation exception, not a general partial-object rule.

`SETS-062` — Targeted writes start from the raw parseable source and therefore preserve unknown and currently invalid fields not deliberately edited. They must not serialize the effective validated object as a replacement for raw user data.

`SETS-063` — The existing startup architecture diagram already captures schema validation between source discovery and attributed merge. This document specializes that boundary; no additional diagram is needed to convey a new relationship.

## Acceptance scenarios

**SETS-A01 — Unknown-field round trip.** Add an unknown top-level key and an unknown key under `attribution`. The top-level key remains effective/preserved; the nested unknown key is absent from the validated view but remains in the raw edit base.

**SETS-A02 — Whole-source versus caught-field failure.** Set `cleanupPeriodDays` to `3.5`. The complete source is ineffective. Set one invalid effort value instead; only `effortLevel` becomes absent.

**SETS-A03 — Feature-gated enum.** Configure permission mode `auto` with the classifier gate off and on. The source fails in the first run and validates in the second; `bubble` fails in both.

**SETS-A04 — Selector grammar and later enforcement.** Put two selectors in one MCP allow entry. Validation fails because exactly one selector is required. Put the same server in valid allow and deny lists; enforcement denies it.

**SETS-A05 — Caught plugin-only policy field.** Supply `strictPluginOnlyCustomization:["skills","future-surface"]`; effective value is `["skills"]`. Supply the scalar string `"skills"`; the field becomes absent without dropping other settings.

**SETS-A06 — Raw preservation across a disabled gate.** Disable the XAA gate while a valid `xaaIdp` object is present, edit an unrelated setting, then re-enable the gate. The raw XAA object survives and is validated on reactivation.

**SETS-A07 — Sandbox passthrough.** Set `sandbox.enabledPlatforms` to a supported string array and an extra future sandbox key. Both survive through the passthrough boundary; only the documented platform interpretation changes behavior.

**SETS-A08 — Settings marketplace rejection.** Define a settings-sourced marketplace whose map key and source name differ, or whose plugin source is `"./local"`. Both fail before materialization.

**SETS-A09 — Serializable hook round trip.** Put a prompt hook in settings, perform an unrelated write, and reload. The prompt string is byte-for-byte present and was never transformed into a nonserializable callback.

**SETS-A10 — Schema validity is not managed authority.** Set a managed-only lock field in project settings. It remains schema-valid data but has no managed authority.

## Non-normative provenance

Evidence was specified from the unified settings schema, permission and hook schemas, sandbox settings schema, marketplace/plugin source schemas, feature gates, and their settings readers. Validation-library defaults and source-language types are not normative; the effective and raw behaviors above are.
