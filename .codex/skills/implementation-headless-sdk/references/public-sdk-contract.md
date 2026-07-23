# Public SDK facade and schema boundary

This reference defines the embeddable SDK surface that surrounds the structured protocol. It distinguishes public behavioral APIs from declaration-only artifacts, versioned alpha helpers, daemon primitives, and serialized wire schemas. A standalone package must provide the behavior below even when the documented build's declaration facade contains throwing placeholders that are replaced by its published runtime build.

## Contents

1. [Facade and compatibility](#facade-and-compatibility)
2. [Query and in-process MCP](#query-and-in-process-mcp)
3. [Persistent session helpers](#persistent-session-helpers)
4. [Scheduled work](#scheduled-work)
5. [Remote-control handle](#remote-control-handle)
6. [Sandbox configuration schema](#sandbox-configuration-schema)
7. [Wire-schema ownership](#wire-schema-ownership)
8. [Acceptance scenarios](#acceptance-scenarios)

## Facade and compatibility

**SDKAPI-001 — Declaration/runtime separation.** The import facade exports serializable core types, runtime callbacks/interfaces, settings types, tool types, and alpha control request/response types. Some reference declaration bodies throw “not implemented”; those throws are packaging placeholders, not the intended public runtime. An implementation must either ship working implementations or fail import/build-time capability negotiation—never advertise callable APIs that always throw at first use.

**SDKAPI-002 — Stability labels.** Ordinary query/message/configuration contracts are the stable surface. V2 persistent sessions and direct control protocol types are explicitly alpha/unstable. Daemon scheduler and remote-control primitives are internal. Compatibility changes to an unstable or internal member still require a wire/schema version when persisted sessions or another process can observe them.

**SDKAPI-003 — Abort identity.** Expose a distinct SDK abort error so clients can distinguish caller cancellation from model, protocol, permission, and transport failures. Cancellation still produces the terminal structured accounting required by the selected query adapter; catching the SDK abort object must not strand the child process or iterator.

## Query and in-process MCP

**SDKAPI-010 — Tool builder.** Define an in-process MCP tool from canonical name, description, input schema, asynchronous handler, and optional annotations, search hint, and eager-load marker. Validate handler arguments against the supplied schema before invocation and return the standard MCP call result. Tool construction grants no permission; the tool enters normal registry, hook, policy, and authorization flow.

**SDKAPI-011 — In-process MCP server.** Create a named MCP server configuration with optional version and tool list. Its instance runs in the embedding process and can be supplied wherever SDK MCP configuration is accepted. Shutdown closes outstanding calls, and calls longer than the host stream-close deadline require an explicit timeout configuration rather than an unbounded process hold.

**SDKAPI-012 — Query iterator.** `query` accepts either one text prompt or an asynchronous stream of structured user messages plus options. It returns an asynchronous query handle that projects the same SDK messages and result schemas as stream-json. Only one semantic turn runs at a time per session handle; controls remain responsive through the separate input path. Iterator close/abort settles pending controls and owned subprocesses according to `RUN-*` and `WIRE-*`.

## Persistent session helpers

**SDKAPI-020 — V2 session lifecycle.** Create returns a multi-turn persistent session handle from session options. Resume binds an existing session identifier and applies only explicitly compatible overrides. One-shot prompt is convenience sugar that creates the required runtime, sends one message, waits for the terminal result, and closes it. It cannot change message, permission, usage, or result semantics merely because it is a convenience call.

**SDKAPI-021 — Session-message read.** Read one transcript, implement the selected conversation chain through parent links, and return user/assistant messages chronologically. Optionally include system records. Missing session returns an empty list. Offset and limit apply after coherent chain projection, not to arbitrary physical file lines.

**SDKAPI-022 — Session inventory.** Listing with a directory scopes to that project and its worktrees; omission searches all known projects. Pagination is deterministic. Single-session lookup reads only the resolved file and returns absent for missing, sidechain-only, or unsummarizable sessions. Neither operation mutates recency, tags, or transcript history.

**SDKAPI-023 — Session mutations.** Rename appends a custom-title metadata event. Tag appends/replaces tag metadata, and null clears it. Both resolve the exact session under the optional directory scope, preserve append-only transcript semantics, and fail explicitly on ambiguity or write failure.

**SDKAPI-024 — Session fork.** Fork copies the selected source chain up to the optional checkpoint, assigns a fresh session ID, remaps every copied message UUID and parent link consistently, optionally assigns a title, and writes a coherent new transcript. File undo/history snapshots are not copied. The result contains the new session ID only after its transcript is durably created.

## Scheduled work

**SDKAPI-030 — Scheduled-task record.** A scheduled task contains stable ID, cron expression, prompt, creation time, and optional recurring marker. Watch emits either `fire` for one task or `missed` for a batch. It exposes the next planned epoch time or null without requiring event consumption.

**SDKAPI-031 — Scheduler ownership.** One watcher owns the per-directory PID/liveness lock, file watcher, and timer. Abort releases all three. One-shot tasks are removed before their fire event; recurring tasks are rescheduled or aged out. Missed one-shot tasks are emitted once on initial load and deleted best-effort shortly afterward. A print-mode agent does not run its own scheduler when a daemon owns it.

**SDKAPI-032 — Jitter and missed-work prompt.** Accept runtime-provided recurring fraction/cap, one-shot floor/maximum/minute modulus, and recurring maximum age. The missed-work formatter asks the model to obtain user confirmation before execution; it never silently converts downtime into immediate side effects.

## Remote-control handle

**SDKAPI-040 — Parent-owned bridge.** Internal remote-control connect accepts directory, optional human identity/repository fields, access-token callback, service base URL, organization, and model. It returns null on absent OAuth or registration failure; otherwise the parent process owns a stable remote session even if a child query process crashes. This is distinct from child-owned remote control, which dies with that child.

**SDKAPI-041 — Remote-control channels.** The returned handle exposes session URL, environment ID, and bridge session ID as distinct values; asynchronous inbound prompt, control-request, and permission-response streams; writers for SDK messages, result markers, controls, responses, and cancellations; state subscription over `ready`, `connected`, `reconnecting`, and `failed`; and idempotent teardown. `sendResult` marks a projected turn result and is not a durable client receipt.

**SDKAPI-042 — Pre-entitled is not pre-authorized.** The internal connector may skip product eligibility gates because its caller is already entitled, but it still requires OAuth and never treats transport authentication as permission to execute a tool. Every inbound mutation crosses local control and permission boundaries.

## Sandbox configuration schema

**SDKAPI-050 — Network schema.** Optional network configuration accepts allowed domains, managed-only allowed-domain selection, allowed Unix-socket paths, allow-all Unix sockets, local binding, and HTTP/SOCKS proxy ports. Managed-only affects allow sources, not denials. Unix path filtering is platform-dependent and must report unsupported behavior rather than imply enforcement.

**SDKAPI-051 — Filesystem schema.** Optional filesystem configuration accepts write allow/deny, read deny, read re-allow, and managed-only read allow paths. Re-allow is evaluated inside denied regions under the permission/sandbox precedence contract. Paths remain untrusted input and are normalized by the platform/permission boundary.

**SDKAPI-052 — Sandbox root schema.** Root settings accept enabled, fail-if-unavailable, automatic shell allow when sandboxed, whether explicit unsandboxed requests are honored, network/filesystem blocks, per-rule ignored violations, weaker nested or network isolation switches, excluded commands, and custom search executable/arguments. Unknown settings are retained for forward compatibility, including the externally consumed platform allowlist. A weaker network setting is an explicit security reduction and defaults off.

## Wire-schema ownership

**SDKAPI-060 — Closed discriminators.** Core schemas define the exact permission modes and published permission-result union, hook event names and event-specific inputs/outputs, configuration-change sources, instruction-load reasons and memory types, session-end reasons, MCP transport/config/status unions, user/assistant/result/system/lifecycle messages, and fast-mode state. Reject an unknown member of a security-sensitive discriminator unless its enclosing protocol explicitly defines forward-compatible ignore behavior. [The SDK permission wire catalog](sdk-permission-wire.md) is canonical for permission field spelling, optional/null behavior, and the narrower reference waiter parser.

**SDKAPI-061 — Control schema catalog.** Control schemas declare initialize, interrupt, tool permission, permission/model/thinking updates, MCP status/message/server replacement/reconnect/toggle, context usage, rewind, queued-message cancellation, read-state seeding, hook callbacks, plugin reload, task stop, runtime flag settings, settings read, elicitation, control response/error/cancel, keepalive, and environment update. The published outer response union is closed, but the reference stdio reader does not invoke the aggregate stdin validator: it performs minimal routing and then validates the subtype payload against the schema stored with the correlated waiter. `WIRE-009` and `WIRE-PERM-050..053` define that observable compatibility boundary; builders must still emit the published closed envelope.

**SDKAPI-062 — Schema/runtime drift.** If a runtime handler supports a newer internal control absent from the public schema, keep it transport-versioned and internal until the public union is deliberately expanded. Never widen the schema to arbitrary objects merely to hide drift. Generated type aliases and re-export modules are projections of these schemas and add no independent behavior.

**SDKAPI-063 — Event identity injection.** The bounded in-process SDK event queue accepts task start/progress/terminal and session-state events only in noninteractive mode, drops the oldest when its 1,000-entry cap is reached, and injects fresh event UUID plus current session ID on drain. A direct terminal helper is used only when the ordinary model-facing task-notification path did not already emit the same terminal event.

## Acceptance scenarios

### `SDKAPI-A01` — Fork link integrity

Fork through an intermediate message and verify every copied UUID is new, every parent points within the new chain or null root, later messages and undo snapshots are absent, and the source is unchanged.

### `SDKAPI-A02` — Scheduler single owner

Start two watchers for one directory and verify only the lock owner emits. Abort it, then verify the second can acquire and no one-shot task double-fires.

### `SDKAPI-A03` — Parent survives child crash

Connect the parent-owned remote handle, crash and replace a query child, and verify the handle retains remote logical identity while the new child receives only admitted inbound prompts. Pending child-local controls are explicitly failed or reissued, never silently inherited.

### `SDKAPI-A04` — Managed sandbox narrowing

Supply user and managed allowed domains with managed-only enabled plus a user deny. Verify only managed allows participate and the deny still applies. On a platform without Unix path filtering, verify an explicit unsupported result.

### `SDKAPI-A05` — Unknown security discriminator

Send a syntactically valid permission response with an unknown permission `behavior`. Verify waiter-payload rejection, denial/error, and no implicit allow. Separately send an unknown outer response subtype with a valid allow payload: verify the reference compatibility reader's non-`error` branch and the explicitly versioned hardened-envelope alternative in `WIRE-PERM-A04`. Send an unknown ordinary top-level event under the documented forward-compatible path and verify safe ignore.

### `SDKAPI-A06` — Queue cap and terminal deduplication

Enqueue 1,001 task events in headless mode and verify the oldest is absent on drain and every retained record gains a unique UUID/current session ID. Exercise a task path that already emits a model notification and verify the direct terminal helper is not also called.

## Non-normative provenance

Evidence came from the SDK facade, core and control schema catalogs, sandbox schema, generated type projections, and the bounded SDK lifecycle event queue. The original packaging language and placeholder function bodies are non-normative.
