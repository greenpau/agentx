---
name: implementation-transcript-recovery
description: Implement the append-only session transcript and recovery protocol, including message graph links, sidechains, metadata events, deduplication, write queues, branches, tombstones, compact and snip relinking, resume, fork, worktree restoration, and compatibility repair. Use when persisting or rebuilding a conversation after interruption.
---

# Implementation Transcript Recovery

## Workflow

1. Resolve the frozen application-home and workspace identities, acquire the
   `TX-011` session directory, and persist accepted semantic events
   incrementally in an append-safe format with stable message and session
   identities.
2. Preserve the transcript as a graph: distinguish sequential parents, logical compact parents, streamed assistant siblings, tool-result source parents, branches, and sidechains.
3. Flush and materialize files under explicit durability, security, deduplication, and remote-ingress rules.
4. Load defensively, repair legacy and parallel-tool topology, choose the intended live leaf, and restore associated snapshots and metadata.
5. Apply resume or fork ownership rules atomically before accepting a new turn.

Read [the complete transcript and recovery contract](references/transcript-recovery-contract.md) before implementing or auditing this domain. Use the [architecture diagram](assets/architecture.drawio) to inspect the event graph and recovery transformations. For the exact shared JSON-lines parser, malformed-record recovery, and 100 MiB tail-read behavior used beneath transcript loading, read the supporting [portable JSON-lines contracts](../implementation-platform-lifecycle/references/portable-data-primitives.md#json-lines-parsing-and-tail-reads) (`PRIM-030` through `PRIM-035`); this is a supporting contract link, not an actionable skill route.

## Boundaries

Own authoritative session history and restore semantics. Do not persist replace-in-place progress as conversation history. Defer active-context reduction to memory/compaction and presentation-only ordering to the relevant surface.

## Completion check

- Preserve all `TX-*` contracts in the reference.
- Round-trip normal turns, parallel tools, legacy progress, rewind branches, compact preserved segments, snips, tombstones, sidechains, non-fork resume, fork resume, and missing worktrees.
- Confirm a resumed model sees a coherent, paired message sequence and not merely the lines that happened to be last on disk.
