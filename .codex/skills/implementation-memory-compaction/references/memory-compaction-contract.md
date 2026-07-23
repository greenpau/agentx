# Memory and Compaction Implementation Contract

This document defines persistent file memory, shared-memory synchronization, derived memory, consolidation, and active-context reduction. The durable transcript remains authoritative throughout; memory is selected context with its own provenance and lifecycle.

## Contents

- [Context accounting and strategy order](#context-accounting-and-strategy-order)
- [Automatic compaction](#automatic-compaction)
- [Full and partial summary compaction](#full-and-partial-summary-compaction)
- [Session memory](#session-memory)
- [Microcompaction and API context edits](#microcompaction-and-api-context-edits)
- [Context collapse and cleanup](#context-collapse-and-cleanup)
- [Failure, cancellation, and disabled behavior](#failure-cancellation-and-disabled-behavior)
- [Persistent file memory and relevance recall](#persistent-file-memory-and-relevance-recall)
- [Team memory synchronization](#team-memory-synchronization)
- [Automatic dream consolidation](#automatic-dream-consolidation)
- [Constants](#constants)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Context accounting and strategy order

**MC-001 — Derived projection.** Compaction, session memory, microcompaction, and context collapse produce model-context projections. They do not delete or silently rewrite the authoritative transcript. Record explicit boundary/edit metadata so resume can implement the intended projection.

**MC-002 — Turn pipeline.** Within a query iteration, apply context-pressure stages in this order:

1. select messages after the effective compact boundary;
2. apply persisted aggregate tool-result replacements;
3. apply explicit history snip;
4. run pre-request microcompaction;
5. project and, when eligible, commit context collapse;
6. evaluate proactive automatic compaction;
7. invoke the model;
8. handle reactive prompt-too-long recovery after a model rejection.

Do not run two strategies concurrently against the same mutable message projection.

**MC-003 — Token metric.** Use the latest API usage when it represents current context; otherwise conservatively estimate message content. Count input, cache creation, and cache read for context pressure. For rough message estimation, count text, tool names/inputs, tool-result text, thinking text but not signatures, and approximate each image/document as 2,000 tokens, then multiply the aggregate by 4/3.

**MC-004 — Effective context window.** Begin with the model's input window. Reserve the smaller of its output allowance and 20,000 tokens for the compaction summary. A valid positive automatic-compaction-window override can only reduce the base window. Effective window equals adjusted input window minus the reservation.

**MC-005 — Strategy ownership.** A query source identifies the owner. Main-thread sources include the ordinary REPL prefix and SDK. Forked summary, session-memory, context-collapse, subagent, and prompt-suggestion sources are distinct. Strategy guards and cleanup use this identity rather than guessing from message content.

**MC-006 — API-round grouping.** For lossy truncation decisions, group messages by API round: a boundary occurs when an assistant block begins with a new API response ID. Assistant fragments sharing an ID, including interleaved tool results, remain in one group. This creates split points where tool-use/result pairing is normally complete; final API normalization still repairs malformed historical input.

**MC-007 — Compaction result.** Represent a completed transformation as a value containing one boundary marker, one or more summary user messages, restoration attachments, SessionStart hook messages, optional preserved messages, optional user-display text, precompact context size, compaction-call usage, and estimated true resulting-context size. Construct this value completely before installing it into the live conversation.

## Automatic compaction

**MC-010 — Threshold.** Default proactive automatic threshold is `effective window - 13,000`. A valid percentage override in `(0,100]` may lower but never raise this threshold.

**MC-011 — Warning state.** With automatic compaction enabled, calculate percent remaining against the automatic threshold; otherwise use the full effective window. Warning and error flags begin 20,000 tokens below that threshold. Clamp percent remaining at zero or above. The hard blocking limit is `effective window - 3,000`, unless a valid positive test/configuration override replaces it.

**MC-012 — Enablement.** Automatic compaction requires all of:

- compaction not globally disabled;
- automatic compaction not independently disabled;
- user configuration enables it;
- query source is not `compact` or `session_memory`;
- the context-collapse agent is not compacting itself;
- no active strategy mode deliberately owns proactive pressure, such as reactive-only or enabled context collapse.

Manual compaction may remain available when only automatic compaction is disabled. Global disable suppresses both.

**MC-013 — Snip adjustment.** Subtract a snip's estimated freed tokens from stale assistant usage when deciding whether to auto-compact; surviving usage fields can reflect the pre-snip prompt.

**MC-014 — Tracking state.** Track whether the current chain already compacted, turns since compact, a unique compact turn ID, and consecutive failures. On success, reset turn counter and failure count and assign a new compact turn ID. Preserve enough data to diagnose same-chain recompaction.

**MC-015 — Failure circuit breaker.** After three consecutive automatic-compaction failures, skip further proactive attempts for that session/chain until an explicit state reset. Do not spend an unbounded number of doomed API calls.

**MC-016 — Strategy preference.** When eligible, try session-memory compaction before summary compaction. If session memory cannot safely compact or would leave context at/above the automatic threshold, fall back to ordinary summary compaction.

**MC-017 — Task budget.** When a task-level token budget spans multiple compactions, subtract the authoritative final precompact context from remaining task budget. Do not reset the budget merely because the active message projection became smaller.

## Full and partial summary compaction

### Shared summary protocol

**MC-020 — Full precondition.** Reject full compaction of an empty conversation as “not enough messages” without making an API call.

**MC-021 — Hook order.** For a compaction attempt:

1. enter compacting status and announce PreCompact hook progress;
2. run PreCompact hooks with trigger `auto` or `manual` and user custom instructions;
3. merge user instructions first and hook-provided instructions second;
4. stream/generate the summary;
5. build boundary, summary, preserved messages, attachments, and SessionStart hook messages;
6. record cache/telemetry markers and re-append session metadata;
7. run PostCompact hooks with the generated summary;
8. in all paths reset compacting UI/status in a finalizer.

PreCompact may contribute user-display text and instructions. PostCompact may contribute additional user-display text. Hook errors fail the attempt according to hook policy; they do not leave half-installed messages.

**MC-022 — Summary isolation.** The compaction agent produces text only. Its permission function denies every tool call. A fallback request may advertise a bounded read/search schema for protocol/cache reasons, but permission still denies execution.

**MC-023 — Summary input sanitation.** Before an isolated summary request:

- use only active messages after the latest effective compact boundary;
- replace user and nested tool-result images/documents with `[image]` or `[document]` markers;
- remove discovery/listing attachments that will be reinjected;
- normalize and repair tool pairing;
- disable thinking;
- use a concise summarizer system prompt.

**MC-024 — Cache-sharing fork.** By default, first try a one-turn forked summarizer that reuses the main conversation's cache-safe prefix. Do not override output tokens in a way that changes the fork's thinking/cache key. Deny tools and skip cache writes. An aborted or API-error assistant record is not a valid summary even if it contains text.

**MC-025 — Streaming fallback.** If the fork fails or returns no valid text, use an isolated streaming request. Bound summary output by both the compact allowance and model maximum. An optional retry gate permits two streaming attempts; with the gate off, make one. Retry incomplete/no-response streams with abort-aware backoff. An activity tracker sends a heartbeat and refreshes compacting status every 30 seconds while a long compact call is active.

**MC-026 — Prompt-too-long retry.** If the compaction request itself is too long, retry at most three times by dropping oldest API-round groups. If the provider reports a token gap, drop enough groups to cover it; otherwise drop at least one and approximately 20 percent. Always preserve at least one group. If the remaining first record is assistant-role, prepend a synthetic meta user marker. If no safe reduction remains, fail with an actionable conversation-too-long error.

**MC-027 — Summary validation.** A valid summary contains nonempty assistant text and is not an API-error message. Empty text, API error, user abort, prompt-too-long exhaustion, or incomplete streaming fails the attempt without replacing active messages.

**MC-028 — Cache reset and attachments.** After a valid summary, snapshot then clear read-file and nested-memory caches. In parallel, re-read recent files and inspect asynchronous agents. Restore:

- at most five recent files, skipping files already present in a preserved tail;
- active async-agent state;
- current plan and plan-mode instructions;
- invoked skill contents;
- deferred-tool, agent-listing, and MCP-instruction deltas absent from preserved messages;
- SessionStart hook output for a compacted session.

Do not reset invoked-skill content or blindly reinject the full skill listing.

**MC-029 — Postcompact budgets.** File restoration has a 50,000-token total budget and 5,000-token per-file cap. Invoked-skill restoration has a 25,000-token total budget and 5,000-token per-skill cap. Prefer truncating a selected skill/file with a marker over silently exceeding the budget.

**MC-030 — Boundary and summary.** Emit a system compact boundary with trigger, precompact token count, and last precompact UUID. Preserve the set of previously discovered deferred tools in boundary metadata. Follow it with a hidden/transcript-only user summary that contains the formatted summary and transcript-recovery reference.

**MC-031 — Result ordering.** Install a compaction result as:

```text
boundary
-> summary message(s)
-> preserved messages, if any
-> restoration attachments
-> SessionStart hook results
```

Record this segment before discarding the preboundary active array.

**MC-032 — Success markers.** On success, notify cache-break tracking of an expected cache drop, arm the one-shot post-compaction marker, re-append session metadata near the transcript tail, and clear the last session-memory summarized UUID when replacement invalidates it.

**MC-033 — Manual error display.** Manual compaction errors other than user abort or “not enough messages” generate an immediate user-visible compaction error. Automatic failure is quiet except diagnostics and failure tracking, because a later attempt can succeed.

### Full compaction

**MC-040 — Full transform.** Full compaction summarizes the complete active conversation and normally preserves no ordinary messages. Resulting active context is boundary, one hidden summary, restorations, and hook output.

**MC-041 — True postcompact size.** Distinguish the compaction API call's usage from the estimated size of resulting context. Record both. The former is dominated by precompact input; the latter determines whether the next turn is likely to recompact.

### Partial compaction

**MC-050 — Directions.** Support:

- `from`: keep messages before the pivot and summarize the pivot and later tail;
- `up_to`: summarize messages before the pivot and keep the pivot and later suffix.

Reject an empty summarize side with a direction-specific explanation.

**MC-051 — Kept-message filtering.** Remove progress in both directions. For `up_to`, also remove old compact boundaries and old compact-summary messages from the kept suffix so an older boundary cannot supersede the new summary. For `from`, retain prior summary/boundary history because it may represent older already-compacted context.

**MC-052 — Cache behavior.** `up_to` can send its prefix directly and reuse that prefix cache. `from` sends the full conversation because a tail alone is not the existing prefix. Prompt-too-long retry may sacrifice cache identity to unblock the operation.

**MC-053 — Partial summary metadata.** A visible partial-summary message records messages summarized, optional user context, and direction when messages are kept. If no messages are kept, use the hidden full-summary presentation.

**MC-054 — Preserved anchor.** Annotate the boundary with preserved segment `{head, anchor, tail}`. For prefix-preserving `from`, anchor is the new boundary. For suffix-preserving `up_to`, anchor is the last new summary message. The transcript loader uses this metadata to splice records whose original parents cannot be rewritten.

**MC-055 — API invariant expansion.** Before selecting a preserved suffix, expand its start backward to include:

- every assistant tool-use needed by any kept tool result;
- every streamed assistant fragment sharing an API response ID with a kept fragment;
- associated thinking fragments needed for normalization.

Do not expand earlier than the latest effective compact boundary.

## Session memory

### Extraction

**MC-060 — Eligibility.** Automatic session-memory extraction is feature-gated, requires automatic compaction, is disabled in remote mode, and runs only for the exact main REPL query source. Register the post-sampling hook synchronously; evaluate remote gates/config lazily when it fires.

**MC-061 — Extraction thresholds.** Defaults are:

- initialize after current context reaches 10,000 tokens;
- update only after at least 5,000 tokens of context growth since last extraction;
- normally require three tool calls since the prior update.

Token growth is always required. Extraction proceeds when growth is met and either tool-call threshold is met or the last assistant turn has no tool calls. Positive remote configuration may replace defaults; zero/invalid values do not.

**MC-062 — Sequential extraction.** Serialize extraction attempts. Mark start time, set up the file using an isolated child context, run one cache-sharing fork with query source `session_memory`, and mark completion in a finalizer. Waiters stop waiting after 15 seconds; an extraction older than 60 seconds is stale and is ignored.

**MC-063 — Memory file.** Use a dedicated session-memory path with owner-only file/directory permissions. Initialize from a stable template when absent. Remove it from the parent read cache after setup so note-taking does not pollute normal file context.

**MC-064 — Extraction authority.** The memory fork may use only Edit on the exact session-memory path. Deny every other tool or path. The prompt instructs it to update fixed sections and stop, not answer the user.

**MC-065 — Memory content limits.** Keep each section near or below 2,000 tokens and total memory near or below 12,000 tokens. Prompt oversized sections and total memory to condense. Never treat the template alone as meaningful memory.

**MC-066 — Safe summarized boundary.** After successful extraction, record current context size. Advance `lastSummarizedMessageId` only when the last assistant turn has no tool calls, preventing later compaction from separating unresolved tool-use/results.

### Session-memory compaction

**MC-070 — Compaction enablement.** Session-memory compaction requires its memory feature and compaction feature gates, unless explicitly forced/disabled by environment. Wait for current extraction under MC-062. Fall back when file is absent, template-empty, inaccessible, or its saved summarized UUID is not present.

**MC-071 — Preserved-window defaults.** Preserve at least 10,000 estimated tokens and at least five text-bearing messages, expanding backward until both hold, with a 40,000-token hard target/cap. Positive remote values may replace these defaults.

**MC-072 — Resume without boundary UUID.** If memory exists after process resume but no `lastSummarizedMessageId` exists, initially treat all messages as summarized, then expand backward under the minimum-preservation rules. Record a diagnostic resume path.

**MC-073 — Pair-preserving start.** Adjust the start as required by MC-055. Remove old compact boundaries from preserved messages so installing them cannot trigger a second prune.

**MC-074 — Memory summary.** Truncate each oversized memory section at a line boundary near 2,000 tokens and append a marker. Format this content as the hidden compact summary. If any section was truncated, include the path to full session memory. Add the active plan attachment and SessionStart hook output.

**MC-075 — Threshold validation.** Estimate the complete result: boundary, summary, preserved messages, attachments, hooks. If an automatic-compaction threshold was supplied and the result reaches or exceeds it, reject session-memory compaction and fall back to ordinary summary compaction.

**MC-076 — Session-memory success.** Annotate a suffix-preserved segment anchored at the summary, reset the saved summarized UUID, perform source-aware postcompact cleanup, notify cache tracking, and arm post-compaction state. There is no separate compaction API-call usage; reported postcompact metrics represent resulting context.

## Microcompaction and API context edits

**MC-080 — Compactable tools.** Client-side result clearing applies only to known high-volume file read/write/edit, shell, search, glob, and web tools. Preserve unsupported/custom/MCP result semantics unless their own protocol explicitly opts in.

**MC-081 — Time-based precedence.** Evaluate time-based microcompaction before cached editing. It requires an explicit main-thread query source, an enabled configuration, a prior assistant timestamp, and a gap at least the configured threshold. Defaults are disabled, 60 minutes, and keep five recent compactable results.

**MC-082 — Time-based transform.** When the server prompt cache is presumed expired, replace old eligible tool-result content with `[Old tool result content cleared]`, always keeping at least the most recent result even if configured keep count is zero. If no tokens are saved, make no change. Reset cached-edit state and notify cache diagnostics of the expected miss.

**MC-083 — Cached editing scope.** Cached microcompaction is available only when built, enabled, model-supported, and on the main thread. Forked agents must not register their tool IDs in main-thread process-global edit state.

**MC-084 — Cached edit state.** Track tool-result IDs in wire-message order, deleted references, pinned edits, and whether tool schemas have been sent. When trigger/keep rules select old results, queue one API cache-edit block without changing local message content. Pin it at its original user-message position for byte-stable resends.

**MC-085 — Cache-deletion accounting.** Record the cumulative cache-deleted token baseline before a new edit. After the response, compute the operation delta and only then emit the corresponding boundary/diagnostic. Cached API counters are cumulative across requests.

**MC-086 — API-native context management.** Optionally send provider context-edit strategies. Thinking cleanup retains all prior thinking by default, or one turn after a long cold-cache gap; omit thinking edits when thinking is redacted. Internal tool clearing defaults to trigger 180,000 input tokens and clear at least the amount needed to target 40,000, separately controlling result-capable tools and input-capable edit tools.

**MC-087 — Disabled microcompaction.** When cached editing is absent/unsupported and the time trigger does not fire, return the exact original messages. Do not silently run a removed legacy client-side algorithm.

## Context collapse and cleanup

**MC-090 — Collapse role.** Context collapse is an optional granular projection that archives spans behind summary commits while retaining authoritative messages and an ordered commit log. When enabled, it owns proactive context pressure; ordinary autocompaction is suppressed, while reactive summary compaction remains an emergency fallback.

**MC-091 — Collapse pressure.** Begin preparing/committing collapse near 90 percent of its effective pressure window and use a blocking/spawn path near 95 percent. Treat these as independently configurable strategy thresholds when the build supplies configuration.

**MC-092 — Commit persistence.** Persist collapse commits in commit order because later summaries can reference earlier summary records. Persist snapshots with last-wins semantics. On resume, clear any prior in-process store first, then replay commits and apply the final snapshot before the first query projection.

**MC-093 — Compact interaction.** A compact boundary resets stale collapse commits/snapshot because their archived spans refer to preboundary messages. The collapse agent itself must never autocompact and thereby reset the main thread's shared collapse state.

**MC-094 — Postcompact cleanup.** After a successful compact:

- reset microcompact/cache-edit state;
- clear system-prompt sections, classifier approvals, speculative checks, beta tracing, and transcript UUID cache;
- sweep optional attribution file-content cache;
- on main thread only, reset context collapse and clear user/memory discovery caches;
- retain invoked-skill contents and the sent-skill listing state needed to avoid expensive redundant reinjection.

Undefined owner is safe only for known main-thread-only manual clear/compact callers.

## Failure, cancellation, and disabled behavior

**MC-100 — Atomic installation.** Do not replace the active message projection until the summary/strategy result is fully valid and attachments/hooks required for installation are available. A failed attempt leaves the prior active context usable.

**MC-101 — User abort.** Propagate abort into forked and streaming summarizers, hook execution, waits, and backoff. Normalize the user-abort error without showing a misleading generic manual-compaction notification.

**MC-102 — Reactive recovery bound.** On model prompt-too-long, first allow a pending context-collapse drain once, then reactive summary compaction once. Do not repeatedly alternate error, compact, and stop hooks. If recovery fails, surface the withheld original error and stop.

**MC-103 — Media failure.** Media-size/provider media errors may trigger one reactive compact path without a context-collapse drain. Keep the same bounded guard.

**MC-104 — Optional axes.** State separately whether session memory, persistent file memory, relevance selection, team memory, team sync, automatic dream consolidation, cached edits, API-native edits, context collapse, prompt-cache sharing, and streaming retry are build-included, runtime-enabled, model/provider supported, account-eligible, and policy-allowed. Disabled behavior follows each contract above and must not leave partial global state.

## Persistent file memory and relevance recall

**MC-MEM-001 — Enablement precedence.** Resolve automatic file memory by the first applicable rule:

1. an explicit truthy disable environment value turns it off;
2. an explicit falsy disable value turns it on and overrides later rules;
3. simple/bare mode turns it off;
4. remote mode without a persistent remote-memory directory turns it off;
5. an explicit `autoMemoryEnabled` setting decides;
6. otherwise it is on.

The background extraction agent has a separate experiment and noninteractive gate; enabling the file-memory prompt does not imply that extraction runs.

**MC-MEM-002 — Directory identity and path safety.** Resolve the memory base from a remote persistent-memory override or the user configuration home. Resolve the full automatic-memory directory from, in order, a validated cowork full-path override, a validated setting from policy/flag/local/user sources, or `<base>/projects/<sanitized-canonical-repository-root>/memory/`. Never accept the project-controlled settings source for this override. A setting may expand a nontrivial home-relative suffix; the environment full-path override may not. Reject relative paths, filesystem roots and near-roots, drive roots, network-share roots, null bytes, and home/ancestor collapses. Canonical repository identity makes linked worktrees share memory; fall back to stable project root when no canonical repository root exists. Cache by project root only after treating environment/settings as session-stable.

**MC-MEM-003 — File model and taxonomy.** `MEMORY.md` is a concise index, not the store of detailed facts. Topic files are Markdown with maintained name/description/type frontmatter and use the closed types `user`, `feedback`, `project`, and `reference`. Organize semantically rather than chronologically, update or remove stale/incorrect entries, avoid duplicates, and never store information readily derivable from current code/version control, transient task progress, plans, or secrets. An explicit remember/forget request updates the appropriate topic/index immediately.

**MC-MEM-004 — Prompt modes and entrypoint.** In ordinary indexed mode, instruct a two-step write: update a topic file, then add a one-line pointer to `MEMORY.md`. Under relevance/skip-index mode, topic files remain writable but the prompt omits the pointer requirement because relevant files are surfaced separately. Load `MEMORY.md` through the instruction-file pipeline when present. Truncate it first at 200 lines and then at 25,000 UTF-16 code units, at a newline when possible, and append a visible truncation warning. Directory creation is idempotent/best-effort; a creation failure is diagnostic and does not crash prompt construction.

**MC-MEM-005 — Assistant daily-log mode.** When the assistant/KAIROS build and runtime mode are active, append new observations to `<memory>/logs/YYYY/MM/YYYY-MM-DD.md` as short timestamped bullets. This mode takes precedence over team-memory prompt composition. Treat the daily file as append-only and derive the date at execution time so a cached prompt survives midnight; a separate consolidation process distills logs into topic files and `MEMORY.md`. The distilled index may still be loaded for orientation but is not the write target in this mode.

**MC-MEM-006 — Header scan.** Recursively enumerate Markdown topic files except every basename `MEMORY.md`. Read at most the first 30 lines for frontmatter, settle files independently so one failure does not abort the scan, sort successful headers newest-first by modification time, and retain at most 200. Missing/invalid type remains untyped rather than being coerced. The manifest exposes filename, timestamp, optional description, and valid type but not full content.

**MC-MEM-007 — Relevance selection.** Ask the product's default Sonnet-class selector to return at most five manifest filenames with a structured JSON result and a 256-token ceiling. Filter every returned name against the offered manifest. Exclude paths already surfaced before selection so they do not consume its five slots. Supply recently successful tool names so ordinary reference/API memories for tools already working are discouraged, while warnings/gotchas remain eligible. Failure, malformed output, or abort yields an empty selection. If the prompt explicitly mentions configured agents, search only their declared memory directories; otherwise search automatic memory. Combine multiple selected directories concurrently and cap the aggregate at five.

**MC-MEM-008 — Nonblocking prefetch and injection.** Start relevance selection once from the last non-meta, multiword user prompt, chained to the turn abort. It runs while the main response and tools proceed. At the post-tool collection point, consume it only if already settled; never wait for it. If it is still running, give later iterations of the same turn another zero-wait chance, then abort/dispose it when the query exits. Before injection, remove paths read/written/edited during any iteration and mark surviving paths in the cumulative read cache. Inject each selected file as a system-reminder attachment with at most 200 lines and 4,096 bytes; retain a useful prefix plus a truncation notice. Stop new prefetch once currently projected relevant-memory attachments total 60 KiB.

**MC-MEM-009 — Freshness and compaction interaction.** A future modification time clamps to age zero. Memories older than one day carry a warning that code/file-line claims are point-in-time and must be verified. Track surfaced paths and byte total by scanning the current message projection rather than a hidden durable counter. Compaction therefore intentionally resets this relevance budget when old attachments leave context. Cache invalidation may re-read changed entrypoints without pretending that a sync pull is a new human instruction event.

**MC-MEM-010 — Explicit empty recall.** A successful bounded memory list or relevance recall returns an ordered collection even when no eligible entry exists. Represent that outcome as an explicit empty collection, not an absent/null value; reserve unavailable and failed outcomes for their distinct error paths.

**MC-MEM-011 — Direct-store recall bounds.** Enumerate a direct persistent-memory store through its pinned directory descriptor and materialize no more than 512 directory entries. Before reading candidates, cap their aggregate declared size at 8 MiB; recheck the same aggregate against bytes actually read so pathname races cannot expand the workload. Exceeding either boundary fails the complete recall with `ErrRecallLimit` rather than returning an order-dependent partial context. Candidate ordering remains deterministic.

## Team memory synchronization

**MC-TEAM-001 — Scope and trust.** Team memory exists only when automatic memory and the team-memory runtime gate are both enabled. Store it under `<automatic-memory>/team/`, with its own `MEMORY.md`. Prompt instructions distinguish private and shared destinations; load shared entrypoint content inside an explicit `team-memory-content source="shared"` wrapper and treat it as untrusted collaborator-authored context, never higher-priority system policy.

**MC-TEAM-002 — Server key containment.** Treat every server entry key as untrusted. Reject null bytes, absolute paths, backslashes, encoded traversal/separators, and compatibility-normalized Unicode traversal. Normalize and require lexical containment, then resolve the deepest existing ancestor and require real-path containment. Reject dangling symlinks, loops, inaccessible containment checks, and prefix lookalikes. Skip one unsafe entry without aborting valid siblings.

**MC-TEAM-003 — Availability and session state.** Network sync additionally requires build inclusion, first-party OAuth on the first-party service with inference and profile scopes, and a GitHub repository slug. Keep ETag/checksum, believed per-entry server hashes, and any learned server entry limit in one watcher-owned session state; none is a durable local sync journal. Startup creates this state only after availability checks.

**MC-TEAM-004 — Pull semantics.** Perform conditional GET with the current checksum unless explicitly requesting a full pull. Treat `304` as unchanged and `404` as an empty server that clears stale believed hashes. Retry timeout/network/eligible unknown failures up to three retries after the first attempt, but not authentication or schema failures. Validate the response before mutation. Refresh believed hashes when provided; if absent, leave them empty so the next push is full. Server content wins per key: independently validate, size-check, compare, create parents, and overwrite local content; matching content is skipped to preserve modification time. A remote omission never deletes a local file. Invalidate memory-file caches only if at least one write succeeds.

**MC-TEAM-005 — Local scan and secret boundary.** Recursively scan every regular file in the team directory, not only Markdown. Skip unreadable files and files above 250,000 encoded bytes. Before upload, scan content for credential patterns; omit the complete matching file and report only its path to the user plus safe rule label, while analytics records rule IDs but neither path nor secret value. File Write/Edit validation applies the same secret guard before the model can place sensitive content in shared memory.

**MC-TEAM-006 — Delta and deletion semantics.** Read the local snapshot once per push, hash each included file, and upload only keys whose hash differs from the session's believed server hash. Server PUT is upsert: absent local keys are not deletion requests, so deleting locally does not delete remotely and a later pull can restore them. A full `sync` is pull-first then push, so server content wins same-key conflicts before delta computation. A watcher-triggered push does not pull first and intentionally lets the active local edit overwrite that server key after conflict probing.

**MC-TEAM-007 — Batching and partial commit.** Sort delta keys and greedily form request bodies under the 200,000-byte soft limit; a single file may form a larger solo batch within its 250,000-byte file cap. Each batch is an independent committed upsert. Thread the returned checksum and update believed hashes after each success. If a later batch fails, report the overall push incomplete with the number already uploaded; do not pretend atomic rollback or re-upload committed earlier batches on the next attempt.

**MC-TEAM-008 — Optimistic conflict.** Send the current checksum as the match precondition. On `412`, fetch hashes-only metadata, replace believed hashes, recompute the delta against the unchanged local snapshot, and retry at most two times after the original attempt. Do not download bodies or merge into disk during this path. Identical concurrent content falls out of the delta; a locally changed same key overwrites the server on retry, while server-only keys arrive on a later pull.

**MC-TEAM-009 — Learned limits.** There is no client default entry-count cap. Learn a positive server limit only from the structured too-many-entries `413`; the current push remains failed. On later pushes, sort keys and include the first N, so later alphabetical keys remain local-only. Because the server validates merged entry count and this protocol has no deletion operation, even a truncated delta can continue failing until server-side entries are removed by another mechanism.

**MC-TEAM-010 — Watcher lifecycle.** At startup, pull before starting the recursive directory watcher so pull writes do not echo. Start watching even after an empty server or failed initial pull. Debounce local events for two seconds; serialize pushes, and if another edit arrives during a push, retain pending work and re-arm after completion. Post-tool File Write/Edit hooks explicitly schedule the same push as a backup for missed/coalesced filesystem events. A watcher-start failure leaves session sync state available so explicit notifications can still push.

**MC-TEAM-011 — Suppression, shutdown, and crash.** Suppress repeated watcher pushes after missing OAuth/repository or a permanent client error other than conflict/rate limit. An observed unlink clears suppression and schedules another attempt. Shutdown closes the watcher, awaits an in-flight push, then best-effort flushes pending changes within the surrounding short shutdown budget. A process crash can lose debounce/session state while leaving files. On next startup the required pull occurs before any push; consequently an unsynced local same-key edit can be overwritten by server content. This is a documented loss window, not two-way transactional synchronization.

## Automatic dream consolidation

**MC-DREAM-001 — Enablement and trigger position.** An explicit `autoDreamEnabled` setting overrides the remote-experiment default. Disable automatic dream in assistant/KAIROS mode, remote mode, when automatic memory is off, or when the feature is unavailable. Initialize its runner during background housekeeping; invocation before initialization is a no-op. On a main-agent, non-bare successful stop path, launch it fire-and-forget alongside prompt suggestion/extraction and before executing user Stop hooks. A later blocking Stop hook does not retract already launched consolidation.

**MC-DREAM-002 — Scheduling gates.** In cheapest-first order require: at least 24 hours since the consolidation lock timestamp, no session scan within the last 10 minutes, and at least five other valid project transcript sessions touched since that timestamp. Exclude the current session. Configuration may replace the positive hour/session thresholds. A test-only force path may bypass enable/time/session checks but not the memory-directory precondition; production behavior remains bounded.

**MC-DREAM-003 — Lock and rollback.** Use `<memory>/.consolidate-lock`; its modification time is both last-success time and acquisition time, and its body is the holder process ID. A recent lock under one hour blocks only when that process is live; dead/unparseable holders are reclaimable, and a lock older than one hour is stale even if the ID is live. Acquisition writes the candidate ID then re-reads it so the last writer wins a race; this is not an operating-system atomic lock. Success leaves the new timestamp. Failure or explicit kill restores the prior timestamp and clears/unlinks the holder. A crash leaves a recent dead holder that the next process can reclaim.

**MC-DREAM-004 — Fork authority and prompt.** Run one transcript-skipping, cache-sharing fork with query source `auto_dream`. Its prompt names the automatic-memory root, transcript directory, and selected session IDs and tells the worker to use narrow transcript searches. Permit memory-root Read/Edit/Write operations and only read-only shell commands; deny state-changing shell commands and writes outside the memory root. Bound the fork through ordinary agent limits and expose cancellation through its dedicated controller.

**MC-DREAM-005 — Live task projection.** Register a UI-only dream task with phase `starting`, switch to `updating` when an observed Edit/Write tool request first names a path, deduplicate those paths, and retain at most 30 assistant turn summaries. Observed touched paths are a lower bound because unrecognized/write-through-shell activity is not captured. Completed, failed, and killed dream tasks set `notified=true` because they never enqueue a model-facing task completion.

**MC-DREAM-006 — Completion and failure.** On fork success, mark the live task complete and keep the acquired timestamp. Append an inline “Improved” memory-saved message only when at least one path was observed. Do not roll back file edits if later reporting fails. On fork failure, mark failed and roll back the timestamp; on explicit kill, the task abort path performs the rollback and the outer failure handler must not repeat it. Gate/read/scan/lock failures are diagnostics and do not fail the user's turn.

**MC-DREAM-007 — Manual and crash caveats.** Manual dream records the consolidation timestamp optimistically when its prompt is built, without a completion callback; a failed manual run can therefore suppress automatic consolidation until the time threshold passes. The lock is scheduling evidence, not a transaction over memory files: a process crash during edits can leave partially consolidated files while a later run is delayed or reclaims the lock. Implementors must not infer that a fresh timestamp proves every intended memory update completed.

## Constants

**MC-110 — Required defaults.** Preserve:

| Concern | Default |
| --- | ---: |
| compact summary output reserve | 20,000 tokens |
| automatic-compaction buffer | 13,000 tokens |
| warning/error buffer | 20,000 tokens |
| blocking/manual reserve | 3,000 tokens |
| consecutive automatic failures | 3 |
| compact prompt-too-long retries | 3 |
| optional compact streaming attempts | 2 |
| compact keepalive | 30 seconds |
| restored files | 5 |
| file restore total/per-file | 50,000 / 5,000 tokens |
| skill restore total/per-skill | 25,000 / 5,000 tokens |
| session memory initialization/growth/tools | 10,000 / 5,000 / 3 |
| extraction wait/stale | 15 / 60 seconds |
| session memory section/total guidance | 2,000 / 12,000 tokens |
| session-memory compact min/text-count/max | 10,000 / 5 / 40,000 |
| time microcompact enabled/gap/keep | false / 60 min / 5 |
| API edit trigger/target | 180,000 / 40,000 tokens |
| `MEMORY.md` index | 200 lines / 25,000 UTF-16 code units |
| topic header scan | 30 lines / 200 newest files |
| relevance selection | 5 files / 256 selector-output tokens |
| relevant-file surface | 200 lines / 4,096 bytes each / 60 KiB projected total |
| team sync request / file / soft batch | 30 s / 250,000 bytes / 200,000 bytes |
| team pull / conflict attempts | 4 / 3 total attempts |
| team watcher debounce | 2 seconds |
| auto-dream hours / prior sessions / scan throttle | 24 h / 5 / 10 min |
| auto-dream stale holder / retained UI turns | 1 h / 30 |

## Acceptance scenarios

**MC-A01 — Disabled auto only.** Automatic compaction is disabled but global compact is enabled. No proactive compact occurs; manual compact still succeeds.

**MC-A02 — Circuit breaker.** Three automatic summary attempts fail. Later turns skip proactive attempts instead of issuing more summary calls; manual or explicit recovery remains separately available.

**MC-A03 — Full compact.** A conversation with images, active plan mode, two invoked skills, recent files, and MCP deltas produces a text-only summary request and installs boundary, hidden summary, bounded restorations, and hooks in MC-031 order.

**MC-A04 — Prompt-too-long compact.** The first compact request exceeds provider context. Oldest API rounds are removed without leaving assistant-first input; retry succeeds by the third attempt and records that loss explicitly.

**MC-A05 — Partial `from`.** Earlier messages remain before a boundary-anchored summary of the tail. Resume uses preserved metadata and retains earlier prompt-cache prefix.

**MC-A06 — Partial `up_to`.** Prefix becomes a summary, old boundaries are removed from the kept suffix, and preserved head anchors to the new summary.

**MC-A07 — Tool-pair preservation.** A preservation index lands between streamed thinking, tool-use, and result fragments. It expands backward so normalized postcompact messages contain every required tool-use and thinking fragment.

**MC-A08 — Session-memory fallback.** Memory exists but the resulting summary plus 40K preserved tail exceeds auto threshold. Session-memory strategy returns no result and ordinary compaction runs.

**MC-A09 — Extraction safety.** A memory fork requests shell or edits another path. Permission denies it; main conversation file cache and transcript remain untouched.

**MC-A10 — Cold-cache gap.** After a 61-minute gap with time-based strategy enabled, all but the five newest eligible results are cleared before the request, cached-edit state resets, and at least one result is always retained.

**MC-A11 — Subagent compact.** A subagent compacts successfully. Shared main-thread user context, memory discovery, and context-collapse commit log remain intact.

**MC-A12 — Reactive failure.** Prompt-too-long survives one collapse drain and one reactive compact. The original model error is surfaced; stop hooks do not create an infinite continuation loop.

**MC-A13 — Memory path override attack.** A repository-controlled setting points automatic memory at a sensitive home directory and a trusted setting points at a home-relative topic directory. The repository setting is ignored, the trusted nontrivial suffix is expanded/validated, and root/home/ancestor collapses remain rejected.

**MC-A14 — Relevance is nonblocking.** A selector remains pending through the first post-tool collection point and settles before the second iteration. The first point waits zero time, the second injects it once, and query exit aborts an unresolved selector without failing the turn.

**MC-A15 — Relevance bounds and freshness.** Six files are selected including one 3-day-old 20 KiB file. At most five surface, the large file carries only its 4,096-byte/200-line-bounded prefix plus truncation notice, and its header warns that code claims require current verification.

**MC-A16 — Compaction resets recall budget.** Relevant-memory attachments reach 60 KiB and suppress further selection. After compaction removes those attachments from the active projection, scanning that projection reports reclaimed budget and a later genuinely relevant file may surface again.

**MC-A17 — Team pull containment.** A server response contains a valid file, lexical traversal, encoded traversal, dangling-symlink escape, and oversized entry. Only the valid bounded file is written; unsafe siblings are skipped independently and cannot escape the team directory.

**MC-A18 — Team deletion is nonpropagating.** Delete a local shared-memory file and push. The request contains no deletion marker; the server copy remains and the next pull restores it locally.

**MC-A19 — Team partial batch commit.** A delta forms three sorted batches and the third fails. The first two remain committed with updated believed hashes, the operation reports incomplete with their uploaded count, and retry does not re-upload them.

**MC-A20 — Team conflict policy.** A `412` is followed by a hashes-only response in which one key now matches local and another differs. The matching key drops from the retry; the differing local key overwrites server content without a body merge; server-only content is not written locally until a later pull.

**MC-A21 — Team secret defense.** One team file contains a recognized credential. File Write/Edit validation rejects newly authored secret content; an existing matching file is omitted from upload; no log/telemetry contains the secret value.

**MC-A22 — Unsynced edit crash.** A local same-key edit is waiting in the debounce window when the process crashes. On restart, session sync state is empty and startup pulls before pushing, so server content may overwrite the unsynced edit. The implementation exposes this compatibility window rather than claiming transactional two-way sync.

**MC-A23 — Dream lock race and rollback.** Two processes try to acquire consolidation; only the process ID observed on re-read proceeds. Inject fork failure and verify the prior timestamp/body state is restored. Inject a crash and verify a later process reclaims the dead holder even before one hour.

**MC-A24 — Dream stop-hook ordering.** Launch automatic dream from a valid main stop phase, then make the user Stop hook block. Consolidation was already started and continues independently; the blocked user turn still follows ordinary hook continuation semantics.

**MC-A25 — Dream progress is incomplete evidence.** The consolidator edits one file through the supported edit capability and changes another through an unobserved mechanism. The UI flips to updating and lists the observed path only; completion does not claim the list is exhaustive, and no model-facing task notification is enqueued.

**MC-A26 — Empty memory recall.** Recall from an empty private store and from a store whose entries are all filtered. Both succeed with an explicit empty collection that serializes as `[]`; a disabled or inaccessible store remains a distinct unavailable/error outcome.

**MC-A27 — Bounded direct-store recall.** Populate a private store with 513 directory entries, then with individually valid memory files whose aggregate size exceeds 8 MiB. Both recalls fail with `ErrRecallLimit` before unbounded enumeration or content retention; a store within both limits still returns the same deterministic relevance order.

## Non-normative provenance

Behavior was specified primarily from `services/compact/autoCompact.ts`, `compact.ts`, `microCompact.ts`, `apiMicrocompact.ts`, `sessionMemoryCompact.ts`, `postCompactCleanup.ts`, `grouping.ts`, `services/SessionMemory/*`, `memdir/*`, relevant-memory attachment/query-loop integration, `services/teamMemorySync/*`, `services/autoDream/*`, `tasks/DreamTask/*`, transcript compact metadata, and optional context-collapse call sites. Some context-collapse implementation is build-excluded in this source snapshot; its documented rules are the observable integration contract rather than a prescribed algorithm.
