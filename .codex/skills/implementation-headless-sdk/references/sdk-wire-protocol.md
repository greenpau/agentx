# Structured SDK Wire Protocol

## Contents

1. [Framing and envelopes](#framing-and-envelopes)
2. [Input records](#input-records)
3. [Core output records](#core-output-records)
4. [System and lifecycle events](#system-and-lifecycle-events)
5. [Control request and response protocol](#control-request-and-response-protocol)
6. [Control operations](#control-operations)
7. [Permission race](#permission-race)
8. [Ordering, replay, and duplicate suppression](#ordering-replay-and-duplicate-suppression)
9. [Failure behavior](#failure-behavior)
10. [Acceptance scenarios](#acceptance-scenarios)
11. [Non-normative provenance](#non-normative-provenance)

## Framing and envelopes

Use newline-delimited JSON: one complete object per line, UTF-8 encoded. Escape Unicode line-separator and paragraph-separator characters so a consumer that splits on line boundaries cannot corrupt one record.

The incremental reader:

- accepts arbitrary chunks
- retains a partial trailing record
- processes a final unterminated record at EOF
- ignores blank lines
- parses records in arrival order
- may normalize documented compatibility aliases before schema validation

- **WIRE-001 — Parse failure.** Malformed JSON reports the offending line through diagnostics and terminates with status 1.
- **WIRE-002 — Unknown type.** A syntactically valid record with an unknown top-level type warns and is ignored for forward compatibility.
- **WIRE-003 — Shared FIFO.** Ordinary SDK events, outbound control requests, cancellation records and keepalives enter one FIFO writer with at most one drain in progress.
- **WIRE-EOF-001 — Final-record precedence.** At input EOF, parse and validate a nonempty unterminated tail exactly as a normal line before declaring the input closed. A valid final `control_response` resolves and removes its matching waiter, and is echoed only when replay mode requests it. Only waiters still pending after that dispatch receive stream-closed rejection; the matching response cannot lose to blanket EOF cleanup.

## Input records

| Type | Required fields | Optional/conditional fields |
| --- | --- | --- |
| `user` | API user message, `parent_tool_use_id` nullable | UUID, session ID, synthetic flag, tool-use result, priority `now/next/later`, originating timestamp; replay requires UUID, session ID and `isReplay=true` |
| `control_request` | `request_id`, request object containing `subtype` | subtype-specific fields |
| `control_response` | response object with `request_id` and subtype `success` or `error` | success payload; or error string and pending-permission recovery detail |
| `keep_alive` | none beyond type | ignored semantically |
| `update_environment_variables` | environment variable map | values follow the environment-update validation contract |

Inbound user role must be user. Replayed assistant/system records accepted by compatible transports implement prior context; they never execute historical tool calls.

## Core output records

Every ordinary session record carries a session identifier and UUID unless specifically documented otherwise.

**User**

- API user message.
- Nullable parent tool-use identifier.
- Optional synthetic flag, tool-use result, priority and originating timestamp.
- Replay form requires UUID, session ID and `isReplay=true`.

**Assistant**

- API assistant message.
- Nullable parent tool-use identifier.
- UUID and session ID.
- Optional error category: `authentication_failed`, `billing_error`, `rate_limit`, `invalid_request`, `server_error`, `unknown`, or `max_output_tokens`.

**Initialization system event**

- `type=system`, `subtype=init`.
- API key source, product version, cwd, tool names, MCP server name/status pairs, model, permission mode, slash-command names, output style, skill names, plugin descriptors, UUID and session ID.
- Optional agent names, API betas and fast-mode state.

**Success result**

- `type=result`, `subtype=success`.
- Total and API duration in milliseconds, error flag, turn count, result text, nullable stop reason, nullable total cost, aggregate usage, per-model usage with nullable cost, permission denials, UUID and session ID.
- Optional structured output and fast-mode state.

**Error result**

- `subtype` is `error_during_execution`, `error_max_turns`, `error_max_budget_usd`, or `error_max_structured_output_retries`.
- Same accounting fields as success plus an array of error strings; no success result text is required.
- The schema has no `cancelled` subtype and no separate cancellation-reason member. A hard SDK interruption of active work ordinarily ends as `error_during_execution` with `is_error=true`, durations, turn count, nullable stop reason, cost, aggregate and per-model usage, permission denials, errors, UUID, session ID, and optional fast-mode state. When caller cancellation is observed while opening or consuming the provider stream, the existing `stop_reason` field is `cancelled`, never `provider_error`.

Each permission denial records tool name, tool-use ID and effective tool input.

- **WIRE-012 — Unknown cost fidelity.** When authoritative pricing is unavailable, emit JSON `null` for both `total_cost_usd` and each affected model's `costUSD`. Emit numeric zero only when the accounting owner explicitly reports a known zero cost.
- **WIRE-013 — Whole-record credential validation.** Before the first structured write, install the immutable session/provider credential validator for that encoder. Marshal one complete record, apply every physical transformation including separator escaping and the terminating newline in memory, then validate those exact final bytes against the bounded union before the first write. Inspect every duplicate member occurrence; individually safe fields or a safe JSON suffix plus the line terminator may not reconstruct a literal. A validation failure writes no partial record, permanently fails that encoder with a credential-free diagnostic, and does not fall back to an unvalidated writer. The validator is immutable after encoding begins; a profile with no credential material retains ordinary byte-identical JSON.
- **WIRE-014 — Unambiguous members.** Reject duplicate JSON member names before map or struct decoding can apply last-member-wins behavior. Compare decoded member names so escape-equivalent spellings collide, and apply the rule recursively to control requests, control responses, operation payloads, and pending nested control records in both inbound and raw outbound projections. Canonical and documented alias names remain distinct members; their separate precedence rule still applies.
- **WIRE-015 — Callback containment.** Invoke the configurable whole-record validator and output writer outside the encoder state mutex while preserving serialized complete-record writes. A callback may reenter encoder state inspection/configuration and receive its normal post-start rejection without deadlock. Never format or traverse a callback-owned error. A validator rejection or panic and a writer failure, short write, or panic latch one fixed output failure; no later record invokes either callback. Preserve only exact trusted standard-library leaf identities needed by host exit policy through a sealed classification projection, without retaining the raw callback wrapper. Give the validator an exact copy so it cannot mutate committed bytes. Likewise, a control-broker emitter or ordered post-cancellation callback error/panic becomes one fixed emission failure. Roll back an unresolved waiter, preserve a synchronously selected result, and settle every detached cancellation waiter even when callbacks fail.
- **WIRE-016 — API-key source discriminator.** The `apiKeySource` vocabulary is
  the closed pair `user | temporary`: file-backed provenance emits `user`,
  transient process/flag provenance emits `temporary`, and absent provenance
  emits `user`. The standalone application-home `auth.json` profile always
  emits `user` in both the `system/init` record and the `initialize` response's
  `account` object. Never emit the credential path, field name, or value as
  source metadata.

## System and lifecycle events

| Discriminator | Required semantic data |
| --- | --- |
| `stream_event` | Raw model stream event, nullable parent tool-use ID |
| `system/compact_boundary` | trigger `manual/auto`, pre-compaction token count, optional preserved-segment head/anchor/tail IDs for resume relinking |
| `system/status` | status `compacting` or null; optional permission mode |
| `system/post_turn_summary` | summarized UUID, status category, title/detail/description, recent action, needed action, noteworthy flag and artifact URLs |
| `system/api_retry` | attempt, maximum retries, delay, nullable HTTP status and normalized error category |
| `system/local_command_output` | output content |
| `system/hook_started` | hook ID, name and event |
| `system/hook_progress` | hook identity plus stdout, stderr and combined output |
| `system/hook_response` | hook identity, streams/output, optional exit code and outcome `success/error/cancelled` |
| `tool_progress` | tool-use ID/name, parent ID, elapsed seconds and optional task ID |
| `auth_status` | authenticating flag, output lines and optional error |
| `rate_limit_event` | allowed/warning/rejected state and optional reset/utilization/overage data |
| `system/files_persisted` | successful filename/file-ID pairs, failed filename/error pairs and processing timestamp |
| `system/task_started` | task ID, description, optional tool-use ID/type/workflow/prompt |
| `system/task_progress` | task identity, description, usage, optional last tool and summary |
| `system/task_notification` | task identity, status `completed/failed/stopped`, output file, summary and optional usage |
| `system/session_state_changed` | `idle`, `running`, or `requires_action` |
| `tool_use_summary` | summary and preceding tool-use IDs |
| `system/elicitation_complete` | server and elicitation identifiers |
| `prompt_suggestion` | predicted next prompt |
| `streamlined_text` | internal text-only assistant projection |
| `streamlined_tool_use_summary` | internal cumulative tool summary projection |

- **WIRE-004 — Idle authority.** `session_state_changed=idle` is emitted only after a held result and finite background task loop have drained.
- **WIRE-005 — Compact relinking.** When preserved segment metadata exists, resume logic splices that exact segment around the named anchor rather than treating the summary as the whole prior context.

## Control request and response protocol

Outbound request:

```text
{
  type: "control_request",
  request_id: unique-string,
  request: { subtype: operation-name, ...operation-fields }
}
```

Inbound response:

```text
{
  type: "control_response",
  response: {
    subtype: "success" | "error",
    request_id: matching-string,
    response: operation-payload-if-success,
    error: message-if-error,
    pending_permission_requests: optional-array-of-complete-control-requests
  }
}
```

Cancellation:

```text
{ type: "control_cancel_request", request_id: matching-string }
```

- **WIRE-006 — Correlation map.** Register a pending request before its envelope can drain. Remove it exactly once on response, local abort, EOF or shutdown.
- **WIRE-007 — Abort.** Local abort enqueues cancellation, rejects the waiting operation immediately, and ignores an eventual known-late response.
- **WIRE-008 — EOF.** After final-record dispatch, input closure rejects every request still pending with a permission/control-stream-closed error.
- **WIRE-009 — Layered response validation.** The published wire schema defines a closed outer `success | error` union, while the reference stdio compatibility reader routes a parsed record with only minimal outer checks and validates the schema stored with the selected waiter before resolution. For the exact `can_use_tool` fields, absent/null rules, published-versus-runtime asymmetry, and unknown-subtype behavior, use [the SDK permission wire catalog](sdk-permission-wire.md). Never interpret the compatibility reader's non-`error` success branch as permission to skip the operation payload validator.
- **WIRE-010 — Interrupt acknowledgement.** An inbound `interrupt` aborts the active turn and prompt-suggestion scopes, then enqueues `control_response` with matching `request_id`, subtype `success`, and no operation payload. This acknowledgement confirms that the interrupt request was accepted; it is not the active turn's terminal result and does not close the process. If a turn was active, its ordinary `error_during_execution` result follows later.
- **WIRE-011 — Permission cancellation before interrupt acknowledgement.** Aborting the active turn synchronously invokes every pending host-permission waiter's abort listener. Each listener first enqueues `control_cancel_request` in the shared FIFO and rejects its local waiter; only after those listeners return does the interrupt handler enqueue its success response. Consequently, pending permission cancellations precede the interrupt acknowledgement. With no pending permission, the acknowledgement is the first interrupt-specific output.

## Control operations

| Subtype | Request | Response/behavior |
| --- | --- | --- |
| `initialize` | optional hooks, SDK MCP names, JSON schema, replacement/append system prompts, agent definitions, suggestion and progress-summary flags | commands, agents, current/available output styles, models, account, optional PID and fast-mode state |
| `interrupt` | no fields | abort active turn; immediately enqueue correlated success with no payload; ordinary terminal result follows separately when a turn was active; keep input/process open |
| `can_use_tool` | exact closed request in `WIRE-PERM-020..023` | exact decision and compatibility parser in `WIRE-PERM-040..053` |
| `set_permission_mode` | mode; optional internal plan marker | update subsequent permission behavior and emit status as needed |
| `set_model` | optional model identifier | update subsequent turns |
| `set_max_thinking_tokens` | nullable maximum | update thinking budget |
| `mcp_status` | none | current MCP server statuses |
| `get_context_usage` | none | categories, tokens, colors/deferred flags, totals, maximum and display grid data |
| `rewind_files` | user message ID, optional dry-run | can-rewind, optional error/files/insertions/deletions |
| `cancel_async_message` | queued message UUID | `cancelled=true/false` |
| `seed_read_state` | normalized path and observed modification time | seed only when disk is not newer; pending content overlay survives exactly one query clone/replace |
| `hook_callback` | callback ID, typed hook input, optional tool-use ID | callback payload; callback failure safely returns an empty result where the hook contract allows |
| `mcp_message` | server name and JSON-RPC message | route to named SDK MCP connection |
| `mcp_set_servers` | complete dynamic server map | respond with added, removed and per-server errors before initiating connections that could require callbacks |
| `reload_plugins` | none | refreshed commands, agents, plugins, MCP statuses and error count |
| `mcp_reconnect` | server name | reconnect explicit server |
| `mcp_toggle` | server name and enabled flag | change availability |
| `stop_task` | task ID | request terminal task stop |
| `apply_flag_settings` | settings map | merge flag layer and update live configuration |
| `get_settings` | none | effective merge, ordered raw sources, optional runtime-applied model/effort |
| `elicitation` | server, message, optional form/url mode, URL, ID and requested schema | action `accept/decline/cancel`, optional content |

Effective setting sources are ordered low to high priority: user, project, local, flag, policy; later sources override earlier ones. Runtime-applied model/effort may differ from the disk merge because environment and session defaults also participate.

Implementations may expose newer transport-specific controls such as end-session, OAuth/channel/auth/title/side-question or remote-control operations. Add them only as explicitly versioned schema members; do not silently accept untyped control objects.

## Permission race

Use [the SDK permission wire catalog](sdk-permission-wire.md) as the sole exact schema definition. This section owns only the race and scheduling semantics.

When ordinary tool permission evaluation returns ask:

1. Publish `requires_action` and start the host `can_use_tool` request.
2. Start the applicable permission hook concurrently.
3. If the hook returns a decisive allow/deny/update first, abort/cancel the host request and apply the hook result.
4. If the hook returns pass-through, keep waiting for the host.
5. If the host returns first, apply it as the winner. A late hook cannot replace that permission outcome, although already-started compatibility side effects may still finish under the hook contract.
6. On either path, settle the original tool-use ID once.
7. Return session state to `running` only when no other permission prompts remain.

- **WIRE-PERM-001 — First decisive result.** Wall-clock completion, not invocation order, determines the winner except that pass-through is nondecisive.
- **WIRE-PERM-002 — Fail closed.** Hook or host errors become denial/cancellation, never implicit allow.
- **WIRE-PERM-003 — Defensive description.** Build `requires_action` details from a safe tool summary; if summary generation fails, use the tool name.
- **WIRE-PERM-004 — Synthetic network request.** Sandbox network access uses the same correlated permission path and denies on any protocol error.

## Ordering, replay, and duplicate suppression

- All output records and control requests share one FIFO.
- Replay mode may echo user/control acknowledgements so the host can implement command lifecycle.
- Keep a bounded set of 1,000 resolved tool-use IDs. Evict the oldest when full.
- Ignore a late duplicate response whose tool-use ID is in that resolved set; this prevents duplicate assistant/tool result IDs.
- A response with no pending request and no known resolved tool identity is orphaned and may be routed to an explicit orphan handler.
- Complete command lifecycle for every response UUID, including replayed or duplicate responses.

Hard-interrupt order, after any earlier FIFO records, is:

```text
control_cancel_request for each pending host permission
control_response/success for interrupt
permission denial or cancellation settles internally
one terminal tool_result for every accepted tool-use ID
task progress and terminal task events for finite work
result/error_during_execution
prompt_suggestion only if suggestion work survived (normally it was aborted)
remote internal/resume-event flush
session_state_changed/idle when idle emission remains enabled
```

Finite background work may hold the ordinary result after the acknowledgement. Long-lived teammates are excluded from that result holdback. The remote internal-event flush is not a visible-client-event flush, and FIFO position is not a durable delivery acknowledgement.

## Failure behavior

- Malformed framing is fatal; schema-invalid operation payload returns a correlated error when request identity is recoverable.
- A missing request ID cannot be correlated and is fatal to the control record.
- Input EOF rejects pending controls and prevents new outbound permission requests.
- A final valid unterminated response is processed before this rejection; malformed tail JSON is fatal with status 1.
- Hook callback failure returns a safe empty callback result only where the hook contract permits; safety decisions remain fail-closed.
- Elicitation failure resolves as cancel.
- Long-running operations that require the reader to accept callbacks, OAuth completion or interrupt must detach from the serial control dispatcher after sending an acknowledgement/state token.

## Acceptance scenarios

1. Split three NDJSON objects across arbitrary chunks and omit the final newline; verify all three parse once.
2. Include U+2028/U+2029 in assistant text; verify one physical line record and exact decoded text.
3. Queue assistant output, then a permission request; verify the request never overtakes the assistant event.
4. Abort a pending control; verify one cancel envelope, immediate local rejection and ignored known-late response.
5. Close input with two pending permissions; verify both reject and session does not remain `requires_action`.
6. Race hook allow against host deny in both completion orders; verify first decisive result wins exactly once.
7. Send the same late tool response twice; verify one tool result and no duplicate API identifier.
8. Request dynamic MCP replacement; verify the response is sent before connection begins and reports added/removed/errors.
9. Emit task progress, notification, result, suggestion and idle; verify exact order.
10. Request settings; verify source order and distinguish effective merge from runtime-applied model/effort.
11. Interrupt an active turn with no pending permission; verify immediate payload-less success acknowledgement, a later ordinary `error_during_execution` result with no cancelled subtype, and an input stream that remains usable.
12. Interrupt while two host permissions are pending; verify both cancel records precede the interrupt acknowledgement in FIFO order, every tool-use ID is paired, finite task events precede the result, and idle follows the internal-event flush.
13. End input with a valid unterminated `control_response` for one of two pending requests; verify that request resolves normally before EOF and only the other receives stream-closed rejection.
14. End input with a malformed unterminated record; verify diagnostic plus status 1 rather than partial control resolution.
15. Observe `result` then `idle` on a CCR v2 worker and close immediately; verify the implementation does not claim either visible event was durably acknowledged merely because remote internal events were flushed.
16. Project one result with unavailable pricing and another with an authoritative zero price; verify total and per-model costs are respectively JSON `null` and numeric `0`.
17. Configure credentials equal to the canonical separator between two safe fields in a structured result and to the safe marshaled-record suffix plus its newline. Add duplicate members whose earlier escaped value decodes to a credential and later value is safe. Verify the complete-frame validator inspects every occurrence and rejects each case before stdout receives a byte, later writes return the same safe terminal failure, and a credential-free encoder preserves normal FIFO NDJSON output.
18. Reenter encoder configuration from the validator and writer, panic or return an error with panicking `Error`, `Is`, and `Unwrap` methods, and mutate validator-owned bytes. Verify no deadlock or raw error call, byte-exact output on success, one fixed latched failure on error, and no post-failure callback.
19. Panic from an initial control emitter before and after synchronous resolution, from cancellation emission, and from the ordered post-cancellation callback. Verify unresolved IDs roll back, an already selected response wins, every detached waiter reaches `ErrAborted`, the broker remains reusable, and no pending ID is stranded.
20. Repeat member names at the outer envelope, nested request/response, operation payload, and pending-control levels, including an escape-equivalent spelling and raw outbound payloads. Verify every duplicate is rejected with the fixed ambiguity diagnostic while the documented canonical/alias pair remains valid.
21. Initialize a standalone application-home `auth.json` session; verify both
    SDK initialization forms emit `apiKeySource=user` and never expose the
    credential path, field name, or value.

## Non-normative provenance

Evidence was specified from the public SDK core/control schemas, structured stream parser/writer, headless control dispatcher, permission bridge and output projection. A schema/handler drift was observed for some newer internal controls; the versioned contract above must be treated as the standalone authority.
