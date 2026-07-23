---
name: implementation-mcp-lsp
description: Implement Model Context Protocol configuration, transport and connection lifecycle, OAuth, elicitation, channel permissions, result normalization, and plugin-provided language servers. Use when implementing external capability servers or editor-intelligence providers.
---

# Implementation MCP and LSP

## Preserve external protocols as untrusted adapters

MCP servers and language servers contribute remote capabilities through explicit protocol boundaries. Validate configuration before connection, retain source and scope, bound every request and output, and translate external lifecycle state into session registries without letting a failed integration corrupt the conversation.

Use the [architecture diagram](assets/architecture.drawio) to inspect both provider lifecycles and the [IDE adapter diagram](assets/ide-adapter.drawio) to trace lockfile identity, workspace/process validation, WSL path translation, connection generations, teardown, RPCs, and notifications. Read [MCP runtime](references/mcp-runtime.md) for configuration, precedence, policy, transports, state, discovery, concurrency, requests, output, and cleanup. Read [MCP authentication, elicitation, and channels](references/mcp-auth-elicitation-channels.md) for OAuth, step-up authorization, user elicitation, server-initiated notifications, and channel gating. Read [IDE adapter](references/ide-adapter.md) for lockfile discovery, workspace validation, editor transports, RPCs, notifications, diff handoff, and extension installation. Read [LSP runtime](references/lsp-runtime.md) for plugin configuration, initialization, server state, requests, crash recovery, diagnostics, and reload. Requirement identifiers `MCP-*`, `MCPOAUTH-*`, `CHANNEL-*`, `IDE-*`, and `LSP-*` are stable implementation anchors.

## MCP workflow

1. Discover eligible server definitions by scope, validate transport-specific schemas, and require project approval where applicable.
2. Apply enterprise exclusivity, plugin-only policy, disablement, allow/deny policy, and semantic duplicate suppression before connecting.
3. Create the transport, drive connection state through pending to connected, failed, needs-auth, disabled, or terminal cleanup, and publish state in bounded batches.
4. Discover tools, resources, prompts, instructions, capabilities, and subscriptions with size caps and precise cache invalidation on list-change notifications.
5. Route requests through per-placement concurrency limits, timeouts, cancellation, permission, and normalized output storage.
6. On authentication or elicitation requirements, suspend only the affected request, use the documented user/SDK/hook protocol, then resume or return an explicit protocol error.

## LSP workflow

1. Accept language-server definitions only from enabled plugins, merge standard configuration then manifest overrides, expand only approved variables, and reject traversal.
2. Initialize all eligible plugin servers concurrently while preserving deterministic final registry order and generation identity.
3. Spawn a server, perform the initialize/initialized handshake, advertise the fixed client capabilities, and enter running state.
4. Route bounded language queries and document lifecycle notifications. Retry content-modified responses with the documented schedule.
5. Publish capped, cross-turn-deduplicated diagnostics and clear them on edits.
6. Recover crashes only within the configured restart budget; on plugin reload, prevent stale generations from publishing and stop old processes best-effort.

## Invariants

- A server's display name, scoped identity, source, transport, and connection instance remain distinct.
- Manual MCP configuration outranks plugin contribution; disabled definitions do not suppress unrelated duplicates.
- OAuth tokens and client secrets are never included in model-visible context or ordinary logs.
- Elicitation content is schema-validated, retry-bounded, and completed exactly once.
- Channel delivery requires every gate—capability, runtime, identity, OAuth, managed policy, session configuration, and allowlist—not merely an MCP connection.
- LSP failure is optional-feature failure; it never prevents core session startup.

## Verification checks

- A project MCP server remains unavailable until approved and becomes unavailable again if its material definition changes incompatibly.
- Enterprise-managed MCP configuration excludes ordinary manual sources rather than merging beneath them.
- Session-expiry signals close and clear an HTTP transport so a later request creates a fresh session.
- An OAuth insufficient-scope response performs bounded step-up authorization without discarding the usable base grant.
- An elicitation that repeatedly fails schema validation terminates after three correction attempts.
- A crashing language server restarts no more than its configured budget and stale output from a prior plugin generation is ignored.
