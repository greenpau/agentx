# Hook event and execution protocol

This document defines hook sources, all event inputs, matching, execution transports, structured output, authority, asynchronous behavior, and failure handling. `HOOK-*` identifiers are normative and stable.

## Contents

1. [Common event envelope](#common-event-envelope)
2. [Configuration schema](#configuration-schema)
3. [Matcher and conditional rules](#matcher-and-conditional-rules)
4. [Execution timing, progress, and environment](#execution-timing-progress-and-environment)
5. [Command result protocol](#command-result-protocol)
6. [Event authority table](#event-authority-table)
7. [Interactive command prompts](#interactive-command-prompts)
8. [Asynchronous command hooks](#asynchronous-command-hooks)
9. [HTTP hooks](#http-hooks)
10. [Failure and recovery](#failure-and-recovery)
11. [Acceptance scenarios](#acceptance-scenarios)
12. [Non-normative provenance](#non-normative-provenance)

## Common event envelope

`HOOK-001` — Every hook input is one JSON object containing:

```text
session_id: string
transcript_path: string
cwd: string
permission_mode?: string
agent_id?: string
agent_type?: string
hook_event_name: exact event name
<event-specific fields>
```

Paths and identifiers use the active session snapshot. Omit optional fields rather than serializing language-specific undefined.

`HOOK-002` — Supported event names and fields:

| Event | Event-specific input |
| --- | --- |
| `PreToolUse` | `tool_name`, `tool_input`, `tool_use_id` |
| `PostToolUse` | `tool_name`, `tool_input`, `tool_response`, `tool_use_id` |
| `PostToolUseFailure` | `tool_name`, `tool_input`, `tool_use_id`, `error`, optional `is_interrupt` |
| `PermissionRequest` | `tool_name`, `tool_input`, optional `permission_suggestions` |
| `PermissionDenied` | `tool_name`, `tool_input`, `tool_use_id`, `reason` |
| `Notification` | `message`, optional `title`, `notification_type` |
| `UserPromptSubmit` | `prompt` |
| `SessionStart` | `source` in startup/resume/clear/compact, optional `model`, `agent_type` |
| `SessionEnd` | `reason` in clear/resume/logout/prompt_input_exit/other/bypass_permissions_disabled |
| `Setup` | `trigger` in init/maintenance |
| `Stop` | `stop_hook_active`, optional `last_assistant_message` |
| `StopFailure` | error enum, optional `error_details`, optional `last_assistant_message` |
| `SubagentStart` | `agent_id`, `agent_type` |
| `SubagentStop` | `stop_hook_active`, `agent_id`, `agent_transcript_path`, `agent_type`, optional `last_assistant_message` |
| `PreCompact` | `trigger` manual/auto, nullable `custom_instructions` |
| `PostCompact` | `trigger` manual/auto, `compact_summary` |
| `TeammateIdle` | `teammate_name`, `team_name` |
| `TaskCreated`, `TaskCompleted` | `task_id`, `task_subject`, optional `task_description`, `teammate_name`, `team_name` |
| `Elicitation` | `server`, `message`, optional mode/form/url fields, id, requested schema |
| `ElicitationResult` | `server`, optional id/mode, `action` accept/decline/cancel, optional content |
| `ConfigChange` | `source` user/project/local/policy/skills, optional `file_path` |
| `InstructionsLoaded` | `file_path`, `memory_type` User/Project/Local/Managed, load reason, optional globs/trigger/parent paths |
| `WorktreeCreate` | `name` |
| `WorktreeRemove` | `worktree_path` |
| `CwdChanged` | `old_cwd`, `new_cwd` |
| `FileChanged` | `file_path`, `event` change/add/unlink |

`HOOK-003` — Instruction load reasons are `session_start`, `nested_traversal`, `path_glob_match`, `include`, and `compact`. Event schemas are versioned wire contracts; unknown required enum values fail validation rather than mapping to an existing value.

## Configuration schema

`HOOK-010` — Hook configuration maps event name to an ordered list of groups `{matcher?, hooks[]}`. Each persisted hook is one of:

```text
command { type:"command", command, if?, shell?:"bash"|"powershell",
          timeout?, statusMessage?, once?, async?, asyncRewake? }
prompt  { type:"prompt", prompt, if?, timeout?, model?, statusMessage?, once? }
agent   { type:"agent", prompt, if?, timeout?, model?, statusMessage?, once? }
http    { type:"http", url, if?, timeout?, headers?, allowedEnvVars?,
          sensitive_path_segments?:nonnegative-int[], statusMessage?, once? }
```

Internal/session registrations may also be callbacks or functions; they are not serialized into settings.

`HOOK-011` — Timeout in persisted configuration is seconds and must be positive/bounded. `async` and `asyncRewake` are command-hook capabilities and must not be accepted on transports that cannot honor them.

`HOOK-012` — Source snapshot order begins with frozen settings hooks, followed by registered SDK/plugin hooks and session/skill/agent/internal callbacks. Managed-only hook policy drops plugins and session sources. Plugin-only policy admits only approved plugin/managed/built-in provenance for the locked hooks family.

`HOOK-013` — A global managed hook disable stops all hooks. A user `disableAll` stops nonmanaged hooks while managed hooks remain. Interactive hooks require workspace trust; headless invocation supplies its explicit trust boundary. Simple/restricted mode may disable hooks entirely.

## Matcher and conditional rules

`HOOK-020` — Empty matcher or `*` matches all. A matcher containing only alphanumeric/underscore names separated by `|` performs exact alternatives after legacy tool-name normalization. Other strings are regular expressions. Invalid regex never matches and emits a diagnostic.

`HOOK-021` — Event match query is:

| Event family | Query |
| --- | --- |
| tool and permission events | canonical tool name |
| session start/end | source or reason |
| setup | trigger |
| compact | trigger |
| notification | notification type |
| stop failure | error code |
| subagent | agent type |
| elicitation | MCP server |
| config change | settings source |
| instructions loaded | load reason |
| file changed | file basename |

Events without a query execute all groups whose matcher is empty/all; a nonapplicable matcher cannot invent a query.

`HOOK-022` — Optional `if` is permission-rule syntax evaluated only for `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, and `PermissionRequest`, using validated tool input and the tool-specific permission matcher. On other events, a hook with `if` is skipped. Treat an injected condition matcher as callback-owned code: contain its panic with a fixed value-opaque dispatch error, and finish the complete matching pass before claiming any `once` execution so matcher failure cannot strand an unexecuted claim.

`HOOK-023` — Deduplicate settings hooks by type, effective payload, shell (default bash included), and condition; the last duplicate wins. Plugin and skill hooks include canonical root in the key so identical templates from different roots both run. Internal callback/function identities are unique.

`HOOK-024` — Group the executable snapshot by transport—commands, prompts, agents, HTTP, callbacks, functions—without losing source order metadata. All matched hooks run concurrently; ordering metadata is for deterministic display and tie-break rules, not serial execution.

`HOOK-025` — HTTP hooks are unsupported at `SessionStart` and `Setup` because startup/headless control may deadlock. Omit with a diagnostic.

## Execution timing, progress, and environment

`HOOK-030` — Default global/per-hook bound is 10 minutes unless a narrower transport/event default applies:

| Kind/event | Default |
| --- | --- |
| prompt hook | 30 seconds |
| agent hook | 60 seconds |
| HTTP hook | 10 minutes |
| SessionEnd caller budget | 1,500 ms, bounded environment override |
| status/file suggestion helper | 5 seconds |
| detached async registry | 15 seconds unless explicit bounded timeout |

`HOOK-031` — Run matched hooks concurrently and collect results in completion order. Each has an independent abort/timeout. Parent cancellation terminates synchronous children; `asyncRewake` survival follows `HOOK-061`. Every worker settles exactly one result even when hook execution, resolver, sanitization, or result normalization panics; recover at the exact callback boundary with a fixed diagnostic, never format the recovered value, and release a failed `once` claim for retry.

`HOOK-032` — Emit progress before each hook. Buffer at most 100 pending execution events. When streaming subprocess output, update at most once per second and only when visible output changed. Always emit hook execution events for SessionStart/Setup; other events may require telemetry/progress opt-in.

`HOOK-033` — Command stdin begins with exactly one JSON line containing the event envelope. Bash is default. On Windows, bash means the supported Git Bash and native paths are converted to POSIX form. PowerShell is explicit and runs noninteractive/no-profile; it does not use bash prefixes or the environment-file protocol.

`HOOK-034` — Environment contains the sanitized process environment plus stable project root. Plugin hooks receive plugin root and data directory; skill hooks receive skill root through the plugin-root compatibility variable. Plugin user options become `AGENTX_PLUGIN_OPTION_<UPPER_IDENTIFIER>` after validation; sensitive values follow the secure injection policy.

`HOOK-035` — Bash hooks for SessionStart, Setup, CwdChanged, and FileChanged receive `AGENTX_ENV_FILE` for deliberate environment updates. Missing active cwd falls back to original project cwd. A missing plugin root is a nonblocking precheck error, not a shell exit-2 block.

`HOOK-036` — Substitute plugin root/data and validated user configuration before spawning. On Windows, `.sh` selects Bash. Never interpolate untrusted values without shell-safe structured substitution.

## Command result protocol

`HOOK-040` — Exit status semantics:

- `0`: success;
- `2`: blocking/special feedback subject to event authority table;
- any other status: nonblocking hook error unless event explicitly says otherwise.

`HOOK-041` — If stdout begins with `{`, parse and validate structured JSON. Malformed structured output becomes a nonblocking hook error; do not reinterpret it as trusted plain text. Non-JSON stdout follows event text semantics.

`HOOK-042` — Common structured result:

```text
{
  continue?: boolean,
  suppressOutput?: boolean,
  stopReason?: string,
  decision?: "approve" | "block",
  reason?: string,
  systemMessage?: string,
  hookSpecificOutput?: object
}
```

`continue:false` stops the owning continuation with optional reason. Returned `hook_event_name`, when present in specific output, must exactly equal the invoked event.

`HOOK-043` — Event-specific structured output supports:

| Event | Fields |
| --- | --- |
| PreToolUse | permission decision allow/deny/ask, reason, updated input, context |
| UserPromptSubmit | context |
| SessionStart | context, initial user message, watch paths |
| Setup/SubagentStart | context |
| PostToolUse | context, updated MCP tool output |
| PostToolUseFailure | context |
| PermissionDenied | retry |
| PermissionRequest | allow(updated input/permissions) or deny(message/interrupt) |
| Elicitation / result | action and content |
| CwdChanged/FileChanged | watch paths |
| WorktreeCreate | worktree path |

Validate the hook-result envelope, event discriminator, and event-specific declared fields before use. Validate `updatedPermissions` entries through the permission-update schema. A `PreToolUse` replacement becomes the candidate input entering the subsequent ordinary permission analysis; it is not itself an allow and the already-completed tool-schema/semantic pass is not repeated. A winning `PermissionRequest` replacement follows `PERM-034`/`PERM-042`: validate the hook response envelope and selected value's declared object shape, then execute the selected object without a second tool-schema, semantic, permission, safety, classifier, sandbox, or prompt pass. Bounded revalidation/reauthorization is an intentional hardening divergence.

`HOOK-044` — Aggregate concurrent permission decisions by `deny > ask > allow`, independent of completion order. Accumulate additional context in completion order. Associate updated inputs only with the contributing allow/ask/passthrough result; a losing allow cannot override a deny.

## Event authority table

`HOOK-050` — Apply text and exit status as follows:

| Event | Exit 0 / output | Exit 2 / block | Other failure |
| --- | --- | --- | --- |
| PreToolUse | output hidden unless structured context | block tool; stderr to model | user-visible only |
| PostToolUse / PostToolUseFailure | output enters transcript/model context | stderr to model; tool already terminal | user-visible only |
| PermissionDenied | structured retry; output transcript | event-specific denial feedback | user-visible only |
| Notification | output hidden | no side-effect rollback | user-visible only |
| UserPromptSubmit | stdout to model with prompt | block and erase original model submission; stderr to user | user-visible only |
| SessionStart | stdout to model | block ignored | nonzero user-visible |
| Setup | stdout to model | block ignored | nonzero user-visible |
| Stop | normal completion | feedback to model and continue turn | user-visible/error |
| StopFailure | fire-and-forget | ignored | ignored/logged |
| SubagentStart | stdout to subagent | block ignored | diagnostic |
| SubagentStop | normal completion | feedback and continue subagent | diagnostic |
| PreCompact | stdout appended to custom instructions | block compaction | continue with diagnostic |
| PostCompact | stdout user-visible | stderr user-visible | stderr user-visible |
| SessionEnd | bounded best-effort | no continuation to block | user-visible/logged |
| PermissionRequest | structured decision | structured denial | unresolved ask remains |
| TeammateIdle | normal idle | prevent idle plus feedback | diagnostic |
| TaskCreated/Completed | normal transition | prevent transition plus feedback | diagnostic |
| Elicitation | structured response | deny elicitation | diagnostic/default deny |
| ElicitationResult | structured override | convert to decline | diagnostic |
| ConfigChange | apply candidate | block reload | diagnostic according to result |
| InstructionsLoaded | observability only | never blocks | diagnostic |
| WorktreeCreate | first successful nonempty path wins | failed candidate | if none succeeds, caller error/fallback contract |
| WorktreeRemove | best-effort | logged | logged |
| CwdChanged/FileChanged | env/watch updates | nonzero user-visible; no rollback unless owning caller defines it | user-visible |

`HOOK-051` — “To model” means deliberate transcript/context projection by the owning event, not raw terminal output. “User-visible” remains UI/structured progress and does not silently enter model context.

## Interactive command prompts

`HOOK-060` — A running command hook may emit line-delimited prompt objects:

```text
{ "prompt": <id>, "message": <text>,
  "options": [{ "key": <id>, "label": <text>, "description"?: <text> }] }
```

Serialize prompt handling per hook, display through the active approval/question adapter, then write one line `{ "prompt_response": <id>, "selected": <key> }` to child stdin. Remove protocol lines from final stdout. Prompt cancellation closes child stdin and cancels the prompt.

## Asynchronous command hooks

`HOOK-061` — Configuration `async:true` registers detached completion under the ordinary async registry. `asyncRewake:true` creates a detached command whose terminal failure/exit-2 becomes a task notification capable of waking the model. A new-prompt interruption does not automatically kill `asyncRewake`; hard session cancellation does.

`HOOK-062` — A synchronous command may opt into async mode only with first stdout line `{ "async": true, "asyncTimeout"?: <milliseconds> }`. Validate and bound the timeout, strip the control line, and transfer ownership exactly once.

`HOOK-063` — Detached work has identity, deadline, output storage, cancellation, and completion notification. It cannot become an anonymous child process. `once` hooks are removed only after successful execution through the owning session callback.

## HTTP hooks

`HOOK-070` — HTTP hook performs POST of the event JSON. Only 2xx is success; body must be JSON, with empty body normalized to `{}`. Do not follow redirects.

`HOOK-071` — `allowedHttpHookUrls` absent means open subject to SSRF; empty means deny all; entries may use documented wildcard syntax. Per-hook `allowedEnvVars` intersects, never expands, the managed/global HTTP-hook environment allowlist. Missing variables substitute empty. Expand placeholders in one pass over the original template only; never recursively interpret inserted bytes. Reject unterminated, self-referential, mutually referential, or otherwise residual `${` expansion as an unexecutable descriptor. Bound expanded output to 64 KiB while constructing it. Strip CR, LF, and NUL from expanded header values, then reject every remaining ASCII control byte except horizontal tab and reject DEL. Header names must satisfy the RFC token grammar accepted by `net/http`.

`HOOK-072` — Network route precedence is sandbox proxy, environment proxy, then direct. Direct resolution is DNS-pinned before connect. Block IPv4 ranges `0/8`, `10/8`, `100.64/10`, `169.254/16`, `172.16/12`, `192.168/16`; block IPv6 unspecified, unique-local `fc00/7`, link-local `fe80/10`, and mapped blocked IPv4. Loopback remains supported for local automation. When using an explicit proxy, target resolution occurs at the proxy and direct DNS guard cannot be applied; proxy policy is therefore authoritative.

`HOOK-073` — Redact credentials and sensitive headers from logs. Before constructing the session's provider, transcript, task, tool-result, hook, and presentation sinks, parse `RawQuery` explicitly and derive decoded query values, their exact raw encoded spellings, every expanded header value, its nonempty `textproto.TrimString` wire alias, and supported authorization aliases from the complete frozen reachable HTTP-hook snapshot; promote them into the same bounded session-wide immutable credential union. Reject any query `ParseQuery` error, including raw semicolon separators and malformed percent escapes, before network execution rather than relying on `URL.Query`, which silently drops errors. An all-SP/HTAB header has an empty wire value and contributes no raw whitespace literal. Interpret authentication only for case-insensitive `Authorization` and `Proxy-Authorization`; custom headers whose opaque value begins `Basic` remain opaque. Those authorization headers permit only Basic or Bearer. Bearer must satisfy RFC 6750 `b64token` syntax with padding only at the end. Basic uses strict padded or raw base64 decoding, rejects noncanonical encodings, and contributes the configured token, both canonical padded and raw encodings, the complete decoded `username:password`, and each nonempty component. The host, query parameter names, and path are routing metadata by default. An HTTP descriptor may opt path-carried credentials in with JSON field `sensitive_path_segments` (Go field `SensitivePathSegments`), whose entries are nonnegative zero-based indices into the URL's nonempty escaped path segments. Reload requires an HTTP(S) URL with a nonroot path, rejects out-of-range indices, and sorts and deduplicates the indices. A nonempty selection promotes the complete nonroot raw, escaped, decoded, and canonical path plus the raw, decoded, and `PathEscape` aliases of only the selected segments. If a selected segment decodes to embedded `/`, also promote every nonempty decoded subcomponent and its `PathEscape` alias; unselected route segments are not standalone literals. Segment selection is safety metadata omitted from executable dedup identity, so otherwise-identical definitions retain last-wins behavior. Safe descriptor serialization exposes indices but never the URL or header values. A later dispatch whose rederived response scope is not covered by the frozen union is a composition error; an unfrozen runner must also reject any widening response scope before network execution because a hook-local set cannot authorize bytes for downstream framing. Before command stdin or HTTP request egress, semantically sanitize the complete event envelope, including reserved identity fields, nested values, every duplicate JSON member, JSON raw messages, object keys, and scalar spellings. Construct and validate the exact outbound body; for command stdin this is the complete canonical envelope plus its one terminating newline, not the unframed JSON body. No delimiter may be appended after credential validation. Semantically sanitize both command and HTTP structured output before parsing authority, context, progress, updated input, or transcript projection, and apply the same whole-envelope validation to permission-request and post-result hooks. Response-controlled parser, request-construction, and body-read diagnostics use fixed categories rather than echoing external bytes. Before returning concurrent hook results, union the host set with every matched sibling's response-scoped set, marshal the complete ordered aggregate—including aggregate authority fields, the full result array, and a same-shaped safety surrogate containing every public result error string—and reject the entire aggregate if structural separators or adjacent sibling results reconstruct any member; independent per-field or per-result checks are insufficient. The frozen session union must remain attached through later context tags, fallback reasons, updated-input encoding, tool observers, transcript/provider JSON, and terminal or structured framing. Capture sufficient literal lookahead before applying output caps; every truncated result ends in a set-safe terminal marker or is suppressed. HTTP response structured output has the same schema/authority as another hook of that event.

`HOOK-074` — Bound exact-literal redaction work before executing an external hook. The complete session union after adding every executable, allowed, statically request-serializable member of the frozen HTTP-hook snapshot, each rederived HTTP response scope, and the final cross-sibling union admit at most 256 unique nonempty literals and 64 KiB of aggregate literal bytes. Preflight request construction, expanded RFC-valid header names and values, and standard-library wire serialization without network access. Validate every response-scope shape in a deterministic first pass before accumulating or bounding any literal: selected path indices and escapes; explicit query parsing; sorted header names; single-pass expansion; stripped field values and wire trimming; and every supported authorization scheme, field count, Bearer token, strict Basic base64 token, canonical encoding, and decoded colon. Header-map insertion or iteration order cannot choose between isolation and startup failure. Malformed authorization, invalid names, values, expansions, or queries, mutated path selections, denied targets, and any other descriptor that cannot reach request execution contribute no frozen scope and do not abort startup; dispatch returns a fixed nonblocking error before `client.Do`, while a valid sibling still runs. Only a true aggregate literal-count, literal-byte, expanded-output, or safe-marker workload failure aborts startup. An oversized frozen session union rejects startup before constructing shared sinks; a larger installed host set rejects dispatch; an oversized query, header, or opted-in path response scope rejects that HTTP hook before network execution; an oversized final sibling union discards the aggregate without returning authority or output. Diagnostics expose only the workload or shape class, never any rejected literal.

## Failure and recovery

`HOOK-080` — A hook timeout produces one normalized hook error and kills/aborts its transport. Sibling hooks continue unless the parent event is cancelled. Normalize callback-owned errors by sampling `Error` once behind panic containment and storing only detached safe text before aggregate safety projection; aggregate encoding must never retain or repeatedly format the source error object.

`HOOK-081` — Hook aggregation completes even after a decisive denial so accepted sibling executions receive terminal records; the owning tool waits only within its bounded event deadline.

`HOOK-082` — A process crash or session recovery never replays a side-effecting pre/post hook blindly. The common external profile does not persist enough phase evidence to prove whether such a hook ran before the crash; treat prior execution as unknowable and do not fabricate or repeat it. Internal/explicitly saved hook attachments are diagnostic evidence, not a transaction journal.

`HOOK-083` — Hook output is size-bounded and may be persisted outside the transcript with a concise normalized reference. Never let unbounded stdout exhaust context or memory.

`HOOK-084` — Late `PermissionRequest` hook effects are a source-faithful compatibility boundary. Settling the approval race does not cancel an already-running permission hook. Its late result cannot replace the first claimant, but processing before the losing claim check may still persist hook-supplied permission updates, update ephemeral diagnostics, or honor `interrupt=true`. A safer implementation may suppress every losing-racer effect only as an explicitly documented behavioral divergence.

## Acceptance scenarios

1. **HOOK-A01 — Concurrent decision precedence.** Three concurrent PreToolUse hooks finish allow, deny, then ask. Final decision is deny; all three completion records exist and the tool never starts.
2. **HOOK-A02 — Post-effect blocking limit.** A PostToolUse hook exits 2. Stderr is model-visible feedback, but the tool's completed result and side effects remain authoritative.
3. **HOOK-A03 — Prompt-submit block.** A UserPromptSubmit hook exits 2. The original prompt is not sent to the model; user sees feedback; transcript records the blocked routing event without inventing an assistant response.
4. **HOOK-A04 — DNS rebinding.** An HTTP hook URL resolves first to public then attempts DNS rebinding to `10.0.0.1`. Pinned direct resolution prevents the private connection.
5. **HOOK-A05 — Interactive command protocol.** A hook emits two prompt lines. They are answered serially, protocol lines are stripped, and prompt cancellation terminates stdin without treating a partial line as structured hook output.
6. **HOOK-A06 — Rewake lifecycle.** An `asyncRewake` hook survives a new prompt, later exits 2, records terminal/output evidence according to its task subtype, and makes at most one completion-notification enqueue attempt in the same live task-state generation. The latch and model wake queue are process-local: a crash can lose the wake or later recovery can repeat it. Hard session cancellation kills the hook and records the terminal state where its owning evidence store permits; a durable outbox plus consumption acknowledgement is an explicit safer divergence.
7. **HOOK-A07 — Config-change veto boundary.** ConfigChange hook blocks an edit. Disk stays changed, active settings generation remains old, and the hook cannot rewrite managed policy through returned permissions.
8. **HOOK-A08 — Late permission claimant.** A host approval wins while a PermissionRequest hook is running. The tool uses the host decision exactly once. The late hook cannot replace it, but source-compatible mode applies any pre-claim permission update or interrupt and records that behavior as the `HOOK-084` compatibility boundary.
9. **HOOK-A09 — Credential reflection and semantic aliases.** Command and HTTP hooks receive nested input containing escaped strings plus Boolean, number, null, and duplicate-member spellings, while output reflects an authorization header, a noncanonical raw-encoded query value, a padded opaque header after `net/http` wire trimming, and an escaped permission decision. Repeat with canonical padded and raw Basic headers whose hook reflects either encoding, decoded `username:password`, username, and password separately; reject quoted/invalid Bearer, unsupported schemes, and noncanonical Basic, while a custom `X-*` value `Basic label` remains allowed and opaque. Configure `/api/v1/hooks/path%2Ftoken` with sensitive path index `3`; verify the frozen set covers the full path, `path%2Ftoken`, `path/token`, `path`, and `token` aliases, does not include `api`, `v1`, or `hooks` as standalone literals, and sanitizes a loopback hook that reflects both the decoded segment and its tail into reason and context. Repeat `/api/v1/hook` without an index and verify ordinary route segments remain unpromoted while dispatch succeeds. Reject negative and out-of-range indices, safely serialize only their canonical indices, and preserve last-wins dedup when otherwise-identical definitions differ only in this metadata. Give source credentials the exact canonical sequence spanning `tool_input` and `tool_name` in a permission request, then repeat across fields in one result, across adjacent results, inside a public result error, and across the final safe command-envelope suffix plus its newline. Give the first of two HTTP siblings a response-scoped query secret that appears only when the first result's closing bytes join the second result's opening bytes. Also choose query/header/path secrets that appear only after safe hook fields are inserted into the user-prompt `<hook_context>` wrapper, a deny/allow fallback reason, and updated tool-input framing. Verify the frozen snapshot contributes every alias to the session union before any shared sink is created, that an unfrozen widening scope fails before network, and that a mismatched later scope fails closed. Whole-envelope validation blocks request egress where applicable and otherwise rejects the complete aggregate without returning authority or output. Input is projected or fails before egress; output is projected before parsing and cannot enter hook authority, model context, transcript, diagnostics, or a later result by joining across structural, framing, sibling, downstream-wrapper, error-surrogate, or truncation boundaries.
10. **HOOK-A10 — Redaction workload ceiling and executability parity.** A host set with 257 unique literals rejects dispatch without executing any hook. An HTTP hook whose otherwise-valid expanded response-secret scope exceeds 64 KiB is rejected before the request is sent. Independently valid sibling response scopes whose final union exceeds either aggregate ceiling produce no returned aggregate. Separately configure or mutate malformed Basic/Bearer authorization, an out-of-range selected path segment, an invalid RFC header name, header values containing `0x01` or DEL, a semicolon or malformed-percent query, and self/mutually referential or unterminated header expansion beside a valid sibling; freeze skips each invalid descriptor, its bytes do not enter the session union, dispatch emits a fixed error without network execution, and the valid sibling completes. Retain CR/LF/NUL stripping and horizontal-tab acceptance. A repeated original-template expansion that would exceed 64 KiB aborts freeze as a true workload without network activity. Repeat a map containing both malformed authorization and an oversized valid header under shuffled insertion orders: complete shape validation deterministically classifies it as unexecutable before workload accumulation. No diagnostic contains configured literal material.

## Non-normative provenance

Reference behavior was specified from hook schemas and SDK event types, configuration snapshot/manager, command and HTTP executors, event consumers, hook progress projection, async hook registry, environment construction, and SSRF guard under `utils/hooks/`, hook services, SDK schemas, and event-owning runtimes. Paths and symbols are provenance only.
