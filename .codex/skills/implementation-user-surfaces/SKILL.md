---
name: implementation-user-surfaces
description: Implement the observable product surfaces that adapt the shared session runtime to an interactive terminal, headless CLI, structured SDK stream, or feature-gated optional experience. Use when selecting a product entry surface, preserving cross-surface semantics, or deciding which specialized surface contract must be loaded.
---

# Implementation User Surfaces

## Objective

Implement product surface as an adapter over shared session, capability, permission, task, transcript, and persistence contracts. Preserve surface-specific input, output, prompts, and lifecycle behavior without creating a second meaning for model messages, tools, tasks, or cancellation.

See the [surface architecture diagram](assets/architecture.drawio) for the required boundary map.

## Shared surface contracts

- **SURF-001 — Semantic parity.** Interactive, headless, SDK, bridge, and remote clients consume the same normalized session events. A surface may suppress, decorate, batch, or serialize an event, but must not change its semantic meaning.
- **SURF-002 — Early identity.** Determine entrypoint and interaction mode before initializing logging, configuration error presentation, terminal state, or output sinks.
- **SURF-003 — Transcript separation.** Treat the transcript as authoritative event history. Terminal-only progress, overlays, spinners, local component views, and redraw artifacts do not enter model context unless deliberately translated into a typed message.
- **SURF-004 — Output ownership.** Exactly one adapter owns each process output channel. Interactive rendering may patch console output; structured and text modes must avoid initializing that renderer so stdout remains protocol-clean.
- **SURF-005 — Permission parity.** All surfaces cross the same permission boundary. Interactive mode renders a local decision dialog; structured and remote modes correlate the decision over their control protocol.
- **SURF-006 — Scoped terminal results.** Every accepted tool use and finite turn-owned operation reaches a success, failure, denial, cancellation, or killed result before its live owner releases it. A long-lived task may instead be handed off with registered identity, owner, retrieval, cancellation, and later-notification paths. Live surface shutdown settles every waiter it still owns; after process death or a named compatibility loss window, restart classifies orphaned tasks/controls only from surviving durable evidence and does not fabricate a terminal result for erased process-local state.
- **SURF-007 — Feature absence.** Build exclusion, runtime gating, eligibility, authentication, policy, platform support, and current availability are independent. Disabled optional surfaces leave the core runtime usable.
- **SURF-008 — Presentation backpressure.** A slow UI or transport may buffer boundedly, but semantic event ordering and durable state updates must survive presentation delay or disconnect.
- **SURF-009 — Opaque operational failures.** Exit-code selection and user-visible projection classify foreign failures only from exact sentinels, surface-owned context state, detached values, and package-sealed snapshots. A surface may traverse exact standard-library or package-owned wrappers, but stops at a foreign child and never invokes foreign `Error`, `Is`, `As`, or `Unwrap` behavior. Unknown failures receive fixed diagnostics, and a blocking error method cannot delay completion or exit.

## Implementation workflow

1. Identify the entry surface before loading terminal, protocol, or optional-experience dependencies.
2. Load the specialized skill for that surface.
3. Map its inputs into the shared normalized message and control contracts.
4. Map shared session events back into the surface's presentation or wire representation.
5. Preserve permission, cancellation, result, flush, and shutdown ordering.
6. Test the same semantic scenario through at least interactive and headless/SDK adapters.

## Specialized workflows

Use [implementation-terminal-engine](../implementation-terminal-engine/SKILL.md) to implement terminal rendering, byte-level input parsing, focus, selection, prompt editing, keybindings, Vim behavior, paste handling, and prompt history.

Use [implementation-interactive-repl](../implementation-interactive-repl/SKILL.md) to implement the interactive session controller, prompt dispatch guard, queued work, dialogs, message projection, fullscreen transcript, and cancellation behavior.

Use [implementation-headless-sdk](../implementation-headless-sdk/SKILL.md) to implement CLI mode selection, one-shot output, the serialized headless runner, SDK NDJSON schemas, correlated controls, event ordering, and structured shutdown.

Use [implementation-optional-experiences](../implementation-optional-experiences/SKILL.md) to implement feature-gated assistant viewing, voice input, terminal companion behavior, browser-extension automation, direct desktop control, and supported absence or stub behavior.

## Cross-surface acceptance

- Submit the same prompt interactively and through structured input; both produce equivalent model-visible messages and tool decisions even though presentation differs.
- Deny a tool locally and through an SDK permission response; both yield a normalized denial tied to the original tool-use identifier.
- Interrupt during streaming; every accepted tool/control identifier terminates and durable transcript state remains resumable.
- Emit terminal-only progress; it is visible in the interactive UI but absent from implemented model context and replayed semantic history.
- Start a build with every optional experience excluded; interactive and headless core workflows still initialize and shut down normally.

## Non-normative provenance

Behavior was specified from the entrypoint, CLI, REPL, terminal renderer, prompt input, SDK schema, optional-experience, and presentation areas of the repository. Paths and implementation symbols are evidence only and are not required by an implementation.
