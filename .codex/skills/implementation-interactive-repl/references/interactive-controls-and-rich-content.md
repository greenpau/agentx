# Interactive Controls and Rich Content

## Contents

1. [Shared control boundary](#shared-control-boundary)
2. [Selection controls](#selection-controls)
3. [Fuzzy pickers and workspace navigation](#fuzzy-pickers-and-workspace-navigation)
4. [Tabs and wizards](#tabs-and-wizards)
5. [Themes and responsive layout](#themes-and-responsive-layout)
6. [Markdown, code, and tables](#markdown-code-and-tables)
7. [Diff presentation](#diff-presentation)
8. [Failure behavior](#failure-behavior)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Non-normative provenance](#non-normative-provenance)

## Shared control boundary

Reusable controls translate terminal events into transient view state and exactly-once callbacks. They do not alter the transcript, settings, files, permissions, or tasks directly. A domain dialog supplies typed options and commits the returned decision through its owning service.

- **REPL-CTL-001 — Focus versus value.** Track the focused item separately from the committed value. Navigation may change focus and preview without invoking the final selection callback.
- **REPL-CTL-002 — Stable identity.** Identify an option by its typed value or explicit key, never by a label or current screen position.
- **REPL-CTL-003 — Overlay ownership.** Every interactive control registers its overlay/focus ownership while mounted. Escape settles only its own cancellation path; a global interrupt handler cannot also consume that Escape.
- **REPL-CTL-004 — Disabled semantics.** A disabled control ignores input. A disabled option may be displayed but is skipped for focus, numeric selection, toggle, and commit.
- **REPL-CTL-005 — Bounded viewport.** Keep the focused row visible while exposing whether entries exist above or below. Recompute against terminal height without losing semantic focus.
- **REPL-CTL-006 — Declared cursor.** An inline text field declares the native cursor position after layout so IME and accessibility follow the visible caret.

## Selection controls

An option contains a stable value, renderable label, optional description, disabled state, and either ordinary-selection or inline-input behavior. An input option may additionally define initial value, placeholder, empty-submit policy, a change callback, label/value separator, cursor-reset policy, editor handoff, and paste/image callbacks.

The default visible option count is five. Supported layouts are compact, expanded, and compact-with-description-below. Descriptions may instead appear inline.

- **REPL-CTL-007 — Navigation.** Up/Down moves among enabled options; Page Up/Down moves by a viewport. Navigation wraps at ends unless the owner supplies an explicit boundary callback, in which case invoke it instead.
- **REPL-CTL-008 — Reset on option replacement.** When a semantically changed option set arrives, rebuild option links, restore a valid initial/default focus, clamp the viewport, and reset selections whose prior values no longer exist. Referential churn with deep-equal content must not erase interaction state.
- **REPL-CTL-009 — Numeric shortcuts.** Visible 1–9 indices may directly choose or toggle corresponding enabled options. Normalize full-width digits. When indices are hidden, numeric input cannot activate an invisible mapping.
- **REPL-CTL-010 — Single select.** Enter commits the focused ordinary option unless selection is globally disabled. Escape cancels. A focus callback is one-way notification and must not be fed back as controlled focus without loop protection.
- **REPL-CTL-011 — Input option.** Enter on a nonempty input commits it. The declared empty-submit policy determines whether empty input commits, cancels, or first enters edit mode. Tab may toggle edit mode; an explicit editor shortcut may hand current text to an external editor and accept the returned value.
- **REPL-CTL-012 — Paste state.** Inline input owns its text, cursor, pasted-content map, selected image, and removal callbacks. Switching option focus must not attach a paste to the wrong option.
- **REPL-CTL-013 — Multi-select.** Without a separate submit row, Space toggles and Enter submits. With a submit row, Enter toggles an option and submits only while that row is focused. Input options are selected while their value is nonempty and deselected when cleared.
- **REPL-CTL-014 — Selection callback timing.** Notify live multi-select changes after each toggle, but call final submit once with a stable ordered snapshot. Cancellation never masquerades as an empty selection.

## Fuzzy pickers and workspace navigation

A fuzzy picker owns query text, ranked results, focused result, visible window, optional preview, action list, empty/loading message, and cancellation. The general picker defaults to eight visible results, reserves ten rows for surrounding chrome, and never shrinks below two visible results.

- **REPL-PICK-001 — Query generation.** Tag each asynchronous search with a generation or abort token. Results and preview reads apply only if still current and mounted.
- **REPL-PICK-002 — Deterministic ranking.** Rank using normalized searchable text, preserve stable source order for equal scores, and derive highlights without modifying labels.
- **REPL-PICK-003 — Action routing.** Enter performs the primary action on the focused result. Additional configured shortcuts perform named alternate actions against the same focused snapshot.
- **REPL-PICK-004 — Preview failure.** A failed preview displays an unavailable placeholder while keeping selection and actions usable.

Quick-open behavior:

- Show at most eight results and read at most twenty preview lines when the preview is below.
- Move preview to the right at 120 or more terminal columns; otherwise place it below.
- Empty query yields no file suggestions.
- Selecting may open the file externally; alternate actions insert either `@path ` or `path ` into the prompt.

Workspace text-search behavior:

- Debounce search by 100 ms and cancel the preceding search and preview read.
- Retain at most ten matches per file and 500 total matches, visibly marking truncation.
- Show at most twelve results, further clamped to at least four and the available terminal rows minus fourteen.
- Read four lines of context on each side of the focused line.
- Move preview to the right at 140 or more columns; otherwise place it below.
- Alternate insertion forms are `@path#Lline ` and `path:line `.

Search and quick-open resolve paths inside the active workspace contract. A preview or external-editor action must not expand a relative path outside that boundary without the same permission checks as any other file access.

## Tabs and wizards

Tabs have stable identifiers, labels, content, optional banner, controlled or uncontrolled selection, and a separately tracked header-focus state.

- **REPL-CTL-015 — Tab navigation.** Left/Right changes the focused/selected enabled tab when header navigation is active. Content-owned arrow handlers may explicitly disable header navigation.
- **REPL-CTL-016 — Modal scroll handoff.** When tab content lives in a modal scroll viewport, page/scroll commands target that viewport without changing tabs. Changing tabs resets or restores scroll according to the dialog contract.
- **REPL-CTL-017 — Responsive header.** Measure labels by terminal cells. Clip or scroll the header rather than wrapping identifiers into ambiguous rows.

A wizard owns an ordered step list, current index, accumulated typed data, optional title, step-counter visibility, non-linear navigation history, and completion/cancellation callbacks.

- **REPL-WIZ-001 — Data merge.** A partial step update shallow-merges into accumulated wizard data unless that field's schema defines a nested merge.
- **REPL-WIZ-002 — Next and completion.** Next advances one step. Next from the final step marks completion, unmounts step content, clears navigation history, and invokes completion once with the final data after state publication.
- **REPL-WIZ-003 — Back history.** After a non-linear jump, Back pops the actual prior index. Otherwise it decrements. Back from the first step invokes cancellation.
- **REPL-WIZ-004 — Valid jump.** Ignore a jump outside `[0, step_count)`. A valid jump records the current step before moving.
- **REPL-WIZ-005 — Explicit cancel.** Cancel clears navigation history and invokes cancellation without completion. Ctrl-C/Ctrl-D follow the enclosing dialog's graceful-exit contract.

## Themes and responsive layout

The presentation layer resolves semantic theme roles into raw terminal colors. Domain components request roles such as primary text, muted text, warning, success, error, suggestion, diff-added, or diff-removed; they do not hard-code transport or transcript semantics into colors.

- **REPL-VIS-001 — Theme switching.** A committed theme is persistent settings state. A preview theme is transient and reverts on cancel.
- **REPL-VIS-002 — Color fallback.** Unsupported color depth maps to the nearest legible palette while retaining emphasis and text labels; color is never the sole status signal.
- **REPL-VIS-003 — Width safety.** Derive dialog, pane, divider, progress, label, path, and preview widths in terminal cells, with a positive minimum. Truncate decorative content before action labels or safety decisions.
- **REPL-VIS-004 — Narrow adaptation.** At narrow widths, stack panes, reduce visible lists, hide secondary descriptions, or use scrollable detail views. Preserve the same selected item and callback semantics.

## Markdown, code, and tables

Before Markdown parsing, remove prompt-only internal markup according to the message-normalization contract. Plain text with no Markdown marker in the first 500 characters takes a fast single-paragraph path. Parsed token lists are cached by a content hash in a 500-entry recency cache so virtual-list remounting does not repeatedly lex immutable messages.

- **REPL-RICH-001 — Safe formatting.** Markdown changes presentation only. Raw HTML and embedded terminal controls are escaped or rendered inert; links use the hyperlink abstraction.
- **REPL-RICH-002 — Syntax highlighting fallback.** Code fences may load a highlighter lazily. While unavailable, disabled, unsupported, or failed, render complete plain code with fence/language context; never hide content.
- **REPL-RICH-003 — Streaming boundary.** During streaming, preserve a monotonically growing prefix ending before the final nonspace top-level block and reparse only the unstable suffix. If replacement content no longer begins with the retained prefix, reset and parse the replacement. An unclosed code fence remains one unstable block.
- **REPL-RICH-004 — Immutable cache key.** Hash the full normalized content rather than retaining the whole string as the cache key. Promote hits and evict the oldest entry at capacity.

Markdown tables:

1. Reserve border and padding overhead plus a four-column safety margin.
2. Give every column at least three cells.
3. Compute each column's minimum from its longest word and ideal from its longest unwrapped cell.
4. If ideal widths fit, use them. If minima fit, distribute remaining cells in proportion to each column's overflow. Otherwise scale minima and allow hard word breaks.
5. Preserve cell ANSI styles while wrapping; center headers and honor each data-column alignment.
6. Vertically center multiline cell content within its row.
7. If any cell would exceed four wrapped lines, switch to the accessible vertical record layout.

## Diff presentation

A diff view accepts structured hunks plus file path, old/new line ranges, optional source context, theme, width, dim state, and highlighting policy. It supports a changed-file list and per-file detail view.

- **REPL-DIFF-001 — Semantic markers.** Always expose added/removed markers and counts as text, not color alone. Explicitly label untracked, binary, large, and truncated files.
- **REPL-DIFF-002 — File viewport.** Show at most five changed files at once, keep the selected file near the center, and display counts above/below. Paths truncate from the start or middle without hiding the distinguishing suffix.
- **REPL-DIFF-003 — Gutter selection.** Mark diff markers and line-number gutters nonselectable in fullscreen so copied content contains source lines. If the gutter would consume the full width, render one safe column instead.
- **REPL-DIFF-004 — Gutter width.** Allocate one marker cell, two padding cells, and the decimal width of the largest old/new line number.
- **REPL-DIFF-005 — Highlight fallback.** If native/optimized coloring is absent, disabled, errors, or returns no result, render the structured fallback with equivalent lines and markers.
- **REPL-DIFF-006 — Render cache.** Cache an immutable hunk by theme, positive integer width, dim state, gutter split, first-line discriminator, and file path. Retain at most four variants per hunk; clear stale variants during repeated resize.
- **REPL-DIFF-007 — View transitions.** List-to-detail and previous/next-file navigation preserve selected file identity. Closing detail returns to the list; closing list settles the owning dialog.

## Failure behavior

- Missing, duplicate, or no-longer-valid option values trigger deterministic focus repair and a diagnostic; they never select an arbitrary row.
- Asynchronous results arriving after cancel/unmount are ignored.
- An unavailable external editor leaves the dialog open or reports a local error according to the action contract; it does not claim success.
- Theme, Markdown, highlighter, preview, or optimized-diff failures use legible local fallbacks and cannot abort the semantic session.
- A zero-row or zero-column terminal renders the smallest safe control shell and defers rich content.

## Acceptance scenarios

**REPL-CTL-A01 — Viewport navigation.** Navigate a five-row viewport through disabled options and both ends; verify focus, wrap/boundary callbacks, and visible window.

**REPL-CTL-A02 — Independent inline inputs.** Type and paste into two input options, switch focus, and return; verify independent values, paste maps, and native caret.

**REPL-CTL-A03 — Multi-select settlement.** Exercise a multi-select with and without a submit row; verify Space/Enter semantics and exactly-once final submit.

**REPL-CTL-A04 — Option replacement.** Replace an option list asynchronously; verify a removed selection is repaired while a deep-equal replacement retains state.

**REPL-CTL-A05 — Stale fuzzy results.** Resolve fuzzy-search requests out of order; verify only the newest results and preview appear.

**REPL-CTL-A06 — Nonlinear wizard back.** Jump from wizard step one to four, then Back; verify return to one and preserved data before final completion.

**REPL-CTL-A07 — Streaming Markdown fence.** Stream Markdown containing an unfinished fence and later close it; verify stable blocks never reparse visibly and final output equals one-shot rendering.

**REPL-CTL-A08 — Responsive table.** Render a table at wide, constrained, and extremely narrow widths; verify the allocation algorithm and vertical fallback.

**REPL-CTL-A09 — Diff resize cache.** Resize a highlighted diff through five widths; verify correct content, a bounded per-hunk cache, nonselectable gutter, and fallback parity.

**REPL-CTL-A10 — Colorless fallback.** Disable color and highlighting; verify every status, code block, and diff remains understandable.

## Non-normative provenance

Evidence was specified from the reference select and multiselect controls, navigation reducers, fuzzy picker, tab and wizard primitives, theme wrappers, quick-open and workspace-search dialogs, Markdown/token/table renderers, code highlighting, structured diff and diff-navigation views. Component names, source layout, libraries, and rendering framework are non-normative.
