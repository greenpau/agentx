# Plugin and marketplace lifecycle contract

This document is normative for plugin identity, manifests, marketplaces, dependency trust, installation state, caching, component loading, update, and removal. `PLUG-*` and `MARKET-*` identifiers are stable.

## Contents

1. [Identities and state separation](#identities-and-state-separation)
2. [Plugin manifest](#plugin-manifest)
3. [Marketplace names and schemas](#marketplace-names-and-schemas)
4. [Marketplace intent and materialization](#marketplace-intent-and-materialization)
5. [Source policy](#source-policy)
6. [Dependency resolution](#dependency-resolution)
7. [Installation transaction](#installation-transaction)
8. [Uninstall, marketplace removal, and data ownership](#uninstall-marketplace-removal-and-data-ownership)
9. [Cache cleanup and active reload](#cache-cleanup-and-active-reload)
10. [Failure matrix](#failure-matrix)
11. [Acceptance scenarios](#acceptance-scenarios)
12. [Non-normative provenance](#non-normative-provenance)

## Identities and state separation

`PLUG-001` — Canonical installed plugin identity is `<plugin-name>@<marketplace-name>` and matches, case-insensitively, `^[a-z0-9][-a-z0-9._]*@[a-z0-9][-a-z0-9._]*$`. Normalize identity consistently while preserving declared casing only for display.

`PLUG-002` — Keep these records separate:

| Record | Meaning |
| --- | --- |
| marketplace intent | configured source expected to exist |
| known marketplace | materialized source, location, freshness, auto-update |
| enabled intent | scope-local boolean in settings |
| installed registry | materialized plugin versions/locations by scope and project |
| version cache | immutable plugin files for a version/checksum |
| active session descriptor | frozen contribution root/version currently loaded |
| plugin data/options/secrets | durable plugin-owned state independent of cached code |

Changing one does not implicitly mutate all others.

`PLUG-003` — Installed registry version 2 maps plugin ID to an array of records containing scope, optional project path, install path, version, install/update timestamps, and optional source checksum. Migrate a version-1 singleton record to a user-scope array entry and versioned cache path without dropping recoverable metadata.

`PLUG-004` — Scope identities are `managed`, `user`, `project`, `local`, and session-only explicit directory. Project/local records include the original project path; only records matching the active original project apply.

## Plugin manifest

`PLUG-010` — Standard manifest path is `.agentx-plugin/plugin.json`. In non-strict discovery, a missing manifest may synthesize a minimal descriptor from marketplace name/description. Invalid JSON or schema makes the plugin manifest unusable.

`PLUG-011` — Manifest metadata may include name, version, description, author, homepage, repository, license, keywords, and dependencies. Strip unknown top-level keys from the validated runtime descriptor while retaining diagnostics/raw data for forward compatibility.

`PLUG-012` — Standard component locations are:

```text
commands/
agents/
skills/
output-styles/
hooks/hooks.json
```

The manifest may add relative `./...` files/directories or inline maps where that component schema allows. Resolve relative paths inside the canonical plugin root, reject traversal, and deduplicate canonical targets.

`PLUG-013` — Missing optional components produce structured nonfatal component diagnostics. In strict mode, a manifest that redundantly/ambiguously references the standard hooks path is invalid for that hook contribution rather than loading it twice.

`PLUG-014` — Plugin-specific settings may supplement only the allowlisted fields `agent` and `settings.json` overrides. They cannot replace manifest identity or add arbitrary executable paths.

`PLUG-015` — User configuration option schema supports identifiers with values of type string, number, boolean, directory, or file. Title and description are required. Sensitive options use secure storage; non-sensitive options may use settings. Validate identifiers and never expose secret values as environment diagnostics or model context.

## Marketplace names and schemas

`MARKET-001` — Marketplace names are normalized identifiers. Reserved official names include:

```text
agentx-code-marketplace agentx-code-plugins agentx-plugins-official
agentx-marketplace agentx-plugins agent-skills life-sciences
knowledge-work-plugins
```

Reserved official names may resolve only to an AgentX-owned GitHub organization. Reject Unicode/lookalike impersonations and non-ASCII names for this trust check. `inline` and `builtin` are reserved internal source identities.

`MARKET-002` — Official marketplaces default to automatic update except `knowledge-work-plugins`; third-party marketplaces default false unless explicitly configured.

`MARKET-003` — Supported marketplace source variants are tagged values:

| Variant | Required/optional fields |
| --- | --- |
| URL | URL, optional headers |
| GitHub | repository, optional ref/path/sparse paths |
| Git | URL, optional ref/path/sparse paths |
| package registry | package name/version as supported |
| file | local marketplace file |
| directory | local marketplace directory |
| inline settings | validated marketplace object supplied by managed/explicit settings |
| policy matcher | host pattern and/or path pattern; valid only in policy rules |

`MARKET-004` — Plugin artifact source variants include plugin-relative `./...`, package registry, Python package source, Git URL, GitHub, and Git-subdirectory. A pinned commit `sha` is exactly 40 lowercase hexadecimal characters.

`MARKET-005` — Marketplace document contains an owner, plugin entries, optional `forceRemoveDeletedPlugins`, metadata, and an explicit cross-market dependency allowlist. Strict parsing is default; unknown entry fields are stripped/rejected according to schema. Marketplace metadata may supplement a partial plugin manifest but cannot violate identity or source policy.

`MARKET-006` — A settings-provided marketplace is a remote/source definition only; its declared name must equal the settings key. Mismatch is invalid rather than an alias.

## Marketplace intent and materialization

`MARKET-010` — Intent sources include `extraKnownMarketplaces`, explicit additional plugin directories, managed seeds, and implicit official intent when an enabled plugin references the official marketplace. Apply deterministic priority with implicit official lowest, then explicit directory and settings/managed authority as defined.

`MARKET-011` — Persist known marketplace records in `known_marketplaces.json` with source, install location, last update, and auto-update state. A corrupt file on a read-only discovery path yields an empty known set plus diagnostic; a mutation path throws and refuses to overwrite recoverable user data.

`MARKET-012` — Managed seed marketplaces are registered read-only. First seed for an identity wins, `autoUpdate=false`, install path is recomputed from the seed, and user remove/refresh is blocked.

`MARKET-013` — Remote marketplace materialization must resolve within the application marketplace cache root before update, cleanup, or plugin resolution. Local file/directory sources keep their user path and are read-only; never rewrite them as cache content.

`MARKET-014` — Ordinary startup is cache-only and performs no network. Explicit refresh or full synchronization materializes remote sources. A dedicated synchronous-install environment switch may request a blocking full load. Missing required cache yields a structured `plugin-cache-miss`, not silent network access.

`MARKET-015` — Git operations are noninteractive and bounded to 120 seconds unless an explicit bounded override is configured. Disable credential prompts. Validate checkout/subdirectory/sparse paths before using them.

## Source policy

`MARKET-020` — Policy evaluation order is:

1. blocked marketplace/source rules;
2. allowlist, when present;
3. source schema and identity validation;
4. download/materialization;
5. plugin enablement.

No network access occurs before steps 1–3 succeed.

`MARKET-021` — An absent allowlist is open. An explicit empty allowlist denies every nonmanaged marketplace. Blocklist always wins.

`MARKET-022` — Exact source matching is used for allowlist. Blocklist additionally treats equivalent GitHub and Git URL forms as the same source. If a block rule omits ref/path, it matches all refs/paths; if present, it matches exactly.

`MARKET-023` — Host-pattern rules apply only to network Git/GitHub/URL variants. Path-pattern rules apply only to local file/directory variants. Invalid administrator regular expressions are diagnosed and do not match; when active strict policy encounters an unknown/corrupt marketplace source, fail closed.

`PLUG-020` — `enabledPlugins[id] = false` in policy blocks installation and activation. A managed plugin name mapped either true or false reserves the name against session `--plugin-dir` replacement.

`PLUG-021` — Plugin-only customization filtering is applied again at component registration. A valid installed plugin does not make unrelated filesystem components trusted.

## Dependency resolution

`PLUG-030` — Resolve dependencies with depth-first search and emit postorder, so every dependency is materialized before its dependent root. Detect and report cycles with the identity path. A not-found dependency aborts activation of that root.

`PLUG-031` — An unqualified dependency inherits the declaring plugin's marketplace. A cross-market dependency may auto-install only when:

- it is already enabled; or
- the root plugin's marketplace explicitly allowlists the target marketplace.

Trust does not propagate transitively: a dependency's allowlist cannot authorize the root's next cross-market hop.

`PLUG-032` — Never skip the requested root because an existing dependency is enabled. Enabled dependencies may skip redundant recursion/materialization if their exact usable version is present.

`PLUG-033` — During active-session loading, an unavailable dependency demotes the dependent plugin through a fixed-point pass. This demotion is in-memory and does not rewrite the user's enabled intent.

`PLUG-034` — Disable or uninstall reports reverse dependencies but does not block the user's explicit operation. Dependents become unavailable with diagnostics on the next resolution.

## Installation transaction

`PLUG-040` — User-installable scopes are user, project, and local. Managed plugins are administrator-installed; they may be updated only through their managed lifecycle. Session explicit directories are never persisted as installations.

`PLUG-041` — Install is intentionally settings-first:

1. resolve plugin against a materialized, policy-approved marketplace;
2. write enabled intent to the selected scope;
3. resolve/materialize root and dependencies;
4. register installed version records;
5. publish success for a future/current reload as supported.

If materialization fails after intent, keep an explicit disabled/error record or roll back only through a documented compensating transaction; never claim active success.

`PLUG-042` — Enable/disable resolution uses most-specific scope `local > project > user`. An explicit higher-precedence value may override a lower one. Managed block remains outside and above editable scope precedence.

`PLUG-043` — Version cache path is marketplace/plugin/version (or equivalent immutable content identity). Updates install to a new path and update disk registry; they do not modify the active session's frozen descriptor.

`PLUG-044` — Copying a local plugin into cache may fall back to using the validated marketplace path directly when safe. External/remote copy failure cannot use an arbitrary source path; mark install failed/disabled.

## Uninstall, marketplace removal, and data ownership

`PLUG-050` — Uninstall removes enabled intent from the named scope, removes its matching installed registry record, and marks the version cache orphaned only when no relevant scope still references it.

`PLUG-051` — When the final installed scope for a plugin disappears, also delete plugin options, secure secrets, and plugin data directory according to user confirmation/ownership policy. Removing one scope while another remains does not delete shared state.

`PLUG-052` — A delisted plugin can still uninstall using installed-registry identity; marketplace lookup is not required for cleanup.

`MARKET-030` — Removing a marketplace removes intent/known record, remote cache, enabled entries across editable scopes, installed registry entries, orphans, and last-reference plugin options/secrets/data. Managed seed removal is rejected before mutation.

`MARKET-031` — Marketplace refresh with `forceRemoveDeletedPlugins` may remove delisted plugins only through the same dependency and data cleanup transaction; no raw cache deletion may strand settings entries.

## Cache cleanup and active reload

`PLUG-060` — Mark unused version directories with `.orphaned_at`; delete only after seven days if still unreferenced. Search/glob providers ignore orphaned trees immediately.

`PLUG-061` — Validate cache paths before cleanup. Never recursively remove an unresolved environment path, filesystem root, marketplace root, or local user source.

`PLUG-062` — A plugin cache/enablement change invalidates plugin descriptor, commands, agents, hooks, skills, output styles, MCP, prompt, and listing caches. Removal prunes registered commands/agents/hooks promptly. Newly added components may wait for explicit session reload; do not partially synthesize them into an old registry generation.

`PLUG-063` — Built-ins are not entries in the installed registry. Session explicit plugin source is `<name>@inline`, always enabled for that invocation, highest source precedence over marketplace installation unless a managed-name reservation blocks it.

`PLUG-064` — Active source precedence is session explicit directory, then installed marketplace, then built-in. Preserve source diagnostics when a higher source is rejected and a lower source becomes active.

## Failure matrix

| Failure | Scope and recovery |
| --- | --- |
| installed registry corrupt | discovery uses empty registry with prominent diagnostic; mutation must avoid destructive overwrite |
| known marketplace corrupt | read-only discovery empty; mutation throws |
| marketplace entry malformed | omit entry, keep valid entries |
| dependency cycle | root and cycle dependents unavailable; no partial activation |
| update fails | old active/cache record remains usable |
| cache path escapes root | fail closed before read/write/delete |
| component malformed | omit component; plugin remains for other valid components |

## Acceptance scenarios

1. **PLUG-A01 — Dependency order and cycle.** A root depends on B and C, and C depends on D. Installation order is B, D, C, root; a C->root cycle aborts without root activation.
2. **PLUG-A02 — Reserved marketplace identity.** A third-party marketplace uses a reserved official name. It is rejected before clone even if otherwise allowlisted.
3. **PLUG-A03 — Corrupt known-marketplace state.** A known-marketplace file is corrupt. Startup reports no known marketplaces without rewriting; `marketplace add` refuses to overwrite the file.
4. **PLUG-A04 — Last-reference cleanup.** User and project scopes install the same version. Uninstalling project leaves cache, data, and user activation. Uninstalling the final user scope orphans code and removes owned configuration/data.
5. **PLUG-A05 — Frozen active version.** Background update installs version 2 while session uses version 1. Disk points to version 2; all session component invocations stay on version 1 until reload.
6. **PLUG-A06 — Family policy isolation.** Plugin-only policy locks hooks. A plugin hook loads; a same-shaped user hook is filtered; plugin skills remain governed by the separately configured family lock.

## Non-normative provenance

Reference behavior was specified from plugin and marketplace schemas, manifest loaders, installed/known registry managers, policy matchers, dependency resolution, install/update/remove operations, cache orphan cleanup, plugin option storage, and component registry reload utilities under `plugins/`, `services/plugins/`, and `utils/plugins/`. Paths and symbols are provenance only.
