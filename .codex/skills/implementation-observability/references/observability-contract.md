# Observability Contract

## Contents

1. Authority and event model
2. Privacy and routing
3. Usage and cost
4. Metrics, logs, and traces
5. Feature evaluation
6. Buffering, fallback, and retry
7. Diagnostics and health
8. Shutdown and failure containment
9. Acceptance scenarios
10. Provenance

## Authority and event model

**OBS-001 — Non-authority.** Observability consumes copies of semantic events and operational measurements. It cannot decide whether a turn, tool, permission, task, persistence append, or remote delivery succeeded.

**OBS-002 — Structured envelope.** An event declares stable name/version, timestamp, session/turn/request correlation where permitted, source surface, profile, attributes, privacy class, and destination eligibility. Unknown optional attributes do not invalidate the semantic operation.

**OBS-003 — Separate products.** Keep local authoritative usage/cost accounting, user-facing stats, diagnostic logs, analytics events, metrics, and traces distinct. Export failure cannot roll back local accounting.

**OBS-004 — Bounded identity.** Use stable correlation IDs only where permitted. Never use raw prompt, source, command, path, hostname, token, email, or arbitrary error body as an unrestricted metric label.

**OBS-005 — Error classification.** Record a bounded error class/name and sanitized diagnostic message. User-visible errors come from the owning domain, not from telemetry formatting.

## Privacy and routing

**OBS-010 — Filter first.** Apply privacy/opt-out and essential-traffic rules before constructing or enqueueing a sink payload that contains optional data. Persisted retries re-evaluate current privacy and managed policy before every send; a prior opt-in is not continuing export authority.

**OBS-011 — Independent gates.** Evaluate build inclusion, user privacy, managed policy, environment override, feature flag, identity eligibility, and sink kill switch separately. A cached feature value cannot bypass an explicit privacy or policy denial.

**OBS-012 — Sensitive-data exclusion.** Exclude prompts, tool input/output, file contents, secrets, tokens, authorization headers, exact paths, repository content, and arbitrary extension payloads unless a narrowly documented diagnostic flow obtains explicit authority and redaction.

**OBS-013 — Attribute review.** Every attribute declares type, bounded domain/cardinality, allowed destinations, and whether it has been verified not to contain code or paths. Free-form values are truncated and sanitized before diagnostic-only use.

**OBS-014 — Local diagnostics.** Debug and diagnostic logs may contain more operational context than analytics but remain secret-safe, bounded, permission-protected, and subject to retention cleanup.

**OBS-015 — Sink isolation.** Route each event independently to eligible sinks. One sink's exception, backpressure, authentication failure, or invalid response does not prevent other sinks or the semantic operation.

**OBS-016 — Attachment privacy.** Attachment bytes, base64, source paths,
runtime storage paths, provider data URLs/bodies, and decoded image/PDF content
are prohibited in logs, diagnostics, traces, metrics, telemetry, automatic
crash reports, and unrestricted error strings at every verbosity. Bounded
reason codes, media kind/MIME, decoded-size buckets, attachment count, duration,
and stable correlation identities may be observed only when their destination
and cardinality policy permit them. A private local DEBUG record may also carry
one bounded exact aggregate decoded-byte count for the complete admitted turn;
it may not carry per-item sizes or attachment/storage identities. Import and
provider failures remain attributable without serializing hostile content. For
a media-bearing provider failure, classify only from the closed trusted
status/code/parameter vocabulary and then replace all provider-owned
diagnostics and correlations with one fixed runtime message before any error,
retry, log, transcript, or presentation projection; provider prose is never
retained even when split into individually short base64-like fragments.

## Usage and cost

**OBS-020 — Cumulative stream usage.** Provider stream deltas may contain cumulative rather than incremental counts. Update the last message snapshot accordingly, then add a completed message's usage to aggregate counters exactly once.

**OBS-021 — Dimensions.** Track input, output, cache-read, cache-creation, service-specific cache details, web-search requests, model, context/output limits, API duration, retry-excluded API duration, tool duration, total duration, and cost when known.

**OBS-022 — Unknown cost.** Preserve token/usage evidence and mark cost unknown when pricing is unavailable. Never silently report zero as a known price.

