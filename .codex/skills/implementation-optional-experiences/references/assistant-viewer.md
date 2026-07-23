# Persistent Assistant and Viewer Mode

## Contents

1. [Availability and activation](#availability-and-activation)
2. [Embedded assistant mode](#embedded-assistant-mode)
3. [Viewer-only attachment](#viewer-only-attachment)
4. [Paged remote history](#paged-remote-history)
5. [Failure and supported absence](#failure-and-supported-absence)
6. [Acceptance scenarios](#acceptance-scenarios)
7. [Non-normative provenance](#non-normative-provenance)

## Availability and activation

Persistent assistant behavior exists only when its module is included in the build. Runtime activation additionally depends on the appropriate project/user setting or explicit force flag, account eligibility/feature gate, and compatible agent configuration.

- **OPT-AST-001 — Build exclusion.** When the module is excluded, ordinary startup contains no unresolved import, assistant command side effect, prompt addendum, team context or viewer hook.
- **OPT-AST-002 — Explicit force.** A hidden/daemon force option may bypass the ordinary eligibility check only for its intended managed launch path; it still obeys authentication, policy and capability safety boundaries.
- **OPT-AST-003 — Bounded gate.** Interactive eligibility checking is bounded to roughly 5 seconds. Failure or timeout falls back to ordinary mode unless explicit forced mode requires a hard failure.
- **OPT-AST-004 — Valid identity.** Do not activate merely because a setting is true. Require the expected assistant agent identity/configuration so a partially copied project cannot silently change session meaning.

Assistant command routing is position-sensitive: an `assistant` command at the documented command position may attach to an optional session identifier. The same word inside print-mode prompt text does not activate the viewer. Print mode does not masquerade as the interactive viewer.

## Embedded assistant mode

When active in the local interactive session:

- Mark the activation path for diagnostics/telemetry without treating it as model content.
- Initialize the assistant's team/coordinator context before ordinary initial team computation; assistant context takes precedence for the leader while teammates retain their own identities.
- Append the assistant-specific system-prompt addendum after ordinary prompt construction using the same append precedence as other explicit addenda.
- Reuse normal tool, permission, transcript, task and cleanup behavior.
- Treat assistant mode as stable for the session lifetime so scheduled-work behavior does not oscillate when settings hot-reload.
- A perpetual assistant bridge/session does not use ordinary finite-session auto-teardown rules.

- **OPT-AST-005 — No authority expansion.** The assistant addendum and perpetual lifecycle do not grant new tools or bypass permission policy.
- **OPT-AST-006 — Context precedence.** Assistant team context replaces only the initial leader context; it does not overwrite delegated agent identity.
- **OPT-AST-007 — Shared transcript.** Assistant messages remain ordinary normalized session events and use normal persistence/recovery.

## Viewer-only attachment

Viewer mode attaches the local terminal to a remotely running assistant session.

Activation flow:

1. Resolve the requested session ID or discover eligible sessions.
2. If the required assistant installation/service is absent and installation is supported, perform the explicit installation flow, report where it was installed, and ask the user to reconnect after the daemon starts.
3. Build a viewer-only remote session configuration.
4. Add an informational local message that identifies the attached session without placing it in remote model context.
5. Render the ordinary REPL through the remote session adapter.
6. Route user sends, interrupts and permissions to the remote owner.

- **OPT-AST-008 — Viewer isolation.** Do not create a local query engine or execute remote tool-use messages locally.
- **OPT-AST-009 — Session identity.** Remote session ID is authoritative for event history and transport. Local viewer UI may abbreviate it only for display.
- **OPT-AST-010 — Existing messages.** Live events and paged history use the same SDK-message conversion rules, including user text and tool results.

## Paged remote history

History API contract:

- Prepare authenticated base URL and headers once per attached session.
- Fetch pages of 100 events by default.
- Latest page uses an anchor-to-latest request and returns chronological events.
- Older pages use the oldest returned event ID as a `before` cursor.
- Each request has a 15,000 ms timeout.
- A successful page contains chronological events, nullable first ID and `hasMore`.
- Non-200, transport, authentication or parse failure returns a best-effort failure state rather than replacing current messages.

Viewer paging state:

- Cursor state `not-yet-fetched`: initial page has not been requested.
- Cursor state `continuation(value)`: older pages may exist and `value` is the opaque service cursor.
- Cursor state `end-of-pages`: history is exhausted. On JSON boundaries this state may be encoded by an explicit `null` sentinel; absence means `not-yet-fetched` only in the owning in-memory record.
- One in-flight older-page request at a time.
- Begin prefetch when the viewport is within 40 rows of the top.
- On initial load, chain at most 10 pages to fill an underfull viewport, bounding the case where raw events convert to no visible messages.

Sentinel states use one stable message identity and mutate text in place:

- `loading older messages…`
- `failed to load older messages — scroll up to retry`
- `start of session`

- **OPT-AST-011 — Prepend anchoring.** Before prepending a noninitial page, record scroll height and visible position. After layout, shift scroll by the exact height delta so the same content remains under the viewer.
- **OPT-AST-012 — Divider anchoring.** Notify the unseen-divider controller of both message-count and height deltas so its semantic boundary remains fixed.
- **OPT-AST-013 — Retry cursor.** A page failure preserves the current cursor and changes the sentinel to retry text. It does not mark history exhausted.
- **OPT-AST-014 — Stable sentinel.** Reuse sentinel identity so virtualization treats state changes as one row rather than remove/insert churn.
- **OPT-AST-015 — Cancellation.** Unmount cancels/ignores pending initial work; a late page cannot mutate a different viewer session.

## Failure and supported absence

- Missing build module: assistant behavior is absent and ordinary mode proceeds.
- Ineligible account or false gate: ordinary mode proceeds without assistant prompt/team mutation.
- Missing session ID: discovery/picker may run; cancellation returns to ordinary command flow.
- History authorization or page failure: retain live/current messages and show retry sentinel where applicable.
- Remote disconnect: viewer reports disconnected/reconnecting state and retains received history.
- Installation failure: report a user-visible local error; do not leave a half-attached viewer.

## Acceptance scenarios

- **OPT-AST-A01 — Supported absence.** Build without assistant modules; verify
  normal startup, command parsing and print prompts containing “assistant”.
- **OPT-AST-A02 — Identity and bounded gate.** Enable the project setting
  without valid agent identity, then time out the eligibility check; verify the
  mode does not activate and ordinary interactive startup proceeds in bounded
  time.
- **OPT-AST-A03 — Viewer isolation.** Attach to a known session; verify local
  input is sent remotely and remote tool use is never executed locally.
- **OPT-AST-A04 — Paged anchoring.** Load a 100-event latest page, scroll
  within 40 rows of the top, and load older history; verify chronological order
  and the exact height-delta viewport adjustment.
- **OPT-AST-A05 — Empty conversion bound.** Return zero visible messages from
  several raw pages; verify at most 10 automatic fill attempts.
- **OPT-AST-A06 — Retry sentinel.** Fail an older-page request; verify stable
  sentinel identity, retry text and preserved cursor, then succeed on the next
  scroll.
- **OPT-AST-A07 — Divider anchoring.** Prepend history while an unseen divider
  exists; verify divider and viewport remain attached to the same messages.
- **OPT-AST-A08 — Session cancellation.** Unmount during initial fetch and
  attach to another session; verify the late first response is ignored.

## Non-normative provenance

Evidence was specified from the reference assistant activation branches, assistant remote viewer construction, SDK event adapter, assistant history client and scroll-up history hook. Several internal assistant modules are absent from the external source; their implementation details are intentionally not assumed.
