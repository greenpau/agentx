# Input normalization, attachments, pasted content, and queues

## Contents

1. [Input envelope](#input-envelope)
2. [Base normalization](#base-normalization)
3. [Untrusted Unicode normalization](#untrusted-unicode-normalization)
4. [References and attachments](#references-and-attachments)
5. [Images](#images)
6. [Large pasted text](#large-pasted-text)
7. [Queue priorities](#queue-priorities)
8. [Wake and notification behavior](#wake-and-notification-behavior)
9. [Queue acceptance cases](#queue-acceptance-cases)

## Input envelope

Represent submitted input as a stable envelope rather than a bare string:

| ID | Field | Meaning |
| --- | --- | --- |
| IQ-001 | `value` | String or ordered content-block array. |
| IQ-002 | `mode` | `prompt`, `bash`, `orphaned-permission`, or `task-notification`. |
| IQ-003 | `uuid` | Stable event identity. |
| IQ-004 | `priority` | Optional `now|next|later`. |
| IQ-005 | policy | Permission mode and workload/origin metadata. |
| IQ-006 | paste state | Number-keyed pasted text/image records plus pre-expansion value. |
| IQ-007 | routing | `skipSlashCommands`, bridge origin, meta flag, and optional agent identity. |

Non-prompt modes require string input. Preserve the complete envelope while it is queued in the current process. The live queue is not a durable store and is not implemented on process restart; only inputs already committed to transcript/history or another owning durable subsystem can be resumed.

## Base normalization

For a content-block array, resize/downsample image blocks first. Treat the last block as the main prompt only when it is text; preserve earlier blocks in order. If the last block is non-text, keep all blocks as attachments/context and use no invented prompt string.

For ordinary prompt mode:

1. retain raw/pre-expansion text for history and special-keyword checks;
2. apply slash routing unless skipped;
3. expand paste placeholders and references at the appropriate command/prompt phase;
4. create the user message with UUID, permission mode, origin, and display metadata;
5. add deliberate attachment messages;
6. run submit hooks and return a query/no-query decision.

Shell mode routes to local shell-input handling, not slash parsing. Orphaned permission and task notifications preserve their distinct origin and presentation semantics.

An optional ultraplan feature may rewrite a matching *non-slash interactive prompt* to `/ultraplan`. Detect against pre-paste-expansion text. Headless or unavailable profiles must leave it ordinary text rather than routing to a command absent from their registry.

`IQ-008` — Treat a terminal, text-mode, or standalone-protocol input reader as
a host callback boundary before normalization. Preserve only exact EOF.
Replace every other callback error, invalid byte count, or panic with a fixed
local failure without formatting, classifying, or retaining its error graph.
Start `Close` asynchronously and bound any pump join so a broken callback
cannot delay cancellation or timeout. Bytes accompanied by a non-EOF failure
are not admitted as prompt or protocol input.

## Untrusted Unicode normalization

`SAN-001` — Always sanitize text that crosses the declared untrusted external-input boundaries, including MCP payload values, deep-link content, and session-tag input. This is a security normalization before display/model use, not a permission grant and not a general rewrite of already authoritative transcript bytes.

`SAN-002` — For one string, repeat this complete pass until a pass produces no change: normalize to Unicode NFKC; remove every code point in general categories Format (`Cf`), Private Use (`Co`), and Unassigned (`Cn`); then remove explicit ranges U+200B–U+200F, U+202A–U+202E, U+2066–U+2069, U+FEFF, and U+E000–U+F8FF. The explicit ranges remain required even on platforms whose Unicode-category matcher already covers them.

`SAN-003` — Bound the loop to ten executed passes. Reaching pass ten is a terminal sanitization error even if that pass might have become stable; include only a bounded first-100-character diagnostic and never continue with partially sanitized content. Ordinary unchanged input executes one pass and returns.

`SAN-004` — Recursive sanitization maps strings through `SAN-002`, arrays element-by-element in order, and enumerable record entries into a fresh plain record while sanitizing both key and value. Numbers, booleans, null, and an absent-value sentinel pass through unchanged. Do not preserve a custom object prototype as trusted behavior.

`SAN-005` — Key normalization can make two distinct input keys equal. Compatibility behavior processes source enumeration order and lets the later sanitized key overwrite the earlier value. An implementation may reject collisions as a documented security hardening, but must not silently merge values or depend on source-language map quirks.

`SAN-A01` — Supply compatibility characters plus tag/format, bidirectional isolate, byte-order mark, private-use, and unassigned code points nested in array values and record keys. Verify fixed-point normalization, order preservation, key sanitization, later-key collision behavior, and unchanged numeric/Boolean/null values.

`SAN-A02` — Supply an adversarial string that continues changing through the tenth pass. Verify a bounded terminal error, no model/display use of the partial value, and no raw full input in diagnostics.

## References and attachments

Recognize and resolve only supported reference forms, including:

- `@` file and directory references relative to session context;
- active IDE selection/open-file context;
- agent/teammate mentions;
- MCP resource references;
- pasted-image placeholders and direct image blocks;
- large pasted-text placeholders.

Resolution is contextual and permission-aware. Preserve literal text for ambiguous/unresolved references and attach an attributable warning rather than silently dropping it. Directory references produce a bounded inventory/context representation, not an unbounded recursive copy. External resources remain untrusted.

For ordinary prompts, extract references from raw prompt content. For a prompt command, extract from *expanded skill content* so references authored by the skill resolve against the skill/session root and raw slash syntax does not become an accidental attachment.

## Images

Filter empty image paste records. Persist accepted pasted images to a session/cache location, resize or downsample in parallel within bounded concurrency, then append supported image blocks after text. Preserve image ID and media type through history and same-process queueing, and through resume only after the owning history/transcript record is durable. Add hidden model-visible image metadata only deliberately. Failure of one image is attributed and does not reorder successful images.

## Large pasted text

Interactive paste handling uses two thresholds with different purposes:

- A paste above 800 characters enters the large-paste capture path.
- Display input above 10,000 characters is collapsed to the first 500 and last 500 characters with placeholder `[...Truncated text #N +<lines> lines...]`; the middle remains authoritative `PastedContent`.

Store text content-addressably using SHA-256, first 16 hexadecimal characters, under `paste-cache/<hash>.txt`, with owner-only mode `0600`. Cache/read failures degrade to in-memory content or an explicit missing-paste error; they never submit the placeholder as if it were the full text. History stores enough ID/hash metadata to reload content and periodically cleans aged cache entries. Recalling/editing history may recollapse the same content without changing its identity.

## Queue priorities

Priority ordering is `now` before `next` before `later`.

| Priority | Observable stop point |
| --- | --- |
| `now` | Abort/interrupt the in-flight model/tool path when allowed and process immediately. |
| `next` | Let the current tool reach a terminal result, then attach the input between that result and the next API request in the same turn. Wakes `Sleep`. |
| `later` | Wait for the current turn terminal boundary, then start a new query. Wakes `Sleep`; after a sleep continuation the drain threshold may admit it in that turn. |

The ordinary enqueue helper defaults to `next`; task/notification enqueue helpers may default to `later`. Dequeue stably within equal priority. Filter by agent identity so a teammate never consumes another agent's queue. Convert eligible mid-turn prompt/task-notification entries to attachments/context, and remove only entries actually consumed.

Queue admission, queue-operation diagnostics, and model/transcript consumption are separate events. The queue is process memory: a crash after enqueue and before consumption loses the item, and diagnostic queue-operation transcript records are filtered during resume rather than replayed. Same-process removal prevents a second consumption from that live queue instance; there is no cross-restart exactly-once guarantee. A durable outbox with acknowledgement is an intentional stronger design, not reference-compatible behavior.

An `immediate` command bypasses the normal queue only for a recognized local UI command and still participates in lifecycle completion. Expand any paste placeholder before invoking it. Immediate does not grant permission to interrupt arbitrary model/tool work.

## Wake and notification behavior

Queued `next` or `later` input cancels an interruptible `Sleep`. Local shell completion commonly queues at `next`; other background task/workflow/framework notifications may queue at `later`. Scheduled jobs queue at `later`. Channel/bridge messages generally use `next`, `skipSlashCommands=true`, and retain origin so external text cannot become local control accidentally.

## Queue acceptance cases

- **IQ-A01:** Enqueue one item of each priority during a tool call; `now` interrupts as allowed, `next` appears before the next API request, and `later` starts after turn completion.
- **IQ-A02:** Two same-priority messages retain enqueue order and are consumed once from the same live queue generation.
- **IQ-A03:** A teammate queue item with another agent ID remains queued when the current agent drains.
- **IQ-A04:** A 20,000-character paste displays collapsed text, submits complete restored content, and survives history/restart through its cache identity.
- **IQ-A05:** A missing paste-cache file yields an explicit error and never submits the truncation placeholder as authoritative content.
- **IQ-A06:** A prompt skill containing an `@file` resolves after expansion; the raw `/skill` line is not independently scanned into a duplicate attachment.
- **IQ-A07:** A bridge message `/model` cannot open local UI; ordinary `/shrug` remains text.
- **IQ-A08:** A queued message wakes `Sleep` once without generating an idle status-only model turn.
- **IQ-A09:** Crash after a queue enqueue but before conversion to a durable user/attachment message. Resume does not replay the queue-operation diagnostic or invent the input; the item is lost unless its external sender has an independent redelivery protocol.
- **IQ-A10:** Return an uncomparable reader error whose `Error`, `Is`, and
  `Unwrap` methods panic, an invalid count, and a panic from `Read`; separately
  block or panic in `Close`. Verify no host error method executes, no partial
  bytes are admitted, the fixed failure is stable across terminal/text/MCP
  adapters, and cancellation completes within the pump bound.
