# Settings resolution contract

This document is normative for ordinary configuration loading, merging, attribution, editing, and trust-derived views. `SET-*` identifiers are stable; an implementation may change storage libraries or data structures but must preserve the behavior attached to each identifier.

## Contents

- [State model](#state-model)
- [Ordinary sources and locations](#ordinary-sources-and-locations)
- [Read, parse, and validate](#read-parse-and-validate)
- [Merge semantics](#merge-semantics)
- [Source-local edits](#source-local-edits)
- [Trust-sensitive derived views](#trust-sensitive-derived-views)
- [Applying a generation](#applying-a-generation)
- [Failure and recovery](#failure-and-recovery)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## State model

`SET-001` — Represent each source as an independent record containing at least:

- source identifier;
- resolved location or inline origin;
- parsed raw object;
- validated effective object, if usable;
- validation and I/O diagnostics;
- editability and authority class;
- load generation and modification evidence.

`SET-002` — The merged snapshot is immutable to consumers and includes enough field-level or query-time provenance to answer which source supplied a behaviorally significant value. Return defensive copies: recursive object merge and array concatenation may mutate intermediates.

`SET-003` — Keep four lifetimes separate:

| Lifetime | Examples | Persistence rule |
| --- | --- | --- |
| Process bootstrap | entrypoint, original working directory, source filter, explicit settings input | Fixed before ordinary settings use |
| Source snapshot | user, project, local, flag, policy objects | Reloadable as coherent generations |
| Durable account/global state | onboarding, trust records, approved external keys | Stored separately from settings precedence |
| Derived runtime state | environment, permissions, sandbox, registries | Rebuilt from one merged generation |

## Ordinary sources and locations

`SET-010` — Use these canonical source identifiers and default locations:

| Source | Default location | Editable | Trust class |
| --- | --- | --- | --- |
| `pluginSettings` | enabled plugin-contributed settings object | No | trusted only after plugin policy/load validation |
| `userSettings` | configuration home `/settings.json` | Yes | user |
| `projectSettings` | original working directory `/.agentx/settings.json` | Yes | project |
| `localSettings` | original working directory `/.agentx/settings.local.json` | Yes | project-local |
| `flagSettings` | command-line inline JSON or a command-line file | No through settings editor | explicit invocation |
| `policySettings` | selected managed-policy source | No | managed |

The configuration home is normally the user's AgentX configuration directory; a cowork/managed surface may select its own home before this contract begins. Project paths are rooted at the original session working directory, not a later `cwd` change.

`SET-011` — An enabled-source command-line filter may exclude `userSettings`, `projectSettings`, and `localSettings`. It never excludes explicit `flagSettings` or `policySettings`. Plugin contribution eligibility remains governed by plugin policy and mode.

`SET-012` — Bare mode does not infer ordinary filesystem customization. It uses only explicitly permitted command-line settings and managed constraints required by the bare-mode contract. Domains must query the source snapshot rather than accidentally reading the user's normal settings file.

## Read, parse, and validate

`SET-014` — Read each file independently. A missing file contributes an empty object without an error. A directory, unreadable file, or syntactically invalid JSON contributes no effective settings and produces a source-attributed diagnostic.

`SET-015` — Parse JSON as an object. A parseable object is retained as raw user data even when full schema validation fails. Unknown fields and invalid-but-parseable values survive later targeted edits so the application does not erase data introduced by newer versions.

`SET-016` — Validate permission-rule arrays item by item before full settings validation. Exclude and warn about malformed individual rules while keeping valid siblings. If the remaining complete object still fails the settings schema, the entire source is ineffective for the generation.

`SET-017` — A syntactically malformed file is not a safe edit base. Automated writes to that source must fail with a diagnostic rather than replacing it with a newly serialized subset.

`SET-018` — Guard against recursive settings access while a source is loading. A recursion guard returns an empty snapshot for the recursive edge, reports the condition, and allows the outer load to finish; it must not deadlock or publish a half-built object.

`SET-019` — Cached getters may return immutable clones. A source-and-provenance inspection API must perform or expose a fresh coherent read when callers explicitly request current source data.

## Merge semantics

`SET-020` — Merge ordinary sources from lowest to highest precedence in exactly this order:

1. `pluginSettings`;
2. `userSettings`;
3. `projectSettings`;
4. `localSettings`;
5. `flagSettings`;
6. `policySettings`.

`SET-021` — For two values at the same key:

| Lower value | Higher value | Result |
| --- | --- | --- |
| object | object | recursively merge keys |
| array | array | concatenate lower then higher, then remove duplicates while preserving first occurrence |
| any other combination | any | higher value replaces lower value |

An empty higher object does not erase lower object keys. An empty higher array does not erase lower array items. Source-local editing has different semantics; see `SET-031`.

`SET-022` — Duplicate removal uses semantic equality appropriate to validated settings values. Preserve deterministic order. Never sort arrays whose order is user-visible or precedence-bearing.

`SET-023` — `null` is a scalar replacement, not deletion, when the schema permits it. Missing/undefined is absence. A source update uses explicit deletion, not serialized undefined.

`SET-024` — Policy is highest ordinary merge precedence only after one complete managed-policy source has been selected by `POL-010`. Never merge competing policy authorities together.

`SET-025` — Derive provenance at the same recursion granularity as behavior. If one object has keys from user and policy, a query for each key must report the corresponding source; labeling the whole object with only the final source is insufficient.

`SET-026` — Snapshot publication is atomic: consumers see the entire previous generation or entire new generation, never a mix of newly merged settings and stale source attribution.

`SET-027` — Deterministic merge pseudocode:

```text
effective := empty object
provenance := empty tree
for source in precedence_order:
    if source is eligible and valid:
        (effective, provenance) := merge(effective, source.value,
                                         provenance, source.id)
publish(deep_copy(effective), deep_copy(provenance), generation)
```

## Source-local edits

`SET-030` — Only `userSettings`, `projectSettings`, and `localSettings` are ordinary editable settings destinations. A caller must name the destination; never infer policy or flag input as writable.

`SET-031` — Editing one source applies operations to that source's raw object:

| Operation | Source-local behavior |
| --- | --- |
| set scalar/object | replace value at the addressed key |
| set array | replace the source-local array; do not use merged concatenation |
| explicit delete | remove the addressed key |
| set undefined/absent sentinel | treat as explicit delete, never serialize a language-specific undefined token |

`SET-032` — Serialize deterministic, human-readable JSON, terminate with a newline, and use an atomic or append-safe write. Preserve the existing permission mode where practical. Mark the path as an internal write before the final filesystem-visible replacement.

`SET-033` — Writing an otherwise empty source produces `{}` followed by a newline. Do not delete the file implicitly because file presence may be significant to tooling.

`SET-034` — After a successful write, reset the central settings cache, publish one new generation, and let the internal-write suppression path consume resulting watcher events.

`SET-035` — When creating or updating local project settings, asynchronously ensure the local settings path is ignored by source control. Failure to update ignore metadata is a nonfatal diagnostic and must not roll back the settings write.

`SET-036` — If a write loses a race with an external edit, do not blindly overwrite. An implementation may use compare-and-swap metadata, an exclusive update queue, or re-read-and-apply, but the observed result must preserve both explicit edits or report a conflict.

## Trust-sensitive derived views

`SET-040` — Never treat the effective merge alone as proof that code-bearing configuration is trusted. The following settings can cause execution or materially weaken approval and therefore retain source provenance:

- API-key, AWS-auth, credential-export, telemetry-header, or related helper commands;
- hooks and extension paths;
- permissions and sandbox relaxations;
- default bypass/dangerous permission choices;
- automatic-mode opt-in and rule configuration.

`SET-041` — In an interactive session, a helper command from project or local settings may run only after workspace trust is accepted. Headless execution's trust contract is established by invocation; it still honors managed policy and source filtering.

`SET-042` — Trusted consent for bypass/dangerous permission prompts excludes project settings. User, explicit flag where allowed, and managed policy may contribute according to the owning permission contract; project content cannot silently suppress that consent boundary.

`SET-043` — Automatic-mode enablement/configuration concatenates only trusted user, local, explicit flag, and policy contributions according to its schema. Project-wide settings do not independently opt a user into automatic execution. The permission skill defines the resulting mode behavior.

`SET-044` — Plugin-only customization policy is either boolean `true` (lock all supported customization families) or a list drawn from `skills`, `agents`, `hooks`, and `mcp`. When locked, only built-in/bundled, enabled plugin, and managed-policy contributions are trusted. User, project, local, added-directory, and dynamic contributions are filtered for that family.

`SET-045` — Do not discard blocked customization definitions from diagnostics. Record source and policy reason, but keep them absent from active registries and model context.

## Applying a generation

`SET-050` — Derive environment variables before constructing network clients or subprocess environments. Apply certificate/proxy cache invalidation when related values change.

`SET-051` — Rebuild permissions and sandbox state before publishing newly enabled tools or hooks that could request execution.

`SET-052` — Registry reloads consume the same generation identifier. A slow extension load from generation `N` cannot publish after generation `N+1` becomes current.

`SET-053` — Some settings are startup-only. Retain their active values until a new session/process and report that the persisted value differs; never half-apply a startup-only setting to one subsystem.

## Failure and recovery

`SET-060` — A malformed low-precedence source does not prevent a valid higher source or unrelated domain from loading. A malformed managed source follows the fallback selection rules in `POL-010` and is reported prominently.

`SET-061` — Cache corruption or I/O failure must not mutate the last coherent in-memory generation. Either retain the prior generation or, on initial load, use the documented empty/default behavior.

`SET-062` — Logging and telemetry may include source identifiers, schema paths, and file locations appropriate to diagnostics, but never secret values, authorization headers, helper output, or full sensitive settings objects.

## Acceptance scenarios

**SET-A01 — Attributed recursive merge.** User sets `{a:1, x:{u:true}, p:["u","shared"]}`, project sets `{a:2, x:{p:true}, p:["shared","p"]}`, and policy sets `{x:{locked:true}}`. The result is `a=2`, all three object keys, and `p=["u","shared","p"]`, with per-key provenance.

**SET-A02 — Item recovery and raw edit base.** A user file contains one invalid permission rule and one valid rule. Only the invalid item is omitted. A later unrelated editor write preserves the original unknown fields and deliberately removes or retains the invalid item according to the source-local edit operation—not by implementing from effective settings.

**SET-A03 — Trust-gated helper.** Project `apiKeyHelper` is present before trust. Source inspection reports it, but no subprocess starts. After trust and cache rebuild, the helper may run.

**SET-A04 — Internal write coalescing.** A settings writer records an internal mark, atomically replaces the file, resets the cache, and receives three filesystem events. Exactly one new settings generation is published.

**SET-A05 — Malformed edit base.** A syntax-error local file receives an attempted rule update. The operation fails without changing bytes and points to the local source.

## Non-normative provenance

Reference behavior was specified from the settings loaders, schemas, configuration storage, trust checks, settings editor, and environment-application utilities under `utils/settings/`, `utils/config*`, `schemas/`, and bootstrap initialization. Paths and implementation symbols are provenance only.
