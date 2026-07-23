# MCP configuration and runtime contract

This document defines MCP server sources, transport schemas, precedence, policy, connection state, discovery, request execution, output normalization, and cleanup. `MCP-*` identifiers are normative and stable.

## Contents

1. [Identity, scope, and transport](#identity-scope-and-transport)
2. [Configuration schemas](#configuration-schemas)
3. [Discovery and precedence](#discovery-and-precedence)
4. [Configuration editing](#configuration-editing)
5. [Managed allow and deny policy](#managed-allow-and-deny-policy)
6. [Connection state machine](#connection-state-machine)
7. [Discovery and cache invalidation](#discovery-and-cache-invalidation)
8. [Request scheduling and cancellation](#request-scheduling-and-cancellation)
9. [Output normalization and storage](#output-normalization-and-storage)
10. [Failure and recovery](#failure-and-recovery)
11. [Acceptance scenarios](#acceptance-scenarios)
12. [Non-normative provenance](#non-normative-provenance)

## Identity, scope, and transport

`MCP-001` — A server descriptor has canonical server name, scoped identity, source/scope, transport configuration, enable/approval/policy state, connection state, generation, and diagnostics. A new connection instance receives its own internal identity; it does not change the public server name.

`MCP-002` — Supported scopes are `local`, `user`, `project`, `dynamic`, `enterprise`, `agentx`, and `managed`. Preserve source because precedence, approval, policy, editing, and channel eligibility depend on it.

`MCP-003` — Supported transport tags are `stdio`, `sse`, `sse-ide`, `http`, `ws`, `ws-ide`, `sdk`, and `agentx-proxy`. A transport absent from a build is an explicit unsupported state, not an alias to a similar transport.

## Configuration schemas

`MCP-010` — Standard-input/output transport schema:

```text
{
  type?: "stdio",
  command: nonempty string,
  args?: string[] default [],
  env?: map<string,string>
}
```

Do not invoke through a shell unless the stdio adapter explicitly defines one. Validate executable and environment under ordinary permission/trust policy.

`MCP-011` — Remote transport schema:

```text
{
  type: "sse" | "sse-ide" | "http" | "ws" | "ws-ide",
  url: absolute supported URL,
  headers?: map<string,string>,
  headersHelper?: trusted command descriptor,
  oauth?: {
    clientId?: string,
    callbackPort?: integer,
    authServerMetadataUrl?: HTTPS URL,
    xaa?: boolean
  }
}
```

Reject credentials in invalid locations and unsupported URL schemes. Header helpers are code-bearing configuration and require source trust.

`MCP-012` — `sdk` and `agentx-proxy` definitions are constructed by their owning integration and validated through dedicated schemas; do not accept them from arbitrary project JSON.

`MCP-013` — Expand environment variables only through the MCP environment-expansion grammar; never invoke a shell to expand config. A missing variable may remain a detailed internal loader error for the affected definition, but every public descriptor/status diagnostic uses the fixed message `MCP configuration expansion failed` and omits variable, server, or source identity unless the complete configured credential union proves the final projection safe.

## Discovery and precedence

`MCP-020` — Discover project `.mcp.json` files from project root toward the active directory. For the same server name, the closer applicable project definition wins. Project definitions remain inactive until approved by the project-MCP approval store.

`MCP-021` — Merge ordinary active definitions with this precedence from lower to higher:

1. enabled plugin contributions;
2. user configuration;
3. approved project configuration;
4. local configuration.

Dynamic inputs are session contributions and remain separately attributed. Enterprise-managed MCP configuration is exclusive when valid: it replaces ordinary manual sources rather than merely winning name collisions.

`MCP-022` — Plugin-only customization policy retains enterprise/managed and enabled-plugin MCP definitions and filters ordinary user/project/local/dynamic definitions for the locked family.

`MCP-023` — Exact manual server identity wins over plugin identity. If multiple plugin definitions collide, deterministic plugin registry order selects the first. An enabled manual server wins over a AgentX Cloud connector of the same semantic endpoint. A disabled candidate does not suppress an otherwise eligible duplicate.

`MCP-024` — Semantic duplicate key is exact stdio command-plus-argument vector or canonical remote URL after unwrapping the supported relay/proxy representation. Environment and headers do not distinguish semantic duplicates. Record all shadowed sources for diagnostics.

## Configuration editing

`MCP-030` — A user-added name contains only alphanumeric characters, underscore, or hyphen and is nonempty. Reserved product integration names such as `chrome` and `computer` cannot be replaced by ordinary add operations.

`MCP-031` — Before persisting a server: validate transport schema, enforce enterprise exclusivity, apply allow/deny policy, reject semantic duplicate, and select editable scope. Write project files atomically using a temporary file, data flush, permission preservation, and rename.

`MCP-032` — Project configuration includes a disabled-server list. Built-in computer-use integration starts disabled and requires explicit enabled state. Disablement is source-specific and does not act as a global duplicate tombstone.

`MCP-033` — Project approval binds to the material server definition or a stable fingerprint, not only name. A security-relevant command/URL change requires renewed approval.

## Managed allow and deny policy

`MCP-040` — Collect deny rules from all policy sources; deny union wins. Allow policy is open when absent and deny-all when explicitly empty.

`MCP-041` — Policy entry dimensions include server name, stdio command identity, and remote URL pattern as supported by schema. If any allow entries specify commands, every stdio definition must match a command rule; name-only match is insufficient. If any allow entries specify URLs, every remote definition must match a URL rule.

`MCP-042` — Unknown/custom and SDK-provided definitions use their explicit policy category. The SDK control transport is exempt from ordinary filesystem MCP filtering only through its owning trusted SDK contract, not because the string `sdk` appears in user configuration.

`MCP-043` — Apply policy before process spawn, DNS/network connect, OAuth, or project approval prompts. Policy rejection is a disabled/blocked descriptor with reason.

## Connection state machine

`MCP-050` — Public connection states are `pending`, `connected`, `failed`, `needs-auth`, and `disabled`.

```text
configured + enabled + allowed -> pending
pending + initialize/discovery success -> connected
pending/connected + auth challenge -> needs-auth
pending/connected + terminal transport/protocol error -> failed
any + settings/policy disable -> disabled
failed/needs-auth + explicit retry/reconcile -> pending
any + removal/shutdown -> close transport, unregister, no descriptor
```

`MCP-051` — Batch observable state publication within about 16 ms so bursts do not render one update per server, while internal request state remains immediate.

`MCP-052` — Default constants:

| Operation | Bound |
| --- | --- |
| connect/initialize | 30 seconds, configurable by `MCP_TIMEOUT` |
| non-GET remote request | 60 seconds |
| GET/SSE stream | long-lived, governed by liveness/cancellation |
| tool request | 100,000,000 ms default, configurable by `MCP_TOOL_TIMEOUT` |
| needs-auth negative/status cache | 15 minutes |
| reconnection attempts | 5 |
| reconnect backoff | 1 s exponential, cap 30 s |
| server instructions/description | 2,048 characters each |

Treat environment overrides as bounded positive integers.

`MCP-053` — HTTP transport sends `Accept` compatible with both JSON and `text/event-stream`. Session-expiry evidence—HTTP 404 combined with JSON-RPC error `-32001`—closes the transport, clears session/fetch state, and requires a fresh connection on retry.

`MCP-054` — Register every stdio process, network transport, subscription, timer, and abort controller with idempotent cleanup. Start independent provider closes concurrently under one bounded manager deadline for shutdown and for stale or failed connections retired during reconciliation. Preserve deterministic reconcile/reconnect order with short internal leases and operation tokens rather than a mutex held across provider code. Invoke the factory and every connection callback—including state, generation, discovery, preparation, registration, cancellation, waiting, and close—without a manager lock; contain callback panics behind fixed diagnostics. Track a constructed candidate before calling `Connect`, invalidate active tokens when shutdown begins, and publish callback results only after revalidating the exact entry and current token. `Close` bypasses the operation queue, detaches both published and in-flight entries, and starts each entry's once-owned bounded cleanup even when an earlier callback never returns or re-enters the manager. Stdio shutdown sends graceful protocol/process signals within a bound, then escalates termination. Process exit always produces terminal connection state.

`MCP-055` — The standalone MCP host owns one bounded stdin scanner. Invoke its
host `Read` and `Close` callbacks through the shared input-isolation boundary:
preserve exact EOF, but replace every other error, invalid count, or panic
without inspecting the callback error graph. Start `Close` asynchronously so a
broken callback cannot delay cancellation or worker settlement, and never
dispatch a partial request after an input failure.

## Discovery and cache invalidation

`MCP-060` — On connected initialization, discover only capabilities advertised by the server: tools, resources, resource templates, prompts, instructions, logging/subscription features, elicitation, and channels as applicable.

`MCP-061` — Validate every remote descriptor: canonical safe name, JSON schema structure/depth, bounded descriptions, URI and MIME fields, and unique protocol identity. Omit a descriptor if its name, description, schema, or annotations reflect any source-classified server credential, including short values and JSON-escaped wire spellings. Classify explicit environment and header values as credentials when their canonical names match the shared sensitive-name policy, when a header carries a syntactically recognized Basic or Bearer scheme, or when expansion already tagged the value as sensitive. For header-name classification, normalize ASCII hyphen/underscore spelling before applying that policy so names such as `X-API-Key` and `Ocp-Apim-Subscription-Key` cannot evade the corresponding `API_KEY` or `SUBSCRIPTION_KEY` predicate; preserve the actual header spelling for transport. An ordinary operational value under a nonsensitive name remains provider configuration and does not enter the exact-secret set merely because it is explicit; renaming a host-model credential does not bypass the independent host-value isolation contract. Invalid entries are omitted without dropping valid siblings.

`MCP-062` — Cache tool/resource/prompt lists by server connection generation and capability. A protocol `list_changed` notification invalidates exactly the affected list and republishes the merged registry; it does not reset unrelated lists or servers.

`MCP-063` — Server instruction deltas are deliberate context changes with size bounds and provenance. They cannot modify core system policy or insert local credentials.

`MCP-064` — Bound each server's distinct source-classified credential set to at most 256 literals and 64 KiB in aggregate. Include sensitive-name environment/header values, complete Basic/Bearer header values and extracted scheme tokens, values tagged by sensitive expansion, and the union across same-name candidate configurations. Freeze the union across every configured definition before returning manager descriptors, status, diagnostics, tool catalogs/results, or cleanup errors: a disabled, rejected, failed, or zero-tool server can still be named or reflected by another provider and therefore remains relevant to those shared public projections. Keep the complete expanded configuration registry for diagnostics, reload, and later reconnect, but never copy invalid configuration identity into an unguarded diagnostic. Reject an over-budget provider/tool before constructing a result redactor so configuration cannot turn every result into unbounded literal-by-output work. Reject session/provider composition when the aggregate nonempty union has no set-safe streaming terminal marker; do not install a pre-closed sanitizer that silently converts safe output or successful tasks into empty success. Do not weaken classification because a sensitive value is short: if such a value makes an immutable protected frame unsafe, composition or that frame fails closed with a credential-free diagnostic.

## Request scheduling and cancellation

`MCP-070` — Local/stdio servers permit at most three concurrent requests by default. Remote servers permit twenty. Queue excess with cancellation and backpressure; do not create unbounded promises.

`MCP-071` — Every model-facing MCP tool invocation crosses ordinary schema validation, permission rules (`mcp__server__tool`), hooks, timeout, cancellation, and result pairing. Connection status alone is not authorization.

`MCP-072` — Correlate request identifier, tool-use identifier, manager publication generation, exact connection generation, catalog epoch, and cancellation. A binding-aware model-facing descriptor is callable only through the versioned catalog from which it was discovered. Discovery may perform provider I/O without a manager lifecycle lock, but publication reacquires one short lease and discards a catalog if manager publication or connection generation changed. Clone and validate custom catalogs before publication; cap each provider's catalog and diagnostics by both item count and its configured message-byte bound, discard provider-owned aliases, and replace untrusted diagnostics with fixed projections. Invocation may perform authority-free preparation without that lease. The built-in connection revalidates manager publication and atomically registers its exact connection/catalog version under one short lease; arbitrary external registration callbacks run outside the lease after a short manager acceptance snapshot and must validate the supplied version without provider I/O before `Await`. Validate, size-bound, clone, and credential-check custom tool results before returning them. Release every manager lease before provider I/O or response waiting so reconciliation, reconnect, and bounded close cannot be held hostage by an uncooperative callback or call. Keep plain exported `Connection` implementations source- and behavior-compatible through an explicit static-catalog binding to the captured connection instance and manager/connection generation; they do not gain dynamic catalog-epoch guarantees, but a later lookup can never redirect their call to a replacement instance. A stale binding or late result from an old generation is discarded but receives cleanup/accounting; it cannot execute through or satisfy a newer provider with a reused public name.

`MCP-073` — Cancellation sends protocol cancellation when supported, aborts transport wait, removes queued work, and emits one terminal tool result. A sibling failure does not leave accepted request IDs unmatched.

## Output normalization and storage

`MCP-080` — Normalize MCP content blocks—text, image, audio, resource links, embedded text/blob resources, structured content, and protocol errors—into shared tool-result blocks while retaining MIME type and safe annotations. Merge every source-classified server credential from `MCP-061` and `MCP-064` with the session set before projection, and carry that bounded aggregate union to provider request framing plus shared manager/config, model-request, hook, transcript, task, result-index, and structured-output validation boundaries without exposing literals to capability implementations. Sanitize decoded structured/resource JSON, every duplicate member occurrence, metadata, and joined text before applying message/result bounds. Construct and inspect each exact physical encoding—including the terminating newline of stdio JSON-RPC and JSONL records—and every complete public result/diagnostic envelope so fields, descriptors, records, early pagination diagnostics, or a safe body suffix plus its delimiter cannot reconstruct a source credential after validation. Partial lists and accumulated diagnostics are discarded on any later page/cursor/request failure. Classify provider failures only from exact sentinels, manager-owned context state, detached values, and package-sealed snapshots. The manager may traverse exact standard-library or package-owned wrappers, but stops at a provider-owned child and never invokes provider `Error`, `Is`, `As`, or `Unwrap` behavior; a blocking error method cannot delay reconciliation, invocation, reconnect, or close, and an unknown failure receives a fixed diagnostic. Amplification, key collision, duplicate-member collapse, scalar suppression, structural reconstruction, unsafe diagnostic formatting, hostile error traversal, or missing safe terminal framing fails closed.

`MCP-081` — Output cap precedence is explicit environment override, then remote feature/config value, then 25,000 tokens. Use a cheap estimate around half the character count, then exact token measurement near/over the boundary. Persist oversized full output to protected tool storage and return a concise reference plus truncated preview.

`MCP-082` — Resize/normalize images to at most 1,600 pixels on the longest supported dimension before model projection unless a higher-detail protocol explicitly permits otherwise. Preserve original outside model context only under secure output storage policy.

`MCP-083` — Treat `isError` and JSON-RPC failure as terminal tool errors without discarding safe server-provided diagnostic content. Never render raw secrets/authorization response fields.

## Failure and recovery

| Failure | Required behavior |
| --- | --- |
| malformed server definition | disabled/omitted descriptor; no connect |
| one invalid discovered tool | omit tool; server remains connected |
| initialize timeout | failed or needs-auth as classified; transport closed |
| transient disconnect | bounded reconnect; active requests terminate/retry only per caller contract |
| session expiry | clear transport session and reconnect fresh |
| oversized output | secure persistence plus bounded result reference |
| shutdown hang | force close after bound; no process leak |

## Acceptance scenarios

1. **MCP-A01 — Semantic duplicate precedence.** Plugin and approved user config define the same remote URL under different names. User/manual definition wins semantic dedup; headers do not create two connections.
2. **MCP-A02 — Approval fingerprint.** A project server command changes after approval. Its fingerprint changes, it returns to unapproved state, and no subprocess starts.
3. **MCP-A03 — Enterprise exclusivity.** Enterprise MCP config is valid while user/local definitions exist. Only enterprise definitions enter the active registry.
4. **MCP-A04 — Targeted list invalidation.** A server emits tools-list changed. Tool list refreshes; cached resources and other servers remain untouched.
5. **MCP-A05 — Queued cancellation.** Twenty remote calls run and a twenty-first queues. Cancelling the queued call produces a terminal result without consuming a transport slot.
6. **MCP-A06 — Session expiry.** An HTTP session returns 404 plus `-32001`. Connection closes, session identity clears, and next request initializes a new session.
7. **MCP-A07 — Descriptor credential reflection.** A server places one short opaque value under a sensitive environment name and one JSON-escaped Basic/Bearer authorization value into tool descriptions, schema, or annotations. Repeat with `X-API-Key` and `Ocp-Apim-Subscription-Key` header names to prove hyphenated spelling reaches the sensitive-name classifier. Every affected descriptor is omitted with a safe diagnostic; healthy sibling descriptors remain discoverable. A nonsensitive operational `DEBUG=1` value remains usable configuration and is not treated as a credential, while `TOKEN=1` remains sensitive and cannot escape merely because its value is short.
8. **MCP-A08 — Uncooperative provider shutdown.** Two provider closes start together; one completes and one never returns. The manager reports the stubborn provider after its aggregate deadline, retains closed state on repeated calls, and does not wait forever before session shutdown can continue. Repeat while each factory, `Connect`, `Reconnect`, `State`, or `Generation` callback either never returns or re-enters `Close`: shutdown bypasses the serialized operation queue, closes every candidate whose construction completed exactly once, invalidates the callback's publication token, and finishes within its own aggregate deadline without publishing stale state.
9. **MCP-A09 — Bounded credential projection.** Configure same-name candidates whose distinct sensitive-name environment/header values exceed the literal-count budget, then a healthy sibling within budget. The oversized provider/tool is omitted with a safe diagnostic and performs no result redaction work; the sibling semantically sanitizes escaped structured/resource content and remains bounded after replacement amplification. Keep one disabled and one failed definition with incompatible credentials in the full registry: neither enters the active sink union, both remain visible to diagnostics and `/mcp reload`, and the frozen session cannot hot-enable either outside its union; a later session that publishes one builds the new union before exposing its descriptors or results. Construct a within-budget aggregate set that occupies every terminal-marker candidate and verify composition fails before any provider, stream, transcript, task, or structured sink starts. Give the sibling credentials that appear only when safe descriptor fields, hook/result fields, transcript/task fields, or structured-output fields are joined by their final JSON framing, and when a safe stdio request or JSONL body suffix is joined to its newline. Add an earlier duplicate JSON member whose escaped value decodes to a credential and a later safe member of the same name. The aggregate sink validator inspects every occurrence and rejects every complete frame before network, external-hook, durable, or stdout egress. As classification controls, `DEBUG=1` permits ordinary initialization and output while `TOKEN=1` is retained in the protected union and fails closed at any incompatible frame.
10. **MCP-A10 — Catalog-generation linearization.** Block tool-list I/O while reconciliation publishes a replacement; the stale catalog is discarded and does not block reconciliation or close. Block authority-free call preparation while replacement or close wins; built-in registration fails stale, cancels the preparation, and performs no provider I/O. Then register a current binding and block its response: reconciliation and bounded close still start without waiting for the response, while the accepted request remains paired only with its captured connection/catalog generation and can never redirect to the replacement. Repeat with an external binding-aware implementation whose registration callback blocks; manager lifecycle operations still proceed, and the callback cannot perform provider I/O before a version-checked `Await`. Finally, a plain exported connection retains its historical static tool catalog and invokes the captured instance, while replacement or connection-generation drift fails stale instead of redirecting the call.
11. **MCP-A11 — Initialize/process-exit race.** A stdio child returns a valid initialize result, receives the initialized notification, and exits before `Connect` publishes its final state. The process-exit failure is monotonic: connect rechecks the same process identity and state under lock, never overwrites `failed` with `connected`, returns a terminal failure, and leaves no callable transport. Repeat while close or reconnect advances generation; the stale finalizer cannot mutate the new generation.
12. **MCP-A12 — Direct projection union.** Give server A a credential equal to server B's otherwise valid name, then reconcile both. Give an invalid server a name/source equal to its own credential, and let a list accumulate an invalid-descriptor diagnostic before a later page fails. Manager snapshot, debug formatting, tool/result projection, cleanup, and the failed list return no credential, partial descriptor, or partial diagnostic; valid credential-free generations remain callable.
13. **MCP-A13 — Standalone input isolation.** Feed the standalone host an
uncomparable reader error whose `Error`, `Is`, and `Unwrap` panic, then invalid
counts and panics from `Read`, plus blocking or panicking `Close`. Verify no
error method executes, no request reaches a handler, the host reports only its
fixed input failure, and cancellation remains bounded.

## Non-normative provenance

Reference behavior was specified from MCP schemas/types, configuration merge and approval services, policy validation, transport factories, connection manager, request scheduler, discovery caches, output storage/truncation, and connection UI hooks under `services/mcp/` and MCP utilities/tools. Paths and symbols are provenance only.
