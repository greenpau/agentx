# Message Projection, Scrolling, and Dialogs

## Contents

1. [Projection boundaries](#projection-boundaries)
2. [Message normalization and row construction](#message-normalization-and-row-construction)
3. [Streaming and virtualization](#streaming-and-virtualization)
4. [Scrolling, fullscreen, and search](#scrolling-fullscreen-and-search)
5. [Dialogs and overlay arbitration](#dialogs-and-overlay-arbitration)
6. [Notifications and footer state](#notifications-and-footer-state)
7. [Failure behavior](#failure-behavior)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Projection boundaries

Maintain three distinct representations:

1. **Authoritative transcript events:** normalized user, assistant, tool, system, attachment, task and boundary messages used for recovery and model-context projection.
2. **Derived presentation rows:** grouped, collapsed, filtered, annotated or virtualized views of those events.
3. **UI-only state:** spinners, progress, overlays, local command views, selection, scroll position and notifications.

- **REPL-MSG-001 — No reverse mutation.** Expanding, collapsing, filtering or searching a row never rewrites the transcript event.
- **REPL-MSG-002 — Explicit hidden metadata.** Any metadata that affects model context but is not ordinarily shown remains typed and recoverable; it is not smuggled through component state.
- **REPL-MSG-003 — Transcript completeness.** Full transcript mode can reveal retained semantic events even when the main conversation view hides compacted or low-signal content.

## Message normalization and row construction

Projection order:

1. Normalize message shape and compatibility fields.
2. Remove null, empty, or presentation-only progress artifacts that should not form rows.
3. Reorder only according to explicit semantic pairing rules.
4. Build lookup maps from the complete normalized history.
5. Group or collapse eligible sequences.
6. Apply compacted-history, brief-mode, transcript-mode and verbosity filters.
7. Build stable row identities.
8. Mount all or only the visible rows according to the active renderer.

Assistant content supports visible text, thinking, redacted thinking, tool use and specialized advisor content. User content supports text, images and tool results. Attachments, local/system notices, compaction boundaries, grouped tools and collapsed summary rows remain distinct types.

- **REPL-MSG-004 — Tool pairing.** A tool use and its tool result share a stable semantic key derived from tool-use identity; display grouping cannot lose either terminal outcome.
- **REPL-MSG-005 — Stable rows.** Prefer semantic message/tool identifiers over array positions so streaming updates and prepended history do not remount unrelated rows.
- **REPL-MSG-006 — Unknown blocks.** Log an unknown assistant block diagnostically and render no fabricated content. Retain the authoritative event for forward compatibility.
- **REPL-MSG-007 — Thinking visibility.** Normal nonverbose view may hide thinking, especially historical thinking. Transcript/verbose policy may reveal permitted thinking. Redacted thinking never becomes invented plaintext.
- **REPL-MSG-008 — Compaction visibility.** Retain compact summaries and boundaries semantically. Hide microcompaction and presentation-irrelevant boundaries according to the active view.
- **REPL-MSG-009 — Collapsing.** Reads, searches, hooks, team shutdown and background-shell activity may collapse only when a summary row still exposes status, expansion affordance and failure information.
- **REPL-MSG-010 — Brief projection.** Brief mode may omit ordinary assistant prose and low-signal tools, but must preserve user input, tool outcomes needed to understand the turn, errors and file-only turn context.

Rows are expandable only when an expanded representation contains additional detail. Clicking a nonexpandable row is a no-op.

## Streaming and virtualization

- **REPL-MSG-011 — Immediate semantic append.** Ingest each normalized stream event in order before presentation throttling.
- **REPL-MSG-012 — Frame batching.** Coalesce visible streaming text updates to about 16 ms while preserving exact underlying content.
- **REPL-MSG-013 — Incomplete line safety.** A renderer may hide an incomplete source line that would produce unstable formatting, then reveal it when complete.
- **REPL-MSG-014 — Streaming tool identity.** Assign one deterministic provisional row identity per active tool use and replace/update it without remounting sibling rows.
- **REPL-MSG-015 — Cooperative derivation.** When projection of a large history is expensive, yield approximately every 5 ms and resume from stable input; do not publish out-of-order partial row sets.
- **REPL-MSG-016 — Virtual mounting.** Fullscreen mounts a bounded visible window plus overscan. Main-screen nonvirtual rendering may cap retained mounted rows but cannot discard durable transcript data.

## Scrolling, fullscreen, and search

Scroll state includes current offset, content height, viewport height, bottom-pinned state, unseen divider identity/position, last manual scroll time and optional search match.

- **REPL-SCR-001 — Sticky bottom.** Streaming follows the bottom only while pinned. Manual upward scrolling disables follow until the user returns to bottom or an explicit repin rule fires.
- **REPL-SCR-002 — Human repin.** A new direct human message repins the conversation. Beginning to type in an empty prompt may repin unless the user manually scrolled within the preceding 3 seconds.
- **REPL-SCR-003 — Unseen divider.** When new assistant content arrives while scrolled away, preserve a divider before the unseen region. Prepending older history shifts its index and measured position rather than moving it into the new page.
- **REPL-SCR-004 — Fullscreen ownership.** Enter alternate screen, give Page Up/Down, wheel and configured scroll bindings to the viewport, and restore main-screen state on exit.
- **REPL-SCR-005 — Search source.** Search semantic row text, using a tool-owned text extractor where available. Cache normalized lowercase text by immutable message identity.
- **REPL-SCR-006 — Search overlay.** Search highlighting is a derived screen overlay and does not alter message text or selection extraction.
- **REPL-SCR-007 — Scrollback dump.** Leaving fullscreen may dump selected/relevant transcript content to ordinary scrollback according to user action, but must avoid duplicating content on every render.

## Dialogs and overlay arbitration

Pending interactive requests may include tool permission, hook prompt, sandbox/network permission, worker/task decision, elicitation, cost limit, idle return, settings/menu/select flows, history search, help, transcript and message actions.

- **REPL-DLG-001 — Deterministic priority.** Compute one focused dialog from pending requests using a stable priority order. Render lower-priority status if useful, but route decision input to only the focused owner.
- **REPL-DLG-002 — Modal input capture.** A modal registers its keybinding context and focus before enabling ordinary prompt input. Escape/Enter cannot reach both modal and editor.
- **REPL-DLG-003 — Typing suppression.** A nonurgent automatic prompt may defer appearance while the user is actively typing, subject to a bounded timeout. Security-sensitive approvals do not silently auto-accept.
- **REPL-DLG-004 — Queue preservation.** Opening or closing a dialog does not erase prompt input, paste map or queued commands.
- **REPL-DLG-005 — Decision settlement.** Approve, deny, cancel, unmount and session shutdown each settle the underlying request exactly once.
- **REPL-DLG-006 — Updated input.** An approval may return updated tool input as a first-class decision; presentation must show the effective request before confirmation.
- **REPL-DLG-007 — Nested overlays.** Maintain an overlay stack or equivalent ownership token so closing a child restores the previous focus/interaction surface.

## Notifications and footer state

Notifications have stable keys, priority, optional timeout, text or UI representation, and optional color/status. Adding the same key replaces or updates rather than duplicates.

- Immediate/security/error notifications outrank informational hints.
- Temporary hints expire independently and are removed on the action they describe.
- Voice recording/processing or another exclusive mode may temporarily replace ordinary notification rows, but pending notifications remain recoverable afterward.
- Footer selection and menus are UI-only and use their own keybinding context.
- Loading, permission mode, model, thinking, task/team, MCP and update indicators derive from state; they do not emit transcript messages merely by changing.

## Failure behavior

- A row-specific rendering error yields an isolated diagnostic/fallback row where possible, not loss of the transcript.
- Missing tool renderers fall back to a generic tool representation containing name, status and safe summary.
- Search extraction failures skip that row or use generic text without aborting the viewer.
- A dialog render failure denies/cancels the pending safety request rather than allowing it.
- Virtual measurement drift triggers remeasurement/anchoring; it cannot rewrite message order.

## Acceptance scenarios

**REPL-MSG-A01 — Stable heterogeneous rows.** Render text, thinking, tool use, result and compact boundary; verify each retains semantic identity across grouping and fullscreen toggles.

**REPL-MSG-A02 — Unknown content block.** Receive an unknown assistant block; verify diagnostic logging, no fabricated text and preserved transcript event.

**REPL-MSG-A03 — Streaming burst.** Stream 100 deltas rapidly; verify exact final text with bounded paints and stable row identity.

**REPL-MSG-A04 — Manual scroll during stream.** Scroll upward during streaming; verify no forced jump and an unseen divider appears.

**REPL-MSG-A05 — Human-message repin.** Submit a new human prompt while scrolled away; verify repin and correct divider reset.

**REPL-MSG-A06 — Prepended remote history.** Prepend remote history above an unseen divider; verify viewport and divider stay anchored to the same content.

**REPL-MSG-A07 — Modal arbitration.** Open autocomplete, then a permission dialog; verify only the permission context receives Enter/Escape and focus restores afterward.

**REPL-MSG-A08 — Shutdown settlement.** Cancel a pending dialog by shutting down; verify its request receives exactly one cancellation.

**REPL-MSG-A09 — Projection-mode switch.** Toggle brief mode and transcript mode; verify projection changes while authoritative messages remain byte-for-byte unchanged.

**REPL-MSG-A10 — Highlighted selection.** Search and select highlighted text; verify copied text contains original characters, not highlight control data.

## Non-normative provenance

Evidence was specified from the reference message normalization/rendering components, virtual message list, fullscreen layout and scroll box, message actions, search/highlight utilities, notification and overlay contexts, permission/dialog components, and prompt/footer presentation. These locations are non-normative.
