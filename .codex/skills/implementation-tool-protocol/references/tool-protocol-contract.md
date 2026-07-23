# Tool Protocol Implementation Contract

This document defines the language-neutral protocol shared by all model-callable tools. Concrete tool semantics and policy classifiers are delegated domains.

## Contents

- [Responsibility and data model](#responsibility-and-data-model)
- [Registry and request resolution](#registry-and-request-resolution)
- [Execution pipeline](#execution-pipeline)
- [Permission and hook composition](#permission-and-hook-composition)
- [Scheduling and streaming execution](#scheduling-and-streaming-execution)
- [Result mapping and persistence](#result-mapping-and-persistence)
- [Cancellation, failures, and disabled behavior](#cancellation-failures-and-disabled-behavior)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Responsibility and data model

**TP-001 — Capability boundary.** Treat a tool request as untrusted model protocol input. No request may cause its tool-domain side effect until canonical resolution, structural validation, hook processing, permission composition, and postauthorization semantic validation of the selected input have reached an executable outcome. Lifecycle hooks are separately authorized extension effects and do not authorize the tool they observe.

**TP-002 — Tool descriptor.** A registered tool descriptor must be able to declare:

- canonical name and supported aliases;
- human-facing name, activity text, and input/result rendering;
- input schema and a transport-ready schema representation;
- enabled predicate and model-facing description/prompt;
- read-only, destructive, open-world, and user-interaction classifications;
- concurrency safety and interruption behavior;
- semantic input validation;
- tool-specific permission check and rule matcher;
- optional safety classifier input;
- call operation yielding progress and one terminal domain result;
- domain-result to protocol-result mapping;
- maximum result size and optional result-size opt-out;
- optional idempotent observable-input backfill;
- source/provenance such as built-in, plugin, MCP, or dynamic provider.

Descriptor defaults are enabled, not concurrency-safe, not read-only, not destructive, and interruption mode `block`. A default tool-specific permission check allows execution but remains subordinate to general permission rules.

**TP-003 — Tool request.** A request contains a tool-use ID, requested name, model-originated input value, and originating assistant/message identity. The tool-use ID is the pairing key for all progress, permission, terminal result, persistence, and recovery records.

**TP-004 — Tool-use context.** Supply execution with:

- session options and canonical registries;
- abort signal/controller and agent/query identity;
- current-state reader and immutable state updater;
- permission callback and recorded permission decisions;
- progress, activity, notification, and SDK event callbacks;
- elicitation, hook, task, file-history, attribution, memory, and skill channels;
- read-file cache and loaded nested-memory paths;
- aggregate tool-result replacement state;
- optional rendered prompt frozen for permission/hook diagnostics.

The context is session-scoped. A tool must not reach around it to mutate presentation or transcript state directly.

**TP-005 — Terminal result.** Every accepted request produces exactly one terminal protocol result keyed by its tool-use ID. A result contains model-visible content, error status when applicable, optional structured metadata for presentation, and enough attribution to associate it with its source assistant block. Denial, validation failure, cancellation, sibling failure, and thrown exception are terminal results, not missing results.

**TP-006 — Progress.** Progress is observable, replaceable status and is not a terminal result. High-frequency progress is normally UI-only and must not become a transcript chain participant.

**TP-007 — Permission outcome union.** Represent permission evaluation as a discriminated outcome, not a boolean:

- `allow`, optionally carrying updated input, persisted/session rule updates, and decision provenance;
- `ask`, carrying the reason, suggested input, and interaction metadata needed by an interactive or structured surface;
- `deny`, carrying a model-visible message and structured decision reason;
- `cancel`, distinct from denial when the user or parent scope aborted.

Every stage consuming the outcome must handle all variants explicitly.

**TP-008 — Hook result.** A lifecycle hook result can carry progress records, additional model context, replacement input, a permission suggestion, user-display text, new semantic messages, continuation prevention, or stop. Missing fields mean no contribution. Preserve hook order and provenance when several hooks contribute.

## Registry and request resolution

**TP-010 — Session registry.** Build a deterministic session-scoped registry after filtering contributions by build, runtime gate, mode, platform, policy, provider health, and user choice. Retain source attribution even when names are merged.

**TP-011 — Canonical resolution.** Resolve the exact canonical name first, then supported aliases. A deprecated alias may resolve only through the base tool registry's declared alias mapping; do not reinterpret arbitrary names or allow an extension to impersonate an alias implicitly.

**TP-012 — Unknown request.** If no enabled canonical tool resolves, return an explicit error tool result. Include deferred-tool discovery guidance only when the name plausibly refers to a schema not yet loaded; never execute a similarly named tool by guesswork.

**TP-013 — Original input integrity.** Retain the exact model-originated input for the tool call and transcript. Derive a separate observable clone for backfill, hooks, permission analysis, and display. Unless a hook or user decision explicitly replaces input, backfilled fields must not alter execution input or prompt-cache bytes.

Credential-safe projection is the only permitted mutation of externally visible evidence. When semantic inspection proves that no configured literal changed, retain the exact original JSON bytes, including key order and whitespace. When a decoded key, string, or scalar spelling contains credential material, reject before hooks/permission/execution or expose only a canonical sanitized projection; malformed JSON, key collision, and nonprojectable scalar fail closed.

**TP-014 — Backfill.** Observable backfill must be idempotent. It may add inferred display/permission fields but must not erase or reinterpret model fields. If backfill temporarily canonicalizes a path, restore the original model path for execution unless input replacement was explicitly authorized.

**TP-015 — Semantic Boolean compatibility.** A Boolean tool-input field may accept the Boolean values themselves and, as an input-repair compatibility path, the exact lowercase strings `"true"` and `"false"`. Convert only those two strings; every other string remains invalid. The API-facing schema continues to advertise a Boolean so this tolerance never broadens the model contract.

**TP-016 — Semantic number compatibility.** A numeric tool-input field may accept finite numbers and exact decimal strings matching optional minus, one or more digits, and an optional fractional part. Do not accept whitespace, an empty string, a plus sign, exponent notation, hexadecimal, null, or a nonfinite conversion through this repair path. The API-facing schema continues to advertise a number.

**TP-017 — Empty required-set encoding.** When converting a tool's object schema for model transport, include the JSON Schema `required` member only when at least one property is required. An optional-only object omits the member instead of emitting `null` or an empty array. Mixed schemas preserve only the explicitly required property names, and this transport normalization must not broaden structural validation.

## Execution pipeline

**TP-020 — Required ordering.** Execute a tool request in this order:

1. Resolve canonical tool or alias.
2. If the request scope is already aborted, synthesize cancellation.
3. Structurally validate input against the tool schema.
4. Start any safe speculative classifier work that may overlap later checks.
5. Remove internal-only simulation/control fields from public input.
6. Create the observable/backfilled clone while retaining original input.
7. Run PreToolUse hooks, streaming hook progress as progress.
8. Structurally validate any hook replacement and rebuild its classification and permission projection.
9. Compose hook output, tool checks, configured rules, automated checks, and interactive permission.
10. When an approval supplies edited input, structurally validate it and rebuild the complete classification and permission request; repeat authorization only within the configured finite edit-cycle bound.
11. On a non-executable outcome, emit a terminal denial/cancellation/error result and any deliberate reject attachment.
12. Run side-effect-free semantic validation of the exact authorized input. Filesystem-, task-, or provider-dependent checks occur here so an unauthorized request cannot use them as an existence or content oracle.
13. Call the tool with the selected input, user-modified marker, and tool-use ID; forward progress.
14. Map the domain result exactly once and apply size/persistence policy.
15. Run PostToolUse hooks and append deliberate hook messages.
16. On thrown execution/mapping failure, run PostToolUseFailure hooks and emit an error result.
17. In all paths, stop activity indicators and remove temporary permission-decision state.

No later stage may be moved before a prerequisite merely to reduce latency.

**TP-021 — Structural validation failure.** Report schema issues without calling semantic validation, permissions, hooks that assume valid input, or the tool. When deferred schemas are possible, include a non-authorizing hint rather than silently fetching/executing another tool.

**TP-022 — Semantic validation failure.** After authorization, return the tool's validation explanation as an error result without invoking the tool. Semantic validation must not create side effects. Resource-dependent semantic checks must not run before path and command policy because their error differences could reveal protected-resource state.

**TP-023 — Mapping once.** Map a successful domain result once. Do not remap after hooks because mapping can be expensive or stateful. For ordinary tools, make the mapped result available before PostToolUse. For MCP tools, allow post hooks to transform provider output before final insertion.

**TP-024 — Hook stop.** A hook may request that model continuation stop after producing an explicit hook attachment/result. Honor that as a terminal orchestration condition without discarding the tool result already earned.

**TP-025 — Provider authentication failure.** If an MCP/provider failure indicates authentication is required, update that provider's connection state to `needs-auth` before emitting the normalized failure result.

## Permission and hook composition

**TP-030 — Pre-hook output.** A PreToolUse hook may contribute progress, additional context, updated input, a suggested permission outcome, a continuation-prevention signal, or a stop signal. A hook exception fails the invocation; it is not silently ignored.

**TP-031 — Hook allow ceiling.** Hook allow may skip an approval dialog only when no configured or managed deny/ask rule remains. It cannot override settings deny, settings ask, managed policy, required interaction, or a tool-specific hard denial.

**TP-032 — Hook deny finality.** Hook deny is a final non-executable outcome. Include its reason in the terminal result and run applicable denial/failure lifecycle reporting.

**TP-033 — Forced decision.** Hook ask passes a forced decision into the ordinary permission path. The general permission service still decides how to ask or fail when prompting is unavailable.

**TP-034 — Interaction requirement.** A tool marked as requiring user interaction must pass through the normal permission callback even if hooks suggest allow. Treat interaction as satisfied only when the permission outcome carries explicitly updated input or the tool's documented interaction response.

**TP-035 — Hardened updated-input approval scope.** Permission may return approval with modified input, but the approval covers only the object shown to that responder. Clone and structurally validate the replacement, recompute classification, paths, command analysis, and tool-specific permission projection, then run the ordinary deny/ask/allow composition again. A replacement that is invalid, newly protected, denied, or still requires confirmation cannot inherit the earlier allow. Bound the edit-and-reauthorize loop; exceeding the bound is a terminal denial. Run postauthorization semantic validation only after the final allow. Preserve the untouched model input as request/transcript evidence, the selected input as decision/execution evidence, and set `userModified` whenever an edit cycle occurred. The recovered one-shot compatibility profile skipped these checks; AgentX intentionally uses this fail-closed divergence.

**TP-036 — Denial retry.** Denial may run a classifier-denied lifecycle hook. If that hook explicitly says retry and supplies safe context, return a retry-oriented result; never loop internally without returning control to the query engine.

## Scheduling and streaming execution

**TP-040 — Safety partition.** Preserve model order while partitioning an ordered request list into contiguous groups:

- adjacent concurrency-safe calls form a concurrent group;
- every unsafe call forms a one-item serialized group and acts as a barrier.

Default maximum concurrent execution is 10 unless valid configuration changes it.

**TP-041 — Concurrent completion.** Within a safe group, start eligible calls together and publish progress and terminal bundles as they become ready. Completion order is observable and may differ from accepted request order; never sort results back into accepted order merely for display or persistence. Preserve each bundle's tool-use identity and source-assistant parent.

**TP-042 — Context modifiers.** Buffer context modifiers emitted by concurrent calls and apply them in original request-block order after the group completes. For a serialized unsafe call, apply its modifier immediately before scheduling the next group. A concurrent tool must not depend on another concurrent tool's modifier.

**TP-043 — Streaming executor lifecycle.** Track each accepted request through:

```text
queued -> executing -> completed -> yielded
```

Unknown tools can transition directly from queued to completed with an error. A completed result may be buffered until the consumer requests it; progress remains immediate.

**TP-044 — Unsafe exclusivity.** A streaming executor may overlap safe tools but must not start an unsafe tool until earlier running safe executions reach terminal execution state, and must not start later tools while the unsafe tool is running. This is an execution barrier, not a result-consumption or durability barrier.

**TP-045 — Child abort scopes.** Give each running tool a child cancellation scope linked to the query scope. A tool-local abort normally propagates to the query unless its reason is an orchestrator-owned sibling failure or streaming fallback.

**TP-046 — Sibling failure.** Only the designated shell capability's execution error cancels concurrent siblings under the observed contract. Other tool failures complete independently. Canceled siblings receive synthetic `sibling_error` results; they are not omitted.

**TP-047 — Submit interruption.** Apply descriptor interruption mode only to a submit-interrupt: a newly submitted higher-priority user workload asks the current turn to yield so that workload can run next. Abort running tools whose mode is `cancel`; allow `block` tools to reach a terminal result before the submitted workload begins. Synthesize `user_interrupted` results for accepted canceled work that cannot provide its own result. Do not apply this selective rule to a hard turn interrupt, process signal, shutdown, sibling failure, or streaming fallback.

**TP-047A — Hard turn interruption.** A hard interrupt aborts the turn's parent cancellation scope without consulting tool interruption mode. Propagate that abort to every running child tool, drain results already completed, and synthesize pairing results for every accepted request still missing one. The descriptor's `block` mode does not protect a tool from this operation.

**TP-047B — No executor cancellation timeout.** The common streaming executor supplies cancellation signals and preserves result pairing, but it does not impose a universal timeout on a tool that ignores or delays cancellation. A surface-level shutdown deadline or process kill is a separate lifecycle mechanism and must not be presented as a tool-executor timeout.

**TP-048 — Streaming fallback.** When model streaming falls back and the partial attempt is abandoned, mark the executor discarded, clear attempt-owned results/context changes, and create pairing evidence for every abandoned request. Late work from the discarded attempt must not mutate the replacement executor or next model request.

**TP-049 — Execution barriers are not durability barriers.** An unsafe call may start after earlier safe executions finish even while their terminal bundles remain buffered, undisplayed, not yet submitted to transcript storage, enqueued but unwritten, or written but unflushed. No tool may treat transcript durability as an implicit happens-before relation with a later tool. A surface that deliberately adds a stronger persistence barrier must name it separately.

## Result mapping and persistence

**TP-050 — Empty content.** Normalize absent, empty, whitespace-only, or arrays containing only empty text into a short marker stating that the tool completed with no output. Non-text blocks are not empty. Deliberately credential-suppressed content is a distinct terminal state and must remain empty through tool, capability, engine, transcript, and presentation adapters; never replace it with the ordinary empty-output marker. This prevents an empty tool-result tail from being interpreted as an unintended turn boundary without undoing fail-closed suppression.

**TP-051 — Per-tool size threshold.** Each tool declares a maximum result character count. For finite declarations, the effective default threshold is the smaller of the declaration and 50,000 characters, unless a valid positive per-tool runtime override is present. Infinite declaration is a hard opt-out that runtime overrides cannot re-enable.

**TP-052 — Persistable content.** Persist only the executor's already-sanitized string result, never a credential-suppressed result or an unbounded opaque value. Refuse persistence above the 32 MiB ownership limit. When persistence is unavailable, invalid, or fails integrity checks, return only the bounded preview plus an explicit unavailability marker; never restore the unbounded result after the egress boundary has selected persistence.

**TP-053 — Owned persisted result.** Store the full deterministic result under a session-specific owner-only tool-results directory. Pin and reverify the directory identity around every operation. Name each owner-only regular file from the SHA-256 digest of the exact, case-sensitive tool-use ID with a `.bin` suffix; do not expose the filesystem path to the model. Create the file exclusively, flush its content and directory entry, then append and flush an ID/file/size/content-digest ownership record to a bounded private index; flush the directory when creating or removing either owned entry on platforms that support directory synchronization. A pre-existing unindexed file is hostile during ordinary persistence and must be refused. Startup reconciliation may remove, but never read or adopt, a bounded unindexed crash artifact only when its name exactly matches the result-digest grammar and its pinned identity proves it is an owner-only regular file with one link and an allowed size. An alias, hard link, symlink, oversized file, unexpected name, excess orphan count, or identity change fails closed and is not removed. Preserve every valid newline-framed index record. When the final record is complete but lacks only its newline, validate it and durably append the newline; when the final bytes are a bounded syntactically incomplete prefix of the owned-record grammar, durably truncate only that EOF fragment. Never repair an invalid interior record, unrelated or closed malformed JSON, or a syntactically complete but semantically invalid final record. Once any index-record bytes have been written, a later write, flush, close, or directory-sync failure has uncertain durability: retain the already-flushed result file so restart can validate a complete record or remove an orphan instead of creating a dangling ownership record. Resume and reads may reuse only an indexed entry whose root identity, file identity, link count, size, permissions, and SHA-256 content digest all still match. When a session validator exists, validate a complete ID/file/size/digest/content envelope before persistence and replay that validation over every indexed `.bin` during reopen and read; a valid index/digest alone cannot authorize credential-bearing legacy content. Contain validator panics as fixed persistence failures. Return a stable model-visible reference containing the tool-use ID, original byte size, an approximately 2,000-byte valid-UTF-8 preview, an omission marker, and instructions to use `ToolResultRead`. Prefer a newline cut only when it falls in the latter half of the preview window.

**TP-053A — Bounded persisted-result retrieval.** Register `ToolResultRead` only with a runtime-owned result store. Accept an exact tool-use ID plus non-negative byte offset and a bounded 1–100,000-byte limit (default 30,000). Read only through the verified ownership index and pinned root, return content with `next_offset` and `truncated` metadata, and fail closed for unknown IDs, offsets past the result, aliases, links, replacements, or integrity drift.

**TP-054 — Global fallback limit.** When no effective tool threshold is supplied, use a 100,000-token estimate at four bytes per token, or 400,000 bytes.

**TP-055 — Aggregate wire-message budget.** Optionally enforce a default 200,000-character aggregate budget for tool results in one API-level user-message group. A valid positive runtime override may replace it. Group candidates exactly as message normalization will merge them: progress, attachments, and local system messages do not create boundaries; a genuinely new assistant response ID does.

**TP-056 — Stable replacement state.** Maintain per-conversation replacement state:

- `seen IDs`: results whose persist-or-inline decision has been exposed to the model;
- `replacements`: exact persisted-preview string for IDs that were replaced.

For each group, partition candidates into `must reapply`, `frozen inline`, and `fresh`. Reapply stored replacement bytes exactly. Never replace a previously exposed inline result later because doing so changes a cached prefix.

**TP-057 — Aggregate selection.** If fresh plus frozen eligible content exceeds the group budget, select largest fresh results until estimated remaining size is within budget or no fresh candidate remains. A tool with infinite per-tool result limit is frozen inline and excluded from aggregate replacement.

**TP-058 — Atomic decision publication.** Mark a fresh nonselected result seen immediately. For a selected result, publish `seen` and its replacement together after persistence completes. If persistence fails, mark it seen but leave it inline. This prevents another observer from seeing a selected result as frozen-inline during the persistence await.

**TP-059 — Replacement durability.** For resumable main and agent query sources, best-effort append each new aggregate replacement decision as `{kind, toolUseId, exact replacement string}` in the transcript; ephemeral forks omit it. The write may be asynchronous relative to the request because losing the optimization must not delay the model loop. On resume, mark every candidate in loaded messages as seen and restore exact replacement strings for matching IDs. If a crash lost the record, retain and freeze the original inline result rather than inventing a preview; continuity is preserved at the cost of a prompt-cache miss. For a resumed forked sidechain, parent replacement state may fill inherited gaps. Do not regenerate previews from current formatting code.

**TP-060 — Feature-off behavior.** When aggregate-budget enforcement is disabled, do not allocate replacement state, rewrite messages, or write replacement records. Per-tool finite-size persistence remains independently controlled.

**TP-061 — Credential-safe result boundary.** Before a result reaches persistence, hooks, progress, model continuation, or presentation, merge session-owned and source-owned exact literals into one immutable set. Sanitize content, original/executed/permission input projections, metadata keys and values, post-hook warnings, permission reasons, and every observer request projection—including authorizer-selected denial input and assistant identity—semantically. Unknown metadata types are JSON-projected into safe generic data rather than retained opaquely. Before ledger acceptance, inspect the exact requested/canonical routing identities plus the fixed correlated terminal fallback. Before invoking an observer or progress callback and again after result suppression, inspect the complete raw, canonical, and semantic request/result envelope so JSON field framing cannot reconstruct a source literal across otherwise safe fields. Invoke a progress sink behind its own panic boundary: presentation failure is suppressed and cannot rewrite an already-running or earned semantic effect into `execution_failed`. If the terminal result is unsafe, use only the preflighted correlated fallback; never silently lose an accepted tool-use identity after an effect. Build and validate the exact physical persisted-result index record, including its terminating newline, before append; validating only the JSON body is insufficient. Apply bounded redaction before truncation and append only a set-safe terminal marker; re-sanitize after persistence/replacement framing. Apply a deprecated opaque sanitizer to complete protocol frames as well as individual fields; reject it at construction if it changes mandatory frames, and contain panics as construction/projection failures. If an opaque legacy sanitizer cannot be unioned with a source set, retain source input rejection and suppress every result/observer payload field before whole-frame validation. No fixed fallback string may bypass its unknown sensitive set.

**TP-076 — Opaque extension failures.** Derive invocation codes only from exact sentinels, executor-owned context state, detached values, and package-sealed snapshots. The executor may traverse exact standard-library or package-owned wrapper implementations, but stops at an extension-owned child and never invokes extension `Error`, `Is`, `As`, or `Unwrap` behavior. A blocking error method cannot delay validation, authorization, hooks, execution, result projection, or cancellation. Contain descriptor, tool, and lifecycle-hook panics at their owning boundary, but never format or retain the recovered value: its `String` or `Format` behavior is callback-owned code and may panic again or expose credential-bearing state. Use a fixed stage-specific diagnostic and closed fallback code instead.

**TP-077 — Package-sealed semantic refinement.** Postauthorization semantic validation defaults to `semantic_invalid`. A built-in adapter may refine that code only with unforgeable package-owned typed evidence and an admitted closed code, such as temporary task-runtime contention becoming `unavailable`. An extension-constructed invocation error, ordinary foreign error, or hostile `Error`, `Is`, `As`, or `Unwrap` implementation cannot select the code or execute its methods.

**TP-062 — Least-authority bounded projection.** A tool implementation that captures bounded output receives only maximum lookahead and a projection operation returning safe content, truncation, and suppression. It does not receive the session credential set or raw literals. The common executor owns union construction, result suppression, and downstream propagation.

## Cancellation, failures, and disabled behavior

**TP-070 — Fail closed.** Unknown tool, malformed schema, semantic invalidity, hook exception, permission denial, unavailable required interaction, and thrown execution all return explicit error results. They do not become ordinary text or disappear.

**TP-071 — Query cancellation completeness.** Before returning from an interrupted tool phase, drain completed results and synthesize all missing terminal results. Tool-use/result pairing is stronger than preserving partial work.

**TP-072 — Cleanup.** Activity state, temporary permission decisions, child abort links, progress producers, and provider-specific handles are cleaned in a `finally`-equivalent path.

**TP-073 — Optional axes.** Distinguish a tool being absent from the build, filtered by runtime gate, ineligible for the account, forbidden by policy, unsupported on platform, disconnected, or disabled by the user. Only registered and enabled tools may resolve.

**TP-074 — Original bytes after failure.** A validation, permission, hook, or persistence failure must not mutate the original request object retained for transcript and prompt-cache comparison.

**TP-075 — Closed error-code projection.** Error codes crossing a tool-result, capability, transcript, or presentation boundary use one of two closed vocabularies. Internal invocation results admit exactly `cancelled`, `denied`, `execution_failed`, `hook_failed`, `malformed_result`, `permission_failed`, `semantic_invalid`, `sibling_error`, `stale_file`, `structural_invalid`, `timeout`, `unavailable`, and `unknown_tool`; an empty, unknown, untrusted, or otherwise invalid internal code becomes `execution_failed`. Capability and engine results admit exactly `call_batch_interrupted`, `cancelled`, `denied`, `execution_failed`, `hook_failed`, `interrupted`, `malformed_input`, `malformed_result`, `missing_terminal_result`, `permission_denied`, `permission_failed`, `semantic_invalid`, `sibling_error`, `stale_file`, `structural_invalid`, `timeout`, `unavailable`, `unknown_tool`, and `user_interrupted`; an empty, unknown, untrusted, or otherwise invalid capability code becomes `tool_error`. No source-provided code is passed through merely because its message or content was sanitized.

## Acceptance scenarios

**TP-A01 — Malformed alias.** A near-match to a deprecated alias produces an error result and no permission prompt or side effect.

**TP-A02 — Hook allow versus policy ask.** A PreToolUse hook allows a call while settings require ask. The user is still asked; hook allow only removes lower-priority prompting.

**TP-A03 — Updated input.** A user edits a path in the approval prompt. Verify that the replacement is structurally revalidated, every classifier and path projection is rebuilt, and permission is recomposed under the same tool-use ID. An invalid or newly protected path denies without tool execution; a still-ask result may reprompt only within the finite edit-cycle bound. The final allowed object receives postauthorization semantic validation and `userModified=true`, while the untouched model input remains request/transcript evidence.

**TP-A04 — Safe/unsafe batch.** For safe A, safe B, unsafe C, safe D: A and B overlap and may yield B then A; their modifiers apply A then B; C starts after both and completes alone; D starts afterward.

**TP-A05 — Shell sibling failure.** Parallel shell A fails while tool B is running. B receives the sibling abort and one synthetic sibling-error result; A receives its own error result; no ID is missing.

**TP-A06 — Submit interruption.** A higher-priority user workload arrives while a `block` tool and a `cancel` tool run. The cancel tool is aborted and paired synthetically if needed; the block tool is awaited; only after pairing is complete may the submitted workload begin.

**TP-A06A — Hard interruption.** A hard turn interrupt arrives for the same pair. Both child scopes receive abort regardless of descriptor mode, completed results are drained, and every accepted tool-use ID has a real or synthetic terminal result before the interrupted tool phase returns.

**TP-A06B — Noncooperative cancellation.** A tool ignores its cancellation signal. Verify that the common executor does not invent a timeout result; any later forced process shutdown is attributed to the surface or process-lifecycle deadline rather than this protocol.

**TP-A07 — Aggregate budget stability.** A user-message group contains three fresh results above 200,000 characters. The largest results are persisted until under budget. On the next request and after resume, the exact same preview strings are re-applied without file reads or changed formatting.

**TP-A08 — Persistence failure.** The result root is replaced or becomes unavailable while a large text result is mapped. Only the bounded preview and explicit unavailability marker are returned, no false ownership record is written, and neither the unbounded result nor content from the replacement path escapes.

**TP-A09 — Empty output.** A successful silent shell produces a nonempty completion marker and remains a normal successful tool result.

**TP-A10 — Streaming fallback.** A partial response starts two tools and then switches models. The abandoned attempt cannot leak late results into the replacement attempt, and the transcript retains valid pairing evidence.

**TP-A11 — Lost aggregate decision.** The model saw a persisted preview but the process crashes before its best-effort replacement record lands. Resume finds the original result in durable history, classifies it seen-but-unreplaced, preserves the full content, and accepts cache loss instead of fabricating bytes.

**TP-A12 — Exact scalar repair.** Validate Boolean inputs `true`, `false`, `"true"`, `"false"`, and `"False"`, and numeric inputs `30`, `"30"`, `"-5.25"`, `"1e3"`, `""`, and null. Only the exact compatibility forms in `TP-015` and `TP-016` convert; rejected values produce structural validation errors, and the generated API schema still reports Boolean/number types.

**TP-A13 — Credential-safe union and suppression.** Combine session literal `R` with provider literal `*`, exercise no-store truncation at every limit plus persisted previews, structured metadata, progress, hook ask/warnings, an authorizer-controlled denial input, and an output whose guard set has no safe marker. Add source credentials equal to the canonical JSON sequence spanning a safe result `content` value and the following `is_error` field, and to a safe persisted-index JSON suffix plus its newline. The complete observer envelope and physical index frame fail closed, the hook is skipped, no index bytes are appended, and no structural or framing separator reconstructs a credential. Neither literal survives or is reconstructed in results or observer requests; guard exhaustion and opaque-sanitizer panic remain explicitly suppressed through capability and engine normalization, and exact credential-free authorization evidence retains its original bytes.

**TP-A14 — Persisted-result ownership.** Persist a large result, restart the result store, and retrieve it in bounded ranges through `ToolResultRead`. Verify exact case-sensitive ID lookup and stable preview reuse. Simulate a crash after result-file sync but before index append and verify that bounded startup reconciliation removes the identity-safe orphan and permits retry. Append a partial final index frame and verify that restart truncates only that fragment, preserves all prior records, removes the corresponding safe orphan, and permits retry. Append a complete final record without its newline and verify that restart validates the record, appends and flushes the newline, and reuses the content. Inject result, index, and directory-sync failures and verify that no false durability is reported, a started index append never causes synced content to be deleted, and restart converges to either a valid indexed result or a removable orphan. Then test an ordinary unindexed pre-existing file, unexpected filename, excess orphan set, symlink, hard link, root replacement, file replacement, changed size, changed digest, invalid offset, index overflow, invalid interior record, and complete-but-invalid final record; every case fails closed without reading attacker-selected bytes or discarding prior authoritative records.

**TP-A15 — Optional-only object schema.** Convert one object descriptor whose properties are all optional and one with mixed required/optional properties. The first retains its properties and omits `required`; the second emits exactly the explicitly required names. Both continue to enforce their original structural validators.

**TP-A16 — Closed error-code vocabulary.** Exercise every admitted internal invocation code and every admitted capability/engine code and verify exact preservation. Then exercise an empty code, an unknown printable code, a control-bearing code, and a credential-bearing code at each boundary. Each invalid internal value becomes `execution_failed`; each invalid capability/engine value becomes `tool_error`; no invalid source bytes reach observer, transcript, model-continuation, structured, or terminal output.

**TP-A17 — Complete framing and hostile errors.** Use a credential reconstructed only by accepted ID, canonical name, fixed result fields, and `content_suppressed`; verify the routing envelope is rejected before ledger acceptance or execution. Repeat with the deprecated opaque sanitizer and with progress/observer frames. Return an extension error whose `Error`, `Is`, `As`, or `Unwrap` method panics or blocks forever; every exported invocation terminates promptly with the closed fallback code, fixed diagnostic, bounded memory, and zero calls to those methods.

**TP-A18 — Sealed semantic code.** Return a hostile extension semantic error implementing `Error`, `Is`, `As`, and `Unwrap`, then an extension-constructed invocation error requesting `unavailable`. Both terminate as `semantic_invalid`, invoke none of the hostile methods, and never call the tool. A package-owned task-busy semantic marker alone preserves `unavailable`.

## Non-normative provenance

Behavior was specified primarily from `Tool.ts`, `services/tools/toolExecution.ts`, `services/tools/toolHooks.ts`, `services/tools/toolOrchestration.ts`, `services/tools/StreamingToolExecutor.ts`, `utils/toolResultStorage.ts`, and `constants/toolLimits.ts`.
