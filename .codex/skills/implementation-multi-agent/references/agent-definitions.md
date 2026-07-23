# Agent definitions and capability composition

This reference defines how agent descriptions become immutable invocation plans. It covers schema validation, source precedence, enablement, agent-type authorization, MCP readiness, child tool filtering, and permission composition.

## Contents

- [Contract map](#contract-map)
- [Definition schema](#definition-schema)
- [Source discovery and precedence](#source-discovery-and-precedence)
- [Built-ins and mode filtering](#built-ins-and-mode-filtering)
- [Agent-type authorization](#agent-type-authorization)
- [MCP readiness](#mcp-readiness)
- [Tool-pool construction](#tool-pool-construction)
- [Permission composition](#permission-composition)
- [Immutable invocation plan](#immutable-invocation-plan)
- [Failure and disabled behavior](#failure-and-disabled-behavior)

## Contract map

| ID | Requirement |
| --- | --- |
| MA-DEF-002 | Validate every definition before it can shadow a lower-precedence definition. |
| MA-DEF-003 | Reduce same-name definitions in the exact active-source precedence order and retain candidates for explanation. |
| MA-DEF-004 | Parse failure is isolated and reported; valid definitions and built-ins remain available. |
| MA-DEF-005 | Current compatibility behavior excludes local-settings definitions from active reduction even though they may be displayed. |
| MA-BLT-001 | Built-in availability depends on interaction surface, build gates, runtime gates, and coordinator mode. |
| MA-SEL-001 | Explicit agent type, allowed-type rules, default type, and fork selection resolve in a deterministic order. |
| MA-MCP-001 | Required MCP servers must become tool-available within a bounded readiness window before launch. |
| MA-FLT-001 | Child tool filtering applies global exclusions, backend allowlists, explicit allow/deny rules, and availability deterministically. |
| MA-FLT-002 | Agent-type restrictions carried by the Agent tool schema are enforced before resource allocation. |
| MA-POL-001 | Agent-specific permission mode may narrow behavior but cannot override stronger parent or managed constraints. |
| MA-PLAN-001 | Resolution produces an immutable, source-attributed invocation plan consumed by all worker backends. |

## Definition schema

### Required fields

| Field | Contract |
| --- | --- |
| `description` | Nonempty human/model-facing routing description; explains when the agent should be chosen |
| `prompt` | Nonempty child system/task instruction body |

Reject a candidate missing either field. A rejected high-precedence candidate must not erase a valid lower-precedence definition.

### Optional authored fields

| Field | Type and normalization | Semantics |
| --- | --- | --- |
| `tools` | String array; an absent field or exactly `['*']` means wildcard candidate pool | Explicit child tool allow rules |
| `disallowedTools` | String array | Explicit final removals by parsed/base tool rule |
| `model` | Supported model alias or `inherit`; compare `inherit` case-insensitively | Child model selection |
| `effort` | Supported effort label or valid integer under the shared effort schema | Child reasoning effort |
| `permissionMode` | Supported permission-mode identifier | Requested child mode, subject to composition |
| `mcpServers` | Server references and/or validated inline server definitions | Additive child MCP registry |
| `hooks` | Agent-scoped hook configuration | Loaded only when the source is trusted under hook policy |
| `maxTurns` | Positive integer | Child turn ceiling |
| `skills` | Skill name/reference list | Child-invoked skill context |
| `initialPrompt` | String | Additional initial child prompt content, distinct from system prompt |
| `memory` | `user`, `project`, or `local` | Durable memory scope made available to child |
| `background` | Boolean | Definition requests asynchronous execution by default |
| `isolation` | `worktree` in generally available builds; optional remote value only in builds that declare it | Default execution isolation |

Unknown fields are ignored only if the configuration format declares forward-compatible extension fields. Unknown values for closed enums fail that candidate.

### Derived runtime fields

After validation, attach without changing authored meaning:

```text
agentType                 stable registry key
whenToUse                 normalized description
source                    built-in/custom setting source/plugin
filename?                 provenance only
baseDir?                  base for referenced resources
color?                    presentation identity
criticalReminder?         safety/routing supplement
requiredMcpServers[]      readiness requirements
pendingMcpServerSnapshot? diagnostic readiness snapshot
omitProjectInstructions?  explicit context policy
```

Memory-scoped agents with an explicit non-wildcard tool list receive the basic memory file capabilities `Read`, `Write`, and `Edit` as required by the memory contract, still subject to policy and backend restrictions.

### Schema-validation sequence

1. Parse the source document without executing code or interpolation.
2. Normalize scalar spelling and arrays.
3. Validate required fields and closed enums.
4. Validate positive bounds such as `maxTurns`.
5. Parse tool rules without resolving them yet.
6. Validate MCP references/inline definitions as untrusted configuration.
7. Apply trust policy to hooks and referenced resources.
8. Produce either one valid candidate with diagnostics or one isolated candidate failure.

## Source discovery and precedence

### Active reduction order

For definitions with the same `agentType`, apply candidates in this order; each later valid candidate replaces the earlier winner:

| Rank | Source | Meaning |
| ---: | --- | --- |
| 1 | Built-in | Product default |
| 2 | Plugin | Installed extension contribution |
| 3 | User settings | User-wide custom definition |
| 4 | Project settings | Shared repository definition |
| 5 | Flag settings | Current command-line/session override |
| 6 | Policy settings | Managed authoritative definition |

**Compatibility requirement MA-DEF-005:** local-settings definitions are currently discoverable/displayable but are not included in active reduction. Preserve this observable quirk unless a versioned migration intentionally changes it. Do not casually insert local settings between project and flag sources.

The UI's display grouping is not the reducer order. Display may group user, project, local, managed, plugin, command-line, then built-in for explanation. Never derive runtime precedence from display order.

### Reduction algorithm

```text
allCandidates = discover every source candidate and parse diagnostic
winnerByType = empty ordered map

for source in [built-in, plugin, user, project, flag, policy]:
    for valid candidate from source in deterministic source-local order:
        winnerByType[candidate.agentType] = candidate

return {
    active: winnerByType values,
    allAgents: allCandidates,
    failedFiles: isolated parse/validation failures
}
```

Source-local ordering must be deterministic. If a single source contributes duplicate names, choose and document a stable rule rather than relying on filesystem iteration.

In simple/constrained mode, return only supported built-ins. Custom settings, plugin definitions, and their failures do not become active merely because discovery code exists.

Freeze the winning definition or copy it into the invocation plan. A registry refresh during an active child must not mutate that child's model, tools, prompt, or cleanup policy.

## Built-ins and mode filtering

Built-in inclusion is a conjunction of build and runtime conditions:

| Condition | Result |
| --- | --- |
| Simple mode | Only the simple-mode built-in set |
| Noninteractive surface plus built-in-disable environment control | Remove built-in agents; do not remove explicitly supported external definitions unless their own policy says so |
| Coordinator mode | Replace ordinary built-ins with coordinator-approved worker definitions |
| Explore/Plan runtime gate false | Remove the corresponding built-in types |
| Code-guide unsupported in current SDK/entrypoint | Omit it for that surface |
| Verification gate false | Omit verification-specific built-in |
| Build feature absent | Treat the associated built-in as nonexistent, not temporarily unavailable |

Availability checks happen before presenting a type to the model. If a previously visible definition becomes unavailable before invocation, return a typed unavailable-agent error with the current available set; do not silently substitute a differently privileged type.

## Agent-type authorization

### Selection order

1. If `subagent_type` is explicitly supplied, resolve that exact type.
2. Otherwise, if fork selection is eligible and active for the invocation, select the synthetic `fork` definition.
3. Otherwise select `general-purpose`.
4. Apply the current Agent-tool allowed-type rule to the selected type.
5. Apply definition/build/mode availability and required-MCP checks.
6. Only then allocate agent ID and resources.

An explicit type always beats default/fork selection, but it never beats a deny rule.

### Agent-tool type rules

A parsed Agent tool rule may carry a comma-separated set of allowed agent types. Preserve this metadata even when the child itself cannot retain the Agent capability. At invocation:

- an allow set restricts selection to the named types;
- an explicit deny from any applicable permission source wins;
- report which rule/source rejected the type;
- never resolve a denied type and then substitute `general-purpose`.

Recursive fork creation is forbidden when the query source already identifies a fork or the fork boilerplate proves the context is already forked. Teammates also cannot spawn teammates, preserving a flat roster.

## MCP readiness

### Required-server matching

Each required-server pattern is a case-insensitive substring match against available server identities. Every declared pattern must match at least one server that has actually exposed tools. A configured connection with no tools does not satisfy readiness; it may be unauthenticated, failed, or unavailable.

### Bounded wait

If requirements are pending:

```text
deadline = now + 30 seconds
while now < deadline:
    snapshot server connection/tool states
    if every requirement has matching available tools: succeed
    if any required connection reaches permanent failure: fail early
    wait up to 500 ms or until MCP state changes
fail with missing/pending server diagnostics
```

The snapshot attached to the error excludes secrets and includes enough source/state information to explain why launch did not occur. No child/task/worktree is allocated before readiness succeeds.

Agent-specific MCP servers are additive to the selected base pool and deduplicated by canonical tool name. Name collisions follow the shared tool-registry precedence and source-attribution contract. An MCP name passing a syntactic child filter is not authorization to call it.

## Tool-pool construction

### Constant exclusion and allow sets

General child/global exclusions:

```text
TaskOutput
ExitPlanMode
EnterPlanMode
Agent                 # excluded except supported ant/build cases
AskUserQuestion
TaskStop
Workflow              # when the Workflow feature exists
```

Custom-agent exclusions are the same set.

Asynchronous child allowlist:

```text
Read
WebSearch
TodoWrite
Grep
WebFetch
Glob
supported shell tool names
Edit
Write
NotebookEdit
Skill
SyntheticOutput
ToolSearch
EnterWorktree
ExitWorktree
```

In-process teammate additions:

```text
TaskCreate
TaskGet
TaskList
TaskUpdate
SendMessage
CronCreate        # when scheduled-work feature exists
CronDelete
CronList
```

All canonical MCP tool names beginning `mcp__` pass the syntactic child allowlist stage, then remain subject to availability, explicit denial, permission policy, and sandbox/network checks.

`ExitPlanMode` is a special compatibility capability: when the selected child is genuinely in plan mode, it can pass child filters needed to leave that mode. Do not expose it merely because the parent is in plan mode.

### Filtering order

Start with the independently assembled candidate registry appropriate to the child permission mode. Then:

1. Remove tools unavailable by build, platform, policy, or current registry.
2. If this is not the main thread, remove general child/global exclusions, except explicitly supported cases.
3. For custom definitions, apply custom-agent exclusions.
4. If asynchronous, retain only asynchronous allowlist tools plus syntactically eligible MCP tools and required plan-exit compatibility.
5. If an in-process teammate, add/retain team task and message tools; retain Agent only where the invocation validator can prove it will not create a background/nested teammate.
6. Interpret `tools`:
   - absent or exactly `['*']`: no additional authored allow restriction;
   - otherwise parse rules, resolve matching canonical tools, and deduplicate deterministically.
7. Apply `disallowedTools` as final authored removals by canonical/base tool name.
8. Attach allowed-agent-type metadata from Agent rules even if the Agent executable is removed.
9. Apply session/policy permission rules at call time; registry inclusion is not permission approval.
10. Report invalid/unresolved authored tool rules rather than silently pretending the requested tool exists.

The main thread skips child-specific filters. A child pool is assembled independently; it is not merely `parentTools ∩ childRules`. However, parent/managed permission restrictions still compose at authorization time, so this independence cannot amplify authority.

### Tool rule examples

| Authored rule | Meaning |
| --- | --- |
| `['*']` | All candidate tools surviving backend/global filtering |
| `['Read', 'Grep']` | Only canonical matches for those rules |
| `tools=['Read','Edit'], disallowedTools=['Edit']` | `Read` only |
| Agent rule naming `Explore,Plan` | Only those agent types may be selected; does not necessarily retain nested Agent execution |
| `mcp__server__tool` | Syntactically eligible MCP tool, still requiring connected registry and permission |

## Permission composition

### Effective mode

Resolve the child-requested mode from definition and invocation, then compose:

1. Managed policy and hard denial.
2. Parent safety mode that cannot be downgraded, including bypass/accept-edits/automatic mode semantics.
3. Agent definition's requested mode.
4. Backend restrictions, including asynchronous prompt avoidance.
5. Tool/path/command-specific rule evaluation and hooks.
6. Sandbox availability and protected-resource analysis.
7. Interactive decision or safe denial/error.

A definition mode does not override parent `bypassPermissions`, `acceptEdits`, or automatic mode semantics; the parent-effective constraint remains authoritative. Conversely, a stricter child mode can narrow behavior.

Asynchronous workers default to avoiding permission prompts. They either:

- continue only with already allowed/sandboxed operations;
- fail/deny the operation;
- or, when the invocation explicitly supports bubbling, run automated checks first and then request the parent/local UI decision.

If per-agent `allowedTools` creates session-scoped allow rules, replace the inherited session-rule subset while preserving command-line argument rules and all managed/hard denial sources. Record source attribution.

Agent-scoped hooks run only from trusted sources and in the shared tool lifecycle order. Hook output may narrow/deny or request permission according to the hook protocol; it cannot bypass managed policy.

## Immutable invocation plan

Every backend consumes one normalized plan:

```text
InvocationPlan {
  selectedAgentType
  definitionSource
  definitionVersionOrFingerprint
  prompt
  initialPrompt?
  model
  effort
  maxTurns
  effectivePermissionMode
  candidateTools[]
  allowedAgentTypes[]?
  mcpRegistrySnapshot
  skills[]
  memoryScope?
  hooks[]
  requestedBackground
  requestedIsolation
  contextPolicy
  disabledOrWarningDiagnostics[]
}
```

Store/fingerprint the plan with task metadata sufficiently to resume with compatible semantics. Do not include credentials or raw secret configuration. Backend selection, working-directory allocation, and runtime IDs are added by the lifecycle layer after this plan is fixed.

## Failure and disabled behavior

| Condition | Outcome |
| --- | --- |
| High-precedence definition malformed | Report it; retain valid lower-precedence winner |
| Duplicate valid same-source type | Apply deterministic source-local order and expose candidates |
| Local-settings-only custom definition | Display as discovered but do not activate under current compatibility contract |
| Explicit unknown agent type | Typed error with currently available types; no fallback spawn |
| Type denied by Agent rule | Typed denial citing rule source |
| Required MCP server missing/failed | Fail before task/worker allocation after bounded wait or early permanent failure |
| Invalid tool rule | Report unresolved rule; do not widen to wildcard |
| Async child asks for disallowed capability | Capability absent or denied; no parent-tool pass-through |
| Unknown MCP name | Unavailable error; no fabricated proxy tool |
| Untrusted hooks | Omit/deny with diagnostic according to policy; do not execute |
| Background feature disabled | Ignore automatic background request only according to documented foreground fallback, or reject if the invocation semantically requires background |
| Registry changes after launch | Existing invocation keeps frozen plan; new invocations use refreshed registry |
