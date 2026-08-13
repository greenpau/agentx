---
name: implementation-headless-sdk
description: Implement noninteractive execution and the structured SDK stream, including provider-free native session management, CLI mode inference and validation, stdin acquisition, text/JSON/NDJSON output, the serialized headless turn runner, correlated control requests, permission races, replay and deduplication, task/result ordering, and protocol-clean shutdown. Use for print mode, native session inventory or deletion, automation, SDK clients, structured I/O, or headless lifecycle behavior.
---

# Implementation Headless SDK

## Objective

Expose the shared semantic session engine through deterministic noninteractive contracts suitable for shell use, automation, and bidirectional SDK clients.

See the [headless and SDK architecture diagram](assets/architecture.drawio) for the CLI, reader, runner, control, and output ordering boundaries. Use the [remote I/O transport diagram](assets/remote-io-transports.drawio) for transport selection, observation, processing, delivery, and close boundaries. Use the [explicit update command state machine](assets/update-command.drawio) for installation diagnosis, mechanism routing, output, and exit behavior.

## Load references by task

- Read [cli-contract.md](references/cli-contract.md) to implement entry-mode inference, prompt and repeatable attachment acquisition, output modes, option conflicts, validation, initialization ordering, errors, and exit behavior.
- Read [cli-grammar.md](references/cli-grammar.md) to implement exact root option spellings and arity, parser boundaries, early position-sensitive rewrites, public subcommand grammar, gates, aliases, and supported absence.
- Read [headless-runner.md](references/headless-runner.md) to implement the concurrent input reader, serialized run loop, initialization, priorities, replay, result holdback, tasks, EOF, cancellation, and shutdown.
- Read [sdk-wire-protocol.md](references/sdk-wire-protocol.md) to implement NDJSON framing, attachment capability negotiation and imports, typed user content, SDK event schemas, control correlation, environment updates, permission races, duplicate suppression, and wire-level acceptance cases.
- Read [sdk-permission-wire.md](references/sdk-permission-wire.md) to implement the exact `can_use_tool` request, permission-update and response unions, absent/null behavior, request-ID compatibility, waiter-specific validation, cancellation, and orphan-response semantics.
- Read [remote-io-transports.md](references/remote-io-transports.md) to implement the SDK URL adapter, WebSocket, Hybrid, SSE, and CCR worker transports, ordered uploaders, retry and cursor semantics, close loss windows, and stdout protection.
- Read [public-sdk-contract.md](references/public-sdk-contract.md) to implement the public SDK facade, session inspection and mutation helpers, scheduler and remote-control handles, sandbox configuration, and schema-compatibility boundary.
- Read [management-subcommands.md](references/management-subcommands.md) to implement provider-free native session inventory/deletion and the noninteractive agents, authentication, auto-mode, MCP, plugin, marketplace, setup-token, doctor, and installer command adapters with exact output and exit behavior.
- Read [update-command.md](references/update-command.md) to implement the explicit update/upgrade command, including installation reconciliation, package-manager guidance, native forcing, legacy status mapping, and the specified channel/installed-target mismatch.

## Core contracts

- **SDK-001 — Clean channels.** Structured stdout contains only valid protocol records. Diagnostics use stderr or an explicit structured diagnostic event.
- **SDK-002 — Shared semantics.** Text, aggregate JSON, streaming JSON, and SDK modes project the same underlying session events with documented filtering and aggregation.
- **SDK-003 — Dual-loop coordination.** Read input concurrently so interrupt and permission responses remain responsive; serialize semantic turns through one run mutex.
- **SDK-004 — Correlated controls.** Match every control request and response by request identifier. EOF, cancellation, malformed responses, or shutdown settle all pending requests explicitly.
- **SDK-005 — FIFO output.** Session events and control requests share one ordered outbound queue. A later permission request cannot overtake prior assistant or task output.
- **SDK-006 — Terminal result ordering.** Emit finite background task progress and notifications before the result, prompt suggestion after the result, and authoritative idle only after result enqueue and the adapter's remote internal/resume-event flush. This is not a visible-client delivery acknowledgement.
- **SDK-007 — Idempotent input.** Treat the stable prompt/user UUID, committed attachment correlations, and resolved tool-use identifiers as replay/deduplication keys; acknowledge duplicates without rerunning them or reimporting media.

## Implementation workflow

1. Determine noninteractive mode and validate all option relationships before producing protocol output.
2. Acquire argv/stdin input under the selected text or streaming contract.
3. Initialize a non-rendering application state and protocol output sink.
4. Start the input reader and serialized semantic runner.
5. Apply initialization controls before draining prequeued work.
6. Stream or aggregate normalized session events according to the selected output mode.
7. Hold terminal results until finite background work settles, distinguish semantic, remote-internal, and visible-output drains, then report the correct exit status.

## Boundary rules

- Never initialize the interactive terminal renderer in structured mode.
- Route native session inventory and deletion through their provider-free CLI
  adapter before semantic-session construction; do not imply that equivalent
  duplex SDK controls exist.
- Route `--list-providers` through strict provider-registry discovery before
  workspace or semantic-session construction. Publish the same credential-free
  descriptor grammar as SDK initialization, but select no profile so an editor
  can discover a valid multi-provider/no-default registry before launch.
- Recompute tools and late-arriving extension clients at turn boundaries rather than freezing the first-turn registry.
- Keep transport-specific reconnection outside the stdio protocol; both must preserve the same event order and correlation semantics.
- Fail closed on permission/control errors and fail fast on malformed protocol framing.

## Non-normative provenance

Evidence came from the reference command-line entrypoint, top-level option parser, print/headless runner, structured I/O stream, SDK schemas, and protocol adapters. Paths and implementation types are not normative.
