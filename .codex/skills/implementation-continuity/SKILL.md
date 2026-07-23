---
name: implementation-continuity
description: Implement conversation continuity across incremental persistence, resume, branching, rewind, compaction, context pressure, and crash recovery. Use when defining what survives a turn or process, rebuilding a session from durable events, or reducing context without losing authoritative history.
---

# Implementation Continuity

## Preserve authoritative history

Treat the durable transcript as an append-safe event graph, not a screen buffer or replaceable message array. Keep authoritative history distinct from projections such as compact summaries, cached prompt edits, file-history snapshots, memory extracts, and UI-only progress.

Use the [architecture diagram](assets/architecture.drawio) to inspect the persistence, recovery, and context-reduction boundaries.

Continuity work must preserve message identity, parent relationships, tool-use/result pairing, branch selection, metadata ownership, background-task references, and explicit compaction boundaries. A crash, disconnect, partial stream, or interrupted write must either resume coherently or explain why recovery is impossible.

## Specialized workflows

- Use [implementation-transcript-recovery](../implementation-transcript-recovery/SKILL.md) to implement the append-only transcript protocol, message graph, sidechains, metadata, branching, tombstones, resume, fork, and consistency repair.
- Use [implementation-memory-compaction](../implementation-memory-compaction/SKILL.md) to implement token pressure, summaries, preserved segments, session memory, microcompaction, context collapse, cleanup, and post-compaction reinjection.

## Continuity acceptance criteria

- Killing a process after accepted user input but before model output still leaves the user turn resumable.
- Rewound and compacted sessions load the intended live branch without resurrecting removed context.
- Derived summaries reduce active context without destroying the authoritative event history or tool pairing evidence.
- Resume restores session identity and state without accidentally adopting another session's cwd, metadata, or replacement records.
