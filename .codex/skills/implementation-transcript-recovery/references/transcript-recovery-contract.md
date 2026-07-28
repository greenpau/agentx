# Transcript and Recovery Implementation Contract

This document defines the authoritative append-only session history and how a coherent live conversation is implemented from it.

## Contents

- [Event and identity model](#event-and-identity-model)
- [Write protocol and durability](#write-protocol-and-durability)
- [Graph construction and branching](#graph-construction-and-branching)
- [Load, filtering, and repair](#load-filtering-and-repair)
- [Resume, continue, and fork](#resume-continue-and-fork)
- [Native session inventory and deletion](#native-session-inventory-and-deletion)
- [Remote persistence and disabled behavior](#remote-persistence-and-disabled-behavior)
- [Limits and constants](#limits-and-constants)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Event and identity model

**TX-001 — Authoritative form.** Persist a session as an append-safe event stream, normally one JSON object per line. The file is an event graph with metadata, not a serialized screen and not necessarily the exact active message array.

**TX-002 — Stable identities.** Every transcript message has a globally unique message UUID, timestamp, session ID, and zero or one parent UUID. Session IDs are stable opaque identifiers, native directory IDs obey `TX-068`, and an active conversation changes identity only through the atomic session-switch contract.

**TX-003 — Transcript-message types.** Only semantic user, assistant, and
system records participate as transcript messages. A version-2 user record may
contain ordered native attachment blocks inside its typed message; there is no
separate native attachment transcript-message type. Legacy/meta attachment
records retain their compatibility meaning where explicitly supported. For a
model-backed turn, the accepted durable user message carrying its turn identity
and timestamp is the authoritative start marker. Progress is ephemeral
presentation state and neither persists in new transcripts nor advances the
parent cursor.

**TX-004 — Transcript record.** A message record can contain:

- `parentUuid` and optional `logicalParentUuid`;
- `isSidechain`, optional team/agent name, and optional agent ID;
- optional prompt ID on user input;
- the full semantic message, including message UUID/timestamp;
- session stamp: session ID, cwd, client entrypoint, user type, product version, source-control branch, and optional plan slug.

Session-stamp fields are assigned by the destination session after copied message fields so resume/fork cannot retain the source session's ownership metadata.

**TX-005 — Metadata and lifecycle events.** Support append-only events for at least:

- session summary, custom/AI title, last prompt, task summary, and tag;
- agent name, color, selected agent, coordinator/normal mode, worktree state, and linked pull request;
- file-history and attribution snapshots;
- queued-message lifecycle operations;
- speculation acceptance;
- native attachment provider-rejection quarantine, keyed by stable ID and
  digest;
- tool-result content-replacement decisions;
- ordered context-collapse commits and last-wins context-collapse snapshot;
- completed provider-usage records and, when terminal append and flush succeed,
  exactly one terminal turn-result for each finalized model-backed turn.

Metadata may be session-scoped rather than parent-linked. Usage and terminal
turn results carry the owning turn ID, remain durable but model-hidden, and are
session authority independent of whether DEBUG diagnostics are enabled. A
terminal append or flush failure is surfaced as a transcript-finalization error,
not silently described as durable completion.

**TX-006 — Chain versus display.** Parent UUID expresses implementation topology. UI order and API normalization may merge or reorder related records. Never derive authoritative parent relationships from the current rendered order alone.

**TX-007 — Sidechains.** A sidechain belongs to an agent or fork and may be stored in a dedicated transcript. It retains agent identity and can inherit parent context. The main session's UUID set and a local sidechain's UUID set have different deduplication rules.

**TX-008 — Worktree state.** Persist worktree state as a tri-state value: event never written/unknown, explicit `null` meaning exited, or an active worktree descriptor. This distinguishes a clean exit from a crash while inside a worktree.

**TX-009 — Native attachment event version.** Version-2 user events may carry
the closed version-1 typed user-content union and attachment manifests. Persist
stable ID, kind, bounded display name, verified MIME, normalized decoded size,
SHA-256 digest, and immutable storage ID only—never bytes, base64, source path,
runtime path, or provider request data. Version-1 legacy text events remain
loadable; a version-1 event that attempts attachment content is invalid rather
than being interpreted as text.

## Write protocol and durability

**TX-010 — Lazy materialization.** Do not create a fresh session file for metadata or progress alone. Buffer such events while the file pointer is absent. Materialize the file when the first user or assistant message is accepted, append cached startup metadata, then flush buffered events in order.

**TX-011 — Session path.** In the standalone Go profile, place each persistent
session at
`<application-home>/sessions/<workspace-hash>/<session-id>/` and its
authoritative event file at the literal `transcript.jsonl` child. Derive one
deterministic bounded workspace hash from the normalized absolute selected
workspace, then freeze it with the selected application-home path before resume
or creation. `--continue` searches only that workspace directory; explicit
resume and fork cannot escape it through a session identifier or caller-supplied
path. Acquire and verify a direct session directory at layout boundaries.
Individual transcript, lock, task, and result stores retain only the identities
their owning contracts specify; this placement rule does not claim
subtree-wide descriptor rooting across every later operation. The pre-CLI
bootstrap creates only the application home and `sessions/`;
`--no-session-persistence` may use an owned temporary session directory but
must not create a workspace-hash or session-ID child. A persistent `--bare`
session may create its session child but must not create or load the separate
project-memory tree. Do not silently scan, copy, or delete legacy session roots;
an upgrade migration is an explicit, backup-first user operation that preserves
directory ownership and file permissions.

**TX-012 — Local queue.** Maintain a FIFO append queue per target file. Default scheduling delay is 100 milliseconds; remote/internal-event operation may lower it to 10 milliseconds. Drain each file in order and split a batch before accumulated serialized content reaches 100 MiB.

**TX-013 — Filesystem modes.** Append files with owner read/write mode `0600`. If a parent directory is absent or inaccessible under an NFS-like error, create it recursively with mode `0700` and retry append.

**TX-014 — Flush.** Flush cancels a pending timer, awaits the active drain, drains newly queued records, then waits for nonqueue mutations such as tombstone rewrites. Shutdown flushes before re-appending metadata and releasing resources.

**TX-015 — Persistence suppression.** Suppress all transcript writes when any authoritative control requires it: tests without an explicit test opt-in, cleanup retention set to zero, explicit session-persistence disable, or prompt-history suppression. Apply the same guard to materialization and individual appends so metadata cannot create a forbidden file.

**TX-016 — Main-chain deduplication.** Maintain the main session's known UUID set. Ordinary message records with existing UUIDs are not appended again. Metadata events whose semantics are append/last-wins remain appendable independently.

**TX-017 — Sidechain deduplication.** Local agent sidechains may write inherited message UUIDs even when the main transcript already contains them, because the separate sidechain must be independently resumable. Do not add those UUIDs to the main-file known set. Remote persistence uses one session last-UUID chain and must not resend duplicates.

**TX-018 — Incremental record filtering.** Before recording a growing message slice, remove already-recorded UUIDs. Already-recorded chain participants advance the starting parent only while they form a prefix before the first new message. Once a new message appears, later recorded messages do not move the cursor. This lets compact boundary/summary records precede deduplicated preserved messages without making future messages chain back into precompact history.

**TX-019 — Parent cursor.** Start with an explicit parent hint or null. For
each new chain participant, use the cursor as parent and then advance the cursor
to that message UUID. A typed user message advances once regardless of how many
native attachment blocks it contains; supported legacy/meta attachment records
and chain-participating system messages advance independently. Progress never
does.

**TX-020 — Tool-result parent.** A user-role tool-result record with source assistant UUID uses that assistant as its effective parent rather than the sequential cursor. This preserves association when parallel streamed assistant blocks exist.

**TX-021 — Compact boundary parent.** A compact boundary writes `parentUuid=null` to form a new load root and stores the former cursor in `logicalParentUuid` for provenance/forking. It then becomes the cursor for subsequent compact summary and continuation messages.

**TX-022 — Last prompt.** After a non-sidechain chain write, derive the first meaningful user text from that slice, ignoring synthetic XML-like notifications and interruption markers. Flatten newlines and retain at most 200 visible characters plus an ellipsis. Cache it for resume-list metadata.

**TX-023 — Metadata tail refresh.** Re-append cached last prompt, title, tag, agent, mode, worktree, and pull-request metadata during compaction and shutdown so tail-only session listing can recover it. Before re-appending title/tag, absorb a newer external writer's value from the last 64 KiB. An explicit empty external value clears the cached field.

**TX-024 — Resumed-file adoption.** Non-fork resume explicitly points the writer at the existing loaded file and immediately re-appends restored metadata. This permits rename/tag persistence even if the user exits before sending another message.

**TX-025 — Tombstone removal.** Removing an orphaned UUID first searches the last 64 KiB for the full top-level UUID field, truncates at that line, and rewrites following tail bytes. If absent, rewrite the full file only when file size is at most 50 MiB. Preserve malformed lines. Above the limit, log and leave the orphan rather than risk out-of-memory failure.

**TX-026 — Enqueued is not flushed.** Treat storage submission, ordered queue admission, local append completion, remote append completion, and explicit flush as different states. A normally awaited transcript call may return after queue admission while the delayed local writer still owns the bytes. Only the explicit flush contract drains the queue; an execution barrier or visible result is not equivalent evidence.

**TX-027 — Whole-record credential validation.** When session composition supplies a credential validator, marshal each fully normalized event, append its exact JSONL newline in memory, and validate that complete physical record against the bounded session/provider exact-literal union before queueing or appending any byte. Apply the same validator during recovery to every successfully decoded exact physical record before schema diagnostics or indexing; include the existing LF, and for a valid unterminated tail also validate the LF-framed form that the next append would create. After normalization, validate the complete recovered `Snapshot`, including derived diagnostics, before returning it. Inspect raw, decoded, canonical, duplicate-member, and final framed spellings according to `AUTH-014`; individually safe fields, diagnostic identities, or a safe body suffix plus the line terminator must not reconstruct a credential. Isolate malformed records without copying their raw bytes into diagnostics. This seam is validation-only: it may reject but never rewrite event identity, sequence, parentage, or content behind the in-memory indexes. Rejection leaves the durable file, accepted/resolved indexes, sequence, and parent cursor unchanged. Contain validator panics as fixed validation failures. A profile with no configured credential material retains byte-identical ordinary encoding.

**TX-028 — Clock callback boundary.** Invoke a configured transcript clock before acquiring the append ownership gate, contain panic and zero-time results with the host wall clock, and guard recursive clock entry without calling the callback again. Batch stamping works on a detached event slice. No clock callback may observe partially mutated append indexes or deadlock by reentering `Append`; preparation under the gate has only a callback-free host-clock fallback.

**TX-029 — Session-owned attachment store.** Keep attachment state below the
native session directory in owner-private `attachments/blobs`,
`attachments/manifests`, and `attachments/uploads` children. Blobs are
immutable and content-addressed by normalized SHA-256; manifests bind stable
attachment IDs to verified blobs. Cap unique committed blob storage at
536,870,912 bytes. Opening a store removes abandoned temporary uploads; after
the transcript snapshot is available, the runtime invokes collection with its
durable attachment-reference set. Collection removes only unreferenced
committed artifacts, and a referenced blob is never evicted merely to admit a
new import.

## Graph construction and branching

**TX-030 — Append topology.** Parents normally appear earlier in the file, but the live transcript can be a DAG because parallel assistant content blocks and tool results form sibling branches. Rewind and retry leave older branches physically present.

**TX-031 — Leaf candidates.** Compute graph leaves from parent references.
Only the nearest eligible user or assistant at a terminal path may anchor a
resumable conversation. An attachment-only typed user message is an eligible
user leaf. Detached manifests, blobs, uploads, legacy/meta attachments without
an eligible conversation message, and session-scoped system metadata do not
become conversation leaves by themselves.

**TX-032 — Active leaf.** For ordinary resume, choose the most recent valid main-chain leaf by timestamp. Analytics/history views that explicitly request every branch may retain all leaves and choose by their own documented policy.

**TX-033 — Parent walk.** Build the initial conversation by walking parent UUID from leaf to root, recording seen UUIDs to detect cycles, then reverse the result. A missing parent terminates with the coherent reachable suffix rather than inventing a record.

**TX-034 — Cycle handling.** On repeated UUID, stop and return the partial acyclic chain, log an error/diagnostic event, and never loop indefinitely.

**TX-035 — Parallel assistant groups.** Streaming may emit one assistant record per completed content block. Siblings from one API response share an API message ID but have distinct UUIDs. Index these groups by API message ID and tool-result records by their source assistant parent.

**TX-036 — Parallel result recovery.** After the parent walk, for each on-chain assistant group, find off-chain sibling assistant blocks and their off-chain tool results. Sort recorded siblings and results by timestamp, preserving file order on ties, and splice them after the last on-chain group member so assistant blocks precede results and normalization can merge them. This restores recorded completion order; it does not claim that result order equals accepted tool-use order. Mark inserted UUIDs seen.

**TX-037 — Branch consistency checkpoint.** A turn-duration system event may record the in-session message count. On resume, compare its position in the implemented chain and emit a diagnostic delta: positive means resurrection, negative means truncation, zero means round-trip consistency.

## Load, filtering, and repair

**TX-040 — Defensive parse.** Parse complete JSONL records, tolerate an incomplete/crash-truncated final line as the parser contract permits, and isolate unreadable/malformed entries rather than corrupting already parsed state. An unreadable/nonexistent session yields no records. Apply the supporting [portable JSON-lines and 100 MiB tail-read contracts](../../implementation-platform-lifecycle/references/portable-data-primitives.md#json-lines-parsing-and-tail-reads) (`PRIM-030` through `PRIM-035`) wherever this domain delegates to the shared parser; this reference link does not transfer transcript ownership to the platform skill.

**TX-041 — Large-file streaming load.** Above 5 MiB, use a forward reader with approximately 1 MiB chunks. Bound peak memory by the surviving output rather than whole file size. A disable switch can force full loading for diagnosis/compatibility.

**TX-042 — Precompact skip.** While scanning, a real compact-boundary record without a preserved segment discards accumulated preboundary message bytes. A boundary marker appearing inside user content is not a boundary; parse and verify the record type/subtype. A boundary with a preserved segment does not truncate because preserved records may physically precede it.

**TX-043 — Attribution compaction.** During streaming load, retain only the most recent attribution snapshot and append it to the surviving buffer; restore consumes last-wins state and does not require original position.

**TX-044 — Preboundary metadata.** If message bytes are skipped, separately scan the discarded range for recognized small session-scoped metadata records. Use bounded marker matching and cap an incomplete carry at 64 KiB so a huge message line cannot cause quadratic growth. Later metadata overwrites earlier values.

**TX-045 — Dead-branch prefilter.** For large files without preserved segments and when all leaves are not requested, a byte-level index may walk the latest main chain before JSON parsing. Preserve all metadata records. Apply the optimization only when it eliminates at least half the buffer by bytes; otherwise parsing the full buffer is cheaper and semantically equivalent.

**TX-046 — Legacy progress bridge.** Old transcripts may contain progress UUIDs in the parent chain. Build a bridge from each progress UUID to its nearest non-progress ancestor, collapse consecutive progress entries, skip the progress record, and rewrite later parents through the bridge.

**TX-047 — Snapshot restoration.** Rebuild file-history snapshots along the selected conversation, replacing an earlier snapshot when an update event names the same message. Rebuild attribution by its last-wins contract. Restore context-collapse commits in append order and the latest snapshot, resetting this state at compact boundaries.

**TX-048 — Content replacement restoration.** Collect main replacement records by session ID and sidechain records by agent ID. Preserve append order. These records contain the exact model-visible replacement string and feed the tool-result budget implementation. Because aggregate replacement records are best-effort optimization metadata, a crash may leave a tool result without its record; restore that result as seen and inline rather than failing resume.

**TX-049 — Preserved-segment metadata.** A compact boundary may name `headUuid`, `anchorUuid`, and `tailUuid` for messages retained with their original on-disk parents. Only the final boundary's segment is live; a later boundary without a segment makes prior segment metadata stale.

**TX-050 — Preserved-segment validation.** Before mutation, walk from tail backward to head through recorded parents without cycles. If head, tail, anchor, or an intermediate link is missing, leave the full loaded history unpruned and log diagnostics. Do not partially relink.

**TX-051 — Preserved-segment relink.** For a valid live segment:

1. change the preserved head's parent to the anchor;
2. redirect other children of the anchor to the preserved tail;
3. zero stale input/output/cache usage on preserved assistant records;
4. prune physical records before the absolute last compact boundary except preserved UUIDs.

For a stale segment followed by a plain boundary, prune before the absolute last boundary without preserving the stale segment.

**TX-052 — Snip replay.** Snip boundaries record removed UUIDs. On load, delete those records. For any surviving record whose parent was removed, walk backward through deleted-parent links to the first surviving ancestor or null and rewrite the parent. Use path compression or equivalent bounded traversal. Older snips lacking removed UUIDs load their original history.

**TX-053 — Metadata conflict semantics.** Titles/tags/mode/worktree and other session metadata are last-wins by session. Context-collapse commits remain ordered, not map-deduplicated. Context-collapse snapshot is last-wins.

**TX-054 — Raw read guard.** Callers that insist on loading a raw transcript into memory must reject or avoid doing so above 50 MiB. The optimized streaming resume loader is separate and may process much larger files.

## Resume, continue, and fork

**TX-060 — Restore ordering.** Before the next query:

1. load and repair the target graph and metadata;
2. select the conversation leaf/chain;
3. decide fork versus adoption;
4. switch session identity and owner directory atomically when adopting;
5. open the owning attachment store and verify every selected manifest/blob
   identity, digest, and size;
6. restore cost and metadata;
7. restore cwd/worktree and invalidate cwd-sensitive caches;
8. restore file history, attribution, collapse state, attachment quarantine,
   todo compatibility, and agent selection;
9. reconcile unresolved tool use and replacement state;
10. adopt or materialize the correct destination file.

**TX-061 — Non-fork resume.** Reuse the loaded session ID unless an explicit override is supplied. Use the transcript file's directory as the owning project directory for cross-project/worktree resume. Reset any stale fresh-session file pointer, restore cost, adopt the existing file, and reuse its verified immutable attachment store without consulting an original import path.

**TX-062 — Fork resume.** Keep the new startup session ID. Copy selected source messages into a new transcript, restamping destination session fields. Do not take ownership of the source session's worktree. Seed loaded content-replacement records under the new session because they are separate metadata events and will not be recreated by copying messages. Under the source session lock, verify and copy each referenced immutable blob and manifest into the destination store while preserving attachment/storage identities; the fork does not share a mutable path with the source. Publish neither a usable destination transcript nor success until this copy is complete.

**TX-063 — Agent restoration.** An explicit command-line agent wins. Otherwise restore the saved agent when it remains available, update main-thread agent identity, and apply its model only when the user did not supply another override. If unavailable, clear stale agent bootstrap state and continue with default behavior.

**TX-064 — Mode restoration.** Match coordinator/normal mode to the loaded session. If compatibility requires a switch, refresh built-in agent definitions for the new mode and merge command-line agent definitions again. Emit a warning message when the transition is user-relevant.

**TX-065 — Worktree precedence.** A fresh startup `--worktree` takes precedence over the transcript. Otherwise, if the loaded state says the process was inside a worktree, change directory there using the actual directory change as the race-safe existence check. If missing, persist explicit exited state. Keep project root stable because the transcript does not prove how the worktree was entered.

**TX-066 — Mid-session resume cleanup.** Before switching away from a previously restored worktree, clear its live worktree marker and attempt to return to its original cwd. Invalidate memory, prompt-section, and plan-directory caches whether or not the directory change succeeds.

**TX-067 — Unresolved side-effect uncertainty.** Resume never reruns an unresolved mutating call and never infers whether it succeeded from current filesystem state. In the specified compatibility behavior, omit an assistant message whose tool-use blocks are all unresolved from the resumed live conversation. If a retained assistant group contains both resolved and unresolved IDs, provider-request normalization may insert an in-memory synthetic error result for missing IDs; strict pairing mode fails instead. Neither projection appends proof of the external effect, and the raw on-disk event remains audit evidence.

## Native session inventory and deletion

**TX-068 — Authoritative workspace inventory and selection.** Use one
runtime-owned service for native inventory, latest-session selection, deletion,
and the eligibility decisions shared by resume, continue, fork, and explicit
session creation. Give that service only the frozen application-home
`sessions/` owner and normalized absolute selected workspace from `TX-011`; it
derives the compatibility workspace hash and never accepts an application-home
path, workspace hash, session path, or transcript path from a caller.

Inventory is provider-free and read-only: do not create a semantic session,
query engine, transcript, workspace partition, project memory, or model/provider
connection. Preserve the ordinary frozen application-home and authentication-
presence bootstrap, then stop before semantic initialization. A missing
selected-workspace partition returns an empty inventory without creating it.
Enumerate only direct children of
`sessions/<selected-workspace-hash>/`, enforce explicit workspace, deletion-
stage, and deletion-receipt entry bounds, and never silently truncate.
Content-bind each revision with a bounded digest of the stable transcript
snapshot. Cap one complete digest pass at 2 GiB and the fixed set of repeated
validation passes in one management operation at 8 GiB; exceeding either bound
fails the operation instead of omitting candidates. Use bounded pages derived
from a complete scan; bind each continuation token to the full inventory
generation, require its exact versioned encoded length and a nonterminal
positive offset, so a changed or malformed token returns the stable `stale`
outcome.
Sort by the internal stable `transcript.jsonl` modification time descending and
then session ID ascending. Emit canonical UTC RFC3339Nano `updated_at` only
when the filesystem time has a representable four-digit RFC3339 year; omission
does not change internal ordering.

Treat only ASCII `[A-Za-z0-9_-]{1,128}` names as native session IDs. Ignore
other ordinary names, including Unicode, overlong, traversal-like, and encoded
lookalikes, but fail closed on malformed names or unsafe entries in the
reserved deletion-stage and deletion-receipt namespaces. A resumable candidate
requires a direct owner-private session
directory, a nonempty direct single-link `transcript.jsonl`, and the direct
single-link native session lock. Exclude incomplete-fork markers, committed or
recoverable deletion intents, and validated deletion stages from ordinary
inventory, but still validate the reserved files and any present transcript or
lock inside an incomplete fork. Make the opened file handle the first
authoritative retained transcript and lock identity before comparing direct
rooted entries; a plain Windows `Lstat` identity is only a no-follow type guard
because `SameFile` may otherwise resolve it lazily after a replacement. If any
valid-looking candidate or reserved
stage or receipt has an unsafe, replaced, linked, over-bound, or contradictory
identity, return `store_unsafe` for the whole scan rather than claiming
completeness.

Inventory does not create, remove, rename, truncate, or rewrite session-store
entries. It may descriptor-sync and reverify an already present internal final
receipt or completion marker before treating that metadata as durable; a
failed durability confirmation makes the complete inventory `store_unsafe`
and cannot release the ID for reuse.

Project only a version, closed status, session ID, optional canonical UTC
`updated_at`, and opaque revision or continuation token. The revision binds the
selected workspace plus the session-directory, transcript identity and
content, and lock identity;
inventory statuses are exactly `ok`, `stale`, and `store_unsafe`.
Do not expose transcript text, prompt, title, tool data, filesystem paths,
workspace hashes, or application-home information. Latest-session selection is
the first item in this same ordering. Resume, continue, and fork source
selection must reject any ID not currently resumable. Explicit creation must
also reject an existing candidate, incomplete fork, live deletion intent, or
detached cleanup stage for that ID so deletion recovery cannot be confused with
new ownership.

**TX-069 — Identity-bound, recoverable local deletion.** Delete exactly one
valid native session ID in the selected workspace at one opaque listed
revision through the same provider-free service, without initializing a
semantic session. Do not support bulk deletion, wildcards, force,
active-session termination, descendant-fork cascading, or caller-selected
store paths. Return the closed versioned result union `deleted`, `not_found`,
`stale`, `session_locked`, `delete_incomplete`, or `store_unsafe`; diagnostics
may explain the result on a separate channel, but clients never parse raw OS
paths or human error text.

At the mutation boundary, revalidate the ID, selected-workspace membership,
opaque revision, parent directory, session directory, transcript, and lock
identities. Acquire the target's existing session lock nonblocking; healthy
contention returns `session_locked` and never terminates the owner. While the
lock is held, preflight the platform no-replace adapter and real directory
durability boundary before recording intent. Preflight proves primitive
availability, not that the later target-filesystem syscall cannot fail.
Publish the versioned deletion intent without replacing an existing
destination. Before truncating an interrupted temporary or final intent,
descriptor-verify its direct single-link identity and require its bytes to be
an exact prefix of this transaction. Revalidate the locked target inside the
detach primitive's immediate callback, then atomically detach the direct
session directory with a same-parent no-replace rename into the reserved
invalid-ID staging namespace. Once intent is durable, a precommit detach
failure retains that exact intent and any published pending receipt as the
retry reservation and returns `delete_incomplete`; it does not attempt a
multi-object rollback whose partial commit could contradict recovery metadata.
An unsafe revision or intent mutation while the target lock is held also
retains the reservation and fails closed. Sync the parent and release the lock
only after inventory, resume, continue, and fork selection can no longer reach
the live valid-ID name.

Remove the detached owned directory only through descriptor-rooted
`OwnedDirectory` cleanup, never raw recursive path deletion and never recursive
removal of the live directory while its internal lock remains addressable.
Validate and bound every staging and receipt scan. Before detach, atomically
publish a canonical immutable pending receipt as a fully constructed private
temporary directory renamed no-replace into its opaque final name.
Interrupted temporary receipt directories are recoverable only when their
length-delimited ID and revision and exact record prefix match the live intent
or a full retained stage. Never delete a competing receipt directory that this
operation did not exclusively create. A retained committed intent, transcript,
or lock must be direct, single-link where applicable, and agree with the
stage's length-delimited ID and revision. A stage without a receipt is accepted
only in the complete compatibility form containing a matching intent, nonempty
content-bound transcript, and lock; any partial stage requires its exact
receipt. The receipt binds the workspace partition, original directory,
transcript, and lock through opaque hashes, without storing raw paths or
transcript metadata.

Serialize receipt capacity, publication, retirement, and live-stage creation
with one persistent cross-process registry lock inside the receipt root, always
acquired after the target session lock. While both locks are held, rescan and
reserve workspace, stage, and receipt headroom; refuse before intent when
creating the receipt root would exceed 4,096 workspace entries or another
stage would exceed 512. Count pending, completed, temporary, and receipt-GC
entries together against the 512-entry receipt bound. An empty receipt root may
be recovered after a crash before first lock creation, but once any transaction
metadata exists the persistent direct single-link registry-lock identity is
mandatory; absence or replacement fails closed rather than creating a second
lock inode beside an older held lease. Reverify that registry lease immediately
at live detach and staged cleanup/completion mutation boundaries. Never
recursively remove a final opaque receipt directly. Atomically detach it
no-replace to the strict `g1_` receipt-GC namespace, sync the receipt root, and
clean only the detached `OwnedDirectory`. Later registry-locked mutations
validate and sweep bounded partial or empty GC remnants, so a cleanup crash
cannot turn a final receipt into an uncorrelatable malformed directory.

For detached cleanup, acquire any remaining staged session lock first and the
registry lock second. Revalidate or publish the receipt while both are held,
then release the staged file lock before recursive removal but retain the
registry lock across descriptor-rooted cleanup, partition sync, absence
verification, and completion publication. Concurrent retries therefore return
the stable busy outcome instead of traversing the same stage together.

Treat the intent-at-live-name and detached-stage states as durable recovery
evidence. Both hide the session from ordinary selection and reserve its
original ID. A retry naming the original ID and revision resumes the remaining
detach or cleanup work. A post-intent, post-detach, or cleanup failure returns
`delete_incomplete`; return `deleted` only after the original live identity,
validated stage, and all AgentX-owned contents of that session directory are
absent, the workspace partition has been durably synced, a fresh bounded scan
cannot find the receipt-bound directory under another direct name, and a
durable completion marker has been verified. Pending receipts are never
evicted. Retain up to 256 completed receipts during normal mutation; when space
is needed, remove completed receipts only in deterministic lexical order,
while the entire receipt namespace remains hard-bounded at 512 entries. A
retained completed receipt makes an exact retry idempotently `deleted`; once
retention removes it, absence is `not_found`.

Cancellation before durable intent leaves the live session selectable and
does not reserve its ID. Cancellation after durable intent is a recoverable
post-intent failure: preserve the exact receipt, intent, or detached stage,
release any target, staged, and registry leases in their required order, and
return `delete_incomplete` without claiming deletion. Retry with the original
ID and revision completes the same transaction.

A genuinely new generation may reuse the ID only after staging cleanup
completed and is not part of the old revision's deletion. An old token against
a live or staged newer generation is `stale`, and completed receipts never
reserve the ID. This is deletion
from AgentX's local native session store, not secure media erasure.
It does not delete backups, remote copies, project memory, worktrees,
authentication or configuration, descendant forks, the AgentX VS Code
extension's local data, VS Code presentation caches, or extension-owned topic
metadata.

## Remote persistence and disabled behavior

**TX-070 — Internal-event persistence.** When an internal worker-event writer is registered, emit each transcript message as a typed internal event with compaction and agent metadata. A writer failure logs persistence failure but does not corrupt the local transcript or crash the session.

**TX-071 — Legacy ingress.** When explicitly enabled with an ingress URL, append main-chain transcript messages remotely after local deduplication. A definitive remote append failure records telemetry and initiates graceful process shutdown because remote continuity cannot be guaranteed.

**TX-072 — Shutdown suppression.** Do not start new remote writes after shutdown begins. Finish/flush already accepted local work according to cleanup policy.

**TX-073 — Feature absence.** If attribution, context collapse, remote persistence, agent sidechains, or content replacement is absent or disabled, omit its events and restoration state cleanly. Base message-chain load must remain functional.

## Limits and constants

**TX-080 — Required values.** Preserve these defaults unless explicitly configurable:

| Concern | Value |
| --- | ---: |
| local append delay | 100 ms |
| remote/internal append delay | 10 ms |
| maximum append chunk | 100 MiB |
| tail metadata/tombstone window | 65,536 bytes |
| tombstone full-rewrite ceiling | 50 MiB |
| raw transcript read ceiling | 50 MiB |
| optimized-load threshold | 5 MiB |
| streaming read chunk | 1 MiB |
| cached last-prompt text | 200 characters plus ellipsis |
| native session ID | 1–128 ASCII letters, digits, `_`, or `-` |
| default / maximum inventory page | 100 / 500 sessions |
| workspace inventory entry bound | 4,096 direct entries |
| deletion-stage scan bound | 512 direct entries |
| deletion-receipt namespace bound | 512 direct entries |
| completed deletion-receipt retention | 256 entries, deterministic lexical GC |
| transcript digest bound per complete pass | 2 GiB |
| transcript digest bound per management operation | 8 GiB |

The effective tail window is 64 KiB even where historical prose calls it 16 KiB.

## Acceptance scenarios

**TX-A01 — Metadata-only startup.** A named session exits before any user/assistant message. A new empty session file is not created. A non-fork resumed existing session can still persist the new name because it explicitly adopts its file.

**TX-A02 — Parallel tools.** One API response emits thinking plus two tool-use assistant records and two source-parented result records. Resume implements all siblings/results in API-valid order despite the parent walk reaching only one branch.

**TX-A03 — Compaction with preserved tail.** Boundary and summary are new while preserved messages already exist on disk. New continuation chains after the preserved tail; resume relinks head to summary and does not orphan or duplicate the tail.

**TX-A04 — Broken preserved metadata.** One preserved UUID was never written before crash. Resume detects the broken walk, skips pruning, and loads full history rather than deleting uncertain records.

**TX-A05 — Snip middle range.** A snip removes consecutive records in the middle. Resume deletes them and links the following survivor to the first surviving ancestor before the gap.

**TX-A06 — Legacy progress.** A historic child points to a progress UUID. The loader bridges through progress to the last semantic ancestor and returns a continuous chain without displaying progress.

**TX-A07 — External rename.** An SDK writer changes title while CLI cache is stale. Shutdown tail scan absorbs the newer value and re-appends it rather than resurrecting the old title.

**TX-A08 — Fork ownership.** Forking a worktree session produces a new session ID and restamped messages, preserves replacement decisions, and does not claim or delete the original session's worktree.

**TX-A09 — Huge dead branches.** A 150 MiB append-only file contains an old compacted prefix and fork branches. Streaming load keeps surviving output bounded, restores preboundary metadata, and yields the same active chain as a full parse.

**TX-A10 — Remote append failure.** Local append succeeds but legacy ingress definitively fails. The process begins graceful shutdown and does not continue presenting the session as remotely resumable.

**TX-A11 — Crash after parallel execution.** Two tool-use blocks are durable but only one terminal result reached disk before process death. Resume recovers recorded off-chain siblings/results, does not rerun either side effect, and applies `TX-067`: fully unresolved tool-use messages leave the active projection; a missing member of a retained mixed group receives only an in-memory synthetic error or strict-mode failure. The transcript is not rewritten to claim success or failure.

**TX-A12 — Structural credential at append.** Configure credentials equal to the canonical JSON sequence spanning one safe event field and the next field name, and to the safe event JSON suffix plus its newline. Append each event and verify whole-record validation rejects it before any file byte or accepted/resolved index changes. Include duplicate event-object members whose earlier escaped value decodes to a credential and verify every occurrence is inspected. Append a credential-free event with the same identities and verify its exact normalized sequence, parent, and content round-trip through reopen.

**TX-A13 — Credential-safe recovery.** Plant an otherwise valid legacy record whose content is a configured credential, reopen with the session validator, and verify it is rejected before any recovered index or public snapshot is returned. Repeat with a valid final record lacking LF whose future LF completes a credential, and with individually safe event/diagnostic fields whose final `Snapshot` JSON reconstructs one. A panicking validator returns a fixed failure and does not crash recovery.

**TX-A14 — Hostile append clock.** Configure clocks that panic, return zero, and reenter the same store with a zero-timestamp event. Each outer append terminates, receives a nonzero UTC timestamp, and preserves coherent sequence/index state; the reentrant case persists both events without deadlock or recursive callback growth.

**TX-A15 — Application-home session placement.** Start persistent sessions
from two isolated workspaces under one application home. Each transcript,
session lock, task state, and tool-result store remains below
`sessions/<its-workspace-hash>/<session-id>/`; `--continue`, resume, and fork
select only the first workspace's sessions when launched from the first
workspace. A crafted session ID or symlinked workspace-hash child fails closed;
a persistent replacement observed at an identity-check boundary fails without
mutating the replacement. This scenario does not claim resistance to every
swap-and-restore race inside independently owned stores. A
`--no-session-persistence` run still observes the bootstrapped top-level
`sessions/` directory but leaves no workspace-hash or session-ID child.

**TX-A16 — Turn lifecycle round-trip and diagnostic non-authority.** Run two
model-backed turns in one persistent session: one succeeds after provider usage,
and one ends in a provider configuration error. Reopen and resume the session.
Each admitted turn has one accepted user start marker, its completed usage
events, and exactly one model-hidden terminal `turn_result`; recovery neither
duplicates them nor inserts them into model context. Repeat with DEBUG enabled
and verify diagnostics do not change those durable records. Finally, fail the
accepted-user append after a DEBUG start candidate and verify no diagnostic
record fabricates a durable start, usage, or terminal result; the operation
surfaces transcript admission/finalization failure instead.

**TX-A17 — Bounded authoritative inventory.** Give two workspace partitions the
same valid session ID and list each independently. A missing third partition
returns an empty list without appearing on disk. In one partition, combine
valid sessions with Unicode, overlong, and traversal-like names, an incomplete
fork, and a detached deletion stage. Inventory exposes only complete native
candidates, sorts equal transcript times by ID, and returns every item through
bounded continuation pages. Mutating an identity between pages makes the old
token stale. Replace a valid directory with a symlink or non-directory, or make
its transcript or lock indirect or multiply linked, and verify the entire
inventory fails `store_unsafe`. Its JSON contains only the versioned minimal
fields from `TX-068`.

**TX-A18 — Revision, workspace, and active-lock races.** List one session, then
replace its transcript, session lock, or whole session directory while
preserving apparent size and modification time; also replace transcript bytes
in place while preserving inode, size, and modification time. Deletion with
the old opaque revision returns `stale` and leaves the replacement untouched.
Use a filesystem time outside canonical RFC3339's year range where supported:
inventory omits `updated_at` but preserves internal ordering and revision
binding. Hold the listed lock from another owner and verify deletion returns
`session_locked`; release it and retry the same revision successfully. Delete
one of two workspaces' same-named sessions and verify the other remains
resumable. A malformed or mismatched retained stage or receipt record fails
`store_unsafe`. Race deletion against list, latest, resume, continue, fork, and
explicit creation. If selection or creation acquires the target session lock
first, deletion returns `session_locked`; if deletion publishes durable intent
or commits detach first, every selector rejects the ID and explicit creation
treats it as reserved. Inventory may return only a complete pre-intent or
post-intent generation and never includes a committed intent or deletion
stage.

**TX-A19 — Crash-retry deletion and scope.** Inject failures after committed
intent, during empty and partial receipt publication, after atomic detach,
immediately before descriptor-rooted cleanup, and after stage removal but
before parent sync. Also inject cancellation before durable intent, after
durable intent, after detach, and during cleanup. A pre-intent cancellation
leaves the live generation selectable and unreserved; every post-intent
failure or cancellation returns `delete_incomplete`, releases leases in
protocol order, and preserves retry evidence. List, latest, resume, continue,
fork, and explicit creation cannot select or recreate the pending ID. Retry
with the original ID and revision and verify cleanup continues from the live
intent, receipt, or detached stage. Move or replace the receipt-bound stage
under an ordinary invalid name and verify retry fails closed. Reject partial
stages without a receipt while accepting a fully validated compatibility
stage. Fill workspace or stage capacity exactly and verify a new deletion
refuses before recording intent; contend the receipt registry and verify no
mutation crosses the busy boundary. Unlink or replace the persistent registry
lock while transaction metadata remains and verify inventory and deletion fail
`store_unsafe` without creating a competing lock. Interrupt final receipt
retirement after its atomic `g1_` detach and verify a later mutation sweeps the
bounded partial GC stage. Verify pending receipts are never evicted, completed
receipt retention is bounded and deterministic, and an old receipt returns
`stale` without touching a genuinely recreated live or staged generation.
Report `deleted` only after both names and all owned contents are absent and
the durable receipt transition is revalidated. Verify the operation leaves
descendant forks, worktrees, project memory, auth/configuration, remote or
backup copies, and all AgentX VS Code extension data and presentation metadata
unchanged.

**TX-A20 — Attachment round-trip and privacy.** Persist legacy text, an
attachment-only turn, and an ordered text/image/PDF turn. Reopen and verify the
typed manifests, order, stable prompt UUID, and blob identities. Inspect every
transcript and replay record and verify no binary bytes, base64, source path,
runtime path, or provider body appears. Legacy version-1 text resumes
unchanged.

**TX-A21 — Missing and tampered durable media.** Remove one referenced blob,
then separately mutate bytes while preserving its filename. Resume, compaction,
and provider projection each fail with the owning prompt/attachment identity
before transport; none substitutes a placeholder as authoritative content,
rewrites history, or resends uncertain media.

**TX-A22 — Attachment fork and collection.** Fork a session containing
deduplicated shared blobs and verify the destination owns verified immutable
copies with the same identities and no dependency on original source paths or
the source store. Delete or collect an unreferenced blob without affecting a
referenced sibling, then delete the native session and verify its attachment
store is removed under the recoverable session-deletion protocol. Backup and
remote copies remain outside that guarantee.

## Non-normative provenance

Behavior was specified primarily from `utils/sessionStorage.ts`, `utils/sessionStoragePortable.ts`, `utils/sessionRestore.ts`, `utils/messages.ts`, bootstrap session identity, transcript call sites in the query engine, and content-replacement persistence.
