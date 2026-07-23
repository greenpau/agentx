# Extension registry composition contract

This document defines the shared extension-plane vocabulary and lifecycle. Domain skills define their own discovery formats and precedence. `EXT-*` identifiers are normative and stable.

## Contents

1. [Descriptor model](#descriptor-model)
2. [Discovery phases](#discovery-phases)
3. [Domain ownership and precedence](#domain-ownership-and-precedence)
4. [Policy composition](#policy-composition)
5. [Registry projections](#registry-projections)
6. [Generation and cache lifecycle](#generation-and-cache-lifecycle)
7. [Remote and filesystem trust](#remote-and-filesystem-trust)
8. [Failure boundaries](#failure-boundaries)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Non-normative provenance](#non-normative-provenance)

## Descriptor model

`EXT-001` — Every discovered contribution is an attributed descriptor containing at least:

```text
canonicalName
displayName
kind                         command | skill | style | plugin component |
                             hook | MCP server/tool/resource | LSP server | agent
source                       built-in | bundled | managed | plugin | user |
                             project | local | explicit | dynamic | remote
sourceIdentity               stable provider/plugin/server/path identity
scope                        process/session/project/user/managed as applicable
rawDefinition
validatedDefinition
availabilityDimensions
generation
diagnostics[]
```

`EXT-002` — Availability dimensions are independent booleans or tagged states:

- included in this build;
- runtime feature enabled;
- account eligible;
- platform supported;
- source configured and trusted;
- managed policy permits;
- provider installed/connected;
- descriptor valid;
- enabled for this session;
- callable/visible for this caller.

Do not collapse them to a single `enabled` value until the final registry projection. Preserve the first failing reason for diagnostics and all relevant reasons for audit.

`EXT-003` — Public identity is domain-owned. File basename, plugin path, server connection instance, alias, and display name may differ from canonical name. Normalize aliases at the invocation boundary and retain the original requested spelling for display.

## Discovery phases

`EXT-010` — Extension-plane initialization proceeds in phases:

1. freeze build and startup facts;
2. load managed restrictions and trust state;
3. discover providers and raw descriptors without invocation;
4. parse and schema-validate each descriptor;
5. canonicalize identity and source;
6. resolve collisions with domain-specific precedence;
7. apply enablement, policy, and caller-visibility filters;
8. construct callable registries and prompt/user projections;
9. publish one immutable generation.

`EXT-011` — Discovery is side-effect-limited. Reading descriptors must not execute project helpers, hook commands, skill shell substitutions, language servers, MCP calls, or plugin code beyond the explicit manifest/data parsers. Materializing a marketplace is a separate authorized lifecycle.

`EXT-012` — Provider discovery may run concurrently. Final merge order is deterministic and independent of completion order. Store results keyed by the input provider order or explicit precedence rank.

`EXT-013` — One malformed descriptor yields a descriptor-level diagnostic. A malformed provider index may suppress that provider; it does not erase successfully loaded built-ins or other providers.

## Domain ownership and precedence

`EXT-020` — There is no universal "last file wins" rule. Each domain owns its precedence:

| Domain | Examples of special behavior |
| --- | --- |
| commands/input | built-in safety/control commands may be reserved; remote modes filter local-only commands |
| skills | only the trusted active repository's root `.codex/skills` directory contributes entries |
| output styles | built-in, then plugin, user, project, managed assignment makes managed highest |
| plugins | explicit session directory outranks installed marketplace, which outranks bundled; managed-name collision can suppress session replacement |
| hooks | matched entries combine rather than one name winning; source policy and dedup keys determine inclusion |
| MCP | approved manual server outranks plugin; enterprise configuration may be exclusive; semantic duplicate detection considers transport target |
| LSP | enabled plugins only; later configuration in deterministic plugin order may replace a scoped server identity |

`EXT-021` — A domain must document:

- canonical identity and alias grammar;
- source precedence and tie-breaking;
- whether collisions replace, combine, namespace, or reject;
- caller visibility rules;
- active-session reload behavior;
- unavailable and disabled representation.

`EXT-022` — Preserve all shadowed descriptors as diagnostics containing winner and loser provenance. Never silently make discovery order depend on filesystem enumeration or asynchronous completion.

## Policy composition

`EXT-030` — Apply policy after source attribution is known and before model/user invocation registries are published. Policy can filter but cannot make an invalid descriptor valid.

`EXT-031` — Plugin-only customization policy is family-scoped. For a locked family, allow managed policy, built-in/bundled contributions, and contributions from enabled policy-compliant plugins. Exclude ordinary user, project, local, added-directory, and dynamic sources.

`EXT-032` — Managed allowlists and blocklists are composed as:

1. explicit block/deny;
2. allowlist membership if an allowlist exists;
3. source validity and trust;
4. ordinary user enablement.

An absent allowlist is open; an explicitly empty allowlist denies all for that domain unless the domain documents an essential built-in exception.

`EXT-033` — Compile-time absence and runtime/policy disablement are supported product states. No caller should assume a descriptor exists because the source contained implementation code.

`EXT-034` — Keep blocked definitions out of model context and invocation. A status/configuration surface may expose their names, sources, and policy reasons without exposing secrets.

## Registry projections

`EXT-040` — Derive all projections from the same filtered generation:

- canonical invocation map;
- supported alias map;
- user discovery/search list;
- model-visible prompt/tool listing;
- completion metadata;
- source and diagnostic views.

`EXT-041` — Projection-specific truncation changes presentation only. For example, shortening skill descriptions for context budget cannot remove the invocation map entry or change precedence.

`EXT-042` — Commands, tools, and tasks remain distinct even when an extension supplies all three:

- command: user-routed local action, UI launch, or prompt expansion;
- tool: model-callable validated request/result contract;
- task: identity-bearing asynchronous lifecycle with an explicit persistence profile.

`EXT-043` — Extension invocation returns through core contracts. It cannot call a subprocess, mutate files, send network requests, or write persistent task evidence without the same validation, permission, hook, cancellation, and result pairing as built-ins.

## Generation and cache lifecycle

`EXT-050` — Assign a monotonic generation to every published extension snapshot. Discovery work captures its starting generation and must compare before publication; stale work is discarded and cleaned up.

`EXT-051` — Cache keys include every behavior-bearing input: source canonical path or provider identity, version/checksum, policy generation, build gates, caller mode, and relevant settings generation. Name-only caching is insufficient.

`EXT-052` — Central invalidation fans out to domain caches:

- plugin manifest/component caches;
- command and agent registries;
- hook snapshots;
- skill discovery and prompt listing;
- output style selection and prompt sections;
- MCP definitions, discovery caches, and connection reconcile;
- LSP generation and diagnostics where appropriate.

`EXT-053` — Reload does not mutate in-flight operations. A model request keeps its tool catalog; a hook event keeps its hook snapshot; an invoked skill keeps its body; an MCP call keeps its connection/request identity. The next operation uses the new generation.

`EXT-054` — Removal prunes callable contributions as soon as a coherent new generation is active. Newly enabled plugins may require explicit session reload to add components whose ownership cannot safely change in place. The plugin domain records this asymmetry.

`EXT-055` — Resource cleanup is idempotent and tied to provider instance/generation: close MCP connections, stop old LSP processes, detach watchers, cancel discovery, and release extracted temporary material only when no active reference remains.

## Remote and filesystem trust

`EXT-060` — Canonicalize filesystem roots before deduplication. Reject traversal outside a plugin/managed root for relative manifest references. A symlink target is authorized by the owning source policy, not merely by its lexical parent.

`EXT-061` — Remote descriptors are untrusted protocol data. Apply strict structural validation, identifier grammar, size/depth limits, canonical Unicode handling, timeout/cancellation, and source attribution.

`EXT-062` — Never interpolate remote text into a local command, shell, path, header, or permission rule unless a specialized protocol explicitly validates that transformation.

`EXT-063` — Secret-bearing extension configuration remains in secure/provider state. Model-visible descriptors contain only safe names, descriptions, schemas, and bounded instructions.

## Failure boundaries

| Boundary | Failure scope | Required behavior |
| --- | --- | --- |
| one descriptor | descriptor | omit it; preserve siblings |
| one provider/plugin | provider | preserve other providers and core registry |
| policy service | family/session | use documented validated cache/default; never guess allow |
| reload discovery | generation | retain previous coherent generation |
| cleanup | provider instance | report leak/degraded state; do not corrupt registry |
| prompt projection | presentation | keep invocation state; use bounded fallback representation |

## Acceptance scenarios

1. A plugin and project define the same skill name while output style names also collide. Each domain applies its own precedence; no shared map's insertion order decides both.
2. Discovery of one MCP provider times out while repository-local skills succeed. The MCP provider is failed/unavailable; all unrelated registries publish normally.
3. Plugin-only policy locks hooks and MCP. Repository-local skills remain governed solely by trust and bare-mode gates; plugin skill contributions remain excluded.
4. Generation 8 discovery is slow; settings produce generation 9, which publishes first. Generation 8 completion is discarded and its new connections/processes are cleaned up.
5. An in-flight model turn uses registry generation 4 when a plugin is disabled. Its accepted tool-use identifiers still resolve and finish under generation 4; the next turn sees generation 5 without the plugin.
6. A remote descriptor contains a shell-like string in a description. It stays inert text and never becomes command execution or a permission rule.

## Non-normative provenance

Reference behavior was specified from command, skill, plugin, hook, MCP, LSP, tool, and agent discovery registries; settings-change fanout; prompt composition; and provider lifecycle managers throughout the repository. Source layout and symbols are provenance only.
