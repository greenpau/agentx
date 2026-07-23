---
name: implementation-multi-agent
description: Implement delegated-agent and team execution while preserving agent-definition precedence, tool and permission filtering, explicit background-task durability boundaries, transcript ownership, resume behavior, worktree isolation, team identity, mailboxes, coordinator policy, and result synthesis. Use when implementing or reviewing the Agent capability, local or remote workers, forked context, teammates, teams, coordinator mode, cross-agent messaging, permission relays, orphan cleanup, or asynchronous agent results.
---

# Implementation Multi Agent

## Objective

Implement delegation as explicit work with its own identity, authority, transcript, resource ownership, and terminal result. A child is neither an invisible recursive model call nor a peer with inherited authority: it receives a bounded context and capability set, records an attributable sidechain, and returns a normalized result to an owning session.

Use the [architecture diagram](assets/architecture.drawio) to follow definition resolution, spawn, worker backends, team coordination, task state/evidence lifetimes, and synthesis. Use the [worker support diagram](assets/worker-support.drawio) to separate durable memory and transcript evidence from process-local queues, owned terminal resources, and display-only derivations. The numbered prose contracts remain authoritative for behavior.

## Implementation workflow

1. Load [agent definitions](references/agent-definitions.md) before building registries, prompts, tool pools, MCP contributions, or permission modes.
2. Load [delegation lifecycle](references/delegation-lifecycle.md) before implementing foreground, asynchronous, in-process, forked, worktree, remote, resume, stop, or orphan-recovery paths.
3. Load [teams and coordination](references/teams-coordination.md) before implementing team identity, teammate backends, mailboxes, messages, permission routing, plan approval, shutdown, or coordinator mode.
4. Load [worker support contracts](references/worker-support.md) before implementing persistent agent memory, project snapshots, built-in roles, process-local queues, tmux socket isolation, source-operation summaries, or presentation-only projections.
5. Resolve the effective definition and allowed agent types before allocating resources. Record the winning source and every removal caused by policy, runtime gates, tool rules, or MCP availability.
6. Assign the stable agent/task identifier before creating a worktree, remote session, transcript, pane, mailbox, or process. Use that identifier as the join key; do not derive identity from a mutable display name.
7. Build the child's context, tools, MCP registry, permission policy, hooks, memory, and working directory as explicit inputs. Keep child mutable state separate from the parent store.
8. Register live task ownership and initialize file/transcript evidence before starting work. Stream progress as non-authoritative events; publish terminal live state and finish the configured output-flush attempt before trying to enqueue completion.
9. On stop, crash, or resume, reconcile transcript, tool-use pairs, child processes, MCP clients, hooks, worktree, mailbox state, and task registry. Never synthesize success from partial output.
10. Validate with [acceptance and provenance](references/acceptance-and-provenance.md) and [worker support contracts](references/worker-support.md), especially precedence collisions, stale names, duplicate notifications, orphaned tool calls, lost mail, permission denial, worktree retention, resumed forks, memory-snapshot conflicts, and socket-isolation failure.

## Non-negotiable boundaries