**OBS-023 — Model attribution.** Aggregate per model and overall. Fallback and secondary queries remain attributable to their actual model/query source. Main-request identifiers do not get overwritten by subagent or background requests.

**OBS-024 — Restore.** Session resume restores prior usage, cost, duration, and model counters without counting restored messages as new work. Implement start time as current time minus restored elapsed duration.

**OBS-025 — Reset.** A deliberate new/clear session resets session-scoped counters, prompt/request baselines, line changes, and timestamps while leaving process-health counters that explicitly span sessions intact.

**OBS-026 — Budget inputs.** Budget enforcement reads authoritative local counters, not asynchronously exported metrics. Comparison semantics and unknown-price behavior belong to the query contract.

## Metrics, logs, and traces

**OBS-030 — Counter ownership.** Increment a semantic counter at the authoritative transition, not in a renderer or sink callback. Retried delivery of the same event is idempotent.

**OBS-031 — Duration clocks.** Use a monotonic clock for durations and wall time only for durable timestamps. Measure API, retry, hook, classifier, tool, active-user, startup, and total durations separately.

**OBS-032 — Span lifecycle.** A span has stable parentage, start, attributes, and one terminal status. Cancellation and timeout are not success. Expire abandoned open spans after 30 minutes and scan once per minute.

**OBS-033 — Trace cap.** The local performance trace retains at most 100,000 events. Overflow follows an explicit drop/summary policy and never grows memory indefinitely.

**OBS-034 — First-party exporter defaults.** Default request timeout is ten seconds, export interval ten seconds, maximum batch 200, in-memory queue 8,192, and serialized queued-record budget 64 MiB. Configuration may override only within hard allocation limits: 65,536 queued records, 256 MiB of queued record data, 1,000 records per batch, a one-minute send timeout, 256 admitted attributes, and 4,096 runes per admitted attribute value. Reject an excessive configuration before allocating a channel or starting a worker.

**OBS-035 — Secondary sink defaults.** A secondary analytics sink batches 100 events, flushes every 15 seconds, and bounds a send to five seconds.

**OBS-036 — Startup profiling.** Record checkpoints around expensive initialization without forcing lazy modules to load solely for profiling. Emit the final report before exporter shutdown.

**OBS-037 — Diagnostics truncation.** Bound uncaught exception messages to 2,000 characters and stacks to 4,000 characters. Record error names as low-sensitivity classifications, not arbitrary object serialization.

**OBS-038 — Session turn diagnostics.** Construct one session-scoped structured logger after the complete configured credential union is frozen. The default threshold is INFO; an explicit debug invocation lowers it to DEBUG for that session without changing semantic behavior. Routine model-backed turn start and successful-completion records are DEBUG, so a successful turn emits no lifecycle record at the default threshold. With DEBUG enabled, emit exactly one correlated start record after the turn identity exists and exactly one successful terminal record only after terminal-result publication and durable flush succeed. A failed turn retains one ERROR terminal record after finalization so an ordinary invocation does not discard its bounded safe failure classification. The durable accepted user event, provider-usage events, and terminal turn-result event—not diagnostic logs—are the authoritative session history. Recursive model iterations, stream-event metadata, capability batches/results, usage, and timing are DEBUG detail; retries and exceptional operational conditions may remain WARN or ERROR. Route local CLI records to stderr, bound each complete record to 64 KiB, sanitize the complete encoded JSON plus its physical delimiter against the frozen credential set, and include no prompts, model text, tool arguments/results, file contents, headers, bodies, exact workspace paths, or arbitrary configuration objects. Logger, encoder, sanitizer, and writer panic, error, short-write, nil, or sync failure is observational only and cannot alter result, transcript, permission, tool, or exit semantics.

**OBS-039 — Maximum-safe DEBUG evidence.** DEBUG is the highest-detail safe local diagnostic profile, not permission to serialize payloads. Emit enough bounded structured evidence to explain each admitted stage and decision: session construction and shutdown; trust, bare, persistence, surface, permission mode, and registry counts; reason-coded extension discovery outcomes; turn/model-iteration identity; validated stream-event type and sequence; retry attempt, delay, reason class, and decision; capability batch/result counts and status; usage; API/tool/total duration; provider status, bounded code and safe request ID; and finalization outcome. Include session, turn, model, request, tool-use, or generation correlation only where it exists and remains safe after whole-record sanitization. Provider diagnostics may expose a bounded provider class, route family, configured-versus-default source classification, and a provider-reported minimum API version after strict public-token validation, but not an endpoint, deployment, exact configured API version, URL/query, header, body, prompt, model output, tool payload, file content, skill body, arbitrary frontmatter, or exact workspace path. A short repository-relative caller is allowed. Automatic stack traces are disabled; a deliberate stack requires its own explicitly authorized, sanitized, and `OBS-037`-bounded flow. INFO continues to admit WARN/ERROR conditions but does not inherit routine DEBUG detail.

