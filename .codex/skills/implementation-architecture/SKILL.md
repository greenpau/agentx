---
name: implementation-architecture
description: Develop, review, or extend AgentX across its complete application architecture. Use when tracing a requirement across runtime domains, deciding which implementation subskill owns a behavior, changing cross-domain boundaries, or auditing project coverage.
---

# Implementation Architecture

Develop AgentX as a set of replaceable adapters around one semantic session runtime. Preserve observable contracts, state transitions, ordering, policy decisions, durability, and recovery while keeping package ownership and dependencies explicit.

Open [architecture.drawio](assets/architecture.drawio) for the system boundary and dependency direction, and [skill-routing.drawio](assets/skill-routing.drawio) for the complete root-to-router-to-leaf hierarchy. Interpret every Draw.io asset using [the architecture diagram contract](references/diagram-contract.md): begin with `Context & Boundaries`, follow its product breadcrumb and highlighted lifecycle position, then use `Responsibility Flow` for labeled topology. The highlighted boundary is owned here; subdued nodes are upstream, downstream, sibling, or delegated context. Numbered prose remains authoritative except for topology or ownership explicitly declared in a page's `Authority` note. Read [system-contract.md](references/system-contract.md) before designing shared types or control flow. Read [contract-map.md](references/contract-map.md) when a feature crosses domains, and [glossary.md](references/glossary.md) when defining wire or state vocabulary.

## Implementation workflow

1. Select the product profile: build-time capabilities, runtime feature gates, account eligibility, managed policy, platform support, and current availability. Record each axis independently.
2. Select the entry surface, then route to the relevant broad skill below. Continue into every leaf named by that broad skill whose behavior the feature touches.
3. Model process, session, turn, capability-call, background-task, durable, and presentation state as distinct lifetimes. Do not solve cross-lifetime coordination with accidental global variables.
4. Define canonical semantic events before adapting them to terminal, text, structured, SDK, bridge, remote, or MCP output.
5. Put every side effect behind a validated capability boundary. Treat unknown names, malformed input, unsafe paths, denied permissions, and unavailable isolation as explicit terminal results.
6. Make interruption, retry, cancellation, timeout, partial output, recovery, and cleanup part of each protocol rather than exceptional afterthoughts.
7. Implement the leaf acceptance scenarios and the cross-domain scenarios in [system-contract.md](references/system-contract.md).
8. Use [implementation-conformance-audit](../implementation-conformance-audit/SKILL.md) to verify routing, diagrams, source ownership, contract traceability, and project completeness.

Use [coding-directives](../coding-directives/SKILL.md) to implement or review the AgentX Go runtime and its runtime architecture and conformance profiles.

## Diagram maintenance

Treat diagram layout as tested implementation behavior. Regenerate the two-page domain overviews with `ruby .codex/skills/implementation-architecture/scripts/generate_drawio.rb`, then prove determinism with the same command plus `--check`. After changing a hand-authored diagram, run `ruby .codex/skills/implementation-architecture/scripts/enhance_custom_drawio.rb`; it adds or refreshes the independent-page context panel, semantic connector metadata, and obstacle-safe routes without touching generated overviews. When inference would obscure the real boundary, set concise `customContextStartsWith`, `customContextEndsWith`, or `customContextDefersTo` attributes on that page's `mxGraphModel`; the enhancer treats those values as authoritative context-band overrides, and prose plus the rendered page must verify them. Run the enhancer again with `--check`, render every changed page at exact scale with `ruby .codex/skills/implementation-conformance-audit/scripts/render_drawio_preview.rb INPUT.drawio --output-dir OUTPUT_DIRECTORY --page PAGE`, visually inspect it, and finish with the full implementation audit. Do not manually patch a generated overview or accept XML validity as visual evidence.

## Non-negotiable system invariants

