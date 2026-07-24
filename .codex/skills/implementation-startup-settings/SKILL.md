---
name: implementation-startup-settings
description: Implement startup configuration, settings precedence, managed policy, remote policy refresh, settings synchronization, trust-sensitive derivation, and live reload. Use when implementing or auditing any behavior whose effective value depends on settings source, policy authority, startup ordering, or configuration changes.
---

# Implementation Startup Settings

## Preserve configuration as attributed state

Configuration is not one mutable map. Preserve each source independently, including its authority, location, validation result, editability, and trust status, then derive an effective immutable snapshot. A value and the source that supplied it are both behaviorally significant: policy enforcement, permission persistence, trust checks, diagnostics, and reload hooks all depend on provenance.

Use the [architecture diagram](assets/architecture.drawio) to inspect the startup and live-reload paths. Use the [global/project configuration diagram](assets/global-project-config.drawio) to inspect durable identity, cache coherence, locking, fallback, and corruption windows. Read [durable global and project configuration](references/durable-global-project-config.md) for the complete application-owned `GlobalConfig`/`ProjectConfig` schema, defaults, trust identity, migrations, backups, and concurrency contract. Read [settings schema](references/settings-schema.md) for the exact persisted field grammar, nested schemas, feature-gated keys, unknown-field policy, and compatibility rules. Read [settings resolution](references/settings-resolution.md) for source discovery, merge rules, writes, and trust-derived views. Read [policy, synchronization, and reload](references/policy-sync-reload.md) for managed settings, remote caches, policy limits, cross-device sync, and change detection. Requirement identifiers `GCFG-*`, `SETS-*`, `SET-*`, and `POL-*` are stable implementation anchors.

## Initialization workflow

1. Resolve and freeze the application home, then perform the
   `GCFG-PATH-006` owned-home and `sessions/` bootstrap before command-line
   parsing. Perform the `AUTH-045` direct-child existence gate before the full
   parser. Establish the remaining process facts before loading settings:
   interaction mode, original working directory, platform, build gates,
   explicit settings input, and enabled source set.
2. Locate and parse every eligible source independently. Preserve a parseable source's unknown or temporarily invalid fields for future round trips, but never expose a schema-invalid source as effective configuration.
3. Resolve managed policy as a whole-source authority before ordinary merging. Managed policy selection is fallback, not deep merge.
4. Merge ordinary sources from low to high precedence while retaining field-level source attribution. Return defensive copies because the merge algorithm may mutate arrays or objects.
5. Derive trust-sensitive views, environment changes, permissions, sandbox policy, extensions, models, and other runtime registries from one coherent snapshot.
6. Start remote policy refreshes and settings synchronization only after authentication and eligibility are known. Cached managed data may unblock startup; network work must remain bounded.
7. Start the change detector after initial application. Route external changes through `ConfigChange` hooks, reset shared caches exactly once, then fan out one coherent update.

## Required boundaries

- Keep command-line/bootstrap facts separate from merged settings and from durable global account configuration.
- Distinguish user preference from enforced managed policy and remotely delivered policy limits.
- Never execute project-sourced helper commands before workspace trust in an interactive session.
- A syntax-error file is user-owned evidence: report it and refuse to overwrite it automatically.
- Internal writes must not re-enter the external-change pipeline or cause duplicate application.
- Remote failure may use a previously validated cache, but absence of both cache and network data must follow the documented fail-open or fail-closed rule for that policy family.
- Configuration changes update only domains whose contracts permit hot reload; a component requiring restart must keep the active snapshot stable and explain the deferred change.

## Verification checks

- A scalar, object, and array set differently in all ordinary sources resolve to the exact precedence and merge behavior in `SET-020` through `SET-027`.
- An invalid remote managed payload falls through to the next valid managed source without partially applying the remote object.
- A managed file assembled from fragments applies the base first and non-hidden fragments in deterministic lexical order.
- An external edit blocked by a `ConfigChange` hook leaves the previous effective snapshot and all downstream registries intact.
- An internal atomic write followed by file-system notifications produces no duplicate reload, while a later user edit does.
- Startup with a valid remote cache and an offline network uses the cache; startup with no cache follows the policy family's documented default.
