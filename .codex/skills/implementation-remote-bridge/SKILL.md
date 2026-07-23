---
name: implementation-remote-bridge
description: Implement remote-control bridging and remote session placement while preserving session identity, worker leases, ordered replay, permission and control relays, reconnect behavior, and local safety authority. Use when implementing or reviewing environment-backed or environment-less bridges, bridge daemons and REPL remote control, Hybrid WebSocket/HTTP or CCR SSE transports, remote viewers, direct-connect servers, SSH sessions, or teleport/resume across machines.
---

# Implementation Remote Bridge

## Objective

Implement distributed-session adapters without changing the meaning of a local conversation. Preserve who owns the transcript, which worker epoch may write, what has been durably delivered, and where permission authority remains. Treat remote placement as a transport and lifecycle concern around the shared session engine, never as a second query engine.

Use the [architecture diagram](assets/architecture.drawio) for the broad transport flow, the [identity and replay diagram](assets/identity-replay.drawio) for identity separation and crash-loss boundaries, and the [adapter utility diagram](assets/adapter-utilities.drawio) for inbound trust boundaries, local persistence, and remote plan handoff. The editable Draw.io sources are normative for topology only; the numbered prose contracts define behavior.

## Implementation workflow

1. Classify the requested surface before choosing a protocol:
   - environment-backed bridge daemon, print worker, or legacy REPL bridge;
   - environment-less REPL bridge;
   - remote viewer/controller;
   - direct-connect server;
   - SSH-hosted runtime;
   - teleport or cross-machine resume.
2. Load [bridge architectures](references/bridge-architectures.md) for registration, work claiming, spawn modes, leases, shutdown, and disabled behavior.
3. Load [transport protocols](references/transport-protocols.md) for wire envelopes, flush gating, event ordering, deduplication, delivery acknowledgement, credential renewal, control requests, and permission relay.
   Load [the CCR worker wire catalog](references/ccr-wire-catalog.md) whenever implementing CCR bootstrap, HTTP worker operations, SSE framing, epoch/cursor behavior, or delivery uploads.
4. Load [remote placement](references/remote-placement.md) for viewer, direct-connect, SSH, and teleport contracts.
5. Load [adapter utilities](references/adapter-utilities.md) for crash pointers, inbound attachments and images, ingress credentials, remote eligibility, turn-output persistence, diagnostics, session-identifier parsing, and remote plan handoff.
6. Declare the identity tuple and authority boundary before writing code. At minimum name the local bridge instance, local transcript UUID, environment if present, work item if present, remote logical session, control-surface generation if a hardened implementation adds one, transport worker epoch, observed delivery cursor, control request, and tool-use identifiers. State which associations are absent from reference persistence.
7. Model every lifecycle as an explicit state machine. Keep registration, work ownership, subprocess ownership, transport connectivity, credential validity, and remote session terminal state separate.
8. Apply retry only to operations defined as idempotent or cursor-resumable. Never infer acknowledgement from a successful socket write.
9. Implement cancellation as an ordered protocol: stop accepting new work, settle accepted tool/control requests, publish a terminal result when possible, archive best-effort, close transport, then release environment and local resources.
10. Exercise the conformance cases in [acceptance and provenance](references/acceptance-and-provenance.md) and [adapter utilities](references/adapter-utilities.md), including loss, duplication, stale epochs, credential expiry, reconnect, partial setup, disabled builds, unsafe attachments, stale recovery pointers, and plan-polling ambiguity.

## Non-negotiable boundaries

