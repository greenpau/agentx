# Terminal Rendering Pipeline

## Contents

1. [Responsibility and data model](#responsibility-and-data-model)
2. [Render lifecycle](#render-lifecycle)
3. [Layout and frame rules](#layout-and-frame-rules)
4. [Diff and terminal-write rules](#diff-and-terminal-write-rules)
5. [Fullscreen and scrolling](#fullscreen-and-scrolling)
6. [Resize, suspend, and contamination recovery](#resize-suspend-and-contamination-recovery)
7. [Failure behavior](#failure-behavior)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Responsibility and data model

The renderer accepts a retained presentation tree, terminal dimensions and capabilities, then emits terminal writes and frame-completion observations. It owns presentation layout and physical terminal state, not application or transcript semantics.

**Inputs**

- A retained node tree with text, boxes, styles, borders, flex/layout properties, clipping, focusability, event handlers, scrolling, selection exclusions, hyperlinks, and optional declared native cursor.
- Terminal width and height; use 80 columns by 24 rows when the stream reports neither.
- Mode flags for main screen versus alternate screen, synchronized output, mouse/focus tracking, extended keyboard support, debug repaint, and output contamination.
- The previous logical frame and known physical cursor state.

**Outputs**

- An interned logical frame containing a rectangular screen of cells, viewport dimensions, terminal cursor, optional declared-cursor target, and layout metadata.
- A sequence of terminal control and text writes that transforms the previous physical display into the new frame.
- Frame timing and completion events; these are diagnostics/presentation signals and are not transcript events.

Each screen cell carries a grapheme or spacer marker, style identity, hyperlink identity, selectability, and occupancy. Wide graphemes use a head cell plus tail cells so hit testing, clipping, selection, and diffing agree.

## Render lifecycle

- **TERM-REN-001 — Root lifecycle.** A root supports render, unmount, and wait-until-exit. Re-rendering reuses the root, reconciler, style pools, and front/back buffers.
- **TERM-REN-002 — Scheduling.** Coalesce ordinary updates to a 16 ms frame interval. Publish committed state, layout effects, focus, and cursor declarations before paint, and complete that paint before dispatching the next external input event. Preserve this ordering with the target runtime's scheduler; no particular microtask primitive is required.
- **TERM-REN-003 — Commit layout.** Calculate layout during commit before layout effects. Event handlers and effects may inspect final geometry from that commit.
- **TERM-REN-004 — One paint owner.** Serialize paints per output stream. A second paint cannot interleave bytes with an active synchronized-output transaction.
- **TERM-REN-005 — Empty-diff fast path.** If the logical screen and native cursor target are unchanged, emit no bytes.
- **TERM-REN-006 — Pool maintenance.** Intern repeated characters, styles, and hyperlinks for efficient comparisons. Pool compaction may occur periodically, but must migrate both buffers atomically so cell identity remains coherent.

Lifecycle sequence:

1. Reconcile changed presentation nodes.
2. Calculate root and descendant layout.
3. Run layout effects, including focus and native-cursor declaration.
4. Render visible nodes into the back frame.
5. Apply derived overlays such as search and selection.
6. Diff front against back, accounting for scroll/follow operations.
7. Add terminal-mode preamble and cursor parking.
8. Write atomically where supported.
9. Swap frames and publish frame completion.

## Layout and frame rules

- **TERM-REN-007 — Invalid layout.** If root width or height is invalid or uncomputed, produce an empty safe frame and a diagnostic warning rather than indexing invalid coordinates.
- **TERM-REN-008 — Clipping.** Clip children to every active clipping ancestor. A clipped cell cannot remain in hit-test or selection metadata.
- **TERM-REN-009 — Absolute and relative placement.** Resolve layout first, then translate to root screen coordinates. Positioned descendants may overlap; painting order determines the visible cell.
- **TERM-REN-010 — Stable node cache.** Cache rendered subtrees only when geometry, content, styles, clipping and dependent pools are unchanged. Any layout shift or temporary offscreen render invalidates affected cache entries.
- **TERM-REN-011 — Borders and wide cells.** Border painting, truncation and overwrites must clear both halves of any displaced wide grapheme.
- **TERM-REN-012 — Cursor safety.** Clamp the normal frame cursor to a valid cell. In alternate-screen mode, park it on the last terminal row rather than emitting a line feed that would scroll the screen.
- **TERM-REN-013 — Declared cursor.** A focused editor may declare the physical terminal cursor position for IME and accessibility. Apply the declaration after the screen diff. If the declaration disappears or its node is not rendered, restore the frame cursor safely.

## Diff and terminal-write rules

- **TERM-REN-014 — Double buffer.** Preserve the last successfully emitted frame as the front frame. Build the next frame separately and swap only after constructing the complete write.
- **TERM-REN-015 — Minimal safe damage.** Emit runs that change cells, style, hyperlink, clearing, or cursor position. Do not rely on stale physical cursor knowledge after external output or resume.
- **TERM-REN-016 — Style closure.** Close or reset styles and hyperlinks at run boundaries so a shorter new line cannot inherit attributes from removed content.
- **TERM-REN-017 — Shrink clearing.** When content shrinks, erase obsolete cells and rows explicitly. Never assume writing a shorter line removes the old suffix.
- **TERM-REN-018 — Atomic paint.** When synchronized output is supported, bracket erasure, diff, overlays, and final cursor placement in one transaction. Unsupported terminals receive the same ordered bytes without brackets.
- **TERM-REN-019 — External log output.** If another subsystem writes while the renderer is active, mark the current physical screen contaminated and force one full-damage frame before resuming incremental diffs.

## Fullscreen and scrolling

- **TERM-REN-020 — Alternate-screen ownership.** Enter and exit the alternate screen exactly once per mode transition. Restore mouse, focus, keyboard, cursor, and screen modes on exit even after cancellation.
- **TERM-REN-021 — Fixed viewport.** Alternate-screen output is clipped to terminal rows. The internal viewport may use one extra accounting row to prevent the terminal from interpreting the final cursor position as a scroll request, but no content may become visibly addressable below the physical terminal.
- **TERM-REN-022 — Absolute anchor.** Before a nonempty alternate-screen diff, home the physical cursor and compute the diff from the same known origin. This self-heals cursor movement by multiplexers or terminal decorations.
- **TERM-REN-023 — Scroll optimization.** A scroll container may shift preserved rows and paint only newly exposed damage when layout and sibling geometry make that safe. Otherwise repaint the affected viewport.
- **TERM-REN-024 — Follow-scroll.** When streaming content keeps a viewport pinned to the bottom, shift selection and search coordinates with the content before overlays are applied.
- **TERM-REN-025 — Main-screen history.** Main-screen mode may intentionally append to terminal scrollback. Do not home to row one because prior rows may no longer be addressable.

## Resize, suspend, and contamination recovery

- **TERM-REN-026 — Resize.** Coalesce duplicate dimension events. Update dimensions, invalidate layout and frame assumptions, then render with the new geometry. Do not erase the screen synchronously before the new frame is ready.
- **TERM-REN-027 — Resume.** After process resume, sleep/wake, or external TUI handoff, terminal modes and cursor position are unknown. Re-enable required modes, clear buffer assumptions and repaint completely.
- **TERM-REN-028 — Pause handoff.** Before yielding the terminal to an external program, flush pending presentation changes and restore modes expected by an ordinary shell. On return, reacquire modes and repaint.
- **TERM-REN-029 — Unmount.** Cancel scheduled renders before freeing layout nodes, restore console/output wrappers, show the cursor, disable input extensions, exit alternate screen, and resolve the root's exit waiter.

## Failure behavior

- A terminal write failure stops further incremental assumptions and initiates orderly unmount or process shutdown according to the caller.
- A node render failure is isolated by the presentation framework's error boundary where available; never partially swap a failed back frame into the front frame.
- Unsupported synchronized output, mouse, focus, hyperlinks, Kitty keyboard, or modified-key protocols degrade independently.
- A stale declared cursor, invalid geometry or offscreen target is ignored for that frame and cannot produce an out-of-bounds cursor sequence.

## Acceptance scenarios

- **TERM-REN-A01 — First frame.** Render styled multiline text from an empty
  80×24 frame; verify complete content, final reset and safe cursor.
- **TERM-REN-A02 — Single-cell edit.** Change one narrow grapheme; verify
  unchanged rows are not rewritten.
- **TERM-REN-A03 — Wide overwrite.** Replace a double-width grapheme with a
  narrow one; verify no tail cell remains.
- **TERM-REN-A04 — Shrink clearing.** Remove the last three rows; verify the
  physical display contains no stale text.
- **TERM-REN-A05 — Same-commit caret.** Update editor value and cursor in one
  input event; verify the declared terminal cursor moves in the same frame.
- **TERM-REN-A06 — Resize convergence.** Shrink and expand rapidly; verify final
  geometry wins, no deliberate blank interval, and a complete next frame.
- **TERM-REN-A07 — Contamination recovery.** Write an external diagnostic
  between frames; verify one full repaint followed by incremental output.
- **TERM-REN-A08 — Fullscreen drift.** Perturb the physical cursor out of band;
  verify the next nonempty frame homes first and does not creep vertically.
- **TERM-REN-A09 — Suspend/resume.** Pause for an external TUI and resume;
  verify modes, screen, cursor and input are restored.
- **TERM-REN-A10 — Capability degradation.** Run without synchronized output
  or extended keyboard support; verify content and ordinary keys remain correct.

## Non-normative provenance

The contract covers the terminal root, retained renderer, node-to-output traversal, frame/screen/output pools, log-update diff writer, scrolling widgets, terminal control helpers, and lifecycle integration. These paths and implementation techniques are not required in the maintained AgentX runtime.
