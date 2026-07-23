# Derived Assistance and Build-Gated Services

## Contents

1. [Common boundary](#common-boundary)
2. [Maintained documents](#maintained-documents)
3. [Away summary](#away-summary)
4. [Advisor protocol](#advisor-protocol)
5. [Feedback auto-run adapter](#feedback-auto-run-adapter)
6. [Desktop MCP import](#desktop-mcp-import)
7. [Supported external stubs](#supported-external-stubs)
8. [Failure matrix](#failure-matrix)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Non-normative provenance](#non-normative-provenance)

## Common boundary

These services derive assistance from an existing session or import optional
configuration. None is part of the core query loop. Build inclusion, product
profile, feature evaluation, account eligibility, user preference, platform
support and current runtime state remain separate decisions. A disabled service
registers no unusable callback and creates no timer, task, model request or
transcript mutation.

When a service does run, it uses an explicit query source and the ordinary
model/tool boundary. Derived artifacts have distinct message types or external
files; they never masquerade as the user's original request or as an
authoritative assistant response.

- **OPT-DER-001 — Derived provenance.** Every generated summary, maintained
  document update or advisor block remains distinguishable from ordinary user
  and assistant content at persistence, model projection and UI boundaries.
- **OPT-DER-002 — No authority inheritance.** Observing a conversation does
  not grant a background helper the main turn's tools or permission decisions.
  Each helper receives an explicit, least-authority capability set.
- **OPT-DER-003 — Bounded absence.** A false gate, unsupported build/profile,
  missing optional file or unavailable external installation resolves without
  delaying or weakening the main session.

## Maintained documents

The maintained-document service exists only in its designated internal product
profile. Initialization then registers two hooks: a file-read observer and a
post-sampling observer. External builds register neither and require no stub
task.

A file becomes tracked when read content contains a case-insensitive Markdown
heading matching `# MAGIC DOC: title`. The heading matcher is line-oriented and
may find the heading on any line. The title is trimmed. The first content line
after the heading—allowing one intervening blank line—is a document-specific
instruction only when the complete line is wrapped in matching Markdown
emphasis markers (`_..._` or `*...*`); the inner instruction is trimmed.

Tracking is a map keyed by exact path. Repeated reads do not add duplicate
entries. Tracking stores only the path: each update rereads the current file and
redetects its current title/instructions. Clearing the registry is an explicit
session/test lifecycle operation.

- **OPT-MDOC-001 — Read-time discovery.** Only content observed through the
  registered read boundary can enter the registry. Header spelling is
  case-insensitive, but path identity and later edit authorization are exact.
- **OPT-MDOC-002 — Fresh-header authority.** A cached title or instruction is
  never trusted at update time. Deletion, inaccessible content, or removal of
  the marker evicts the path rather than running a stale update.
- **OPT-MDOC-003 — Registry idempotence.** One exact path has at most one
  tracked entry regardless of read count.

The post-sampling hook is serialized across invocations. It runs only for the
main interactive query source, only when the latest assistant turn contains no
tool requests, and only when at least one path is tracked. It snapshots the
current registry iteration and updates documents one at a time. Consequently,
overlapping sampling events cannot run two updates against the same document,
and one document's update completes before the next starts.

For each document, clone the main turn's file-read cache and delete only that
document's entry so the helper receives actual current content instead of an
“unchanged” shortcut. Do not mutate the main turn's cache. A missing or
permission-inaccessible file is evicted. An unexpected read error propagates to
the hook's failure boundary instead of being misclassified as deletion.

- **OPT-MDOC-004 — Idle-only schedule.** A main turn with tool calls, a child
  query source, or an empty registry launches no update.
- **OPT-MDOC-005 — Isolated read state.** The helper's forced reread cannot
  erase or alter the owning session's file-state cache.
- **OPT-MDOC-006 — Sequential documents.** At most one maintained-document
  agent executes at a time, including across concurrent hook notifications.

An update agent uses the designated balanced model, forks the conversation
context, and receives the owning system/user/system context explicitly. Its
declared tool catalog contains only file edit. Its dynamic authorization allows
that tool only when the input is an object whose `file_path` string equals the
tracked path exactly. Every other tool, malformed input and other path is
denied with an explicit reason. The service consumes the agent stream through
completion; output text is not appended as a conversational response.

- **OPT-MDOC-007 — Exact edit confinement.** Similar, relative, normalized or
  sibling paths are not implicitly equivalent at this boundary. Only the exact
  tracked path may be edited.
- **OPT-MDOC-008 — Context fork.** The helper may reason from prior messages
  but owns an isolated tool context and query source. Its output cannot become
  the main turn's assistant answer.

The prompt template is loaded from the configured user home under
`magic-docs/prompt.md`; any read failure falls back silently to the bundled
template. Variables use `{{word}}`. Substitution is one regular-expression pass
with a callback: supplied values preserve literal dollar signs, unknown names
remain unchanged, and placeholder-shaped text introduced by one value is not
expanded again. Variables are current contents, exact path, current title and
the optional instruction section. Document-specific instructions explicitly
take priority over general update guidance.

- **OPT-MDOC-009 — Literal single-pass template.** Replacement neither applies
  regular-expression backreferences nor recursively interprets document text.
- **OPT-MDOC-010 — Custom prompt fallback.** Missing, unreadable or invalidly
  located custom prompt content selects the bundled template without blocking
  the session.

## Away summary

Away summary is a focus-derived convenience message. It requires both its
feature/configuration gate and a cached enablement result. Terminal focus state
is `focused`, `blurred`, or `unknown`; only an explicit blur starts the away
timer. Unknown is a no-op. Focus cancels the timer, aborts an in-flight
generation and clears pending-after-turn state.

After five continuous minutes blurred:

1. If no semantic turn is active, begin generation.
2. If a turn is active, mark one pending attempt rather than competing with it.
3. When the active turn ends, run only if the terminal is still blurred and the
   attempt remains eligible.

At most one away summary is generated after a real user turn. UI-only,
synthetic and derived messages do not reset this allowance. Repeated blur/focus
events cannot create multiple concurrent attempts.

- **OPT-AWAY-001 — Focus timer.** Five uninterrupted blurred minutes are
  required. Unknown, early focus and unmount produce no summary and no live
  timer.
- **OPT-AWAY-002 — Turn-safe deferral.** Generation never overlaps an active
  main semantic turn; one pending attempt may run after that turn, conditional
  on still being blurred.
- **OPT-AWAY-003 — Per-user-turn deduplication.** A real user turn re-arms one
  opportunity. Timer churn and derived messages do not.

Generation uses the most recent 30 normalized messages plus available session
memory, projected through the ordinary safe model-message boundary. It calls a
small, fast model with no tools, no extended thinking and no prompt-cache write.
The instruction asks for a concise one-to-three-sentence account of what
happened while the user was away. A nonempty result becomes a typed system
`away_summary` message; it is not a user statement or assistant turn.

Abort, empty output, stream/transport error and model error resolve to no
message. They do not surface as a failed semantic turn. Cleanup aborts the
request and invalidates late completion so a response from an old session or
focus epoch cannot append.

- **OPT-AWAY-004 — Bounded source window.** Projection includes at most 30
  recent messages and explicit session memory; it does not clone unrestricted
  history into a hidden model call.
- **OPT-AWAY-005 — Typed insertion.** Only nonempty successful output appends,
  using the dedicated system-derived type and ordinary durable ordering.
- **OPT-AWAY-006 — Abort ownership.** Focus, unmount and session replacement
  cancel or ignore old work; late completion cannot mutate the new state.

## Advisor protocol

Advisor support has independent states: environment disable, build/profile
inclusion, first-party authentication, beta eligibility and cached feature
configuration. An explicit environment disable wins. Alternate providers and
API-key-only authentication do not use the first-party advisor beta. A false or
unavailable configuration leaves ordinary requests unchanged.

The base model and advisor model are separate. Recognized public families are
the supported Opus and Sonnet 4.6 families; internal builds may admit additional
configured models. An experiment may pair a base model with an advisor model
only when advisor is enabled and the user has not supplied an explicit advisor
configuration. Model matching uses normalized model identity, not an arbitrary
substring. Explicit valid configuration wins over experiment assignment.

- **OPT-ADV-001 — Gate composition.** Environment disable and provider/profile
  restrictions fail closed without changing ordinary model selection.
- **OPT-ADV-002 — Model-pair precedence.** Explicit valid advisor selection
  wins; otherwise an enabled experiment pair applies only to its normalized
  base model.
- **OPT-ADV-003 — Recognized families.** Unsupported public model identities
  cannot be smuggled into advisor fields; internal expansion remains a separate
  build contract.

Every request in an advisor-enabled session carries the advisor beta header so
historical advisor blocks remain parseable on continuation. Agentic requests
also include advisor-model and advisor-tool instructions. Nonagentic calls may
need the header for history compatibility but do not gain the advisor tool or
its prompt instructions.

The streamed wire vocabulary includes a server-owned advisor tool-use block,
its corresponding result block, an explicit error result and a redacted form.
Unknown or malformed server blocks fail at protocol normalization; they are not
reinterpreted as user-callable local tools. Usage emitted for advisor content
uses the dedicated `advisor_message` iteration/category so cost and telemetry
do not appear as ordinary assistant tokens.

- **OPT-ADV-004 — Historical beta continuity.** Once advisor blocks may exist
  in session history, continuation retains the parsing beta even when a
  particular call does not invite new advisor work.
- **OPT-ADV-005 — Server-tool isolation.** Advisor server blocks are normalized
  and displayed/persisted through their own schema and never dispatched through
  the local capability registry.
- **OPT-ADV-006 — Usage attribution.** Advisor usage is separately categorized
  while remaining part of the owning request's aggregate limits and cost.

## Feedback auto-run adapter

The feedback/issue auto-run adapter preserves a shared UI shape while allowing
an external build to disable internal classification. Its classifier receives
the user issue payload and returns either false or a typed reason. In the
specified external profile it always returns false; no heuristic substitute is
invented.

When a future enabled profile supplies a reason, the confirmation component
invokes its `onRun` callback once on mount and displays the reason. Selecting
the explicit negative confirmation invokes cancellation. The routed command is
the ordinary issue command in external builds; an internal profile may route a
separate positive-feedback command only under its own implementation contract.

- **OPT-FDB-001 — Disabled classifier.** External false means no auto-run UI,
  command, task or message side effect.
- **OPT-FDB-002 — One-shot mount.** An enabled notification invokes its run
  callback once per mounted identity; rerendering cannot repeat the action.
- **OPT-FDB-003 — Explicit cancellation.** Negative confirmation calls the
  cancellation boundary and does not silently execute the routed command.

## Desktop MCP import

Desktop MCP import is available only on macOS and Windows Subsystem for Linux.
macOS uses the desktop application's fixed configuration path. WSL first
derives the Windows user profile when available and converts it into the mounted
filesystem; otherwise it scans the Windows users mount while excluding known
system/default profiles. Other platforms return no importable configuration.

The importer treats the desktop file as untrusted optional input. Missing,
unreadable, malformed, nonobject or missing/nonobject `mcpServers` content
returns an empty map. Each server entry is validated independently against the
stdio server configuration schema; invalid entries are skipped while valid
siblings remain. It does not import unsupported transports or partially coerce
invalid fields.

The command shows a bounded “no servers” result for an empty map. A nonempty map
is passed to an explicit import-selection UI; reading the file alone does not
mutate current settings.

- **OPT-DESK-001 — Platform path resolution.** Only the macOS fixed path or a
  validated WSL Windows-user path is consulted; generic Linux/Windows builds do
  not guess desktop paths.
- **OPT-DESK-002 — Forgiving container, strict entry.** Container/file failure
  yields empty, while entries are independently schema-validated and invalid
  siblings are omitted.
- **OPT-DESK-003 — Explicit import commit.** Discovery is read-only. Settings
  change only through the normal import dialog and configuration writer.

## Supported external stubs

An internal-only hook may have an external implementation that returns neutral
values with the same callable contract. The stub is self-contained: it imports
no absent internal package, allocates no resource and does not require callers
to branch. For the optional “more-right” hook, before-query permits ordinary
dispatch, turn completion resolves without mutation, and render returns no
node.

- **OPT-DER-004 — Stub neutrality.** A stub cannot consume input, add context,
  block a query, change permissions, create background work or keep the process
  alive.
- **OPT-DER-005 — Signature parity.** Enabled and external builds integrate at
  the same call sites; profile selection changes implementation, not caller
  ordering.

## Failure matrix

| Boundary | Expected failure | Required result |
| --- | --- | --- |
| Maintained-document custom prompt | absent or unreadable | bundled prompt |
| Maintained-document current file | missing/inaccessible/header removed | evict exact path |
| Maintained-document agent | tool/path mismatch | explicit denial, no edit |
| Away timer | focus/unmount/session replacement | cancel timer/request, no message |
| Away model | error, abort or empty content | no derived message |
| Advisor eligibility | false, unavailable or provider mismatch | ordinary request without advisor work |
| Advisor wire block | malformed or unknown shape | protocol error/redaction path, never local execution |
| Feedback classifier in external build | always false | no auto-run behavior |
| Desktop config | file/JSON/container invalid | empty discovery result |
| Desktop server entry | schema invalid | omit only that entry |
| External stub | module absent | neutral values, no handles |

## Acceptance scenarios

- **OPT-MDOC-A01 — Discovery grammar.** Read files with mixed-case marker,
  marker after ordinary text, one optional blank line and valid/invalid emphasis
  instructions; verify exact title/instruction extraction and path deduplication.
- **OPT-MDOC-A02 — Update eviction.** Track a file, then delete it, deny access
  and remove its header in separate runs; verify each expected state evicts it
  while an unrelated read error follows the hook error boundary.
- **OPT-MDOC-A03 — Schedule serialization.** Trigger overlapping main-thread
  post-sampling notifications for two tracked files; verify no run after a tool
  turn, then one-at-a-time ordered updates after an idle assistant turn.
- **OPT-MDOC-A04 — Capability confinement.** Ask the helper to edit a sibling,
  use a different tool and supply malformed input; verify explicit denials. Edit
  the exact tracked path and verify only that action is allowed.
- **OPT-MDOC-A05 — Template literals.** Substitute content containing `$1`,
  `$$`, an unknown placeholder and a placeholder matching a later variable;
  verify literal dollars, unknown preservation and no recursive substitution.
- **OPT-AWAY-A01 — Focus epochs.** Blur for 299 seconds, focus, then blur for
  300 seconds; verify only the second epoch starts work and focus cancels a live
  request without a late append.
- **OPT-AWAY-A02 — Active-turn deferral.** Let the timer expire during an
  active turn; verify one pending attempt, execution only after turn completion
  while still blurred, and no second summary until a real user message.
- **OPT-AWAY-A03 — Generation failures.** Return empty text, model error and
  abort, then a nonempty two-sentence result; verify only the last becomes one
  typed `away_summary` system message sourced from at most 30 messages.
- **OPT-ADV-A01 — Gate and pair matrix.** Exercise environment disable,
  alternate provider, explicit model, matching/nonmatching experiment pair and
  unsupported family; verify model/header/tool decisions and precedence.
- **OPT-ADV-A02 — Historical continuation.** Continue history containing an
  advisor server block through a call that invites no new advisor work; verify
  beta parsing remains enabled but no local tool is registered or executed.
- **OPT-ADV-A03 — Wire and usage.** Stream use, success, error and redacted
  advisor blocks; verify schema correlation, safe display/persistence and
  `advisor_message` usage attribution.
- **OPT-FDB-A01 — External no-op.** Classify positive and negative issue text
  in the external build; verify false, no mounted confirmation and no command.
  In a test enabled adapter, rerender the mounted identity and verify one run;
  choose no and verify cancellation.
- **OPT-DESK-A01 — Desktop paths.** Resolve macOS, WSL with Windows profile,
  WSL scan with excluded system users and plain Linux; verify only documented
  paths are read.
- **OPT-DESK-A02 — Partial import.** Provide malformed container data and then
  a map with valid stdio and invalid/unsupported siblings; verify empty for the
  first, only valid entries for the second, and no setting mutation before UI
  confirmation.
- **OPT-DER-A01 — Stub profile.** Call every external stub integration point;
  verify dispatch proceeds, completion/render are neutral and no timer, task,
  message, permission mutation or live handle remains.

## Non-normative provenance

Evidence was specified from maintained-document discovery, prompting and
post-sampling integration; focus/turn-aware away-summary generation; advisor
selection and API projection; feedback auto-run adapters; desktop MCP import;
and an external no-op hook. Current file paths, model nicknames, framework hooks
and internal build labels are provenance only.
