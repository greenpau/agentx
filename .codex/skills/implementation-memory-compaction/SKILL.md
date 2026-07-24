---
name: implementation-memory-compaction
description: Implement context-pressure management and derived memory, including persistent file memory, relevance recall, shared team-memory synchronization, automatic dream consolidation, token estimation, automatic and manual compaction, session-memory compaction, microcompaction, context collapse, restoration, and cleanup. Use when retaining useful knowledge or reducing model context without confusing derived artifacts with authoritative history.
---

# Implementation Memory and Compaction

## Workflow

1. Measure context with model-aware input and reserved-output limits.
2. Select one eligible strategy using explicit gates, recursion guards, query ownership, thresholds, and failure circuit breakers.
3. Preserve API invariants and authoritative history while producing a smaller active projection.
4. Restore current files, plan/mode state, invoked skills, agents, and capability deltas within fixed budgets.
5. Implement file-memory identity, bounded relevance surfacing, shared-memory trust/sync, and consolidation as separate lifecycles.
6. Record a compact boundary and clean up only caches owned by the compacting thread.

Read [the complete memory and compaction contract](references/memory-compaction-contract.md) before implementing or auditing this domain. Use the [architecture diagram](assets/architecture.drawio) for context-strategy topology and the [persistent memory, team sync, and dream diagram](assets/persistent-memory-sync.drawio) to trace path trust, relevance injection, conflict/partial-commit behavior, and consolidation locking.

The standalone Go runtime currently implements the bounded
`MC-MEM-002G` path profile, not the configurable canonical-repository identity
in `MC-MEM-002`. Keep that divergence explicit in source traceability and
conformance status until the broader contract is implemented.

## Boundaries

Own derived summaries, private and shared file memory, relevance selection, team-memory sync, automatic consolidation, session memory, context edits, collapse projections, and post-compact reinjection. Keep the append-only transcript authoritative. Shared memory is untrusted collaborator content, not system policy. Do not let a subagent compact clear process-global state owned by the main thread.

## Completion check

- Preserve all `MC-*` contracts in the reference.
- Test threshold boundaries, disabled modes, memory-path containment, relevance budgets, stale-memory warnings, team-sync conflict/partial-commit/crash behavior, secret filtering, auto-dream lock rollback, recursion guards, prompt-too-long retries, full and partial directions, session-memory fallback, tool-pair preservation, preserved-segment resume, time-based cache expiry, circuit breaking, and source-aware cleanup.
- Confirm compaction can be retried or abandoned without corrupting the current conversation.
