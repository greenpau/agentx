# Terminal Text Layout and Capability Discovery

## Contents

1. [Text and cell model](#text-and-cell-model)
2. [Display width](#display-width)
3. [Wrapping, truncation, and tabs](#wrapping-truncation-and-tabs)
4. [Structured and ANSI styles](#structured-and-ansi-styles)
5. [Bidirectional text](#bidirectional-text)
6. [Terminal capability discovery](#terminal-capability-discovery)
7. [Failure behavior](#failure-behavior)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Text and cell model

Text is logical Unicode plus structured style. Layout, clipping, cursor placement, hit testing, search, and selection must all use the same terminal-cell width function.

- **TERM-TXT-001 — Grapheme integrity.** Segment user-visible text into extended grapheme clusters before measuring or placing complex sequences. Never split a combining sequence, joined emoji, flag pair, or script cluster merely because the storage encoding uses multiple scalar values.
- **TERM-TXT-002 — Wide-cell representation.** A grapheme occupying two columns creates one head cell and one noncopying tail cell. Overwriting, clipping, selecting, or erasing either half repairs both halves.
- **TERM-TXT-003 — Logical content.** Search and copy retain logical grapheme order and characters. Screen styling, wide-cell tails, search overlays, and selection overlays are not copied.
- **TERM-TXT-004 — Line boundaries.** Newline starts column zero on a new logical line. Carriage return and other controls are interpreted only by a trusted parser contract; arbitrary message text cannot move the physical cursor.

## Display width

Use these compatibility rules when the platform does not provide a terminal-cell-width primitive with equivalent behavior:

1. Strip recognized ANSI control sequences before measuring.
2. Printable ASCII has width one; ASCII and C1 controls have width zero.
3. Treat East Asian ambiguous characters as narrow. Wide and full-width characters occupy two cells.
4. Treat combining marks, joiners, variation selectors, byte-order marks, format controls, tag characters, soft hyphen, and script-specific nonspacing marks as zero width.
5. For an ordinary nonemoji grapheme cluster, use the width of its first non-zero-width member unless the host terminal-width primitive reports the actual cluster allocation more accurately.
6. An emoji grapheme normally occupies two cells. A lone regional indicator occupies one; a paired flag occupies two. A digit, `#`, or `*` followed only by the emoji variation selector, without the enclosing-keycap mark, occupies one.
7. Return zero for nontext, empty, or all-control input.

- **TERM-TXT-005 — One width authority.** Measurement, wrapping, truncation, frame placement, declared cursor, selection, and hit testing call the same width contract.
- **TERM-TXT-006 — Host override parity.** A faster host primitive may replace the fallback only when it uses narrow ambiguous width and passes the same conformance corpus. Complex Indic clusters must match actual terminal cell allocation even when that differs from a simplistic first-base estimate.
- **TERM-TXT-007 — ANSI neutrality.** Styling and hyperlink sequences contribute zero columns.

## Wrapping, truncation, and tabs

Supported text overflow modes are hard wrapping without trimming, hard wrapping with boundary whitespace trimming, and truncation at the end, middle, or start. Legacy names for end and middle truncation may normalize to the same semantic modes.

- **TERM-TXT-008 — Hard wrap.** When a token exceeds the available width, split it at grapheme/cell boundaries; do not allow a single long path or identifier to exceed its container.
- **TERM-TXT-009 — ANSI-preserving wrap.** Preserve active structured style and hyperlink identity across a line break while ensuring each emitted line is independently closed/reset.
- **TERM-TXT-010 — Ellipsis.** Use one Unicode ellipsis `…`. Width zero yields empty text; width one yields only the ellipsis. End truncation keeps the left prefix, start truncation keeps the right suffix, and middle truncation assigns `floor(width/2)` cells to the left before inserting the ellipsis.
- **TERM-TXT-011 — Wide-boundary retry.** If a cell-range slice includes a wide grapheme that crosses the requested boundary, tighten the slice rather than returning a string wider than the allocation.
- **TERM-TXT-012 — Tab stops.** Expand tabs before layout at eight-column stops by default. Preserve ANSI sequences without changing the current column; reset the column after newline. A caller may supply another positive interval only through an explicit contract.

## Structured and ANSI styles

Structured text supports foreground and background colors, dim, bold, italic, underline, strikethrough, and inverse. Color inputs support the sixteen named ANSI colors, 256-color indices, 24-bit RGB triples, and validated hexadecimal values. Bold and dim are mutually exclusive at the public component boundary because many terminals encode both through the same intensity state.

Nested text inherits parent style. A child overrides only fields it explicitly supplies. Text wrapping belongs to the outer text/layout node so nested spans do not wrap independently.

External preformatted output may contain ANSI SGR and OSC-8 hyperlinks. Parse it into safe styled spans:

1. Feed the complete string to a control-sequence parser.
2. Track the current text style and hyperlink start/end state.
3. Convert text actions into grapheme-preserving spans.
4. Merge adjacent spans only when every style and hyperlink field matches.
5. Render links through the terminal hyperlink abstraction, not as executable control bytes embedded in ordinary text.
6. Ignore or neutralize unsupported cursor movement, erasure, title, clipboard, and device-control sequences in content strings.

- **TERM-TXT-013 — Raw-output boundary.** Only a deliberately raw terminal component may bypass sanitization, and its callers must already own terminal-state recovery. Ordinary model, tool, Markdown, or message text cannot emit terminal controls.
- **TERM-TXT-014 — Hyperlink closure.** Close an active hyperlink before changing runs, clearing cells, or ending a frame so later screen text cannot inherit it.
- **TERM-TXT-015 — Empty and nonstring content.** Empty ANSI content renders nothing. Nonstring input is converted to visible inert text rather than interpreted as controls.

## Bidirectional text

Some terminals implement Unicode bidirectional display natively; others place cells strictly left-to-right. Apply software bidirectional reordering only on known non-bidi terminal families, including native Windows consoles, Windows Terminal/WSL, and xterm-based integrated environments that require it.

- **TERM-TXT-016 — Capability-conditioned reorder.** Native-bidi terminals receive logical-order text unchanged. Cache the environment decision for the root lifetime.
- **TERM-TXT-017 — Fast LTR path.** Skip the bidirectional algorithm when no Hebrew, Arabic, Syriac, Thaana, or related right-to-left characters are present.
- **TERM-TXT-018 — Cluster-level reorder.** Determine embedding levels from the joined logical string, map levels back to grapheme clusters, and reverse level runs at cluster granularity. Move value, width, style, and hyperlink metadata together.
- **TERM-TXT-019 — Semantic preservation.** Reordering changes only visual cell placement. Copy, search, transcript storage, and model-visible text retain logical order.

## Terminal capability discovery

Terminal response bytes share stdin with keys. Capability probing therefore uses the input parser's typed response channel and must never leak replies into prompt text.

Supported query families include private-mode status, primary and secondary device attributes, extended-keyboard flags, private cursor position, foreground/background color, and terminal name/version.

- **TERM-CAP-001 — Query pairing.** A query consists of outbound bytes plus a predicate for exactly its expected typed response.
- **TERM-CAP-002 — Ordered sentinel barrier.** Append a universally answered primary-device-attributes query after a batch. Terminal responses are ordered: when the sentinel response arrives, resolve unmatched queries queued before that sentinel as unsupported. This avoids guessing with independent per-query timers.
- **TERM-CAP-003 — Concurrent batches.** Keep queries and sentinel markers in one ordered queue. A sentinel drains only entries through itself; later batches remain pending.
- **TERM-CAP-004 — First matching query.** On a typed response, resolve and remove the earliest pending matching query. An explicit device-attributes query may consume the first such response while the separately emitted sentinel consumes the next.
- **TERM-CAP-005 — Unsolicited response.** Drop an unmatched response. It is neither a key nor an application message.
- **TERM-CAP-006 — Cursor ambiguity avoidance.** Query cursor position with the private-marker form so the reply cannot be confused with modified function-key syntax.
- **TERM-CAP-007 — Caller completion duty.** A query has no autonomous timeout; every batch caller must emit its sentinel barrier or cancel/tear down the query owner. Teardown settles remaining callers as unavailable.

Capability results are independent. Absence of synchronized output does not disable hyperlinks; absence of extended keyboard reports does not disable ordinary key input.

## Failure behavior

- Invalid color or style input falls back to an inherited/default style and emits a diagnostic; it cannot create raw terminal bytes.
- Invalid or zero wrap width produces empty output rather than a negative slice or out-of-bounds cell.
- A missing grapheme or bidirectional library uses the documented conservative width/reorder fallback; if visual RTL parity is impossible, logical text and copy behavior remain correct.
- Malformed ANSI is rendered as inert text where safe or ignored as an incomplete control sequence; parser state must not bleed into the next message.
- Capability-query write failure marks that probe unavailable and invalidates assumptions that depended on it.

## Acceptance scenarios

- **TERM-TXT-A01 — Width corpus.** Measure ASCII, combining accents, CJK,
  joined emoji, a flag pair, a lone regional indicator, an incomplete keycap,
  controls and ANSI-styled text; verify documented cell widths.
- **TERM-TXT-A02 — Grapheme truncation.** Truncate wide text at start, middle
  and end for widths zero, one and several; verify allocation and boundaries.
- **TERM-TXT-A03 — Hyperlink wrap.** Wrap a long ANSI hyperlink; verify
  style/link continuity per line and closure before following text.
- **TERM-TXT-A04 — Tab stops.** Expand tabs around colored spans and newlines;
  verify eight-column stops and zero-width control sequences.
- **TERM-TXT-A05 — Bidirectional parity.** Render mixed Arabic and Latin on
  native- and software-bidi targets; verify visual parity and logical copy text.
- **TERM-CAP-A01 — Batch correlation.** Send two concurrent capability batches;
  verify the first sentinel cannot settle queries from the second.
- **TERM-CAP-A02 — Explicit response plus barrier.** Query primary device
  attributes in a sentinel-terminated batch; verify the explicit reply and then
  the barrier settle their respective promises.
- **TERM-CAP-A03 — Malformed reply isolation.** Inject an unsolicited or
  malformed reply between typed keys; verify it never enters the editor and
  later keys still parse.

## Non-normative provenance

Evidence was specified from the reference string-width, wrapping, ANSI-to-span, structured-text, bidirectional-reordering, tab-stop, terminal-query, input-parser, and frame-cell subsystems. Names, source layout, libraries, and optimization choices are provenance only.
