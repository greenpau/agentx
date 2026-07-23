# Terminal Input, Focus, and Selection

## Contents

1. [Input model](#input-model)
2. [Incremental tokenization](#incremental-tokenization)
3. [Normalized events](#normalized-events)
4. [Dispatch and propagation](#dispatch-and-propagation)
5. [Focus](#focus)
6. [Mouse, scrolling, and selection](#mouse-scrolling-and-selection)
7. [Failure and recovery](#failure-and-recovery)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Input model

Treat stdin as an arbitrary byte stream. One read may contain half a sequence, many keys, a bracketed paste, mouse traffic, terminal query responses, or a mixture. Preserve parser state across reads and expose an explicit flush at end-of-input.

Normalized outputs are one of:

- Key input: logical key name, printable text, modifiers, navigation flags, repeat/release metadata when available, and `isPasted`.
- Mouse input: button, press/release/drag kind, zero-based screen coordinates and modifier state.
- Terminal response: focus state, keyboard-protocol flags, cursor position, status/mode response, or another recognized response to an application query.

- **TERM-IN-001 — Response separation.** Syntactically recognizable terminal responses never enter the text editor.
- **TERM-IN-002 — Paste integrity.** Bracketed paste delimiters are control tokens; content between them is delivered as paste data, including empty content.
- **TERM-IN-003 — Chunk independence.** Parsing the same byte sequence in any chunk partition produces the same normalized events after flush.

## Incremental tokenization

The parser recognizes ordinary UTF-8 text and terminal escape families including legacy key sequences, CSI/SS3 navigation, modified keys, Kitty keyboard reports, focus in/out, bracketed paste, SGR and X10 mouse, device/status replies, and cursor-position reports.

- **TERM-IN-004 — Ambiguous escape.** Hold an incomplete escape prefix until it can be classified or explicitly flushed. On flush, emit the safest compatible key/text representation.
- **TERM-IN-005 — High-bit compatibility.** Support terminals that encode meta by setting the high bit or prefixing Escape; normalize these to the same logical modifier where possible.
- **TERM-IN-006 — Alt/meta collapse.** Legacy terminals cannot distinguish alt/option/meta. Expose one logical terminal meta modifier. Preserve super/command separately only when the keyboard protocol reports it.
- **TERM-IN-007 — Mouse tails.** Defensively recognize an orphaned mouse tail caused by delayed chunking, but do not classify ordinary batched text such as bracketed words as mouse input.
- **TERM-IN-008 — Unknown sequence.** Unknown but complete input must either become literal text/key data or a safely ignored terminal response; it must not poison subsequent parsing state.

## Normalized events

Key normalization includes Escape, Enter/Return, Tab, Backspace, Delete, arrows, Page Up/Down, Home, End, wheel pseudo-keys, function keys, printable text, ctrl, shift, terminal-meta, super, and function modifier where detectable.

Escape may appear with a legacy meta flag in low-level parsing; binding resolution must clear meta for the Escape key itself.

An input event carries the parsed record plus propagation state:

- `preventDefault`: suppress component default behavior while allowing observers unless immediate propagation is also stopped.
- `stopPropagation`: stop tree bubbling after current target.
- `stopImmediatePropagation`: prevent later global/hook listeners and tree handlers from processing the event.

- **TERM-IN-009 — Raw-mode timing.** Register the first input listener during layout/commit, not a later passive effect, so keystrokes cannot echo in a cooked-mode gap after initial render.
- **TERM-IN-010 — Ctrl-C policy.** If the application owns Ctrl-C, deliver it as input. If exit-on-Ctrl-C is enabled, perform the registered graceful-exit path rather than dispatching it twice.
- **TERM-IN-011 — Paste callback.** A multi-character paste is delivered as one logical input callback where framing permits; downstream paste assembly still tolerates arbitrary chunking.

## Dispatch and propagation

Dispatch order must support a global chord interceptor, modal/overlay handlers, focused DOM-like handlers, and ordinary component input hooks.

- **TERM-IN-012 — Chord interception.** A key that begins or continues a configured chord is consumed before the prompt editor sees it.
- **TERM-IN-013 — Single-key cooperation.** A single-key binding may flow through component ordering so an autocomplete acceptor can act before a generic submit handler.
- **TERM-IN-014 — Handler liveness.** Listener registration remains stable while invoking the latest callback and active-state value; re-rendering must not reorder listeners accidentally.
- **TERM-IN-015 — Synthetic keyboard event.** DOM-like keyboard handlers and compatibility input hooks receive semantically equivalent modifier and key fields.

## Focus

Each terminal root owns a focus manager with one active element and a bounded previous-focus stack of 32 elements.

- **TERM-FOC-001 — Focus transition.** Focusing a new eligible node dispatches blur on the prior node and focus on the new node, including the related target.
- **TERM-FOC-002 — Restoration.** Removing a focused node or any ancestor containing it dispatches blur and restores the most recent still-mounted eligible node from the stack.
- **TERM-FOC-003 — Traversal.** Next/previous focus traverses eligible nodes deterministically and wraps only if the caller's traversal contract allows it.
- **TERM-FOC-004 — Root isolation.** Focus never crosses terminal roots.
- **TERM-FOC-005 — Terminal focus.** Track `focused`, `blurred`, or `unknown`. Treat `unknown` as focused for throttling and behavior because unsupported terminals emit no reports.

## Mouse, scrolling, and selection

Mouse hit testing uses the rendered screen coordinates and current node geometry. It is active only in a fixed viewport where mouse tracking is explicitly enabled.

Selection state contains anchor, focus, drag flag, word/line expansion span, captured scrolled rows, virtual unclamped coordinates, and whether native terminal selection was bypassed.

- **TERM-SEL-001 — Bare click.** Mouse down establishes a possible anchor, but an unchanged release creates no one-cell selection and does not overwrite the clipboard.
- **TERM-SEL-002 — Linear range.** Normalize anchor/focus into reading order. Select from the first column on intermediate lines, not a rectangular block.
- **TERM-SEL-003 — Word selection.** Double-click expands across cells in the same character class and includes wide-character tails.
- **TERM-SEL-004 — Line selection.** Triple-click selects the logical rendered line. Subsequent dragging extends line by line.
- **TERM-SEL-005 — No-select cells.** Gutters, line numbers, diff sigils, and other marked cells remain outside copied text even when geometrically inside the selection.
- **TERM-SEL-006 — Scroll tracking.** Keyboard scrolling shifts both endpoints with content. Drag auto-scroll keeps the live mouse focus fixed while shifting the anchor. Streaming follow-scroll moves both endpoints with selected content.
- **TERM-SEL-007 — Scrollback capture.** Capture selected rows before they leave the in-memory viewport so copied text remains complete. Clear the selection if both useful endpoints irrecoverably leave the retained range.
- **TERM-SEL-008 — Overlay timing.** Apply selection styling to the completed logical screen before diffing. Use a consistent solid selection background rather than inverting each source foreground.
- **TERM-SEL-009 — Copy text.** Extract graphemes, omit spacer tails and no-select cells, preserve line boundaries, and include captured scrollback segments exactly once.
- **TERM-SEL-010 — Search composition.** Search and selection overlays compose deterministically; neither mutates authoritative cell content.

Wheel events are normalized as scroll input. If the outer scroll viewport cannot move, its handler may return “not consumed” so a focused inner list can use the same wheel event.

## Failure and recovery

- Reset parser, terminal-focus and transient mouse state when a terminal root is fully torn down.
- On malformed coordinates, clamp or ignore the event; never index outside the current screen.
- If mouse tracking is unexpectedly active after a mode transition, swallow recognizable orphan sequences rather than inserting them into the prompt.
- If a focused node disappears during dispatch, finish the current event against a stable target snapshot and restore focus afterward.
- Selection/copy failure is presentation-local and cannot abort the semantic session.

## Acceptance scenarios

- **TERM-IN-A01 — Chunk invariance.** Feed a Kitty modified-key sequence one
  byte at a time and in one chunk; verify identical logical key output.
- **TERM-IN-A02 — Split paste.** Split bracketed-paste start/content/end across
  reads; verify one paste with exact content and no delimiter text.
- **TERM-IN-A03 — Empty paste.** Emit an empty bracketed paste; verify
  `isPasted` with empty content so clipboard-image handling can run.
- **TERM-IN-A04 — Response separation.** Interleave a cursor-position response
  with ordinary typing; verify only typing reaches the editor.
- **TERM-IN-A05 — Chord ownership.** Start a chord, press an invalid
  continuation, and verify both keys are swallowed according to chord
  cancellation rather than the continuation being typed.
- **TERM-IN-A06 — Focus restoration.** Remove a focused modal and verify focus
  returns to the prior mounted input.
- **TERM-IN-A07 — Scroll selection.** Drag from bottom to top while scrolling;
  verify copied text includes rows that left the viewport.
- **TERM-IN-A08 — Bare click.** Click without dragging; verify no selection and
  no clipboard replacement.
- **TERM-IN-A09 — Wide copy.** Select across a wide grapheme and no-select
  gutter; verify one grapheme and no gutter text are copied.
- **TERM-IN-A10 — Unknown terminal focus.** Run without focus reporting; verify
  state remains `unknown` and behavior is focused-compatible.

## Non-normative provenance

Evidence was specified from the reference incremental key parser, input and keyboard event classes, input hooks, root focus manager, terminal-focus signal, mouse dispatch, selection model, scroll widgets, and terminal mode helpers. These are evidence locations only.
