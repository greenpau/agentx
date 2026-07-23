---
name: implementation-plugins-hooks
description: Implement plugin manifests, marketplaces, installation registries, dependency and policy checks, cache lifecycle, component loading, and the complete hook event protocol. Use when implementing extension packaging, plugin updates, or lifecycle interception around runtime events.
---

# Implementation Plugins and Hooks

## Preserve packaging and interception boundaries

A plugin is an attributed, versioned contribution bundle; a marketplace is a trust and materialization source; a hook is an event-scoped interceptor with explicit authority. Keep installation intent, installed registry entries, cached material, active session contributions, and plugin-owned durable data as distinct state.

Use the [architecture diagram](assets/architecture.drawio) to inspect marketplace-to-session loading and hook dispatch. Read [plugin lifecycle](references/plugin-lifecycle.md) for schemas, source policy, dependencies, installation, versioning, cleanup, and component loading. Read [hook protocol](references/hook-protocol.md) for all events, matcher semantics, input/output schemas, process and HTTP execution, ordering, blocking effects, async behavior, and failure handling. Requirement identifiers `PLUG-*`, `MARKET-*`, and `HOOK-*` are stable implementation anchors.

## Plugin workflow

1. Resolve marketplace intent and policy before network or filesystem materialization.
2. Load a marketplace from a validated local or cache-root location, validate every plugin entry, and preserve structured errors per entry.
3. Resolve the root plugin and dependency graph in postorder. Cross-market dependencies require explicit trust from the root marketplace and never confer transitive trust.
4. Record enabled intent, materialize immutable versioned content, and update the scope-aware installed registry. A background update affects the next session, not the active snapshot.
5. Load manifest and standard component directories with canonical-path deduplication, source attribution, and component-level failure isolation.
6. On disable, uninstall, marketplace removal, or cache cleanup, prune active contributions and durable state according to the exact ownership and last-reference rules.

## Hook workflow

1. Freeze a hook configuration snapshot for the operation from settings, managed policy, plugins, SDK/session registrations, skills, agents, and internal callbacks.
2. Select entries by event and matcher, then evaluate optional tool-scoped `if` rules only for supported events.
3. Run all matched hooks concurrently with per-hook cancellation and timeout while preserving source identity and streaming progress.
4. Parse command, prompt, agent, HTTP, callback, and function results into one normalized result type. Validate structured output and the returned event name.
5. Aggregate event-specific decisions with the documented precedence, attach context or updated input, and let the owning runtime transition decide whether blocking still has effect.
6. Retain or detach asynchronous hooks explicitly; remove successful `once` hooks through their owning session callback.

## Invariants

- Policy checks precede download, installation, enablement, and session contribution.
- A plugin identifier, marketplace identifier, version, source, scope, and project identity are not interchangeable.
- A failed component does not invalidate unrelated components unless the manifest itself is unusable.
- Hooks observe an event snapshot; mid-event settings changes cannot add or remove a running hook.
- Exit code, structured decision, and event type jointly determine authority. A post-event hook cannot undo an already completed side effect.
- Hook subprocess output is untrusted and bounded; HTTP hooks are URL-policy constrained and protected from server-side request forgery.

## Verification checks

- Installing a dependency graph records dependencies before the root and rejects cycles without partial activation.
- A managed marketplace seed cannot be removed or refreshed by the user and never escapes its seeded location.
- Updating a plugin creates a new version path and registry record while the active session continues using the prior snapshot.
- Concurrent `PreToolUse` hook results aggregate `deny` over `ask` over `allow` regardless of completion order.
- `PostToolUse` exit code 2 can provide model-visible feedback but cannot claim the tool did not execute.
- A direct HTTP hook resolving to a private address is blocked, while a policy-allowed loopback endpoint remains supported.

