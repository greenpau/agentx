# Failure, recovery, and conformance scenarios

## Failure taxonomy

| ID | Failure | Required behavior |
| --- | --- | --- |
| CF-001 | Unknown valid command | Local `Unknown command: <name>`; no model query, unless bridge-origin unknown slash is intentionally plain text. The command-specific wording is an intentional usability divergence from the legacy `Unknown skill` compatibility text. |
| CF-002 | Invalid slash grammar/path | Preserve as ordinary prompt where the grammar contract says so. |
| CF-003 | Disabled/unavailable | Treat as absent from discovery; exact stale invocation gets an attributable unavailable result. |
| CF-004 | Source discovery failure | Omit only that source slice and log diagnostics. |
| CF-005 | Expansion/load failure | Emit bounded local stderr and commit no partial expansion/UI state. |
| CF-006 | UI dismissal | Resolve once, clear UI, complete lifecycle, and continue queue. |
| CF-007 | Missing paste/attachment | Preserve user-visible intent and return an attributable error; never submit a placeholder as content. |
| CF-008 | Hook block/stop | Preserve visible original input and apply the specified API-bound warning or stop decision. |
| CF-009 | Queue cancellation | Retain or terminally account for every queued UUID; consume none twice. |
| CF-010 | Recovery interruption | Reconcile only command effects represented by durable transcript/task evidence before new input. The live input queue is process-local and is not restored; an item dequeued before a crash can be lost when no durable transcript commit exists. |

## Registry scenarios

- **CA-001:** Load all seven discovery slices with three canonical collisions and aliases. Exact invocation selects the first available descriptor; suggestions retain source-distinct entries.
- **CA-002:** Add a dynamic path-scoped skill after file access. It appears before built-ins, does not duplicate an existing canonical name, and disappears after cache invalidation/state change when no longer eligible.
- **CA-003:** Toggle `agentx-ai`, first-party console, third-party provider, and custom gateway identities. Availability OR semantics match the command contract.
- **CA-004:** Snapshot interactive, headless, remote, and bridge registries. UI and unsafe local commands are absent/blocked at the right boundary while prompt commands retain shared expansion behavior.
- **CA-005:** Exact-invoke a hidden command and fuzzy-search for it. Exact succeeds; ordinary suggestions omit it.

## Parsing and expansion scenarios

- **CA-006:** Fuzz leading slash, literal spaces, tabs, `(MCP)`, Unicode, valid-name characters, invalid punctuation, absolute paths, canonical/displayed/alias collisions, and case. Results match the grammar deterministically.
- **CA-007:** Exercise `$ARGUMENTS`, both positional forms, named arguments, missing values, explicitly present empty arguments, an omitted argument envelope, quoting, failed lexing fallback, and append-without-placeholder.
- **CA-008:** Invoke an inline skill with allowed tools, model/effort, hook, and `@file`. All scoped directives and expanded attachments commit atomically and disappear at the intended scope boundary.
- **CA-009:** Invoke the same skill forked foreground and scheduled background. Child identity, result delivery, and parent transcript ownership remain explicit.
- **CA-010:** Mark a local command sensitive. Raw execution receives the secret, while UI, transcript, persistence, logging, analytics, and errors never reveal it.

## Input and attachment scenarios

- **CA-011:** Submit string, text-block array, image-last array, all-image array, empty prompt, shell mode, orphaned-permission mode, and task notification. Each normalizes without inventing or losing content.
- **CA-012:** Combine file, directory, IDE selection, agent mention, MCP resource, pasted text, and pasted image references. Preserve source order, identity, trust labels, and boundedness.
- **CA-013:** Resize multiple images with one invalid/empty image. Successful images remain ordered, the failure is attributable, and no empty block reaches the model API.
- **CA-014:** Store, recall, edit, recollapse, queue, and submit a large paste; separately persist and resume its history/reference identity. The authoritative paste bytes remain identical and cache permissions are owner-only, but queue membership itself does not survive process restart.
- **CA-015:** A prompt command contains an attachment reference while its raw arguments contain another. Apply the documented expanded-content extraction exactly once and retain literal unresolved text.

## Queue and lifecycle scenarios

- **CA-016:** Enqueue `later`, `next`, and `now` during streaming text, a concurrency-safe tool, a serialized tool, and sleep. Verify stop points, cancellation, result pairing, and stable same-priority order.
- **CA-017:** Mix user, task, scheduled, channel, bridge, and teammate messages. Agent identity and origin filters prevent cross-session consumption; slash-control flags cannot be stripped in transit.
- **CA-018:** Crash after command `started`, after UI installation, after expansion preparation, and after queue dequeue but before transcript flush. On resume, reconcile durable command effects from transcript/task evidence without replaying a committed effect. Treat UI and queue state as lost; a process-local item dequeued before any transcript commit may be absent rather than completed or rolled back, and the transcript commit boundary determines what can be specified.
- **CA-019:** A UI command calls `onDone` twice or throws after calling it. Only the first terminal completion mutates state; the queue remains live.
- **CA-020:** A submit hook blocks one prompt, prevents continuation on another, adds over 10,000 characters on a third, and fails on a fourth. Each produces the specified bounded and attributable outcome.
- **CA-021:** Invoke interactive `/help`. It renders the surface's current non-hidden command descriptors from the dispatch registry, once each in canonical-name order, including argument hints, descriptions, and aliases; it does not substitute process-startup flag usage.

## Cross-language conformance fixtures

Serialize fixture inputs and expected semantic outputs in a language-neutral format. Cover command descriptors, source lists, slash parse records, expanded blocks, transcript metadata, attachment manifests, queue snapshots, hook decisions, and lifecycle events. Ignore rendering-library object identity and module paths. Compare canonical names, ordering, raw/redacted values, stable IDs, states, limits, and user/model visibility.

## Coverage ledger

Maintain one row per CC catalog ID with implementation status, aliases, availability, gates, surface matrix, argument fixtures, happy/error result fixtures, lifecycle assertions, resume assertions, and source attribution. A command is incomplete if it only appears in help or only works interactively.