- `ARCH-001` — One semantic session engine owns input normalization, context creation, model turns, tool continuation, limits, and transcript events. Surfaces adapt; they do not redefine.
- `ARCH-002` — The transcript is an append-safe event history. Screen state, ephemeral progress, and convenience caches are not silently model-visible or durable.
- `ARCH-003` — A command, tool, and task are separate contracts: user routing, model-callable capability, and identity-bearing asynchronous work with an explicit persistence profile.
- `ARCH-004` — Every tool-use identifier accepted by a live turn receives exactly one normalized terminal result, including denial, cancellation, sibling failure, malformed input, or fallback discard. A process-crash orphan follows the explicit unresolved-call recovery projection and can never become implicit success or automatic mutation replay.
- `ARCH-005` — Permission is a composed decision with provenance. For the input actually evaluated in a permission pass, a permissive source cannot override a stronger applicable denial unless the policy contract explicitly grants that authority.
- `ARCH-006` — Concurrent work may change completion time and terminal-result publication order inside a declared safe group, but never accepted request order, serialization barriers, identifier pairing, source parentage, ownership, context-modifier order, or deterministic registry precedence.
- `ARCH-007` — Feature code presence does not imply availability. Compile inclusion, gate, identity, policy, platform, configuration, and health remain separate decisions with explicit disabled behavior.
- `ARCH-008` — Durable writes are append-safe or atomic, secrets are excluded or protected, cleanup is idempotent, and recovery never invents successful side effects.
- `ARCH-009` — User cancellation propagates down owned work and produces inspectable terminal evidence; it does not leave anonymous work running.
- `ARCH-010` — Telemetry, updates, suggestions, optional integrations, and decorative UI may degrade without corrupting session correctness.
- `ARCH-011` — Stable prompt material and registry order remain cacheable; volatile context changes only at declared boundaries.
- `ARCH-012` — Unknown or specified behavior must be marked as an explicit compatibility boundary, not guessed and presented as fact.
- `ARCH-013` — A winning approval that supplies modified input is a specified one-shot compatibility decision for that tool-use ID: the selected object reaches execution without another schema, semantic, permission, safety, classifier, sandbox, or prompt pass. An implementation that closes this gap must label and test its bounded reauthorization behavior as an intentional divergence.

## Behavioral domains

Use [implementation-runtime-core](../implementation-runtime-core/SKILL.md) to implement startup, configuration, state lifetimes, prompt construction, the recursive query loop, and model streaming.

Use [implementation-capability-runtime](../implementation-capability-runtime/SKILL.md) to implement tool contracts, concrete capability families, permission and sandbox decisions, and long-lived task execution with its exact crash profile.

Use [implementation-user-surfaces](../implementation-user-surfaces/SKILL.md) to implement terminal rendering, interactive REPL behavior, headless and SDK protocols, and optional user experiences.

Use [implementation-extension-plane](../implementation-extension-plane/SKILL.md) to implement command and input routing, runtime skills and output styles, plugins and hooks, and MCP or LSP integrations.

Use [implementation-continuity](../implementation-continuity/SKILL.md) to implement transcript persistence, branching and recovery, memory, summaries, and context-pressure transformations.

Use [implementation-distributed-runtime](../implementation-distributed-runtime/SKILL.md) to implement bridge and remote sessions, subagents, teams, mailboxes, worktrees, and distributed permission relays.

Use [implementation-operations](../implementation-operations/SKILL.md) to implement authentication and networking, platform/process lifecycle, diagnostics, usage, telemetry, and updates.

Use [implementation-conformance-audit](../implementation-conformance-audit/SKILL.md) to prove every implementation artifact has an owning behavioral contract and every contract has conformance evidence.

## Boundary rules

Keep dependencies pointed inward:

```text
entrypoint or external event
  -> identity, trust, settings, policy, and registries
  -> session state, normalized input, and effective context
  -> model stream and recursive semantic loop
  -> validation, permission, isolation, execution, and normalized result
  -> transcript, task state, usage, and background notifications
  -> terminal or transport-specific projection
```

Presentation may request semantic actions and subscribe to events, but it must not own permission truth, transcript truth, task truth, or model continuation. External transports may relocate work, but they must preserve identifiers, ordering, cancellation, permission provenance, and finality.

## Completion gate

Do not call an architectural change complete until:

- every reachable contract ID has an implementation owner and at least one acceptance scenario;
- every source artifact's reviewed hash is bound to narrower semantic contracts rather than only a broad domain summary, and every stable contract definition appears in the generated contract-to-scenario manifest;
- every registry has deterministic precedence, alias, collision, disablement, and provenance rules;
- every state machine defines initial, terminal, invalid, cancellation, timeout, and recovery transitions;
- every wire record defines framing, validation, forward compatibility, ordering, and error behavior;
- every diagram page is independently understandable, labels behavioral edges, shows its broader product position and owned boundary, and passes the geometry and authority rules in `ARCH-DGM-*`;
- every background operation has identity, durable or retrievable output, cancellation, cleanup, and completion notification;
- fault-injection covers partial model streams, interrupted tool calls, denied permission, corrupt transcript tails, stale tasks, reconnects, and failed optional integrations;
- the audit skill passes without unmapped source artifacts, broken routes, invalid Draw.io files, placeholder text, or missing diagrams.
