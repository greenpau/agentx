# SDK permission wire catalog

This catalog is the canonical byte-level and record-level contract for the
`can_use_tool` round trip used by a structured SDK host. It distinguishes the
published schema from the reference stdio reader's compatibility behavior.
Use the published schema when constructing records; preserve the reader
compatibility quirks only when wire compatibility with existing hosts requires
them. The permission decision itself remains owned by the permission runtime.

## Contents

1. [Envelope and correlation](#envelope-and-correlation)
2. [Permission request](#permission-request)
3. [Permission update union](#permission-update-union)
4. [Permission response](#permission-response)
5. [Runtime validation boundary](#runtime-validation-boundary)
6. [Decision application](#decision-application)
7. [Cancellation, replay, and orphan responses](#cancellation-replay-and-orphan-responses)
8. [Synthetic network permission](#synthetic-network-permission)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Opaque boundaries](#opaque-boundaries)

## Envelope and correlation

All records are UTF-8 JSON objects framed as one NDJSON record. Field names are
case-sensitive except for the one compatibility alias described below.

```text
request = {
  "type": "control_request",
  "request_id": string,
  "request": permission-request
}

success = {
  "type": "control_response",
  "response": {
    "subtype": "success",
    "request_id": string,
    "response"?: object
  }
}

error = {
  "type": "control_response",
  "response": {
    "subtype": "error",
    "request_id": string,
    "error": string,
    "pending_permission_requests"?: request[]
  }
}

cancel = {
  "type": "control_cancel_request",
  "request_id": string
}
```

- **WIRE-PERM-010 — Exact correlation key.** `request_id` is a non-null
  string and is the sole normal-path lookup key. The request's
  `tool_use_id` identifies the semantic tool call but does not select the
  pending waiter. The response payload's optional `toolUseID` is not compared
  with the request's `tool_use_id` on the normal path.
- **WIRE-PERM-011 — Closed published envelope.** The published response union
  has exactly the `success` and `error` discriminators above. Success may omit
  its operation payload at the generic envelope layer; a particular waiter may
  still require it. Error requires `error`; its recovery field is spelled
  `pending_permission_requests` and contains complete nested
  `control_request` envelopes. None of these optional fields accept JSON null.
- **WIRE-PERM-012 — Request-ID compatibility alias.** On inbound records only,
  `requestId` is renamed to `request_id` at the outer object and inside
  `response`. If both spellings exist, `request_id` wins and the alias is left
  irrelevant. No camel-case aliases exist for any other field.
- **WIRE-PERM-013 — Object normalization.** Published object validators remove
  unrecognized object members while retaining arbitrary JSON-compatible values
  inside fields declared as open records. An omitted optional member differs
  from an explicit null; null is rejected unless a field below explicitly says
  it is allowed.

## Permission request

The inner request is:

```text
{
  "subtype": "can_use_tool",
  "tool_name": string,
  "input": object<string, any-json-value>,
  "tool_use_id": string,
  "permission_suggestions"?: permission-update[],
  "blocked_path"?: string,
  "decision_reason"?: string,
  "title"?: string,
  "display_name"?: string,
  "agent_id"?: string,
  "description"?: string
}
```

- **WIRE-PERM-020 — Required request members.** `subtype`, `tool_name`,
  `input`, and `tool_use_id` are required and non-null. `input` must be an
  object; values inside it may contain null, arrays, objects, strings, numbers,
  and booleans.
- **WIRE-PERM-021 — Optional request members.** Every other member shown above
  is optional but non-null when present. `permission_suggestions` is validated
  member-by-member against the closed update union. `blocked_path` is the
  protected path that caused the ask. `description` is a host-facing prompt;
  `title` and `display_name` are optional presentation labels.
- **WIRE-PERM-022 — Decision-reason projection.** Classifier reasons are sent
  only in builds that expose a classifier, using the classifier's reason text.
  Hook, asynchronous-agent, sandbox-override, working-directory, safety-check,
  and generic reasons send their reason text. Rule, mode, subcommand-result,
  and permission-prompt-tool reasons are omitted. This field is explanatory;
  it does not grant authority.
- **WIRE-PERM-023 — Safe action projection.** Before sending, publish a
  `requires_action` description using the tool's activity description, then
  tool-use summary, then user-facing name. If any formatter throws, use the
  canonical tool name. The state record and wire request share `request_id`,
  `tool_name`, `tool_use_id`, and original `input` even though a CCR worker
  status later projects only a subset.

## Permission update union

All permission updates use discriminator `type` and destination exactly as
spelled. Unknown discriminator, destination, behavior, mode, or wrong member
type makes that update invalid.

| `type` | Required members |
| --- | --- |
| `addRules` | `rules`, `behavior`, `destination` |
| `replaceRules` | `rules`, `behavior`, `destination` |
| `removeRules` | `rules`, `behavior`, `destination` |
| `setMode` | `mode`, `destination` |
| `addDirectories` | `directories`, `destination` |
| `removeDirectories` | `directories`, `destination` |

Each rule is:

```text
{ "toolName": string, "ruleContent"?: string }
```

The closed scalar sets are:

```text
behavior    = "allow" | "deny" | "ask"
destination = "userSettings" | "projectSettings" | "localSettings" |
              "session" | "cliArg"
mode         = "default" | "acceptEdits" | "bypassPermissions" |
               "plan" | "dontAsk"
```

`rules` and `directories` are arrays; a directory is a string. Empty arrays
are valid. No update member accepts null.

- **WIRE-PERM-030 — Update effects.** Valid updates are applied in array order.
  The in-memory context accepts every destination. Only `userSettings`,
  `projectSettings`, and `localSettings` are persisted; `session` and `cliArg`
  remain nonpersistent destinations. The selected input follows the specified
  one-shot approval behavior in
  [`PERM-042`](../../implementation-permissions-sandbox/references/permission-decision.md)
  and
  [`TP-035`](../../implementation-tool-protocol/references/tool-protocol-contract.md):
  it proceeds to the tool without another schema, semantic, permission, rule,
  safety, classifier, sandbox, or approval pass.
- **WIRE-PERM-031 — External mode boundary.** `setMode` accepts the five public
  modes above. Build-internal modes are not wire values and an SDK host cannot
  select them through a permission update.

## Permission response

The published permission-result union is:

```text
allow = {
  "behavior": "allow",
  "updatedInput"?: object<string, any-json-value>,
  "updatedPermissions"?: permission-update[],
  "toolUseID"?: string,
  "decisionClassification"?:
    "user_temporary" | "user_permanent" | "user_reject"
}

deny = {
  "behavior": "deny",
  "message": string,
  "interrupt"?: boolean,
  "toolUseID"?: string,
  "decisionClassification"?:
    "user_temporary" | "user_permanent" | "user_reject"
}
```

The permission waiter deliberately has a narrower compatibility parser:

- allow requires `updatedInput` to be present and to be an object;
- deny requires `message`;
- malformed `updatedPermissions` discards the whole update array and logs a
  warning, but does not reject an otherwise valid allow;
- malformed `decisionClassification` becomes absent rather than rejecting the
  decision;
- unknown `behavior`, missing required members, null optional members, and
  wrong types reject the waiter payload.

- **WIRE-PERM-040 — Published/runtime asymmetry.** Public builders may model
  allow-side `updatedInput` as optional, but the reference stdio permission
  waiter requires it. A compatible host therefore always sends
  `updatedInput`; send `{}` when the original input should be retained.
- **WIRE-PERM-041 — Empty-input sentinel.** On allow, an empty
  `updatedInput` object means “use the original request input.” A nonempty
  object replaces the input in full; it is not merged with the original. The
  SDK/bridge wire exposes no `userModified` member and its adapter performs no
  descriptor equivalence comparison, so downstream tool context defaults
  `userModified` to false even when the selected object's bytes differ.
- **WIRE-PERM-042 — Denial semantics.** A deny is a successful control
  interaction carried inside the outer success envelope. `message` is
  required. If optional `interrupt` is true, abort the owning turn after
  parsing the decision. A user denial is not an outer protocol error.
- **WIRE-PERM-043 — Classification semantics.** `decisionClassification` is
  telemetry attribution, not authorization. When absent, allow is inferred as
  temporary and deny as reject by downstream accounting. Invalid values are
  ignored by the compatibility parser and can never turn denial into allow.
- **WIRE-PERM-044 — Malformed update tolerance.** Invalid
  `updatedPermissions` is treated as if the member were absent, while the
  allow/deny discriminator and required decision fields remain fail-closed.
  This tolerance is deliberately narrower than accepting an arbitrary update.

## Runtime validation boundary

The published aggregate stdin schema is a union of user, control request,
control response, keepalive, and environment-update records. The reference
stdio reader does **not** invoke that aggregate validator. It parses JSON,
normalizes the request-ID alias, performs minimal top-level routing, and then
uses the schema stored with the selected pending waiter.

- **WIRE-PERM-050 — Actual response dispatch.** A response whose inner
  `subtype` is exactly `error` rejects the selected waiter with its `error`
  value. Every other subtype enters the success branch in the compatibility
  reader. The waiter-specific payload validator must then accept the inner
  `response` before any permission effect occurs. For `can_use_tool`, the
  permission parser above therefore rejects an unknown permission `behavior`
  even though an unknown outer response subtype is not independently rejected.
- **WIRE-PERM-051 — Generic versus operation schema.** The generic success
  schema permits an absent open-record payload; `can_use_tool` does not. The
  pending map stores the exact operation response schema out of band. Resolve
  only after that schema succeeds. A request with no response schema resolves
  to an empty object and ignores any supplied payload.
- **WIRE-PERM-052 — Malformed outer record.** JSON parse failure is fatal to
  structured input. A malformed recognized response may throw while reading
  `response.request_id` and takes the same fatal parse-error path. A syntactically
  valid unknown top-level `type` is warned and ignored. These are observable
  compatibility behaviors, not permission grants.
- **WIRE-PERM-053 — Hardened implementation choice.** A standalone runtime may
  validate the published closed envelope before dispatch, which removes the
  unknown-non-error-subtype compatibility quirk. If it does, version that
  hardening at the SDK boundary and still preserve the fail-closed payload and
  correlation outcomes. Never broaden the closed public union to hide the
  reference reader's weaker check.

## Decision application

1. Evaluate ordinary local rules. Send no host request for a decisive local
   allow or deny.
2. For ask, allocate a unique `request_id`, expose `requires_action`, start the
   host request, and concurrently run permission hooks.
3. The first decisive host or hook outcome wins; hook pass-through is not
   decisive. If a hook wins, cancel the host request.
4. Parse the host result using the permission waiter schema.
5. For allow, apply and persist valid updates, resolve `{}` to original input,
   and pass the selected object directly to this one execution. Do not rerun
   schema, semantic, permission, rule, safety, classifier, sandbox, or prompt
   stages. An invalid object may therefore fail only when the tool executes.
6. For deny, optionally interrupt and return the required message.
7. Any stream, parse, or protocol exception becomes a deny whose message starts
   `Tool permission request failed:`; it never becomes allow.
8. Return session state to running only when no other permission waiter remains.

- **WIRE-PERM-060 — Exactly one permission outcome.** Request correlation,
  hook race, host cancellation, and late-response suppression jointly produce
  at most one applied decision for the original tool-use ID. Cancellation or
  parser failure is a deny/cancel outcome and does not strand the tool call.

## Cancellation, replay, and orphan responses

- **WIRE-PERM-070 — Local abort order.** Local abort enqueues one
  `control_cancel_request`, records the request's `tool_use_id` in the bounded
  resolved set, and rejects the waiter immediately. The normal cleanup removes
  the pending entry. An eventual response cannot revive the waiter.
- **WIRE-PERM-071 — Resolved deduplication.** Retain at most 1,000 resolved
  request-side tool-use IDs in insertion order. When capacity is exceeded,
  evict the oldest. An orphan success whose payload `toolUseID` appears in that
  set is ignored.
- **WIRE-PERM-072 — Orphan compatibility path.** A success response with no
  exact pending `request_id` may be offered to an orphan handler. Only a string
  payload `toolUseID` can select an unresolved transcript tool call. This path
  does not validate the remote logical session, epoch, connection generation,
  or equality with an original request. A second process-local handled-ID set
  suppresses repeated orphan execution. Treat this as an explicit compatibility
  risk, not a general correlation rule.
- **WIRE-PERM-073 — Replay.** All control responses close command lifecycle
  when they carry a server-injected string UUID, including duplicates and
  orphans. A normally matched response is yielded back to the semantic input
  stream only when replay mode is enabled.

## Synthetic network permission

- **WIRE-PERM-080 — Network prompt encoding.** A sandbox network ask reuses
  `can_use_tool` with `tool_name="SandboxNetworkAccess"`, a newly generated
  `tool_use_id`, `input={"host": host}`, and
  `description="Allow network connection to <host>?"`. The optional port is
  not included in the reference wire input. Only an allow returns true; every
  exception, closed stream, malformed payload, or deny returns false.

## Acceptance scenarios

### `WIRE-PERM-A01` — Exact allow

Send a correlated success containing `behavior=allow`, a nonempty
`updatedInput`, two valid updates, and `user_permanent`. Verify the input is
replaced rather than merged, updates apply in order and persist only to
persistent destinations, no authorization or validation stage reruns, and the
tool receives `userModified=false` from this adapter even when bytes changed.

### `WIRE-PERM-A02` — Mobile empty-input sentinel

Send `behavior=allow` with `updatedInput={}`. Verify the original input is used.
Omit `updatedInput` entirely and verify the reference waiter rejects and the
tool receives a denial, despite the public result type marking the member
optional. In the empty-object case verify `userModified=false`.

### `WIRE-PERM-A03` — Tolerant auxiliary fields

Send a valid allow with one malformed permission update and an unknown
decision classification. Verify both optional members become absent, the allow
still succeeds, and no malformed update is applied or persisted.

### `WIRE-PERM-A04` — Closed security discriminator

Send a correlated response with an unknown permission `behavior`. Verify the
waiter schema rejects it and no tool runs. Then send an unknown outer response
subtype with a valid allow payload: verify the compatibility reader treats it
as the success branch, while a hardened closed-envelope implementation rejects
it only under an explicitly versioned policy.

### `WIRE-PERM-A05` — Correlation and orphan risk

Send a response with the correct `request_id` but a different payload
`toolUseID`; verify the normal waiter is selected by request ID. After restart,
send an orphan success whose `toolUseID` matches an unresolved transcript use;
verify the documented compatibility handler can admit it without an epoch or
generation fence, and the handled-ID set suppresses a duplicate.

### `WIRE-PERM-A06` — Abort and late response

Abort a pending host prompt. Verify cancel precedes later interrupt
acknowledgement, the request-side tool-use ID enters the bounded resolved set,
the waiter rejects, and a late host response cannot apply a second decision.

### `WIRE-PERM-A07` — Synthetic network fail closed

Request host and port access. Verify the wire carries only the host under the
synthetic tool name, allow returns true, and deny, malformed response, or EOF
returns false.

## Opaque boundaries

The transport does not define how an SDK host renders its dialog or chooses an
allow/deny response. Those are host behaviors. Likewise, a permission update's
path and shell semantics belong to the permission skill; this catalog defines
only its serialized shape, ordering, validation, and handoff.
