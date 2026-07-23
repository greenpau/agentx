# Insights workflow contracts

## CMD-WF-INSIGHTS-001 — Session-history insights report

### Entry, gates, and authority

`/insights [arguments]` is registered as a prompt-expanding command, but its expansion performs a substantial local workflow before returning model-visible text. The command owns local transcript discovery, optional remote collection, derived caches, model-assisted classification and narrative generation, HTML rendering, local persistence, and optional internal upload. The ordinary query loop owns only the final short acknowledgement requested by the returned prompt.

The command is available when local history/report support is present. Its only specified option is detected by whether the raw argument string contains the literal `--homespaces` token text. Unknown text is otherwise ignored. Remote homespace collection is attempted only for an internal-user profile; the same option under an external profile must not acquire remote-shell authority.

The report workspace is a private usage-data directory under the product configuration home. Create directories with owner-only access where the platform supports modes. Write report, metadata, and facet files with owner-only file access. Treat transcript contents, goals, categories, errors, and work-style summaries as private data.

### State machine

| State | Required behavior | Next state / terminal |
| --- | --- | --- |
| `I0 accepted` | Snapshot current configuration home, profile eligibility, arguments, and local project-history root. Do not append a model message yet. | `I1 remote-collection` when eligible and requested; otherwise `I2 discover`. |
| `I1 remote-collection` | Ask the remote-workspace CLI for running workspaces with a 30-second bound. For each running host, independently count/copy its project-history tree. Bound each remote count at 30 seconds and each recursive copy at 300 seconds. Copy into a temporary root, then merge each project directory into the local discovery namespace using a host suffix. Never overwrite an already merged destination. Hosts may run concurrently. | Always continue to `I2 discover`; host failures are partial warnings, not global failure. Clean the temporary root best-effort. |
| `I2 discover` | Enumerate every project-history directory and its lightweight session metadata. Sort newest first. Yield periodically while walking large histories so interactive rendering remains responsive. | `I3 metadata-cache`. |
| `I3 metadata-cache` | Read cached session metadata in batches of 50. Invalid cache records are misses. Fully load at most the newest 200 uncached sessions, in batches of 10; parse/load failures are skipped. Reject meta-sessions identified by their early user-content markers and sessions with unusable dates. Persist successfully derived metadata independently. | `I4 canonicalize`. |
| `I4 canonicalize` | Group branches carrying the same session identity. Keep the candidate with the greatest user-message count, breaking ties by duration. A session is substantive only with at least two user messages and at least one minute duration. | `I5 facet-cache`. |
| `I5 facet-cache` | For each substantive session, read and schema-check the cached facet. Delete invalid cached facets. Select at most 50 uncached sessions for new extraction and run those extractions with a maximum concurrency of 50. | `I6 aggregate`. |
| `I5a extract-facet` | Build a transcript suitable for analysis. If it exceeds 30,000 characters, split into 25,000-character chunks and summarize chunks in parallel, with at most 500 output tokens per chunk. If a chunk summary fails, substitute a 2,000-character truncation of that chunk. Ask a model for one JSON object with no tools, agents, or MCP capabilities, noninteractive permissions, and at most 4,096 output tokens. Extract a JSON object, validate it, and persist only a valid facet. | Return valid facet or `null`; a failed session does not fail the report. |
| `I6 aggregate` | Exclude sessions whose only goal category is `warmup_minimal`. Aggregate sessions, messages, duration, token usage, response times, tool/error/language/edit/write counts, commits/pushes, agents, MCP, web use, and multi-session overlap. Count a multi-session pattern only for the ordered `session A → session B → session A` pattern within a sliding 30-minute window. Accept response times only from 2 seconds through 1 hour. | `I7 narratives`. |
| `I7 narratives` | Generate six standard narrative sections independently and concurrently; an internal profile may request two additional sections. Each section receives bounded aggregate/facet evidence, cannot call tools, and has an 8,192-output-token ceiling. A failed section becomes absent rather than aborting the report. Derive the at-a-glance material from available evidence, not from invented replacement facts. | `I8 render`. |
| `I8 render` | Render one self-contained HTML report. Omit failed/absent sections explicitly or structurally; never represent them as successfully analyzed. Write the final report path with owner-only access. The final report write is the report commit point. | `I9 publish`. |
| `I9 publish` | External profiles use a local file URL/path. Internal profiles may upload the report with the internal file-transfer client to the configured report destination, bounded at 60 seconds. Upload failure falls back to the local file URL plus a manual upload instruction. | `I10 prompt-result`. |
| `I10 prompt-result` | Return hidden prompt content that supplies the resolved report location and instructs the main model to emit the exact concise, report-ready acknowledgement. The full derived analysis is not duplicated into visible chat. | Terminal `completed`, possibly with partial-analysis or upload-fallback notes encoded in the result. |

