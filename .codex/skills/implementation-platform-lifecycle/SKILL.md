---
name: implementation-platform-lifecycle
description: Implement portable filesystem, process, terminal, executable, lock, notification, sleep-prevention, updater-mechanism, signal, and shutdown behavior. Use when implementing an operating-system adapter, owning external resources, handling cancellation or exit, or defining safe degradation on unsupported platforms.
---

# Implementation Platform and Lifecycle

Build a narrow platform port whose resources have explicit owners and bounded cleanup. Read [platform-lifecycle-contract.md](references/platform-lifecycle-contract.md) before implementing filesystem, process, terminal, lock, notification, installer, or shutdown primitives. Read [portable-runtime-services.md](references/portable-runtime-services.md) for the exact filesystem/path adapters, bounded reads, process projections, buffering and timer races, platform and locale probes, notifications, sleep inhibition, retention scheduling, and graceful-shutdown order. Read [portable-data-primitives.md](references/portable-data-primitives.md) when implementing shared JSON, JSON-with-comments, JSON-lines, memoization, collections, hashes, asynchronous sequences, FIFO execution, rolling buffers, error classifiers, Unicode normalization, joining, or truncation. Read [updater-contract.md](references/updater-contract.md) when implementing version policy, registry or object-store discovery, the legacy global-package installer, or update locking. Read [native-installer-contract.md](references/native-installer-contract.md) for native artifact acquisition, version activation, per-version locking, package-manager coexistence, cleanup, and rollback. Use [architecture.drawio](assets/architecture.drawio) to follow acquisition through release, [portable-runtime-services.drawio](assets/portable-runtime-services.drawio) for the platform-port, housekeeping, and shutdown state machines, [portable-data-primitives.drawio](assets/portable-data-primitives.drawio) for parsing, caching, recovery, collection, and bounded-text flows, [updater-state-machine.drawio](assets/updater-state-machine.drawio) for the updater decision and mutation order, and [native-installer-state-machine.drawio](assets/native-installer-state-machine.drawio) for native installation and retention.

## Workflow

1. Probe platform, runtime, terminal, executable, and filesystem capabilities without causing the side effect being tested.
2. Normalize paths and environment at the port boundary; retain both lexical and physical path forms when authorization depends on them.
3. Acquire resources with explicit modes, bounds, abort ownership, and a registered cleanup action.
4. Return structured output that distinguishes success, nonzero exit, signal, timeout, cancellation, unavailable capability, and internal failure at semantic boundaries. Use the deliberately lossy `PORT-PROC-*` no-throw projection only for compatibility callers that do not make decisions from the collapsed cause.
5. On shutdown, protect durable session state and terminal restoration before optional hooks, analytics, or update work.
6. Test every adapter on unsupported and partially available profiles.

## Ownership boundary

Own portable mechanics and resource lifetime, not semantic permission, tool meaning, credential precedence, or telemetry truth. A capability owner chooses whether a platform result permits continuation. The platform layer never broadens authority merely because an OS primitive succeeds.

## Completion check

Satisfy every `PLAT-*`, `PORT-*`, and `PRIM-*` contract and scenario, including symlinks, partial files, lossy process projections, signal races, output pressure, terminal loss, stale locks, child cleanup, unsupported notifications, failed updater mechanics, cache invalidation races, malformed JSON-lines, Unicode length boundaries, and a hanging shutdown callback.
