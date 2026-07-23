---
name: implementation-observability
description: Implement usage and cost accounting, privacy filtering, diagnostics, logs, traces, metrics, feature-evaluation evidence, health reporting, disk fallback, and update-check observation. Use when adding an event, sink, counter, profiler, diagnostic command, or operational alert without making observability a correctness dependency.
---

# Implementation Observability

Observe the semantic runtime without becoming its authority. Read [observability-contract.md](references/observability-contract.md) before defining events, attributes, counters, traces, buffers, disk fallback, diagnostics, or update health. Read the [versioned event wire catalog](references/event-wire-catalog.md) and its [event-wire diagram](assets/event-wire.drawio) when implementing generated internal-event, experiment, authentication-context, or timestamp records. Use [architecture.drawio](assets/architecture.drawio) to trace privacy filtering and sink failure.

## Workflow

1. Start from a canonical semantic event or explicit operational measurement.
2. Classify fields by privacy, cardinality, source, and whether aggregation is sufficient.
3. Apply opt-out, essential-traffic, managed-policy, build, and sink kill-switch decisions before routing.
4. Update authoritative local usage/cost counters separately from best-effort exporters.
5. Bound queues, batches, retries, disk persistence, open spans, and shutdown flush.
6. Verify that disabling or breaking every sink does not alter semantic events, permissions, transcript, side effects, or exit status.

## Completion check

Satisfy all `OBS-*` rules and scenarios. Test privacy filtering, high-cardinality rejection, duplicate exposure, offline disk fallback, process restart, queue overflow, stale spans, malformed sink response, and shutdown timeout.
