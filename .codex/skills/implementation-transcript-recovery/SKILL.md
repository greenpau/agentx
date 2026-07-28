---
name: implementation-transcript-recovery
description: Implement the append-only session transcript and recovery protocol, including native session inventory and deletion, message graph links, sidechains, metadata events, deduplication, write queues, branches, tombstones, compact and snip relinking, resume, fork, worktree restoration, and compatibility repair. Use when persisting, enumerating, deleting, or rebuilding a conversation after interruption.
---

# Implementation Transcript Recovery

## Workflow

1. Resolve the frozen application-home and workspace identities. For a
   conversation, acquire the `TX-011` session directory and persist accepted
   semantic events incrementally; for inventory, open only an existing
   workspace partition without materializing it.
2. Preserve the transcript as a graph: distinguish sequential parents, logical compact parents, streamed assistant siblings, tool-result source parents, branches, and sidechains.
3. Flush and materialize files under explicit durability, security, deduplication, and remote-ingress rules.
4. Load defensively, repair legacy and parallel-tool topology, choose the intended live leaf, and restore associated snapshots and metadata.
5. Use the authoritative native-session inventory for resume, continue, fork,
   creation, and provider-free deletion eligibility.
6. Verify every selected attachment manifest/blob before resume, and copy
   immutable referenced media into a fork's destination store before
   publishing it.
7. Apply resume or fork ownership rules atomically before accepting a new turn,
   or detach and clean one deletion target through its recoverable state
   machine.

Read [the complete transcript and recovery contract](references/transcript-recovery-contract.md) before implementing or auditing this domain. Use the [architecture diagram](assets/architecture.drawio) to inspect the event graph and recovery transformations, and the [native session-management diagram](assets/session-management.drawio) to inspect authoritative inventory, selection guards, atomic deletion detach, and cleanup recovery. For the exact shared JSON-lines parser, malformed-record recovery, and 100 MiB tail-read behavior used beneath transcript loading, read the supporting [portable JSON-lines contracts](../implementation-platform-lifecycle/references/portable-data-primitives.md#json-lines-parsing-and-tail-reads) (`PRIM-030` through `PRIM-035`); this is a supporting contract link, not an actionable skill route.

## Boundaries

Own authoritative session history, native-session selection, local-store
deletion state, and restore semantics. Do not persist replace-in-place progress
as conversation history. Defer active-context reduction to memory/compaction,
CLI/JSON presentation to the relevant surface, and deletion of remote copies or
presentation caches to their owning products.

## Completion check

- Preserve all `TX-*` contracts in the reference.
- Round-trip normal turns, parallel tools, legacy progress, rewind branches,
  compact preserved segments, snips, tombstones, sidechains, non-fork resume,
  fork resume, missing worktrees, content-bound bounded inventory, atomic
  receipt publication, partial-stage recovery, and crash-retry deletion.
- Confirm a resumed model sees a coherent, paired message sequence and not merely the lines that happened to be last on disk.
- Confirm resume, continue, fork, and explicit creation cannot select an
  incomplete fork, deletion intent, detached cleanup stage, or pending deletion
  receipt, while a completed receipt does not reserve a genuinely new
  generation.
- Confirm legacy text resumes without migration loss; native transcripts expose
  manifests only, retain referenced blobs, and fail attributably on missing or
  tampered durable media.
