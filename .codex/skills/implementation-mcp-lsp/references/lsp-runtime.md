# Plugin language-server runtime contract

This document defines LSP configuration, process lifecycle, initialization, requests, crash recovery, diagnostics, and reload. `LSP-*` identifiers are normative and stable.

## Contents

1. [Source and identity](#source-and-identity)
2. [Configuration schema](#configuration-schema)
3. [Manager initialization generation](#manager-initialization-generation)
4. [Server state machine](#server-state-machine)
5. [Crash recovery](#crash-recovery)
6. [Document and query protocol](#document-and-query-protocol)
7. [Diagnostics](#diagnostics)
8. [Failure and recovery](#failure-and-recovery)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Non-normative provenance](#non-normative-provenance)

## Source and identity

`LSP-001` — Language servers are contributed only by enabled plugins. Ordinary user/project standalone definitions are unsupported. Public scoped name is `plugin:<plugin-name>:<server-name>` and scope is dynamic/session-owned.

`LSP-002` — Load each plugin's conventional `.lsp.json`, then apply manifest-declared LSP overrides for that plugin. Initialize eligible plugins concurrently but merge results in deterministic original plugin order; later entries replace an identical scoped server identity.

`LSP-003` — Resolve all relative command/workspace/config paths inside the canonical plugin root. Reject traversal and unsafe symlink escape. Expand only approved variables: plugin root, validated plugin user configuration, and general environment variables allowed by configuration policy.

## Configuration schema

`LSP-010` — Server configuration fields:

| Field | Constraint |
| --- | --- |
| `command` | nonempty; spaces invalid unless represented as an absolute executable path supported by platform |
| `args` | array of nonempty strings |
| `extensionToLanguage` | nonempty map from extension to language identifier |
| `transport` | `stdio` or `socket`, default `stdio` |
| `env` | string map |
| `initializationOptions` | JSON value/object |
| `settings` | JSON configuration object |
| `workspaceFolder` | validated relative/absolute workspace descriptor |
| `startupTimeout` | positive integer |
| `shutdownTimeout` | positive integer in schema |
| `restartOnCrash` | boolean in schema |
| `maxRestarts` | nonnegative integer, default 3 |

`LSP-011` — The specified runtime accepts `shutdownTimeout` and `restartOnCrash` in the schema but rejects configurations that set them because their semantics are not implemented. Preserve this explicit unavailable behavior rather than pretending the fields work.

`LSP-012` — Configuration failure affects only that server/plugin component. It is reported with plugin and field provenance; core startup and other servers continue.

## Manager initialization generation

`LSP-020` — Global initialization status is `not-started`, `pending`, `success`, or `failed`. Bare mode remains not-started/disabled. A failed initialization may retry on explicit demand or next valid generation.

`LSP-021` — Assign a generation before asynchronous discovery/start. Every completion verifies generation before publishing server/diagnostic state. Plugin reload increments generation, requests old managers to stop best-effort, and begins the new initialization; stale old output is discarded.

`LSP-022` — Optional LSP initialization failure is nonfatal. Model-visible LSP tools may remain deferred while status is pending/not-started and become unavailable with a clear diagnostic on failure.

## Server state machine

`LSP-030` — Per-server states:

```text
stopped -> starting -> running -> stopping -> stopped
starting/running/stopping -> error
error -> starting (only within restart/retry policy)
```

Never publish running before the initialize handshake succeeds.

`LSP-031` — Spawn without an implicit shell, with validated executable, arguments, environment, working folder, stdio/socket adapter, process identity, deadline, and cancellation. Capture stderr with size limits for diagnostics.

`LSP-032` — Initialization sends process identity, workspace folder plus legacy `rootPath`/`rootUri`, initialization options default `{}`, and fixed client capabilities. After valid response, send `initialized`; only then route ordinary queries.

`LSP-033` — Client capabilities include UTF-16 positions, text-document synchronization with save, related/tagged diagnostics, hover Markdown/plaintext, definition links, references, hierarchical document symbols, and call hierarchy. Do not advertise workspace configuration or folder-change support that the client does not implement.

`LSP-034` — Shutdown sends protocol shutdown/exit and waits the supported bound; because configured `shutdownTimeout` is unavailable per `LSP-011`, use the runtime's fixed bound. Then kill process and close streams idempotently.

## Crash recovery

`LSP-040` — Unexpected process/transport termination while running enters error. Restart only when runtime's supported crash-recovery policy is enabled and count is below `maxRestarts` (default 3). Reset or retain count only at a documented healthy-generation boundary.

`LSP-041` — Restart creates a fresh connection/request sequence. Fail all pending old-generation requests explicitly. Late responses and diagnostics from the old process are discarded.

`LSP-042` — Exhausting restart budget leaves the server failed/unavailable and reports bounded stderr/cause. It never enters an unbounded crash loop.

## Document and query protocol

`LSP-050` — Track opened document URI, language ID, version, and current content hash. Send open/change/save/close notifications in order as supported. Version is monotonic for a document generation.

`LSP-051` — LSP tool rejects files over 10 MiB. Symbol-oriented preliminary read is capped at 64 KiB. One language query has a five-second timeout unless the tool contract specifies a narrower bound.

`LSP-052` — Translate user/runtime positions consistently to UTF-16 code units. Validate returned ranges/URIs before file access or display; server results are untrusted.

`LSP-053` — On content-modified error `-32801`, retry at most three times after 500 ms, 1,000 ms, and 2,000 ms, refreshing document state as appropriate. Cancellation stops retries immediately.

`LSP-054` — Unsupported server method or capability yields an unavailable/empty result according to the tool schema, not a process crash. Do not send methods the initialize result did not advertise unless the protocol permits them independently.

## Diagnostics

`LSP-060` — Cap diagnostics at 10 per file and 30 total per delivery/projection. Track at most 500 delivered files to prevent unbounded session memory.

`LSP-061` — Cross-turn duplicate key includes message, severity, and exact range. Suppress duplicate model/user delivery while retaining current server state. A materially changed range/severity is new.

`LSP-062` — Clear delivered diagnostic state for a file when it is edited so corrected/reintroduced diagnostics can be surfaced. Close/removal also clears server-owned diagnostic state.

`LSP-063` — Validate diagnostic URI under workspace/path policy, range bounds, severity/tags, related information, and message size. Never follow a diagnostic URI into unauthorized file access.

## Failure and recovery

| Failure | Required behavior |
| --- | --- |
| plugin LSP config malformed | omit server; plugin's other components survive |
| startup timeout | kill process, failed status, bounded retry on next trigger |
| one request timeout | fail request; server may remain running |
| protocol parse corruption | error state and bounded restart |
| content modified repeatedly | fail after three retries |
| plugin reload race | old generation cannot publish; old process stops best-effort |
| diagnostic flood | cap/deduplicate; do not grow transcript unbounded |

## Acceptance scenarios

1. **LSP-A01 — Deterministic plugin order.** Two plugins initialize out of order, but deterministic plugin order makes the later configured scoped identity win.
2. **LSP-A02 — Unsupported configured behavior.** A config sets valid `restartOnCrash`. Schema parsing succeeds, runtime emits explicit unsupported-field failure, and no server starts under false semantics.
3. **LSP-A03 — Restart ceiling.** Server crashes four times with default budget. At most three restarts occur; final status is failed and pending requests are terminal.
4. **LSP-A04 — Content-modified retry.** A request receives `-32801` four times. Waits are 500, 1,000, and 2,000 ms; the fourth response fails without another retry.
5. **LSP-A05 — Diagnostic cap and reset.** A server sends 100 diagnostics across a file. Only 10 for that file and 30 total are projected, duplicate hashes are suppressed, and an edit clears the file's delivered set.
6. **LSP-A06 — Reload generation.** Plugin reload occurs while old initialize is pending. Old success is ignored by generation check and its process is stopped; only the new generation publishes.

## Non-normative provenance

Reference behavior was specified from plugin LSP config loading, manager/server/client state, diagnostics registry, process transport, and LSP tool adapters under `services/lsp/`, plugin loaders, and language-tool definitions. Paths and symbols are provenance only.
