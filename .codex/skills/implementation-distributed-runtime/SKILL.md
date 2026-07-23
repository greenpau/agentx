---
name: implementation-distributed-runtime
description: Implement execution that crosses process, worktree, machine, or service boundaries while preserving the shared session, capability, permission, task, and transcript contracts. Use for remote control, bridge sessions, reconnect and replay, subagents, teams, coordinators, mailboxes, worktrees, or distributed cancellation and result synthesis.
---

# Implementation Distributed Runtime

Relocate or delegate semantic work without changing what a message, permission, tool, task, or terminal result means. Open [architecture.drawio](assets/architecture.drawio) to trace identity and result ownership across boundaries.

## Shared distributed invariants

- Give every parent, child, task, transport epoch, permission request, and identity-bearing delivery an explicit identity and owner. The legacy filesystem mailbox envelope is the named exception: it has sender/timestamp/addressing but no per-record identifier, so array position/predicates are compatibility selectors and retry deduplication is unavailable. Adding a stable mailbox delivery identity requires a versioned envelope and is a safer divergence.
- Separate execution placement from semantic authority. A remote worker may execute a capability; it does not invent a new permission or transcript model.
- Make reconnect safe through replayable events, acknowledgement, deduplication, and explicit epoch/session mismatch behavior.
- Propagate cancellation only through declared ownership edges. Background work that intentionally survives a foreground interruption still requires later cancellation and notification paths.
- Finish the task's configured output-flush attempt and expose any retained evidence before publishing completion. A failed/best-effort flush does not become fabricated durable output, and partial output is never terminal proof. Synthesize child results only after all accepted child work has a terminal state.
- Treat peers, remote messages, mailboxes, restored team state, and server metadata as untrusted protocol input.
- Keep worktree or remote filesystem translation explicit and reversible; never silently reinterpret a path under a different root.

## Specialized workflows

Use [implementation-remote-bridge](../implementation-remote-bridge/SKILL.md) to implement environment registration, bridge and remote-control sessions, remote viewers, transport selection, reconnect, replay, delivery acknowledgement, permission/control relay, direct-connect, SSH, or teleport behavior.

Use [implementation-multi-agent](../implementation-multi-agent/SKILL.md) to implement agent-definition precedence, delegation, background agents, teams, coordinators, worker backends, worktree isolation, mailboxes, leader permission routing, resumption, and result synthesis.

## Collaboration boundaries

Use the task-runtime contracts for lifecycle, output, cancellation, same-live-generation notification suppression, and the documented crash loss/duplication windows. The reference has no persistent notification latch or exactly-once completion delivery across restart; a durable outbox and acknowledgement ledger is an intentional safer divergence. Use permission contracts for the actual allow/ask/deny decision. Use transcript contracts for durable parentage and resume. Use headless/SDK contracts for structured control framing. Distributed adapters own transport, placement, identity translation, and replay only.

## Acceptance gate

Test normal delivery, duplicate replay, disconnect before acknowledgement, stale epoch, late permission response, parent cancellation, intentionally surviving child work, orphan recovery, worker crash, worktree conflict, and cleanup during shutdown. Every synchronous tool-use identifier and finite owned operation must end with inspectable terminal evidence. A deliberately handed-off task may remain nonterminal only after its identity, owner, lifecycle/cancel path, and retrievable evidence are registered; a crash-lost mailbox/control identifier is classified from surviving evidence without fabricating a terminal response.
