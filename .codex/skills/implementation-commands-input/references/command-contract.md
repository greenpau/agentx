# Command descriptor and result contracts

## Shared descriptor

Every command has:

| ID | Field | Contract |
| --- | --- | --- |
| CD-001 | `name` | Canonical case-sensitive invocation name without `/`. Valid names use letters, digits, colon, underscore, and hyphen. |
| CD-002 | `description` | User-facing summary; retain whether it was author-specified because model discovery may require that fact. |
| CD-003 | `aliases` | Optional exact alternate invocation names. Canonicalize after lookup. |
| CD-004 | displayed name | Optional source-aware `userFacingName`; defaults to canonical name. |
| CD-005 | availability | Optional OR-list of `agentx-ai` and `console`; absence means provider-universal. |
| CD-006 | enablement | Optional live predicate; absence means enabled. |
| CD-007 | visibility | `isHidden` defaults false and affects listing/typeahead only, not exact invocation. |
| CD-008 | source | Built-in/settings source or `loadedFrom=commands_DEPRECATED|skills|plugin|managed|bundled|mcp`, with optional plugin/version metadata. |
| CD-009 | invocation policy | `userInvocable`, `disableModelInvocation`, `disableNonInteractive`, and supported surface flags. |
| CD-010 | behavior hints | Argument hint, `whenToUse`, sensitivity, workflow badge, and `immediate`. |

Aliases do not create independent usage history or transcript identities. Source attribution remains available for collisions and autocomplete.

`CD-011` — Incidental formatting of command availability, descriptors,
invocations, results, and registries reports only a fixed type/shape marker.
Descriptor text, callbacks, raw or redacted input, local output, and prompt
content remain available only through their deliberate typed channels; `%v`,
`%+v`, `%#v`, `%s`, and `%q` are not alternate command-output surfaces.

## Prompt commands

A prompt command declares `progressMessage`, `contentLength`, source, asynchronous prompt expansion, and optionally named arguments, allowed tools, model, effort, hooks, skill root, applicable paths, execution context `inline|fork`, and forked agent.

- **Inline:** create visible command/loading metadata, hidden model-visible expanded content, extracted attachment messages, and a scoped command-permission/model directive. Register only trusted hooks declared by the resolved source.
- **Fork:** run expansion in a separate agent context and return its result. Persistent/assistant modes may schedule the fork and enqueue its completion. When MCP discovery is still settling, poll at 200 ms intervals for at most 10 seconds before background launch.
- **Coordinator:** summarize the command request for delegation instead of injecting full worker instructions into the coordinator context.
- `userInvocable=false` rejects direct slash use with guidance to ask the model. `disableModelInvocation=true` keeps direct user use while excluding model Skill discovery.

## Local commands

A local command lazy-loads an implementation, declares `supportsNonInteractive`, and returns exactly one of:

- `{type:text,value}` — local command output; render as system/local stdout, not as an ordinary user prompt;
- `{type:compact,compactionResult,displayText?}` — atomically replace/project conversation history using the compaction contract;
- `{type:skip}` — no transcript/result message.

Pass raw arguments to execution. If `isSensitive`, store `***` in transcript-visible argument metadata whenever raw arguments are nonblank. Load/invocation failure becomes local stderr and does not query the model.

## Local UI commands

A `local-jsx`-equivalent command lazy-loads a modal/inline UI flow and receives an `onDone` callback. The callback may specify:

- display as `skip|system|user`;
- whether resulting messages query the model;
- hidden model-visible metadata messages;
- next input and whether to submit it.

Exactly one completion path clears the UI and emits the command-completed lifecycle event. Dismissal and exceptions must resolve the flow; otherwise the command queue deadlocks. A fullscreen result ending in ` dismissed` may intentionally omit a transcript entry.

## Lifecycle and transcript metadata

Emit `started` and `completed` events under one command invocation UUID. Prompt expansion uses these stable tags:

```text
<command-message>canonical-name</command-message>
<command-name>/canonical-name</command-name>
<command-args>raw-or-redacted-args</command-args>
```

Local stdout/stderr use their corresponding explicit tags. Metadata that exists only to render loading, command headers, or errors stays out of API context unless deliberately marked model-visible.

## Provider availability

- `agentx-ai`: authenticated AgentX Cloud subscriber.
- `console`: direct first-party API customer who is not a subscriber, third-party provider user, or custom gateway user.
- A list is OR, not AND.
- Evaluate availability before enablement every time commands are requested so `/login` and provider changes take effect immediately.

## Descriptor acceptance cases

- **CD-A01:** A hidden enabled command is absent from help but executes when typed exactly.
- **CD-A02:** A sensitive local command receives raw input while persisted/displayed command args are `***`.
- **CD-A03:** A UI command exception calls the terminal completion path, clears UI state, and permits the next queued item.
- **CD-A04:** A prompt command disabled for noninteractive mode is rejected before expansion in headless mode.
- **CD-A05:** One invocation emits one started and one completed event with the same UUID even when dismissed.
- **CD-A06:** Put a unique secret in every string, callback-bearing, input,
  output, and prompt field of each public command value, then render each value
  with `%v`, `%+v`, `%#v`, `%s`, and `%q`. Every rendering contains only its
  fixed opaque shape and no secret; deliberate result and descriptor accessors
  retain their ordinary typed values.