## Feature evaluation

**OBS-040 — Cached evaluation.** Prefer in-memory feature values, then persistent cached values, then declared defaults. Security-sensitive gates may use a more conservative precedence than ordinary experiments.

**OBS-041 — Initialization bound.** Feature-client initialization is bounded to five seconds and must permit startup with cached/default values.

**OBS-042 — Exposure dedup.** Log a feature exposure at most once per session for the same evaluation identity. Repeated cached reads do not create unbounded events.

**OBS-043 — Refresh.** Long-running sessions may refresh gates and notify subscribers. A refreshed value changes only behavior whose contract permits mid-session change; sticky session latches remain stable until their reset boundary.

**OBS-044 — Overrides.** Explicit environment/test overrides are attributed and cannot override privacy, managed policy, or compile exclusion.

## Buffering, fallback, and retry

**OBS-050 — Bounded queue.** Every sink has maximum in-memory record and serialized-byte budgets, batch size, flush cadence, and overflow rule. A record reserves its byte budget before entering the queue and retains that reservation while queued, batched, exported, or persisted; every full, batch-completion, close, and shutdown-drop path releases it. Producers do not await ordinary analytics on the semantic hot path.

**OBS-051 — Disk fallback.** Durable fallback is available only on platforms where the implementation can prove owner-only directory/file access and an opened regular file's single-link identity. When either proof is unavailable—including the current Windows implementation, which does not inspect DACLs—the exporter rejects a non-empty fallback directory with a stable unsupported error before allocating a queue or starting a worker; it must never synthesize affirmative permission or link evidence. On supported platforms, failed eligible batches may persist to an owner-only disk queue acquired through the platform owned-directory boundary. Serialize a versioned sanitized payload atomically, including its destination and traffic class, cap each file at 16 MiB, flush file data before activation, and flush directory mutations on platforms that expose directory fsync. Strictly named same-directory temporary activations also count against retention and become removable crash orphans after a one-hour grace period; fresh temporary files remain counted but non-evictable so one process does not delete another process's active write. Default retention is at most 256 managed files, 64 MiB in aggregate, and seven days, with hard configuration ceilings of 512 files and 1 GiB. Before each write and drain, enumerate at most 1,024 directory entries through a pinned directory root; an externally enlarged directory fails closed without unbounded materialization. Every managed entry must remain a direct owner-only regular file with one link and a stable opened identity. Remove expired entries first, then evict the oldest modification time with filename as the deterministic tie-break until both the reserved file slot and byte budget fit; recompute bounded retained size after every eviction so a single externally injected oversized file cannot defeat the aggregate cap. Count evicted files/bytes, dropped records, and quarantined corrupt files explicitly. A later process may retry bounded entries only when the encoded destination matches the exporter and current privacy/managed policy still enables the encoded traffic; current-policy denial removes the eligible file with drop evidence, while a different destination leaves it for its owning exporter. Validate durable record shape against hard wire ceilings rather than a later process's smaller attribute settings. Reject duplicate JSON members at every depth, unknown envelope/record fields, excessive nesting, filename/batch-identity mismatch, and invalid record shapes. Quarantine only sealed format corruption; identity, link, permission, or I/O uncertainty fails closed without renaming evidence. Quarantined artifacts remain under the same retention budget.

**OBS-052 — Retry containment.** Retry failed persisted batches with a bounded/quadratic delay. After the first endpoint failure in a drain, stop sending remaining batches so one outage does not amplify traffic.

**OBS-053 — Ordering.** Preserve order within a batch or trace where analysis depends on it. Aggregatable counters may coalesce. Never reorder semantic events in their authoritative channels to suit a sink.

