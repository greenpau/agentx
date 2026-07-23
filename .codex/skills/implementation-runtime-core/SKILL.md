---
name: implementation-runtime-core
description: Implement the shared session runtime from process startup through context composition and the recursive model loop. Use when defining runtime ownership, tracing a turn across bootstrap, state, and query boundaries, or deciding whether a core behavior belongs to startup/settings, live state/context, or model orchestration.
---

# Implementation Runtime Core

## Implement the shared engine

Keep process bootstrap facts, mutable session state, durable conversation state, and background work as separate lifetimes. Build one semantic engine that all interactive, headless, SDK, bridge, and remote adapters can drive without redefining messages, turns, limits, tools, or terminal outcomes.

Use the [architecture diagram](assets/architecture.drawio) to orient the core ownership and dependency flow.

Preserve this dependency direction:

```text
entrypoint and policy
  -> session/bootstrap state and registries
  -> normalized input and effective context
  -> recursive query/model loop
  -> capability and continuity boundaries
  -> semantic events consumed by presentation adapters
```

Do not let presentation state become authoritative conversation state. Do not let a transport-specific retry, permission response, or display event invent a different meaning for a shared operation.

## Specialized workflows

- Use [implementation-startup-settings](../implementation-startup-settings/SKILL.md) to implement entrypoint selection, early initialization, trust, settings precedence, migrations, and managed policy.
- Use [implementation-state-context](../implementation-state-context/SKILL.md) to implement bootstrap lifetimes, the observable application store, state reactions, working-directory identity, and effective prompt/context assembly.
- Use [implementation-query-model](../implementation-query-model/SKILL.md) to implement input submission, the recursive query state machine, message normalization, model streaming, retries, continuation, limits, and terminal results.

## Core acceptance criteria

- A session restored after interruption reaches the same semantic state regardless of presentation surface.
- A UI-only progress update never enters model context or durable conversation history unless explicitly translated into a message.
- A new surface consumes shared events and permission contracts instead of forking the conversation engine.
- Cancellation, budget exhaustion, model failure, and successful completion each produce one explicit terminal outcome and leave recoverable evidence.
