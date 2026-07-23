---
name: implementation-tool-protocol
description: Implement the common protocol for model-callable tools, including descriptors, aliases, schema and semantic validation, hook ordering, permission composition, scheduling, progress, cancellation, output persistence, and result pairing. Use when implementing or auditing the execution framework shared by all tools.
---

# Implementation Tool Protocol

## Workflow

1. Define a complete language-neutral tool descriptor and session-scoped registry.
2. Preserve the validation, hook, permission, execution, mapping, and cleanup order exactly.
3. Partition requests by concurrency and shared-state safety while retaining observable ordering guarantees.
4. Propagate cancellation to the correct tool scope and synthesize results for every unfinished accepted request.
5. Normalize and, when necessary, persist results without mutating model-originated input or prompt-cache identity.

Read [the complete tool protocol contract](references/tool-protocol-contract.md) before implementing or auditing this domain. Use the [architecture diagram](assets/architecture.drawio) to inspect the execution sequence and scheduler barriers.

## Boundaries

Own the shared request/result protocol and orchestration pipeline. Defer concrete tool semantics to the tool catalog, policy evaluation to permissions/sandbox, and work that outlives the invocation to the task runtime.

## Completion check

- Preserve all `TP-*` contracts in the reference.
- Test unknown tools, aliases, malformed input, hook-updated input, configured denial, concurrent completion, unsafe barriers, interruption, sibling failure, oversized results, and post-hook failure.
- Confirm every accepted tool-use ID has one and only one terminal result.
