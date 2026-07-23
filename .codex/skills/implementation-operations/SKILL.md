---
name: implementation-operations
description: Implement operational services surrounding the semantic runtime, including credentials and providers, networking and proxies, portable filesystem/process/terminal ownership, cleanup, diagnostics, telemetry, usage, feature evaluation, and updates. Use when implementing platform boundaries or proving that operational failures cannot corrupt a session.
---

# Implementation Operations

Keep operational services replaceable and non-authoritative. Open [architecture.drawio](assets/architecture.drawio) for the ownership boundary around the semantic core.

## Shared operational invariants

- Acquire credentials through one protected port, label their source, refresh under a deduplicated lock, and redact them from prompts, logs, telemetry, subprocess arguments, and durable history.
- Probe platform and provider capabilities before use. Unsupported and temporarily unavailable are different states with different recovery behavior.
- Register every file, process, terminal mode, lock, timer, network connection, sleep inhibitor, and background exporter with an owner and idempotent cleanup action.
- Bound buffers, retries, timeouts, disk fallbacks, and shutdown flushes. A monitor or exporter cannot grow without limit.
- Treat telemetry, suggestions, notifications, diagnostics, and passive update discovery as observers. Their failure must not change semantic events, permission decisions, or durable transcript. An explicitly requested install is a platform mutation with its own status and exit contract, not an observability action.
- Evaluate build inclusion, runtime gate, privacy setting, managed policy, identity, platform, and sink health independently.
- Preserve stdout/stderr and structured-stream purity; operational messages use the surface's declared diagnostic channel.

## Specialized workflows

Use [implementation-auth-network](../implementation-auth-network/SKILL.md) to implement credential precedence, OAuth and API-key lifecycles, provider clients, TLS and custom certificates, proxies, request headers, authentication recovery, and secret handling.

Use [implementation-platform-lifecycle](../implementation-platform-lifecycle/SKILL.md) to implement filesystem and process primitives, terminal and OS integration, executable discovery, locks, notifications, sleep prevention, version policy, legacy and native installers, portable degradation, signals, and graceful shutdown.

Use [implementation-observability](../implementation-observability/SKILL.md) to implement usage and cost accounting, diagnostics, logging, traces, metrics, privacy filtering, feature-evaluation telemetry, disk fallback, updater instrumentation, and health reporting.

## Acceptance gate

Run the semantic core with every optional sink offline, with credentials expiring during a request, behind a failing proxy, on an unsupported platform, and during shutdown with in-flight writes and child processes. The session must either continue coherently or stop at the explicitly dependent boundary, with secrets absent and cleanup bounded.
