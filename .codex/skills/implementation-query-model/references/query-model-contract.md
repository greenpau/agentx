# Query and Model Runtime Contract

This reference specifies the language-neutral behavior of the shared conversation engine and its model transport boundary. “Must” denotes implementation-critical behavior. Feature-gated rules apply only when their stated gate is enabled.

## Table of contents

- [Responsibility and vocabulary](#responsibility-and-vocabulary)
- [Data and state models](#data-and-state-models)
- [Turn admission and outer-engine lifecycle](#turn-admission-and-outer-engine-lifecycle)
- [Complete query-iteration ordering](#complete-query-iteration-ordering)
- [Conversation projection and message normalization](#conversation-projection-and-message-normalization)
- [Model request construction](#model-request-construction)
- [Streaming protocol and assembly](#streaming-protocol-and-assembly)
- [Continuation, hooks, and terminal decisions](#continuation-hooks-and-terminal-decisions)
- [Derived assistance and speculative execution](#derived-assistance-and-speculative-execution)
- [Retry, fallback, and recovery](#retry-fallback-and-recovery)
- [Cancellation and cleanup](#cancellation-and-cleanup)
- [Usage, limits, and structured output](#usage-limits-and-structured-output)
- [Configuration, constants, and disabled behavior](#configuration-constants-and-disabled-behavior)
- [Collaborator contracts](#collaborator-contracts)
- [Acceptance scenarios](#acceptance-scenarios)
- [Provenance](#provenance)

## Responsibility and vocabulary

**QM-001 — Shared semantics.** One conversation engine must define input acceptance, model invocation, tool continuation, retry, limits, and terminal outcomes for every product surface. A surface may translate events but must not assign different meanings to shared operations.

**QM-002 — Four histories.** Keep these representations distinct:

1. durable conversation history, retaining resumable semantic events;
2. mutable in-process conversation history for the active engine;
3. API-bound history, a temporary provider-compatible projection;
4. presentation state, including partial text, spinners, progress, and tombstones.

A transformation required by one provider must affect only item 3 unless a separate contract deliberately records it.

**QM-003 — Turn and iteration.** A *turn* begins when input is accepted and ends with one result. An *iteration* is one model request plus its recovery, hook, or tool continuation. A turn may contain multiple iterations.

**QM-004 — Accepted tool use.** A model tool-use block becomes accepted when the engine publishes it for execution. Every accepted tool-use identifier must receive exactly one terminal tool-result block in the logical attempt, including cancellation and sibling-failure results.

**QM-005 — Terminal outcomes.** Represent success, maximum-turn termination, maximum-cost termination, structured-output failure, cancellation, model/runtime failure, and other policy terminals explicitly. A transport exception is not user cancellation.

## Data and state models

### Engine state

**QM-010 — Engine lifetime.** One engine instance may process several submitted turns and retains:

- mutable conversation messages;
- file-read cache returned by tools;
- aggregate usage for the engine lifetime;
- an abort controller for the active work;
- recorded permission denials for the surface result;
- nested-memory paths already loaded;
- whether an orphaned permission request has been reconciled.

Clear turn-discovered skill names at the start of every submitted turn so discovery metrics and prompts do not grow without bound.

**QM-011 — Turn state.** A turn tracks start time, initial model and thinking policy, turn count, current-message usage, latest assistant stop reason, structured-output call baseline, error-log watermark, accepted input messages, and any replay acknowledgements.

**QM-012 — Iteration state.** Carry these values across iterations:

- current message sequence and tool context;
- automatic-compaction tracking;
- maximum-output recovery count;
- whether reactive compaction was attempted;
- optional maximum-output override;
- optional pending tool-use summary;
- whether a stop hook caused the current retry;
- model-turn count;
- transition reason and transition-specific metadata.

Use a fresh state value for every transition; do not infer recovery state solely from message text.

**QM-013 — Query configuration snapshot.** At query entry, snapshot the session identity and runtime gates whose contracts tolerate staleness, including streaming tool execution, tool-use summary emission, internal-user behavior, and fast-mode availability. Compile/build inclusion gates remain at their guarded behavior and are not converted into mutable runtime data.

**QM-014 — Message model.** Semantic history may contain user, assistant, attachment, progress, and system events. Only user and assistant messages form the provider dialogue. Attachments are translated into user content; eligible local-command system events become user content; progress and most system events never reach the model.

**QM-015 — Assistant block model.** A streamed response may produce several assistant records sharing one provider message identifier, normally one completed content block per record. Preserve this split internally for streaming and transcript timing, then merge matching siblings in the API projection.

**QM-016 — Usage model.** Track per-message cumulative usage separately from aggregate turn/engine usage. Preserve input, output, cache creation/read, service tier, geographic region, iteration count, and provider-specific cache details when available.

**QM-017 — Query identity.** Assign an identity and depth to each query chain. Derive a request's previous-request identifier from the latest assistant in that request's own message chain; never use one process-global value that concurrent main, subagent, teammate, rollback, or fork requests can overwrite.

## Turn admission and outer-engine lifecycle

**QM-020 — Admission order.** Submit a turn in this order:

1. clear turn-scoped discovery state and establish the operational working directory;
2. snapshot persistence eligibility, initial app state, model, thinking policy, and start time;
3. wrap permission evaluation so every non-allow result is collected for the final surface result;
4. fetch prompt parts and construct the effective system prompt;
5. register structured-output enforcement when a schema and its synthetic output capability are both present;
6. reconcile an orphaned permission request once per engine lifetime;
7. parse the input, expand commands/references, and obtain messages, local-command decision, allowed tools, and optional model override;
8. append accepted messages to mutable history;
9. persist accepted input before starting a model request;
10. update command-scoped allow rules;
11. load cache-only skill/plugin metadata concurrently and publish session initialization;
12. either return the local-only result or enter the query loop.

**QM-021 — Prompt composition.** Resolve default prompt, user context, and system context concurrently. A caller-supplied custom system prompt replaces the default prompt and suppresses ordinary system context, but user context still applies. If a caller explicitly supplies a memory-path override with a custom prompt, append memory-mechanics instructions. Append text follows the selected prompt. An explicit higher-level override that is defined as a complete replacement must also replace append text.

**QM-022 — Pre-request durability.** Persist accepted user messages before the first model request. Interactive/resumable modes must await this append. A deliberately bare scripted mode may enqueue the append without waiting. Eager-flush and cowork-like modes must flush before model invocation. A process killed after acceptance but before response must still resume at the user message.

**QM-023 — Local-only commands.** If input processing says no model query is needed, publish eligible local command output and compact-boundary acknowledgements, persist the resulting history, flush where required, emit a success result with no model stop reason, and stop.

**QM-024 — Initialization event.** Publish one initialization event per submission after resolving the effective tool, command, agent, skill, plugin, model, permission, and fast-mode metadata. Metadata discovery for this event must not require a network refresh when the surface contract promises cache-only startup.

**QM-025 — Assistant persistence race.** Assistant transcript appends may be queued without awaiting because later stream deltas mutate the last completed assistant record's cumulative usage and stop reason. Serialization must occur lazily enough to capture those terminal fields. User, compact-boundary, attachment, and deliberate context-changing records preserve their required ordering.

**QM-026 — Result-before-process-exit.** On surfaces allowed to terminate immediately after a result event, flush durable state before publishing the terminal result.

**QM-027 — Persistence milestones.** Distinguish semantic admission, submission to transcript storage, FIFO enqueue, completed local append, completed remote append, and explicit flush. Awaiting transcript submission for a user or tool-result message ordinarily guarantees ordered enqueue and any synchronous remote step, not local disk flush. Assistant blocks may be submitted fire-and-forget so their terminal usage fields can settle before lazy serialization. Neither tool completion nor an unsafe execution barrier proves that any earlier result reached disk.

**QM-028 — Engine clock boundary.** Treat a configured engine clock as an external callback. Contain panic and zero-time results with the host wall clock. Never invoke it while holding turn serialization: an active turn claims a short clock boundary, releases the turn mutex, invokes the callback, and reacquires ownership before publishing the sampled time. Public turn-mutating entrypoints observe that claim and return `ErrBusy`, so a clock that reenters submit, clear, compact, restore, or reasoning configuration cannot deadlock or mutate half-finished state. Preserve total-turn and model-API duration semantics, including monotonic subtraction for the production clock.

## Complete query-iteration ordering

**QM-030 — Iteration pipeline.** Every iteration must preserve this dependency order:

1. publish request-start and advance query-chain tracking;
2. select history after the active compaction boundary;
3. aggregate tool-result budget and persist oversized results where eligible;
4. apply explicit history snips;
5. apply microcompaction;
6. project or commit staged context collapse;
7. evaluate automatic compaction;
8. build the current capability registry and model request;
9. stream the response and, when enabled, begin concurrency-safe tool execution from completed tool-use blocks;
10. resolve stream failure, model fallback, prompt overflow, media overflow, maximum-output recovery, or cancellation before ordinary continuation;
11. if no tool use remains, process stop hooks and token-budget continuation;
12. otherwise drain all tool results and context modifiers;
13. start an optional tool-use summary without delaying the next model request;
14. handle post-tool cancellation or continuation prevention;
15. append assistant blocks, tool results, attachments, and eligible queued notifications in semantic order;
16. consume settled memory and skill-prefetch results at their documented points;
17. refresh dynamically changing tools between model turns;
18. increment and enforce the model-turn limit;
19. transition to the next iteration or return a terminal reason.

**QM-031 — Projection timing.** Context projection and compaction occur before request construction; tool results and queued user notifications affect only a later iteration. Never interleave an ordinary queued user message between an assistant tool-use group and its results.

**QM-032 — Tool continuation signal.** Determine whether tools are needed from actual tool-use content blocks. Do not rely on the provider stop reason, because it may be absent or incorrect.

**QM-033 — Dynamic capability refresh.** Tool availability may change between iterations, for example when an external capability provider connects. Refresh at the turn boundary, not while validating an already accepted block.

**QM-034 — Command lifecycle.** Mark queued commands complete only when the outer query generator returns normally. If it throws or its consumer abandons iteration, retain evidence that processing started but did not complete.

**QM-035 — Compaction accounting.** When task-budget accounting is active, subtract the authoritative final pre-compaction context usage from the remaining task budget. Carry the remainder across every later compact operation.

## Conversation projection and message normalization

### General projection

**QM-040 — Pure API projection.** Build a fresh provider-bound sequence for every attempt. Do not mutate stored content merely to repair provider constraints, add request-local tags, remove unsupported fields, or suppress historical media.

**QM-041 — Attachment movement.** Before conversion, scan history from newest to oldest and bubble attachments earlier until they encounter an assistant message or a user tool-result message. Preserve attachment order. Then translate attachments into model-visible user content.

**QM-042 — Display-only filtering.** Remove virtual user/assistant records, progress, ordinary system records, and synthetic API-error records from the provider request. Translate eligible local-command output into user content so later turns can reference it.

**QM-043 — Content-block normalization.** A helper that exposes one semantic content block per message must split multi-block user and assistant messages deterministically. Once a split changes the chain, derive deterministic identifiers for that message and later messages from the original identifier and block index. Preserve image-paste identifiers with their corresponding image block.

**QM-044 — Consecutive users.** Merge adjacent user messages because some providers reject consecutive user roles. Insert one newline at a text-to-text seam so separate prompts never concatenate lexically. Preserve the non-meta user's stable identity. A merged message is meta only if every operand is meta when history-snip semantics are active.

**QM-045 — Tool-result position.** Within a merged user message, place all tool-result blocks before other blocks. When a user attachment/reminder follows a tool result, fold compatible content into the tool result where the active compatibility behavior requires it; never mix tool references with content types the provider forbids.

**QM-046 — Error tool-result media.** A tool result marked as an error must contain text only. Strip non-text children from old/resumed error results and combine surviving text with a blank-line separator. If none survives, retain a valid empty textual form permitted by the provider contract.

**QM-047 — Assistant sibling merge.** Merge assistant records sharing one provider message identifier by concatenating blocks in stream order. While walking backward, skip tool-result users and assistant siblings from other concurrent responses; stop at another semantic user boundary.

**QM-048 — Tool input projection.** Canonicalize each tool name and apply its API-specific input normalizer to the copied block. If tool search is unavailable, remove tool-search-only caller metadata and remove tool-reference blocks from results. If tool search is available, retain references only for currently available tools, replacing an emptied reference result with explanatory text.

**QM-049 — Media failure quarantine.** When a synthetic size/format error follows a meta user attachment, identify the nearest eligible preceding meta message and strip only the rejected document/image types from that message on later requests. Do not repeatedly resend known-invalid media.

### Final normalization passes

**QM-050 — Thinking cleanup order.** Remove truly orphaned thinking-only assistant records, then remove trailing unpaired thinking from the final assistant, then filter whitespace-only assistants, then repair non-final empty assistant content. The order is mandatory because a later removal can expose an invalid whitespace-only record.

**QM-051 — Orphaned thinking definition.** A thinking-only record is not orphaned when another assistant record with the same provider message identifier contains non-thinking content and will be merged with it. Otherwise exclude it from the request.

**QM-052 — Empty assistant rule.** Every non-final assistant must have non-empty content; insert a neutral placeholder when recovery leaves it empty. The optional final assistant may remain empty when used as provider prefill.

**QM-053 — Historical message tags.** If history-snipping is compiled, runtime-enabled, and not in fixture/test mode, append a deterministic short message tag to the final text block of each non-meta user message after merging. Never persist the tag or emit it when the corresponding snip capability is unavailable.

**QM-054 — Image validation.** Validate all surviving images against provider limits immediately before request construction. Independently cap total request media at 100 by silently removing the oldest excess items, retaining the newest relevant media.

### Pairing repair

**QM-055 — Bidirectional pairing validation.** After normalization, validate both directions:

- each client tool-use block is followed by one result with the same identifier;
- each tool-result identifier refers to a tool use in the directly preceding assistant group;
- identifiers are unique across assistant records and results;
- provider-executed tool-use blocks have their provider result within the same assistant message where required.

**QM-056 — Default repair.** In normal mode:

- remove duplicate tool uses after the first;
- remove duplicate and orphan tool results;
- insert synthetic error results for missing client tool results;
- remove provider-side tool uses missing their required provider-side result;
- insert a neutral assistant/user placeholder if a removal would violate non-empty-content or role-alternation rules.

Synthetic missing-result content must state that the result is unavailable, not pretend execution succeeded.

**QM-057 — Strict pairing.** In strict trajectory/training mode, any pairing defect must terminate validation instead of injecting synthetic context. Include enough structural diagnostics to identify affected roles and identifiers without exposing secrets.

**QM-058 — Signature invalidation.** After an authentication identity change, remove thinking, redacted-thinking, and other credential-bound signed blocks before reuse. Their signatures are not portable between credentials.

## Model request construction

**QM-060 — Per-attempt parameters.** Recompute request parameters for every retry so refreshed authentication, fast-mode state, and a recovery maximum-output override take effect. Preserve turn-level gates that were deliberately snapshotted.

**QM-061 — Model and provider resolution.** Resolve provider-specific model aliases or inference profiles before capability checks. A model off-switch may reject an ineligible request before transport without affecting unrelated models or eligible subscribers.

**QM-062 — Deferred capability search.** Enable deferred tool search only when runtime mode, model, policy, and capability thresholds permit it. Disable it when there are no deferred tools and no providers still connecting. When enabled, send all ordinary tools, the search tool, and only deferred tools already discovered in history. Pending providers keep search available.

**QM-063 — Tool-search compatibility.** Use the provider-appropriate beta/header placement. If the chosen model cannot consume old tool-search fields after a model switch, strip those fields from the API projection even if the saved session contains them.

**QM-064 — Request fingerprint.** Compute attribution/fingerprint data from normalized real conversation input before inserting synthetic deferred-tool announcements. Synthetic inventory must not redefine user-input attribution.

**QM-065 — System prompt envelope.** Prepend attribution and product/surface identity to the effective system prompt. Append advisor or environment-integration instructions only when their capability and beta are active. Build cache breakpoints from stable/volatile section policy without changing prompt semantics.

**QM-066 — Thinking policy.** An explicit disable switch wins. For models supporting adaptive thinking, use adaptive unless adaptive is disabled. Otherwise use the explicit/default budget and keep it below maximum output by at least one token. Send temperature only when thinking is disabled.

**QM-067 — Previous request chaining.** Derive the previous-request identifier from the request's own message array. Removing messages through rollback naturally changes the chain; concurrent query chains remain isolated.

## Streaming protocol and assembly

**QM-070 — Stream states.** Track at least: request created, awaiting first event, message started, block started, block accumulating, block completed, message delta received, message stopped, fallback active, and closed.

**QM-071 — First event.** On message start, record time-to-first-token, initialize the partial message, and apply initial usage. A stream with completed terminal semantics may contain no content blocks; a stream that never starts a message is incomplete.

**QM-072 — Block initialization.** On content-block start:

- initialize text and thinking payloads as empty because some transports repeat the start payload in a delta;
- initialize tool and provider-tool input as an empty JSON string accumulator;
- preserve block index and immutable metadata;
- reject later deltas whose type cannot apply to the block at that index.

**QM-073 — Delta handling.** Append text, thinking, signature, and partial JSON deltas to their matching accumulators. Signatures are not visible output and must not inflate displayed output length or output-speed measures.

**QM-073A — Credential-safe provider deltas.** The shared engine, not only a concrete provider adapter, owns the final credential boundary for incremental model text. For each model response with a nonempty credential union, create one fresh stateful stream projector before reading the first event. Feed text deltas to it in provider order, retain the minimum possible credential suffix across intervening deltas, and append or publish only bytes released as safe. On legitimate stream completion, flush the remaining safe suffix exactly once before terminal projection; on protocol error or cancellation, discard any retained ambiguous suffix. Independently sanitize the completed response object. Per-delta stateless replacement is forbidden because two individually safe progress events may concatenate into a credential. Provider-owned filtering may add defense in depth but cannot replace this shared boundary.

**QM-073B — Credential-safe terminal compositions.** Independently sanitized provider items are not yet safe to accept. Before flushing the retained stream suffix or admitting terminal output to history, transcript, presentation, or the turn outcome, construct and inspect the exact replayable provider-output JSON array, the newline-joined text of all assistant messages, and the newline-joined final-answer selection. Reapply the complete credential union and any legacy host sanitizer to each whole composition. If a literal is reconstructed only across item boundaries, phase filtering, JSON structural separators, or local provenance fields, reject the complete terminal response with a fixed protocol error; do not attempt a correlation-destroying rewrite of individual provider items. This boundary is mandatory even when no transcript store or downstream record validator is configured.

**QM-073C — Credential-safe function-call and event envelopes.** At the shared conversation-to-provider boundary, `FunctionCall.Arguments` is a nested JSON document even though the provider-neutral type stores it as a string. Immediately before every `Provider.Stream` call, semantically inspect every replayed function-call argument document, including decoded string/key/scalar aliases, duplicate keys, and its raw and canonical spellings; then encode and inspect the complete effective request, including its JSON structural separators and wrappers. This shared check applies to custom providers even when a concrete adapter also checks its own request. Before the engine becomes observable, inspect the complete session/model/reasoning identity projection and reject direct or cross-field credential reconstruction; sanitize status copies and keep invalid-effort, unknown-model, restore-mismatch, and duplicate-prompt diagnostics value-opaque. For response-created, response-in-progress, output-item, argument-delta, argument-done, and terminal-response surfaces, retain argument deltas inside the adapter until the one complete argument document is available and passes the same inspection; malformed or reflected documents terminate with a fixed protocol error, and failed, incomplete, cancelled, or abandoned streams discard unvalidated argument bytes. Before every successful public `Next` return, encode and inspect the complete canonical event envelope—including all scalar fields, nested call, response and error values, JSON structural separators, and wrappers—so independently safe fields cannot reconstruct a credential through framing. Classify transport and body failures only from exact sentinels, host-owned context state, detached provider values, and package-sealed snapshots. The host may traverse exact standard-library or package-owned wrapper implementations, but stops at a foreign child and never invokes foreign `Error`, `Is`, `As`, or `Unwrap` behavior; a blocking method cannot delay request, stream, retry, finalization, or shutdown, and an unknown failure receives a fixed provider diagnostic. Panic-isolate injected transport, body-close, retry clock, jitter, sleep, observer, and legacy text-sanitizer behavior so extension failure becomes suppression or a fixed provider failure rather than a process crash. Treat an injected HTTP client and every response body as hostile liveness adapters as well: the coordinator selects on caller, attempt, and watchdog cancellation; isolates at most one potentially blocked body-read loop per attempt; never waits for `Close`; and bounds each class of abandoned transport, read, or cleanup operation by the configured attempt count. A rejected request never reaches the provider, and a rejected event returns no partial event value.

**QM-074 — Block completion.** On content-block stop, normalize provider content and publish exactly one complete assistant record for that block, carrying provider message identity, request identity, timestamp, model, and currently known usage. Tool input JSON may arrive as a string; parse it, use an empty object on an invalid top-level stream value, report diagnostics, and then apply tool-specific correction without making correction failure fatal.

**QM-075 — Message delta.** Treat message-delta usage as cumulative for the response, not additive. Update the last completed assistant record in place with final usage and stop reason so lazy persistence and downstream consumers see the terminal values.

**QM-076 — Stream events.** Publish raw semantic stream events alongside completed messages for adapters that display incremental state. Presentation consumers must atomically replace partial text with the completed block to avoid a blank frame or duplicate rendering.

**QM-077 — Stream completion validation.** Accept an empty content response only if a legitimate terminal stop reason was received. If no message start, no completed block, and no valid terminal reason exist, treat the stream as incomplete and use the permitted non-stream fallback.

**QM-078 — Maximum-output signal.** Convert either an explicit maximum-output stop reason or model-context-window-exceeded terminal into a synthetic maximum-output error consumed by query recovery. Withhold it from ordinary consumers until recovery is exhausted.

**QM-079 — Watchdog.** When stream-watchdog behavior is enabled, reset an idle timer on every event, warn halfway through the configured idle interval, and abort/release the hung response at the full interval. Independently record inter-event stalls longer than 30 seconds without treating each stall as terminal.

**QM-080 — Non-stream fallback.** A permitted fallback request must use the same semantic request after adjusting parameters for non-stream limits. Default timeout is 300 seconds locally and 120 seconds in remote mode unless explicitly configured. A 404 during stream creation may invoke fallback. A configuration may disable fallback where duplicate execution risk is unacceptable, especially with streaming tools.

## Continuation, hooks, and terminal decisions

**QM-090 — No-tool response.** If no tool-use block remains after recovery, process in order: maximum-output recovery, API-error terminal handling, stop hooks, token-budget continuation, then normal completion.

**QM-091 — Stop-hook isolation.** Do not run ordinary stop hooks after an API error because no valid model answer exists. Run failure hooks separately. A top-level stop-hook implementation exception produces a visible warning but does not itself block completion.

**QM-092 — Blocking stop hooks.** A blocking hook contributes meta user messages and starts another iteration with `stop-hook-active=true`. Reset maximum-output continuation count but preserve the reactive-compaction guard to prevent compact/error/hook loops.

**QM-093 — Hook prevention.** If a stop hook explicitly prevents continuation, return a distinct terminal reason. For teammate contexts, after ordinary stop hooks pass, evaluate completion hooks for owned in-progress tasks and then teammate-idle hooks using the same prevent/block semantics.

**QM-094 — Tool result drain.** Drain streaming or ordinary tool execution completely. Normalize each result into API user content, observe continuation-prevention attachments, and apply returned context updates. A streaming executor may have started only safe tools; its remaining-results interface must still terminalize queued and running identifiers.

**QM-095 — Tool-use summaries.** When enabled, after a non-subagent tool batch start a product small/Haiku-class model summary request asynchronously with query source `tool_use_summary_generation`. Supply every tool name with serialized input/output truncated to 300 characters each and at most 200 characters of the last assistant text. Request one short, past-tense progress label. Await the previous batch's summary only after the next model response so it overlaps model latency. Empty output, serialization trouble, abort, or provider failure yields no summary, is diagnostic only, and never fails or enters the authoritative conversation.

**QM-096 — Queued input.** Drain eligible queued notifications only after tool results. If a sleep/wait capability ran, use the documented high-priority selection; otherwise take the ordinary next item. Exclude local slash commands. Route main-thread items and agent-scoped task notifications only to their owning query.

**QM-097 — Maximum turns.** Increment model-turn count only when preparing another model call. At the configured ceiling, emit a maximum-turn attachment/result and stop; do not accept another provider request.

**QM-098 — Fair terminal-result settlement.** Persist accepted tool results serially within one bounded batch-settlement deadline. Before each remaining result, divide the remaining wall-clock budget equally across that result and every sibling still awaiting an attempt, and give the current persistence attempt no more than that share. Continue attempting later siblings after a timeout or persistence failure, aggregate their errors, and append a result to live model history only when its durable commit is known. This fairness bound prevents one blocked result sink from consuming the batch deadline and starving later tool-use identifiers without weakening call/result parent correlation.

## Derived assistance and speculative execution

Derived assistance is optional, non-authoritative work. It may improve progress display or prepare the likely next turn, but it must not silently become durable conversation, change the completed answer, or weaken permission rules.

### Agent progress summaries

**QM-160 — Periodic agent summary.** Coordinator-mode background agents may own a periodic UI summary timer. Wait 30 seconds before the first attempt and schedule the next 30-second interval only after the previous attempt finishes, so requests never overlap. On each attempt, reload the current child transcript, require at least three messages, remove incomplete tool-call structures, and run a cache-sharing fork with query source `agent_summary`. Keep the parent's cache-key request parameters and tool schemas but deny every tool client-side. Select the first nonempty, non-error assistant text, instruct a present-tense 3–5-word file/function-specific label, and update only live agent progress. Failure is diagnostic. Stop clears the timer and aborts the active fork; no summary is transcript or terminal-result evidence.

### Next-prompt suggestion

**QM-161 — Suggestion enablement.** Resolve the explicit suggestion environment control first: a defined falsy value disables and a truthy value enables. Without an override, require the runtime experiment, an interactive session, a non-teammate context, and a setting not explicitly false. Per-turn CLI generation runs only for the exact main REPL query source. Noninteractive/SDK generation is normally disabled even though a separately invoked SDK helper can share validation and outcome telemetry.

**QM-162 — Stop-phase scheduling.** After a valid no-tool answer reaches stop processing, and outside bare/simple mode, launch suggestion generation fire-and-forget before executing user Stop hooks. This scheduling is derived bookkeeping, not a hook result: a later blocking or preventing Stop hook does not retract a suggestion request already started. API-error paths never reach ordinary stop processing, and auxiliary failure never changes the turn's result.

**QM-163 — Suppression gates.** Before and after acquiring live state, suppress generation when aborted; fewer than two assistant turns exist; the last assistant is an API-error record; the last response's uncached input plus cache-write plus output exceeds 10,000 tokens; prompt suggestions are disabled; a worker/sandbox permission is pending; elicitation is queued; permission mode is plan; or an external user's service limit is not allowed. Record a safe suppression reason without injecting a message.

**QM-164 — Cache-sharing suggestion fork.** Run a transcript-skipping, cache-write-skipping fork with query source `prompt_suggestion`. Preserve all API parameters that contribute to the parent cache key; do not lower effort/output or remove advertised tools merely to make the call cheaper. Deny all tool requests through the client permission callback. Inspect all returned assistant messages because a denied tool attempt may be followed by text, and retain the first assistant request ID separately for attribution.

**QM-165 — Output filter and live state.** Trim the first nonempty assistant text and accept only a single likely user utterance: normally 2–12 words, fewer than 100 characters, no newline or asterisk-based Markdown formatting, no multiple-sentence form, no prefixed label, no API/meta/silence marker, no evaluative thanks/praise, and no assistant-voice opening. Permit slash commands and a narrow set of useful one-word confirmations/actions. Store accepted text, prompt variant, generation request ID, and zeroed shown/accepted timestamps only in live presentation state. Acceptance telemetry uses exact input equality; the suggestion itself is not transcript content.

**QM-166 — Suggestion concurrency compatibility.** The compatibility launcher replaces the process-global “current suggestion” cancellation pointer when starting a new request but does not first abort an older request. General abort therefore reaches only the latest pointer, and an older request that later completes can still update live suggestion state. An implementation may add a generation token or cancel-before-replace rule as a safer race fix, but must identify that intentional divergence rather than claiming the reference already rejects stale completion.

### Speculative execution after a suggestion

**QM-170 — Gate, identity, and lifetime.** Speculation is an internal-only, separately configured optimization. Starting it aborts any currently active speculation, allocates an eight-character random identity, creates a child abort scope of the turn, snapshots the working directory, and creates `<temporary>/speculation/<process>/<id>/` as its copy-on-write overlay. Failure to create the overlay disables that attempt. Its messages, mutable refs, boundary, tool count, optional pipelined suggestion, and abort callback live only in application state; process restart does not resume them.

**QM-171 — Copy-on-write filesystem boundary.** For Edit, Write, and notebook edit, require a permission mode that can auto-accept edits: accept-edits, bypass, or plan with bypass availability. Otherwise record an edit boundary and abort. Deny writes outside the snapshotted working directory. Before the first write to an in-root relative path, copy an existing original to the overlay if present, then redirect the write there; remember newly created files too. Redirect later reads of a written path to the overlay. Safe reads outside the root remain ordinary reads and never gain write authority.

**QM-172 — Tool allowlist and boundary.** Permit the bounded read capabilities Read, Glob, Grep, ToolSearch, LSP, TaskGet, and TaskList, including default-working-directory reads. Permit shell only when the ordinary read-only command classifier accepts it; a missing, state-changing, or directory-changing command records a shell boundary and aborts. Every other tool records a denied-tool boundary and aborts. Require this permission callback for every speculative tool call; an advertised schema is never implicit authorization.

**QM-173 — Execution bounds and completion.** Run the fork with query source `speculation`, at most 20 model turns and 100 retained assistant/user messages. Abort at the message bound. Count only successful tool results as executed progress. If the fork returns normally, record a `complete` boundary with completion time and output tokens. Permission-required edit, stateful shell, and unknown tool are deliberate partial boundaries rather than errors. Provider/runtime error cleans the overlay and live state, logs diagnostics, and leaves ordinary user processing available.

**QM-174 — Safe accepted-message projection.** Before acceptance, remove thinking/redacted-thinking; every tool use/result pair lacking a successful result; error/interrupted/pending pairs; standalone interruption text; and empty/whitespace-only messages. Append the real accepted user input to live conversation first. If speculation is not fully complete, trim trailing assistant records so the injected speculative suffix ends at its last non-assistant record and require an ordinary follow-up query. If complete, inject the sanitized speculative messages and allow the turn to finish without re-executing the model. Merge attributable read-file evidence into the main read cache.

**QM-175 — Overlay commit and crash semantics.** On acceptance with at least one clean speculative message, abort the fork, copy every written overlay file to the main working directory sequentially/best-effort, then remove the overlay. File copies are not a transaction and the compatibility path ignores the aggregate copy-success boolean: sanitized messages may still be injected after one file copy failed. A crash before acceptance leaves the main tree unchanged but may orphan temporary overlay files; a crash during acceptance can leave only a prefix of files copied. A safer atomic workspace merge is an intentional divergence.

**QM-176 — Pipelining, abort, and telemetry.** Once speculation fully completes, it may generate the next suggestion against parent context plus accepted suggestion plus speculative messages. Promote that pipelined suggestion and start its speculation only after full acceptance; partial boundaries require a normal query and do not promote it. User typing/ignoring aborts the active fork, removes the overlay asynchronously/best-effort, and resets state without injecting speculative messages. Task-completion notifications may abort active speculation before changing the model-visible queue. Persist, at most, a best-effort telemetry record containing time saved; do not persist overlay state or speculative transcript as authoritative history. Any acceptance-handler exception fails open to ordinary processing of the user's input.

## Retry, fallback, and recovery

### Transport retry

**QM-100 — Retryability.** Retry connection failures, request timeout, lock timeout, eligible rate limits, authentication refresh failures, revoked-token failures, provider credential refresh failures, server errors, overloaded responses, and the recognized input-plus-output context overflow. Respect an explicit no-retry response except for the narrowly authorized internal 5xx override. Mock/simulated limit errors do not retry.

**QM-101 — Retry delay.** Default to exponential backoff starting at 500 ms, capped at 32 seconds, with random jitter from 0 through 25 percent of the capped base. Honor a numeric retry-after header directly.

**QM-102 — Retry ceiling.** Default maximum attempts after failure to 10 unless configured. After the ceiling, throw a typed non-retryable outcome retaining retry context. Persistent unattended retry is a separate mode and does not silently change normal mode.

**QM-103 — Client refresh.** Recreate the provider client after the first retry and whenever authentication, credential revocation, provider credential refresh, or a stale persistent connection requires it. A stale reset/broken-pipe connection may also disable keepalive for the replacement.

**QM-104 — Overload fallback.** Count consecutive overloaded responses only for query sources authorized to wait. Background classifiers and summaries fail promptly. At three consecutive overloads, signal the query layer to switch to a configured fallback model. Without a fallback, eligible external sessions receive an explicit repeated-overload error unless persistent mode applies.

**QM-105 — Model fallback transaction.** On a fallback signal:

1. terminalize every emitted tool use lacking a result;
2. tombstone partial assistant records from the failed attempt for presentation;
3. clear pending streaming tool state and results;
4. discard and recreate the streaming executor;
5. switch model and update model-dependent context;
6. remove model-bound signatures where required;
7. publish a warning/breadcrumb;
8. retry from coherent pre-attempt history.

**QM-106 — Context-overflow max-output adjustment.** For the recognized provider error containing input length, requested output, and context limit, reserve 1,000 tokens. If available output is below 3,000, fail. Otherwise set a retry maximum that satisfies the 3,000 floor and any enabled-thinking budget plus one token, then recompute the request.

**QM-107 — Persistent retry.** When explicitly enabled for unattended work, retry 429 and overload indefinitely while abortable. Cap ordinary persistent backoff at 5 minutes and any reset-directed wait at 6 hours. During waits longer than one heartbeat, emit retry status at most every 30 seconds and check cancellation between chunks. Use rate-limit reset time when valid.

**QM-108 — Fast mode.** For a fast request, a short rate/overload retry-after under 20 seconds may wait and retain fast/cache mode. A longer or unknown wait enters standard-mode cooldown for at least 10 minutes, with a default hold of 30 minutes when no longer duration is known. A provider response stating fast mode is unavailable disables it and retries in standard mode. A permanent overage condition disables it rather than cycling.

### Query-layer recovery

**QM-110 — Prompt-too-long order.** Withhold prompt-too-long from consumers. Attempt one drain of already staged context collapse first. If the retry still fails, attempt reactive full compaction once. If neither succeeds, publish the withheld error, invoke failure hooks, and stop without ordinary stop hooks.

**QM-111 — Media-size recovery.** A media-size rejection may attempt reactive compaction/stripping once but skips context-collapse drain because collapse does not remove media. If the preserved tail still contains invalid media, the one-attempt guard surfaces the next error instead of looping.

**QM-112 — Maximum-output escalation.** When the model used a deliberately capped default, the escalation gate is enabled, no explicit output override exists, and no environment maximum was set, retry the same request once with 64,000 output tokens and no injected user message.

**QM-113 — Maximum-output continuation.** After escalation is unavailable or exhausted, append a meta continuation instruction and resume directly for at most three additional model iterations. Reset the temporary maximum override. After three recoveries, publish the withheld error.

**QM-114 — Runtime exception.** If model streaming unexpectedly throws after publishing tool use, first synthesize missing tool results, then publish the real model/runtime error. Never label it as a user interruption.

**QM-115 — Fallback safety.** If streaming tools can produce non-idempotent external effects, non-stream retry or model fallback must be disabled or must prove that the failed attempt cannot continue executing. Never issue a duplicate request merely to improve presentation.

## Cancellation and cleanup

**QM-120 — Abort propagation.** A turn-level abort must reach model transport, retry sleeps, tool execution, summaries, and hooks. Each layer must translate cancellation only at its own boundary and release registered resources.

**QM-121 — Abort during stream.** Before returning, drain the streaming executor so it produces terminal synthetic results for queued/in-progress tool uses; without streaming execution, synthesize missing results directly. Then perform main-thread environment cleanup. If cancellation reason means a queued replacement user message follows, omit the redundant interruption message; otherwise append one interruption user message.

**QM-122 — Abort during tools.** Drain or cancel tools according to their interrupt contract, emit terminal results, run required main-thread environment cleanup, and stop. Never let a later tool completion resume the query.

**QM-123 — Transport abort classification.** If the caller's signal is aborted, classify the stream failure as user cancellation with terminal stop reason `cancelled`; do not label it `provider_error` merely because cancellation was observed while opening or consuming the provider stream. If only an internal stream controller aborted because of timeout/watchdog, classify it as transport timeout eligible for the configured recovery policy.

**QM-124 — Resource release.** In every success, fallback, error, and cancellation path, clear watchdog timers, cancel or consume response bodies, abort internal controllers, stop activity tracking, and call the stream iterator's return/close operation when available. Account fallback cost in a `finally`-equivalent path.

## Usage, limits, and structured output

**QM-130 — Cumulative stream usage.** Message-start and message-delta usage fields describe the current provider response cumulatively. Nonzero input/cache values update the current record; output may legitimately update to zero. Add one final response usage record to aggregate totals exactly once at message stop.

**QM-131 — Aggregate usage.** Across responses, add token and cost counters while retaining the most recent service tier, region, and iteration metadata. Do not add a cumulative delta repeatedly.

**QM-132 — Cost and turn limits.** Cost termination occurs when accumulated cost is greater than or equal to the configured ceiling. Enforce maximum model turns before issuing the next request. Report both limits as explicit result subtypes.

**QM-133 — Structured output.** When a schema is configured and the synthetic output capability exists, require the model to finish through that capability. Count retries relative to the tool-call count at turn start, not engine lifetime. Default retry allowance is 5. On exhaustion, return a structured-output error and preserve the invalid attempts in transcript history.

**QM-134 — Token-budget continuation.** This optional feature applies only to the main query, with a positive budget. Continue automatically while output is below 90 percent of budget. After at least three continuations, stop early when each of the two most recent output gains is under 500 tokens. Emit completion telemetry only if continuation occurred or diminishing returns was detected.

**QM-135 — Error reporting window.** Turn error results include diagnostic errors recorded after a reference watermark captured at turn start. If the bounded diagnostic ring evicts that exact watermark, including more history is the safe fallback.

## Configuration, constants, and disabled behavior

| Contract | Default | Override or gate | Disabled behavior |
|---|---:|---|---|
| Normal retry ceiling | 10 | configured maximum-retries value | fail after current attempt |
| Retry base delay | 500 ms | none | not applicable |
| Retry delay cap | 32 s | retry-policy override | not applicable |
| Retry jitter | 0–25% | none | deterministic base is not required |
| Consecutive overload threshold | 3 | fallback eligibility/source policy | background auxiliary calls fail promptly |
| Retry output floor | 3,000 tokens | none | overflow fails below floor |
| Non-stream timeout | 300 s local; 120 s remote | API timeout configuration | no fallback if fallback disabled |
| Stream idle timeout | 90 s | stream-idle configuration | no idle watchdog; 30 s stall diagnostics may remain |
| Stream stall diagnostic | 30 s | none | diagnostic only, never a terminal by itself |
| Structured-output retries | 5 | structured-output retry configuration | no enforcement without schema and output capability |
| Maximum-output same-request escalation | 64,000 tokens, once | output-slot gate; prohibited by explicit maximum | proceed to continuation recovery |
| Maximum-output continuation | 3 | fixed recovery ceiling | surface withheld error |
| Token-budget completion threshold | 90% | token-budget feature and positive budget | no automatic nudge |
| Diminishing gain | 500 tokens twice after 3 continuations | token-budget feature | continue until threshold |
| Request media cap | 100 | provider contract | remove oldest excess media |
| Persistent backoff cap | 5 min | persistent unattended mode | ordinary bounded retry |
| Persistent reset cap | 6 h | reset header | cap pathological waits |
| Retry heartbeat | 30 s | persistent unattended mode | no long-wait status heartbeat |
| Fast short-wait threshold | 20 s | fast mode active | switch to standard cooldown |
| Fast minimum cooldown | 10 min | server retry/reset data | not applicable |
| Fast default hold | 30 min | server retry/reset data | not applicable |
| Tool-summary input/output excerpt | 300 characters each | summary feature and non-subagent batch | omit derived summary |
| Agent progress summary interval | 30 s after prior attempt completes | coordinator/background-agent integration | no progress label |
| Suggestion parent uncached ceiling | 10,000 tokens | suggestion feature and main interactive source | suppress suggestion |
| Suggestion shape | 2–12 words; under 100 characters | slash/narrow one-word exceptions | filter candidate |
| Speculation turn/message bounds | 20 / 100 | internal speculation gate | no speculative execution |

**QM-140 — Environment precedence.** An explicit disable wins over an enable/default for thinking, fast mode, fallback, or extended behavior. Validate numeric overrides; invalid values fall back or fail clearly rather than producing negative/unbounded limits.

**QM-141 — Feature dimensions.** Treat build inclusion, runtime feature gate, provider/model support, account eligibility, policy, and current connection availability as independent dimensions. Code presence alone must not enable behavior.

**QM-142 — Test mode.** Test/fixture mode may suppress persistence and ephemeral message-ID tags to preserve deterministic fixtures, but it must not alter semantic normalization, pairing validation, role order, or terminal outcomes unless a test explicitly selects a fake policy.

## Collaborator contracts

**QM-150 — Persistence.** The engine requests append/flush and receives restored history. Persistence owns graph linkage, atomic append, compaction relinking, and recovery. The engine preserves semantic ordering and supplies final usage fields.

**QM-151 — Tool runtime.** The engine supplies ordered tool-use blocks, assistant context, permissions callback, and cancellation. The tool runtime returns progress, exactly one terminal result per accepted identifier, optional context updates, and continuation-prevention attachments.

**QM-152 — Context pressure.** The engine supplies projected history and authoritative last-response context usage. Compaction/collapse returns a new context view plus explicit boundary artifacts. The durable transcript remains authoritative.

**QM-153 — Prompt/context service.** The service returns ordered default, user, and system sections with cacheability metadata. The engine selects replacement/append precedence but does not rediscover repository instructions itself.

**QM-154 — Presentation adapters.** Adapters receive initialization, request-start, raw stream, completed semantic messages, retries, summaries, compact boundaries, and terminal result events. They may hide or coalesce progress but cannot insert presentation-only content into model history.

**QM-155 — Dynamic registry.** Capability discovery supplies canonical names, aliases, schemas, model prompts, enablement, and connection state. The engine snapshots each request's schemas and may refresh only between iterations.

**QM-156 — Authentication.** Authentication supplies clients and refresh/clear operations. Query retry may request a new client but never persists credentials or exposes credential-bound signed content across identity changes.

## Acceptance scenarios

### Normal and local turns

**QM-A01 — Text completion.** Given one accepted user prompt and a stream containing message-start, one text block, message-delta, and message-stop, verify the user prompt is persisted before transport; partial text is shown; one completed assistant record is stored with final usage/stop reason; no tool loop runs; one success result is emitted.

**QM-A02 — Local command.** Given input that resolves locally, verify initialization and local output are published, no model client is called, history is persisted, and a success result has null model stop reason.

**QM-A03 — Process killed before response.** Block transport after admission, terminate the process, restore the session, and verify the accepted user message is present without requiring an assistant response.

### Projection and repair

**QM-A04 — Split streamed assistants.** Given thinking, text, and two tool-use assistant records sharing a provider message identifier with tool results between later records, verify the API projection walks across tool results, rejoins matching blocks in original order, and does not merge a concurrent different-identifier assistant.

**QM-A05 — Consecutive users.** Given two queued text prompts, verify one API user role contains two text blocks separated by exactly one newline at their seam and retains the real user's stable identity.

**QM-A06 — Missing tool result.** Given an assistant tool use followed by unrelated user content, verify default mode prepends a synthetic error result before that content; strict mode instead fails validation before transport.

**QM-A07 — Orphan and duplicate pairing.** Given duplicate tool-use IDs, duplicate results, an orphan result at history start, and a provider tool use lacking its inline result, verify duplicates/orphans are removed, valid first pairs remain, role alternation stays legal, and any emptied role receives a neutral placeholder.

**QM-A08 — Credential switch.** Restore history containing signed thinking from another credential, switch authentication identity, and verify signed blocks are excluded while ordinary text/tool content survives.

**QM-A09 — Media rejection.** After a synthetic oversized-image error, verify only the implicated preceding meta image is omitted from later requests. Given 103 remaining media items, verify the oldest three are omitted.

### Streaming and retry

**QM-A10 — Repeated start content.** A transport places text in block-start and repeats it in a delta. Verify displayed and persisted text appears once.

**QM-A11 — Cumulative usage.** Feed usage 10 output tokens then 25 output tokens in successive cumulative deltas. Verify response aggregate is 25, not 35, and engine total increases once by 25.

**QM-A12 — Incomplete stream.** End a stream without message-start or a valid terminal reason. Verify non-stream fallback runs once when enabled and an explicit transport error results when disabled.

**QM-A13 — Idle watchdog.** Enable a 90-second watchdog, deliver no event, and verify one warning near 45 seconds, abort/release near 90 seconds, timeout classification rather than user-cancel classification, and no leaked response iterator. Repeat with injected transports and streaming/error bodies whose `RoundTrip`, `Read`, or `Close` ignores context: the coordinator still terminates at its deadline, retries only to the configured ceiling, starts no more than one read loop per attempt, and never waits for cleanup.

**QM-A14 — Bounded retry.** Return retryable failures repeatedly. Verify exponential delays begin at 500 ms, never exceed 32 seconds before jitter, include no more than 25% jitter, publish retry events, refresh the client, and fail after the configured ceiling.

**QM-A15 — Persistent retry cancellation.** Enable unattended persistent retry with a two-minute wait, abort after one heartbeat, and verify at least one retry status was published, no further wait occurs, and the terminal outcome is cancellation.

**QM-A16 — Three overloads with fallback.** Emit three eligible overload responses. Verify auxiliary background calls do not wait; a foreground query terminalizes partial tool uses, tombstones partial assistant display, switches model, removes incompatible signatures, and retries from coherent history.

### Recovery and continuation

**QM-A17 — Prompt overflow.** Cause prompt-too-long, stage one collapse, and let the collapse retry fail. Verify collapse drain is attempted once, reactive compact once, ordinary stop hooks never run, and the final error surfaces without a loop.

**QM-A18 — Maximum output.** With slot escalation enabled and no explicit maximum, hit the capped output limit repeatedly. Verify one same-request 64K retry, then at most three meta continuation turns, then the withheld error.

**QM-A19 — Tool cancellation.** Abort after two tool-use blocks are published, one running and one queued. Verify both receive terminal results, transport and tools stop, cleanup runs, and no later completion resumes the model loop.

**QM-A20 — Stop-hook block.** Return a valid assistant answer and one blocking stop-hook message. Verify another iteration starts with the blocking message, maximum-output continuation count resets, reactive-compaction guard persists, and a later successful hook terminates normally.

**QM-A21 — Queued notification ordering.** Queue a user notification while tools run. Verify every tool result immediately follows its assistant group in API history and the queued notification appears only afterward and only in its owning main/agent query.

**QM-A22 — Token budget diminishing returns.** With a positive main-turn budget, provide three continuations under 90%, then two successive gains under 500 tokens. Verify nudges occur before the stop, the fourth continuation is not requested, and subagent queries never receive budget nudges.

**QM-A23 — Structured output exhaustion.** Configure a schema and output capability, make invalid attempts exceeding the default five-call delta, and verify an explicit structured-output failure while preserving attempted calls in transcript history.

**QM-A24 — Consumer abandonment.** Stop consuming the query generator mid-turn. Verify the queued command remains started but incomplete, response resources close, and no false success result is recorded.

### Derived assistance and speculation

**QM-A25 — Tool-summary latency hiding.** Complete one non-subagent tool batch, start its bounded summary, and let the next model response finish first. Verify the summary overlaps that request, appears only as derived progress when it later succeeds, and a failed summary neither changes messages nor fails the turn.

**QM-A26 — Agent summary does not overlap.** Keep a coordinator worker alive across two slow summary attempts. Verify the next 30-second timer begins only after the prior attempt completes, tools remain denied despite advertised schemas, and stop aborts the active attempt without a transcript record.

**QM-A27 — Stop hook blocks after suggestion launch.** Produce a valid answer and launch main-thread suggestion generation, then return a blocking user Stop hook. Verify the auxiliary request was already started, the blocking message still controls the next semantic iteration, and suggestion failure cannot change that result.

**QM-A28 — Suggestion suppression and filtering.** Exercise pending permission, plan mode, one assistant turn, a 10,001-token uncached parent, API-error response, evaluative text, 13 words, a slash command, and `yes`. Verify every suppression/filter reason, and accept only the valid command/single-word exception without transcript mutation.

**QM-A29 — Stale suggestion race compatibility.** Start suggestion A, then B before A completes. Abort through the global pointer and let A finish. Verify the compatibility implementation can still publish A because only B was current; if generation tokens suppress A, record that as a deliberate safer divergence.

**QM-A30 — Speculative copy-on-write.** Speculation edits an in-root file, then reads it and attempts an out-of-root write. Verify the edit/read target the overlay, the main file remains unchanged before acceptance, and the outside write is denied without escaping the root.

**QM-A31 — Partial speculation boundary.** Allow reads, then request a state-changing shell command. Verify a shell boundary aborts the fork, unsuccessful/incomplete tool pairs and trailing assistant content are removed, accepted input plus safe user-ended prefix are injected, and an ordinary query remains required.

**QM-A32 — Non-atomic speculative acceptance.** Produce two overlay files and inject failure copying the second. Verify the first main file may change, the second may remain old, the overlay is cleaned best-effort, and compatibility still injects sanitized messages. A transactional merge implementation must declare its safer divergence.

**QM-A33 — Complete speculation pipeline.** Let speculation finish normally and generate a valid pipelined suggestion. On exact acceptance, inject user input and sanitized complete messages, skip redundant model execution, promote the pipelined suggestion, and start its next speculation. On ignore/user typing, abort and inject none of the speculative messages.

### Result settlement

**QM-A34 — Fair bounded result settlement.** Give one batch three results whose transcript appends block until their per-attempt deadlines followed by one fast result. Verify every result receives an attempt, no attempt deadline exceeds the common batch deadline, the fast sibling is durably recorded and enters live history, blocked-result errors are returned, and total settlement remains bounded by the configured batch budget plus scheduling tolerance.

**QM-A35 — Cross-delta credential containment.** Use a provider implementation other than the Azure adapter and configure credential `secret`. Emit text deltas `sec` and `ret` with an unrelated semantic event between them, then complete the response. Verify the engine-owned stream projector retains the first suffix, publishes only a set-safe replacement when the second delta arrives, and the concatenation of all progress, accumulated assistant text, terminal response, transcript, and structured output contains no credential. Repeat with EOF, a protocol error, and a nonnil but structurally invalid terminal response after `sec`; terminal validation precedes flush and the ambiguous retained suffix is never published. A provider that also filters its stream yields the same safe semantic result without double-redaction leakage.

**QM-A36 — Cross-item terminal credential containment.** Configure credential `abc\ndef` and return separate assistant messages containing `abc` and `def`. Verify the engine rejects the response before provider metadata, assistant messages, history, or outcome text is accepted. Repeat with a commentary message between two explicit final-answer messages so only phase filtering recreates the credential, and with a credential equal to the physical JSON separator between two otherwise safe provider items. Exercise the public engine configuration without a transcript store or validator; the engine boundary alone fails closed and emits only a credential-independent protocol error.

**QM-A37 — Nested argument and public-envelope containment.** Configure credential `secret/path`. Through a custom provider, reject a replayed function-call argument whose JSON decodes either `\u0073ecret/path` or `secret\/path` before `Provider.Stream` is called; also reject a credential reconstructed only by the canonical separator between effective request fields. Repeat those aliases through the concrete adapter in response-created output, streamed argument deltas completed by argument-done, and terminal response output; no argument delta, completed call, or terminal response becomes public before the complete nested document fails. Then choose credentials reconstructed only by the canonical separator between public event fields and between nested response fields. Verify each direct provider `Next` call returns a zero event plus a credential-independent protocol error.

**QM-A38 — Hostile engine clock.** Configure clocks that panic, return zero, and reenter `ClearContext` and `Submit` during an active turn. The outer turn terminates with nonnegative total/API durations and a coherent transcript; each reentrant mutation returns `ErrBusy` without deadlock. A deterministic four-sample clock still measures model API time separately from total turn time.

**QM-A39 — Provider-phase user cancellation.** Cancel the caller while the engine is opening or consuming a provider stream. Verify the terminal outcome status and stop reason are both cancellation (`cancelled`), the structured surface uses its ordinary error result subtype without mislabeling the stop as `provider_error`, and a provider-owned timeout remains a transport/provider failure.

## Provenance

These contracts were specified from the reference responsibilities centered in `QueryEngine.ts`, `query.ts`, `query/config.ts`, `query/tokenBudget.ts`, `query/stopHooks.ts`, `services/api/agentx.ts`, `services/api/withRetry.ts`, `services/toolUseSummary/*`, `services/AgentSummary/*`, `services/PromptSuggestion/*`, `utils/messages.ts`, and their directly invoked prompt, compact, tool, persistence, authentication, and SDK-event helpers. Paths and symbol names are provenance only; the `QM-*` requirements above are the implementation contract.