**OBS-054 — Idempotency.** Include event/batch identity where a receiving service supports deduplication. A local retry never increments authoritative usage again.

**OBS-055 — Retention.** Apply configured cleanup to log, trace, failed-batch, and diagnostic files. Cleanup errors are counted/logged and do not delete unrelated current-session evidence.

## Diagnostics and health

**OBS-060 — Diagnostic snapshot.** Health output identifies product version, surface, platform, installation/update mechanism, auth/provider state without secrets, settings errors, policy state, MCP/LSP health, sandbox capability, and relevant resource status.

**OBS-061 — Source attribution.** A diagnostic value states whether it came from user, project, local, flag, managed, remote, provider, or runtime detection when that distinction affects repair.

**OBS-062 — No mutation by default.** Inspection/doctor operations are read-only unless the user explicitly selects a repair action. Health checking does not silently rewrite corrupt settings, marketplaces, credentials, or transcripts.

**OBS-063 — Update observation.** Version checks and update health are observational. Actual install/locking mechanics belong to the platform contract. A failed check does not prevent normal semantic work unless a managed minimum-version policy explicitly requires exit.

**OBS-064 — Structured purity.** Headless/SDK diagnostics use stderr or typed events according to the surface. Library console output is intercepted or routed so NDJSON remains parseable.

## Shutdown and failure containment

**OBS-070 — Bounded shutdown.** Exporter shutdown is best effort and shares a 500-millisecond overall bound during graceful exit, shortened by an earlier caller deadline. Cancel or drop remaining queued observation work with explicit local evidence when that bound expires, and count sink-shutdown failures. Invoke sinks and fallback writes through cancellation-aware, single-flight host boundaries: an operation that ignores cancellation may strand at most one callback goroutine per exporter, and later retries or shutdown must not fan out more blocked callbacks. Do not begin fallback persistence after the owning send/shutdown context expires. Nil contexts behave as a non-cancelled context. Do not delay durable session cleanup for analytics.

**OBS-071 — Cache eviction hint.** If a main request identity exists, emit the session-end cache-eviction hint before flushing exporters. Absence is valid.

**OBS-072 — Sink total failure.** With every sink disabled, offline, or throwing, local semantic results, permission decisions, transcript bytes, task terminality, and process exit code remain identical except for diagnostic evidence.

**OBS-073 — Observer exceptions.** Catch and contain observer callbacks. If a subscriber is part of semantic state rather than observation, move it to the owning domain instead of weakening this rule. Classify sink failures only from exact sentinels, exporter-owned context state, and exporter-sealed snapshots; never invoke sink-owned `Error`, `Is`, `As`, or `Unwrap` behavior while projecting them. A blocking error method cannot delay export or shutdown, and an unknown sink failure receives a fixed diagnostic.

## Acceptance scenarios

### `OBS-A01` — Cumulative usage stream

Three deltas report cumulative output 10, 20, and 25. The completed assistant message stores 25 and aggregate usage increases by 25 once, not 55. Retrying exporter delivery changes neither value.

### `OBS-A02` — Offline first-party endpoint

The first batch times out. Eligible sanitized records persist with owner-only permissions, the current drain short-circuits later batches, semantic work completes, and a later process retries without duplicate authoritative counters.

### `OBS-A03` — Privacy opt-out

Optional analytics is disabled while essential operational traffic remains policy-eligible. The router never constructs optional sensitive payloads, health output reflects the setting without exposing secrets, and semantic events are unchanged.

### `OBS-A04` — Feature refresh

A runtime gate refreshes during a turn. Exposure remains deduplicated. Sticky turn/session behavior does not change until its declared boundary; a dynamically permitted UI hint may update without altering model/tool semantics.

### `OBS-A05` — Queue and trace pressure

Event production exceeds the 8,192 exporter queue and 100,000 local trace cap. Defined overflow/drop summaries apply, memory remains bounded, authoritative usage remains exact, and shutdown still meets its bound.

### `OBS-A06` — Every sink fails

Logging, metrics, traces, feature exposure, update check, and notification reporting all throw. One local edit turn produces the same permission decision, file side effect, transcript records, result, and exit status as a no-sink run.

### `OBS-A07` — Prolonged fallback outage