### Derived-data rules

- Metadata and facets are caches, not transcript authority. A corrupt cache entry is deleted or ignored and recomputed when within the current cap.
- Persist each successful metadata/facet record as soon as it is valid. Cache population is intentionally non-transactional; later failure leaves reusable private cache entries.
- Do not process more than 200 uncached full sessions or 50 uncached facet extractions in one invocation. Already cached valid records can increase total report coverage beyond those caps.
- Branch deduplication precedes substantive filtering and aggregation.
- A failed remote host, transcript parse, facet, narrative section, or upload is a scoped partial failure. The report remains useful when local discovery and final rendering succeed.
- Do not grant report-analysis model calls any command, tool, MCP, agent, or filesystem capability. Their only output is bounded derived text/JSON returned to the workflow.

### Interactive, headless, and remote behavior

The same prompt-expansion result can be used by interactive and supported noninteractive surfaces. Interactive presentation may show progress, but progress is ephemeral and must not become transcript content. Headless mode must expose failure or the final report location through its ordinary command-result projection; it must not attempt an interactive picker.

Bridge/remote command safety does not imply permission to scrape a remote controller's workstation. `--homespaces` invokes the explicitly configured internal remote-workspace CLI from the execution host only. If that profile or executable is absent, continue with local history.

### Cancellation, retry, and partial failure

Specified analysis calls create their own cancellation controllers rather than inheriting the outer command signal. Therefore canceling the outer turn does not reliably stop already-started facet and narrative requests. Preserve this as an explicit compatibility boundary if exact behavior is required; a new implementation may improve propagation only as a deliberate behavior change with tests proving that cache files and the report commit remain coherent.

No global rollback exists:

- remote temporary data is cleaned best-effort;
- merged remote history and successful metadata/facet caches may remain;
- a previous report remains if failure occurs before the new final write;
- the newly written local report remains when upload fails;
- partial model-derived sections are permitted, but an entirely failed render/write is terminal failure.

Retry is user-driven. A later invocation reuses valid caches, recomputes invalid/missing entries within caps, and overwrites the final report only at the commit point.

### Terminal outcomes

| Outcome | Required visible meaning |
| --- | --- |
| `completed-local` | Report exists locally and the returned prompt points to it. |
| `completed-uploaded` | Internal upload succeeded and the returned prompt points to the uploaded location. |
| `completed-partial` | Report exists, while one or more remote hosts, facets, or narrative sections failed. Do not call this a full analysis. |
| `completed-upload-fallback` | Local report exists; upload failed; result supplies local location and manual recovery instruction. |
| `failed-before-report` | Discovery/aggregation/rendering could not produce a report. Emit a local command error and no success prompt. |
| `cancel-observed` | UI/turn cancellation is acknowledged; in-flight detached analysis may still settle and cache. Never claim those calls were aborted unless their signals were actually propagated. |
| `disabled` | Command is absent or rejected locally when report support is gated; no history scan or model request occurs. |