- **MA-DEF-001 — Deterministic definitions.** Agent definitions are validated, source-attributed, and reduced by explicit precedence. The winning definition is immutable for one invocation even if registries refresh concurrently.
- **MA-ID-001 — Stable identity first.** Allocate agent/task identity before any resource bearing that identity. Display name, team member name, process ID, pane ID, remote session ID, and transcript session ID are separate aliases.
- **MA-AUTH-001 — No authority amplification.** Delegation may narrow capabilities but cannot evade parent policy, managed denial, sandbox requirements, or interactive approval. A definition-level permission mode cannot promote a constrained parent mode.
- **MA-TOOL-001 — Composed tool filtering.** Build the candidate pool, apply global child exclusions, backend restrictions, explicit allow rules, explicit deny rules, agent-type restrictions, availability, and policy in a documented order. MCP tools are still subject to authorization even when they pass name filtering.
- **MA-CTX-001 — Explicit context ownership.** A normal child receives a child system context and selected task context. A fork receives a precise parent-prefix projection. Resume never re-adds a prefix that would duplicate tool-use identifiers.
- **MA-TRN-001 — Attributable sidechain.** Child conversation is persisted incrementally with parent linkage and remains distinguishable from the parent transcript. Only normalized progress and terminal result are projected into the parent.
- **MA-TASK-001 — Async lifecycle and notification scope.** Background work is registered before launch, survives ordinary prompt cancellation when specified, supports explicit stop, and records bounded output evidence. A live `notified` compare-and-set permits at most one completion-enqueue attempt while that task-state generation exists. The latch and model queue are process-local; crash recovery can lose or repeat a completion unless an implementation deliberately adds a durable outbox/acknowledgement ledger.
- **MA-CAN-001 — Cancellation is terminal.** Killed, failed, completed, and interrupted work are distinct. Preserve useful partial text on kill, but never relabel it completed.
- **MA-ISO-001 — Isolation is owned.** Worktree, remote placement, process, terminal pane, MCP clients, hooks, queues, and locks have an owner and idempotent cleanup. Modified worktrees are retained and reported rather than silently destroyed.
- **MA-RES-001 — Resume is reconciliation.** Resume validates durable transcript and metadata, removes or repairs incomplete message structures, restores the original execution class, and falls back only according to an explicit compatibility rule.
- **MA-TEAM-001 — Flat team authority.** The lead owns team-level interactive authority and lifecycle. Teammates do not create nested teammates; peer messaging and shared tasks do not transfer policy authority.
- **MA-MBX-001 — Append-backed compatibility mailbox.** Cross-process messages use locked read-modify-atomic-replace, carry sender and timestamp, and distinguish structured control from plain prompt content. A successfully observed append leaves durable file evidence, but the envelope has no stable message ID and the low-level compatibility writer logs and swallows some persistence failures. Consequently a legacy `sent` result can acknowledge only an attempt after an unobserved append failure; it is never durable-append or read acknowledgement, and retries may duplicate. A typed accepted/append/read protocol with stable deduplication identity is an intentional safer divergence.
- **MA-SEC-001 — Secret-safe delegation.** Prompts, mailbox content, process arguments, environment, diagnostics, task output, and child transcripts must not leak credentials or elevate untrusted peer text into system policy.
- **MA-SYN-001 — Evidence-based synthesis.** Parent and coordinator syntheses use only terminal, accepted worker results plus attributable usage and failure state. Progress, stale output, and unacknowledged mailbox messages are not completion evidence.
- **MA-OFF-001 — Disabled behavior is supported.** Build gates, runtime experiments, noninteractive restrictions, unavailable backends, missing MCP servers, and background-disable controls fail clearly or choose only the documented fallback.

## Required implementation artifacts

Produce these artifacts for a standalone implementation:

1. A definition schema and source-precedence reducer with collision, parse-failure, and disabled-definition tests.
2. A tool-filter decision trace identifying each retained and removed capability and the governing contract.
3. State machines for foreground child, background child, team teammate, remote child, fork, resume, stop, and cleanup.
4. A resource-ownership ledger for transcript, task, process, abort controller, MCP clients, hooks, worktree, remote session, pane, mailbox, and notification.
5. Typed task-result, progress, mailbox, permission, plan-approval, shutdown, and coordinator-notification envelopes.
6. Crash-point tests around task registration, process launch, transcript append, terminal status, output persistence, cleanup, and notification.
7. Conformance tests proving that equivalent work has the same permission and result semantics across foreground, background, in-process, pane, worktree, and remote backends.

## Reference routing

- Use [agent definitions](references/agent-definitions.md) to implement definition validation, precedence, enablement, MCP readiness, tool filtering, and permission composition.
- Use [delegation lifecycle](references/delegation-lifecycle.md) to implement Agent requests, backend selection, context construction, fork semantics, worktrees, asynchronous tasks, cancellation, resume, cleanup, and results.
- Use [teams and coordination](references/teams-coordination.md) to implement team configuration, teammate backends, mailboxes, cross-agent messages, permission and plan relays, shutdown, shared tasks, and coordinator behavior.
- Use [worker support contracts](references/worker-support.md) to implement agent memory and snapshots, built-in role composition, process-local queues, tmux socket isolation, operation summaries, colors, and UI-only projections without confusing them with durable domain state.
- Use [acceptance and provenance](references/acceptance-and-provenance.md) to validate each `MA-*` contract and consult non-normative source provenance only during audits.

## Completion standard

Do not claim this domain implemented until every launched worker can be joined, stopped, resumed where durable evidence permits, or explicitly classified after a crash; every resource has an idempotent owner cleanup; every model-visible completion follows an observed terminal live state and completed output-flush attempt; crash loss/duplication windows are tested; and every synthesis can cite the exact child identity, transcript/result, status, and authority decision that produced it.