- **RB-CORE-001 — Shared semantics.** Remote modes reuse message normalization, tool contracts, permission policy, transcript rules, and query-loop terminal conditions. A transport may project events but must not reinterpret them.
- **RB-AUTH-001 — Local safety authority.** A remote controller may request a mutable operation only through the local permission boundary. Transport authentication proves channel identity, not tool authorization.
- **RB-ID-001 — Layered identity.** Environment, work item, logical session, compatibility session spelling, worker epoch, event sequence, message UUID, control request, and tool-use identifiers remain distinct and are never substituted merely because their string values resemble one another.
- **RB-OWN-001 — Single active writer.** The server-issued worker epoch fences stale workers. Every epoch-scoped worker mutation, heartbeat, event upload, and delivery report carries the active epoch; authenticated SSE and prior-state reads use their separately cataloged cursor/query shape. An epoch conflict terminates or implements the worker connection before further writes.
- **RB-ORD-001 — History before live data.** Initial eligible history is emitted before live messages. Live writes arriving during the initial flush are held by a FIFO flush gate and released only after history completes.
- **RB-RPL-001 — Bounded replay.** Reconnect resumes from an explicit delivery cursor or server cursor. UUID deduplication suppresses echoes and replay duplicates, but bounded caches are not durable acknowledgement.
- **RB-LSE-001 — Explicit lossy windows.** Any deliberately lossy interval, including control traffic during credential recovery, is named and observable while the owning process still has its counters/waiters. It must not be hidden behind an apparent success. Abrupt process death erases memory-only counts and correlation maps; restart reports the documented unknown-loss class plus surviving evidence, not an invented exact count.
- **RB-CAN-001 — Live-generation terminal accounting.** Outside named drop windows, every accepted work item and control request still owned by the live generation reaches success, denial, cancellation, failure, or superseded terminal outcome, and cancellation settles its known waiter. Credential recovery can deliberately drop named control/terminal traffic, and process death erases pending maps. Those paths require explicit loss/orphan classification where evidence survives, not a fabricated correlated response.
- **RB-OFF-001 — Fail closed when unavailable.** Build exclusion, feature gates, account eligibility, organization identity, minimum version, remote host prerequisites, and policy are independent checks with actionable disabled behavior.
- **RB-SEC-001 — Credential isolation.** OAuth tokens, trusted-device tokens, worker JWTs, ingress tokens, and local auth-proxy sockets have different scopes. Never serialize them into transcripts, logs, child prompts, or repository state.
- **RB-CMP-001 — Compatibility is explicit.** Compatibility session-ID conversion, legacy control keys, legacy environment work APIs, and transport-version selection are named shims with independent gates; do not let one imply another.

## Required implementation artifacts

Produce these artifacts for a standalone implementation:

1. A surface-selection table containing build inclusion, runtime gates, account/policy requirements, and fallback behavior.
2. Separate state machines for bridge registration, work claiming, spawned-session ownership, transport connectivity, credential refresh, and teardown.
3. A typed wire catalog for every request, response, event, and control envelope, including forward-compatible unknown-event behavior.
4. An identity and authority ledger showing issuer, scope, persistence, comparison rule, and invalidation event for every identifier or credential.
5. Retry tables with initial delay, cap, jitter, attempt ceiling or elapsed-time ceiling, retryable status classes, and idempotency argument.
6. Ordering and replay tests that inject duplicates, reordering, disconnects, stale epochs, malformed post-ID SSE payloads, stale orphan permission responses, and crashes between observation, delivery reporting, local processing, and memory-queue drain.
7. Placement adapter tests proving that local, bridged, direct, SSH, and teleported sessions expose equivalent conversation and permission semantics.

## Reference routing

- Use [bridge architectures](references/bridge-architectures.md) to implement environment registration, environment-less initialization, work polling, spawn modes, capacity, heartbeat leases, and teardown.
- Use [transport protocols](references/transport-protocols.md) to implement Hybrid and CCR transports, SSE/WebSocket behavior, delivery cursors, flush gates, deduplication, controls, permissions, and token recovery.
- Read [the CCR worker wire catalog](references/ccr-wire-catalog.md) for the exact client-visible CCR routes, methods, headers, request and response bodies, SSE envelope, status handling, retry timing, queue bounds, and opaque service boundary.
- Use [remote placement](references/remote-placement.md) to implement viewer/controller, direct-connect, SSH, and teleport/resume adapters.
- Use [adapter utilities](references/adapter-utilities.md) to implement local recovery pointers, inbound content normalization, ingress authentication, eligibility checks, turn-output persistence, safe diagnostics, session-identifier parsing, and UltraPlan polling or trigger behavior.
- Use [acceptance and provenance](references/acceptance-and-provenance.md) to validate contract coverage and consult non-normative source provenance only when auditing the documented build.

## Completion standard

Do not claim this domain implemented until every `RB-*` contract used by the implementation maps to at least one state transition or wire test; every accepted identifier still owned by the live generation outside a named drop window has terminal accounting; and a process killed at every persistence or delivery boundary either resumes from surviving durable evidence without duplication or classifies the explicit unknown-loss/orphan window without fabricating a terminal state, callback, or exact count.
