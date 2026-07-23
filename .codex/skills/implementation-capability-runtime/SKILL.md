---
name: implementation-capability-runtime
description: Implement the capability boundary that turns model requests into validated, authorized, observable, and recoverable work. Use when tracing a tool call through registry resolution, permissions, hooks, sandboxing, execution, result normalization, or long-lived task creation.
---

# Implementation Capability Runtime

## Preserve the side-effect boundary

Treat every model-requested capability as untrusted protocol input. Resolve and validate it, compose policy and permission decisions, run lifecycle hooks, select isolation, execute under explicit cancellation rules, normalize the result, and record enough evidence for continuation and recovery.

Use the [architecture diagram](assets/architecture.drawio) to inspect the capability boundary and its delegated domains.

Keep these concepts distinct:

- A tool is a model-callable request/result protocol.
- Permission is a composed decision with allow, ask, deny, cancellation, and updated-input outcomes.
- A task is identity-bearing asynchronous work with lifecycle, retrievable output, and a declared persistence and crash profile; not every task field or notification survives restart.
- A catalog describes concrete capability families; it does not replace the common invocation protocol.

Unknown names, invalid schemas, unsafe aliases, unavailable isolation, and malformed results fail closed with explicit terminal tool results. Every accepted tool-use identifier must eventually receive exactly one terminal result, including during cancellation or sibling failure.

## Specialized workflows

- Use [implementation-tool-protocol](../implementation-tool-protocol/SKILL.md) to implement tool descriptors, validation, hooks, permission handoff, execution scheduling, progress, cancellation, and normalized results.
- Use [implementation-tool-catalog](../implementation-tool-catalog/SKILL.md) to implement concrete built-in, environment, web, notebook, browser, and integration tool behavior.
- Use [implementation-permissions-sandbox](../implementation-permissions-sandbox/SKILL.md) to implement permission modes, scoped rules, protected paths, shell analysis, approval updates, and sandbox selection.
- Use [implementation-task-runtime](../implementation-task-runtime/SKILL.md) to implement durable local and remote work, output storage, polling, notification, cancellation, and cleanup.

## Capability acceptance criteria

- A batch containing concurrent-safe and unsafe tools preserves unsafe ordering while exploiting safe overlap.
- A denial, user cancellation, validation error, thrown failure, or sibling cancellation still pairs every accepted request with a result.
- Permission approval cannot weaken managed policy or a more specific denial.
- Restarting the conversational loop never turns surviving background work into anonymous or unowned activity, and never invents recovery for process-local task state.