Fail more batches than both the file-count and aggregate-byte fallback budgets, give existing files equal timestamps, and restart with an externally populated 1,025-entry directory. Disk usage stays within the configured caps, equal-time eviction is filename-deterministic, expired and quarantined artifacts count toward retention, eviction/drop counters provide evidence, and restart rejects the oversized scan without unbounded memory or CPU.

### `OBS-A08` — Hostile fallback and sink

Inject duplicate-member, unknown-field, excessively nested, oversized, hard-linked, permission-relaxed, and directory-replacement fallback entries while an exporter restarts. Only sealed format corruption receives an owner-only quarantine name; unsafe or uncertain filesystem evidence remains untouched, no malformed record reaches the sink, and integer attributes survive retry without precision loss. On a platform without truthful owner-only or single-link proof, configuring fallback fails before worker startup. Persist an optional batch while analytics is enabled, then disable it before restart: retry sends nothing, removes the eligible historical batch with drop evidence, and does not label valid prior bytes corrupt. Separately make export, persistence, and shutdown ignore context forever: close returns by its 500-millisecond or earlier caller deadline, each boundary strands at most one callback, queued-byte accounting reaches zero, and releasing the callbacks lets the worker terminate.

### `OBS-A09` — Headless turn diagnostics

Run one successful persistent text-output turn and one provider-failure turn with default logging, then repeat with debug enabled. stdout remains byte-for-byte the selected text/protocol result. Default stderr has no routine start or successful-completion record; the provider failure retains one ERROR terminal record with a bounded provider-owned safe diagnostic. Debug stderr additionally contains exactly one correlated turn start and terminal record plus session, model-iteration, validated stream-event, retry, capability, usage, and timing metadata, without any prompt, answer, argument, result, credential, exact path, request body, or header. Both levels produce equivalent normalized durable lifecycle history: an accepted user event identifies the start, completed provider usage remains durable under the same turn ID, and exactly one terminal turn-result closes the turn. Repeat with a nil writer and writers that panic, fail, or short-write; semantic outcomes, transcript bytes, tool effects, and exit status remain identical.

### `OBS-A10` — Multi-turn maximum-safe diagnosis

Run two model-backed turns in one persistent session: one performs a retry and capability call before success, and one receives a nonretryable provider configuration rejection. Repeat at INFO and DEBUG and through text, JSON, and stream-JSON surfaces. Normalize random IDs/timestamps and verify semantic stdout plus durable transcript lifecycle are level-independent. INFO retains the retry WARN and terminal ERROR but no routine start/success records. DEBUG has distinct turn IDs, exactly one start and terminal record per admitted turn, and stage-correlated iteration, stream, retry, capability, usage, duration, request, discovery-generation, and finalization evidence sufficient to locate the failing stage. A zero-skill generation reports only safe gates, root relationship/state, counts, and omission reasons. Records may contain a short repository-relative caller and no automatic stack; they contain no credential, prompt/answer, tool payload, skill body, arbitrary frontmatter, endpoint, deployment, exact configured API version, URL/query, header/body, file content, or exact workspace path. A strictly validated provider-reported minimum version may appear only as public remediation metadata.

Separately fail the accepted-user transcript append after the DEBUG attempt-start record. Diagnostic start and ERROR finalization evidence may exist, but no diagnostic line becomes an authoritative transcript start or permits a fabricated durable terminal event. The failed append, logger level, and logger sink cannot change semantic or recovery truth.

### `OBS-A11` — Attachment privacy under failure

Import files whose absolute path, name, bytes, base64, PDF strings, and image
metadata each contain sentinel secrets. Exercise malformed chunks, MIME and
digest mismatch, timeout, cancellation, transcript failure, provider
rejection, and DEBUG diagnostics. If the selected profile includes an automatic
crash-report adapter, exercise it too; the current standalone Go profile has no
such adapter and must record that route as unavailable rather than infer
coverage from ordinary logging. Verify no
sentinel, raw path, data URL, provider body, or runtime path reaches any output
or observation sink; only bounded classifications and allowed correlations
remain, and every semantic cleanup outcome is identical with all sinks
disabled.

## Provenance

Non-normative evidence was surveyed in usage/cost state, analytics and feature-evaluation services, first-party and secondary exporters, diagnostic/logging utilities, tracing utilities, startup profiling, cleanup, and shutdown integration. All implementation-critical behavior is restated above.
