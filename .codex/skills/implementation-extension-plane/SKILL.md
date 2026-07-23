---
name: implementation-extension-plane
description: Implement the session-scoped extension plane that discovers, attributes, filters, names, merges, and invalidates commands, skills, output styles, plugins, hooks, MCP providers, and language servers. Use when deciding how extension contributions become reachable without weakening core runtime contracts.
---

# Implementation Extension Plane

## Preserve compositional registries

Extensions contribute descriptors and protocol adapters; they do not acquire implicit authority. Discover contributions with source provenance, validate them at the boundary, normalize their identities, resolve deterministic precedence, apply feature/trust/policy filters, and publish immutable session registries. Invocation must return through the shared command, tool, query, permission, hook, and task contracts.

Use the [architecture diagram](assets/architecture.drawio) to inspect discovery and registry composition. Read [registry composition](references/registry-composition.md) for common identity, provenance, collision, policy, cache, reload, and failure rules. Requirement identifiers `EXT-*` are stable implementation anchors.

## Specialized workflows

- Use [implementation-commands-input](../implementation-commands-input/SKILL.md) to implement slash-command discovery, user-input routing, references, attachments, local control, and model-bound expansion.
- Use [implementation-skills-output](../implementation-skills-output/SKILL.md) to implement skill discovery and invocation plus output-style selection and prompt effects.
- Use [implementation-plugins-hooks](../implementation-plugins-hooks/SKILL.md) to implement plugin manifests, marketplaces, installation, dependencies, component loading, and lifecycle hooks.
- Use [implementation-mcp-lsp](../implementation-mcp-lsp/SKILL.md) to implement MCP configuration, transports, authentication, elicitation, channels, and plugin language servers.

## Composition workflow

1. Freeze build capabilities, runtime gates, account eligibility, platform support, trust, and managed policy before discovering optional sources.
2. Ask each provider for attributed descriptors without allowing one broken provider to corrupt unrelated registries.
3. Normalize canonical names and aliases within the owning domain. Never use filesystem location alone as public identity.
4. Merge built-ins, managed content, installed plugins, explicit directories, project content, user content, and remote providers according to the owning registry's precedence—not one universal order.
5. Apply disablement, source restrictions, plugin-only customization policy, allowlists, and managed overrides after enough provenance exists to decide correctly.
6. Build derived prompt listings and callable catalogs from the same registry snapshot so displayed availability and invocation agree.
7. On reload, invalidate all dependent caches as one generation. Prevent stale asynchronous discovery from publishing into a newer generation.

## Invariants

- Compile-time inclusion, feature enablement, account eligibility, policy permission, configuration, connection health, and current availability are separate dimensions.
- A collision is resolved deterministically and retains diagnostics for every shadowed contribution.
- Disabled or unavailable extensions remain explainable but are absent from callable/model-visible registries.
- Remote metadata is untrusted protocol input. Validate schemas, cap sizes, normalize names, and isolate failures.
- Removing an extension prunes active contributions immediately where safe; additions that require a session rebuild do not appear until that rebuild completes.
- Extension reload never mutates an in-flight tool call, hook snapshot, or model request.

## Verification checks

- Two providers contributing the same canonical name produce the documented winner and a source-attributed collision diagnostic.
- Plugin-only policy leaves managed, built-in, and permitted plugin contributions while excluding filesystem customization.
- A malformed plugin or disconnected MCP server does not remove repository-local skills.
- A reload racing slow discovery cannot publish old descriptors after the new generation is active.
- The model-visible capability list, user-visible discovery list, and invocation registry all derive from the same filtered generation.
