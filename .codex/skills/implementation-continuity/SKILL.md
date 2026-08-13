---
name: implementation-continuity
description: Implement conversation continuity across incremental persistence, resume, branching, rewind, compaction, context pressure, and crash recovery. Use when defining what survives a turn or process, rebuilding a session from durable events, or reducing context without losing authoritative history.
---

# Implementation Continuity

## Preserve authoritative history

Treat the durable transcript as an append-safe event graph, not a screen buffer or replaceable message array. Keep authoritative history distinct from projections such as compact summaries, cached prompt edits, file-history snapshots, memory extracts, and UI-only progress.

Use the [architecture diagram](assets/architecture.drawio) to inspect the persistence, recovery, and context-reduction boundaries.

Continuity work must preserve message identity, parent relationships, tool-use/result pairing, branch selection, metadata ownership, background-task references, and explicit compaction boundaries. A crash, disconnect, partial stream, or interrupted write must either resume coherently or explain why recovery is impossible.

Attachment continuity additionally preserves the complete immutable manifest
and its session-owned blob independently of the original import path. Resume
revalidates every referenced blob before model projection. Fork copies verified
content into the destination store under the same attachment and
content-addressed storage identities while the source session is locked; it
does not share a mutable pathname or depend on the source file. Missing or
tampered media blocks resume/fork attribution instead of becoming a
placeholder asserted as user content.

Provider continuity is equally explicit. Repeat the selected provider ID,
provider type, logical model, and opaque noncredential route fingerprint on
every durable record, then validate the complete tuple before replay, fork
publication, attachment restoration, or provider I/O. Exclude the API key from
the fingerprint so key rotation alone remains resumable; include the normalized
endpoint route, deployment, and exact API selector so route drift fails closed.
A provider-ID mismatch directs the user to the recorded `--provider ID`.
`--continue` selects the workspace's latest eligible session before this gate;
it does not silently search for a session matching the current provider.

## Specialized workflows

- Use [implementation-transcript-recovery](../implementation-transcript-recovery/SKILL.md) to implement the append-only transcript protocol, native session inventory and deletion, message graph, sidechains, metadata, branching, tombstones, resume, fork, and consistency repair.
- Use [implementation-memory-compaction](../implementation-memory-compaction/SKILL.md) to implement token pressure, summaries, preserved segments, session memory, microcompaction, context collapse, cleanup, and post-compaction reinjection.

## Continuity acceptance criteria

- Killing a process after accepted user input but before model output still leaves the user turn resumable.
- Rewound and compacted sessions load the intended live branch without resurrecting removed context.
- Derived summaries reduce active context without destroying the authoritative event history or tool pairing evidence.
- Resume restores session identity and state without accidentally adopting another session's cwd, metadata, or replacement records.
- Native deletion continuity survives interrupted intent, receipt publication,
  detach, descriptor-rooted cleanup, and parent sync without making a pending
  ID selectable or letting an old receipt target a recreated generation.
- Attachment-only and mixed turns resume with their exact ordered manifests;
  forks contain independent verified blobs; incomplete uploads and committed
  imports absent from durable history are collected without removing blobs
  still referenced by any durable event in that session.
- Rotating only a profile's API key preserves resume/fork eligibility;
  provider/type/model/endpoint-route/deployment/API-selector drift, mixed
  bindings, and unbound legacy history fail before replay or provider I/O.
