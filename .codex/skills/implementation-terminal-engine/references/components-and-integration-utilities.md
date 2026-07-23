# Terminal Components and Integration Utilities

## Contents

1. [Component vocabulary and ownership](#component-vocabulary-and-ownership)
2. [Interactive primitives](#interactive-primitives)
3. [Alternate-screen lifecycle](#alternate-screen-lifecycle)
4. [Imperative scrolling](#imperative-scrolling)
5. [Shared animation clock](#shared-animation-clock)
6. [Stable presentation vocabulary](#stable-presentation-vocabulary)
7. [Color and clear compatibility](#color-and-clear-compatibility)
8. [ANSI image export](#ansi-image-export)
9. [Terminal recording](#terminal-recording)
10. [Preference backup and recovery](#preference-backup-and-recovery)
11. [Lazy and native adapters](#lazy-and-native-adapters)
12. [Acceptance scenarios](#acceptance-scenarios)
13. [Non-normative provenance](#non-normative-provenance)

## Component vocabulary and ownership

The retained terminal tree has two semantic node families. A box owns geometry,
layout, clipping, focusability and pointer/key handlers. A text node owns an
inline run and its text styles. Raw text is legal only within a text context;
layout nodes cannot silently reinterpret it as a box. Host-node creation,
mutation and removal must mark the affected layout ancestry dirty and schedule
one root render. Presentation components are thin declarations over these host
contracts and do not own the semantic conversation.

- **TERM-WID-001 — Box boundary.** A box accepts flex, dimension, position,
  spacing, border, overflow and event properties, forwards an optional host
  reference, and may participate in tab order. Its default presentation is
  unstyled; focus or interaction never implies a visual style.
- **TERM-WID-002 — Text boundary.** A text node defaults to wrapping with
  zero growth, shrink enabled and row direction. It accepts wrap, trim and
  start/middle/end truncation modes. Null or absent children produce no host
  node. Bold and dim are mutually exclusive presentation weights.
- **TERM-WID-003 — Inline integrity.** Nested text and hyperlink runs inherit
  their containing text context. A raw scalar outside that context is a
  construction error rather than an implicitly positioned node. Text style
  mutation invalidates measurement as well as paint.
- **TERM-WID-004 — Dirty ownership.** Attribute, child-order, text, style,
  focusability or geometry changes dirty the smallest affected retained
  subtree and propagate layout invalidation to the owning root. Several
  mutations in one commit schedule one paint; they do not emit partial frames.
- **TERM-WID-018 — Fixed raw-ANSI leaf.** Prewrapped terminal-ready ANSI may
  bypass style-span implementation as one measured leaf whose width is the
  producer-declared column width and whose height is the line count. Empty line
  arrays render nothing. The producer, not this leaf, owns wrapping and must
  guarantee each array element is exactly one terminal row.
- **TERM-WID-019 — Structural convenience nodes.** Newline emits `count`
  literal newline characters inside a text context, defaulting to one. Spacer
  is an empty box with major-axis growth one. A no-select box marks its cells
  outside fullscreen copy/highlight; its extended form excludes from column
  zero through the box's right edge on every occupied row. These are
  declarations over host behavior, not separate layout algorithms.
- **TERM-WID-020 — Error overview degradation.** A render failure overview
  always shows the error message. When a parseable first stack frame names a
  readable source file, it may synchronously show a bounded source excerpt and
  highlight the origin line; unreadable source omits only the excerpt. Parsed
  frames show function and cwd-relative file/line/column, while unparsable
  frames remain visible verbatim. Source lookup failure cannot hide the error.

## Interactive primitives

A button is an unstyled focusable box. Its default tab index is `0`; `-1`
allows programmatic focus without tab traversal. Enter, Return-equivalent, or
Space prevents the button's default key behavior, invokes the action once and
sets a transient active state for 100 ms. Click invokes the same action. Focus,
blur and pointer enter/leave update the render-prop state
`{focused, hovered, active}`. Unmount clears the active timer.

A link chooses its visible content in this order: explicit children, otherwise
the URL. If OSC-8 hyperlinks are supported, it emits a hyperlink run inside a
text node. Otherwise it renders the explicit fallback, or the ordinary visible
content if no fallback exists. Capability absence never hides the label.

- **TERM-WID-005 — Button activation.** Keyboard and pointer activation share
  the callback but one physical event invokes it once. Only activation keys
  consume their default; unrelated keys continue through normal propagation.
- **TERM-WID-006 — Button lifecycle.** Focus, hover and active are transient
  view state. A pending 100 ms active reset cannot update an unmounted button.
- **TERM-WID-007 — Hyperlink degradation.** OSC-8 support selects the encoded
  link representation. Unsupported terminals receive plain fallback text with
  identical layout ownership and no raw escape text.

## Alternate-screen lifecycle

An alternate-screen component performs terminal-mode mutation during the
commit's insertion phase, before the first frame for its children. On mount it
writes, in order: enter DEC private mode 1049, erase the screen, home the
cursor, and—when enabled—turn on button-event, any-event/drag and SGR mouse
reporting. It informs the owning renderer that alternate-screen constraints and
optional mouse tracking are active.

The component is a nonshrinking column box exactly as tall as the current
terminal row count and 100 percent wide. If size is unavailable, its height is
24 rows. Its contents must implement their own scrolling because the alternate
buffer has no main-buffer scrollback.

On unmount it first marks alternate mode inactive, clears the renderer's text
selection, disables the mouse modes it enabled, and exits private mode 1049.
Process/signal cleanup also knows the renderer's alternate-mode state so it can
restore the main buffer if component cleanup is skipped.

- **TERM-WID-008 — Prepaint entry.** Alternate-buffer entry precedes the
  first child paint. Entering after a main-buffer paint would preserve a broken
  intermediate frame and is nonconforming.
- **TERM-WID-009 — Symmetric mode cleanup.** Only enabled mouse modes are
  disabled, selection is discarded, and the main screen is restored on normal
  unmount and emergency cleanup.
- **TERM-WID-010 — Viewport containment.** The alternate root is constrained
  to terminal rows and the renderer keeps cursor restoration inside that
  viewport so a restore newline cannot scroll content.

## Imperative scrolling

A scroll box is a clipped, constrained viewport whose children retain their
full layout height. Paint translates content by negative `scrollTop`, culls
children outside the visible range and clips cells at viewport bounds. Its
imperative operations mutate retained-node scroll fields directly rather than
requiring a component-tree render for each wheel event.

The public operations have these effects:

- `scrollTo(y)` clears sticky-bottom state, pending delta and element anchor;
  stores `max(0, floor(y))`; marks the node dirty; and schedules paint.
- `scrollBy(dy)` clears sticky and anchor state and accumulates
  `floor(dy)` into a pending delta. Opposite directions cancel arithmetically.
  The renderer drains this delta in bounded increments instead of applying an
  unbounded burst in one frame.
- `scrollToElement(element, offset)` stores a one-shot anchor. The renderer
  resolves its computed top in the same layout pass that determines content
  height, avoiding a stale pre-layout coordinate.
- `scrollToBottom()` clears pending/anchor state, marks sticky-bottom true and
  requests a component render because stickiness is an observed attribute.
- clamp bounds describe the currently mounted virtual range. During async
  virtualization, the renderer constrains scroll position to that range so a
  burst cannot reveal a blank spacer.

Imperative mutations notify local subscribers, mark global scroll activity,
mark commit start and coalesce paints in a microtask. Renderer-originated
sticky-follow adjustments do not invoke these imperative subscribers. Cached
scroll height may be one paint stale; a separate fresh-height query reads the
layout engine directly after a commit.

- **TERM-WID-011 — Manual override.** Any explicit `scrollTo` or `scrollBy`
  breaks sticky-bottom behavior and cancels a pending element anchor.
- **TERM-WID-012 — Burst coalescing.** Multiple scroll mutations in one input
  batch produce one scheduled paint while retaining the sum of their integer
  deltas.
- **TERM-WID-013 — Same-pass anchoring.** Element coordinates and scroll
  height used for an anchor come from the same completed layout generation.
- **TERM-WID-014 — Virtual continuity.** Paint mounts the union needed by the
  committed and pending positions and respects mounted-range clamps; draining
  never exposes an unmounted blank interval.

## Shared animation clock

One root clock consolidates animation wakeups. Subscribers declare whether
they keep it alive. If at least one keep-alive subscriber exists, exactly one
interval runs; removing the last one stops it. All callbacks in one tick see
the same elapsed-time snapshot. When paused, `now()` derives current elapsed
wall time instead of returning the last tick's stale snapshot. The epoch begins
on the first `now()` or first running subscription and remains stable.

Changing the tick interval restarts the one interval without resetting the
epoch. Terminal focus uses the normal frame interval; terminal blur doubles
that interval. A passive animation subscriber can observe the shared ticks but
does not keep the process alive. Interval helpers retain the latest callback,
fire only when their requested duration has elapsed, and unsubscribe when
paused or unmounted.

- **TERM-WID-015 — One wakeup source.** Any number of animations share one
  interval, and passive animations alone leave no live timer.
- **TERM-WID-016 — Tick coherence.** Every subscriber invoked by one clock
  tick observes one identical elapsed timestamp.
- **TERM-WID-017 — Focus throttling.** Blur reduces clock frequency to half
  the focused rate without resetting animation phase; focus restores it.

## Stable presentation vocabulary

Fixed glyphs, fallback text and spinner configuration are protocol-like UI
vocabulary. Their values may be localized or restyled in another build only if
golden presentation tests and every consumer are changed together.

- **TERM-PRES-001 — Semantic glyph catalog.** Status indicators distinguish
  success, error, warning, pending, play, pause, effort, bridge and progress.
  Platform substitutions are selected centrally so a consumer never guesses
  whether a glyph occupies one or two cells.
- **TERM-PRES-002 — Spinner resolution.** Missing configuration uses the
  built-in spinner/verb set. Replace mode adopts a nonempty configured set and
  falls back to built-ins for an empty set. Append mode concatenates configured
  entries after built-ins in stable order. Invalid entries do not erase the
  viable fallback.
- **TERM-PRES-003 — Empty-content fallback.** A deliberately stable fallback
  is displayed when a presentation expects model content but receives none.
  Empty content remains distinct from an in-progress stream and from an error.
- **TERM-PRES-018 — Feature-client profile key.** The presentation feature
  client selects one public key for the external build and production or
  development internal keys for the internal build. Internal development is a
  lazily read truthy environment choice so settings applied after module load
  are honored; build profile itself is fixed. An implementation must keep the
  key values in protected configuration while preserving this three-way
  selection and lazy-read timing.
- **TERM-PRES-019 — Title and tab-status controls.** A nonnull terminal title
  is stripped of ANSI first, then assigned through the Windows process-title
  boundary or OSC 0 elsewhere. Null leaves the existing title untouched. Tab
  status uses capability-gated OSC 21337 with fixed idle (green, “Idle”), busy
  (orange, “Working…”), and waiting (blue, “Waiting”) presets, wrapped for a
  multiplexer. Transitioning from a set status to null sends clear; null from
  an already-null state emits nothing.
- **TERM-PRES-020 — Notifications and progress.** Notification adapters emit
  the target terminal's OSC shape for iTerm2, Kitty or Ghostty and wrap it for
  multiplexers. Kitty uses one numeric identity across title, body and focus
  parts. Bell is raw BEL, deliberately unwrapped so a multiplexer can flag its
  window. Progress is capability-gated, clamps and rounds percentage to 0–100,
  maps running/error/indeterminate to their OSC 9;4 states, and clears on null
  or completed. Calling the adapter outside its raw-write provider is a bounded
  programming error.

## Color and clear compatibility

Color strings accept the named 16-color palette, hexadecimal colors,
`ansi256(n)` and `rgb(r,g,b)`. A malformed or unknown color leaves text
unchanged. Structured styles nest background outside foreground outside text
modifiers so reset sequences preserve the intended combination. Theme lookup
happens before this primitive boundary.

At module initialization, an xterm.js terminal identified as VS Code is raised
from 256-color level to truecolor only when the detected level is exactly 2.
Levels 0/1 remain unchanged so `NO_COLOR` and equivalent explicit choices are
honored. This boost runs before the tmux rule. When inside tmux, a color level
above 2 is clamped to 2 unless the explicit truecolor-through-tmux escape hatch
is present. The resulting level is stable for the process.

Clearing a POSIX or modern Windows terminal writes erase-screen, erase-
scrollback and cursor-home. Modern Windows includes Windows Terminal, a
versioned VS Code ConPTY terminal, and mintty identified either directly or by
its MSYS environment. Legacy Windows cannot reliably erase scrollback and uses
erase-screen plus the legacy horizontal/vertical home command.

- **TERM-PRES-004 — Explicit color precedence.** A user request for no color
  wins over terminal inference; tmux transport limits win over an inner
  xterm.js truecolor boost unless explicitly overridden.
- **TERM-PRES-005 — Style nesting.** Modifiers are applied first, foreground
  second and background last, yielding background as the outer ANSI wrapper.
- **TERM-PRES-006 — Safe clear.** The clear sequence never sends an
  unsupported scrollback erase to the legacy Windows console.

## ANSI image export

The ANSI parser splits input on newline and recognizes SGR reset, bold,
foreground colors 30–37 and 90–97, foreground reset 39, indexed color
`38;5;n`, and truecolor `38;2;r;g;b`. Unsupported controls are consumed without
inventing styles. Style state resets at each parsed source line. Empty lines
remain represented; trailing lines whose spans contain only whitespace are
trimmed by the image encoders.

Indexed colors use the 16-color table for 0–15, the 6×6×6 cube for 16–231
(`0` or `55 + component×40`), and grayscale `8 + (index−232)×10` for 232–255.
Default foreground is `(229,229,229)` and default background `(30,30,30)`.

PNG export is deterministic and dependency-free at runtime:

- bundled regular bitmap glyphs occupy 24×48 pixels per terminal cell;
- options default to integer nearest-neighbor scale 1, horizontal/vertical
  padding 48, corner radius 16 and default background;
- dimensions are `(max(1, displayColumns)×24 + 2×paddingX)×scale` by
  `(rows×48 + 2×paddingY)×scale`;
- display width, not code-unit count, advances the cell column; zero-width
  characters do not advance; unknown glyphs use the bundled fallback;
- `░`, `▒`, `▓`, and `█` fill one cell by blending foreground over background
  at alpha 0.25, 0.5, 0.75 and 1;
- output is an 8-bit RGBA PNG. Rounded-corner pixels outside the radius become
  transparent.

SVG export defaults to the font list `Menlo, Monaco, monospace`, font size 14,
line height 22, padding 24 per side, background `(30,30,30)` and radius 8. Its
width estimate is `ceil(maxCodeUnits × fontSize × 0.6 + 2×paddingX)` and height
is `lineCount × lineHeight + 2×paddingY`. XML content and attributes are
escaped; spans preserve spaces and bold/color styling.

- **TERM-PRES-007 — ANSI parser scope.** Image export implements the stated
  foreground/bold subset exactly; it does not claim terminal-emulator cursor,
  erase, background-color or cross-line style semantics.
- **TERM-PRES-008 — Cell-accurate PNG.** Raster placement uses terminal display
  columns and fixed 24×48 cells, including wide and zero-width input behavior.
- **TERM-PRES-009 — Portable encoding.** PNG bytes do not depend on a system
  font, browser, SVG renderer or platform-specific rasterizer.

## Terminal recording

Terminal recording exists only in the designated internal build profile and
when its explicit environment opt-in equals the supported value. It installs
before the retained renderer mounts. The file lives under the product's
project/session recording directory, uses a timestamp and `.cast` suffix, and
starts with one asciicast-v2 JSON header containing initial columns, rows,
Unix-second timestamp, shell and terminal name. Creation mode is owner-only
`0600`.

The recorder wraps standard-output writes at the same boundary as the
structured-output guard. Each write becomes `[elapsedSeconds,"o",utf8Text]`
and is still passed to the original output with the original callback and
return semantics. Resize becomes `[elapsedSeconds,"r","COLSxROWS"]`. A
buffer flushes every 500 ms, after 50 entries, or before exceeding 10 MiB.
Writes serialize through one promise and recording failures never break the
terminal session.

When a provisional session resumes another identity, flush before renaming and
then update the mutable destination so subsequent writes follow the new name.
Cleanup disposes/flushed buffered output, waits for pending append, unregisters
resize and restores the exact original standard-output function.

- **TERM-PRES-010 — Recording transparency.** Recording preserves stdout
  ordering, callback behavior and return value; a recorder failure is local.
- **TERM-PRES-011 — Rename boundary.** Session rename occurs only after prior
  buffered output is durable, and future appends target the renamed path.
- **TERM-PRES-012 — Recorder cleanup.** Shutdown leaves no output wrapper,
  resize listener, timer or pending append owned by the recorder.

## Preference backup and recovery

Apple Terminal setup first asks the platform defaults service to export the
live preference domain to its normal property-list path. Nonzero exit or a
missing resulting file aborts backup. It then exports a `.bak` copy and records
both `setupInProgress=true` and the backup path. A backup result is not success
unless this recovery marker is durable.

Startup recovery does nothing when no marker exists. A marker with no path or
a missing backup is cleared and reported as no backup. A valid backup is
imported through the defaults service. Success best-effort restarts the
preferences daemon, clears the marker and reports restored. A nonzero import
reports failure with the path and intentionally retains the marker for a later
retry/manual recovery. An exception is logged, clears the marker, and reports
failure because repeated automatic execution is not safe after an unknown
exception.

iTerm recovery uses the same marker/path/missing-file states but copies the
backup file over the live preference file. Success and thrown failure both
clear its marker; failure includes the backup path. Recovery checks run only in
the compatible interactive startup profile. iTerm recovery is additionally
limited to the profile that may have modified those settings.

- **TERM-PRES-013 — Recovery marker.** A setting mutation that needs rollback
  establishes its marker and exact backup path before setup is considered in
  progress, then clears it only according to the state table above.
- **TERM-PRES-014 — Retry-preserving import failure.** A known nonzero Apple
  import retains recovery evidence; missing evidence is cleared as no-op.
- **TERM-PRES-015 — Profile-scoped recovery.** Unsupported platforms and
  noninteractive modes neither inspect nor mutate desktop-terminal settings.

## Lazy and native adapters

Syntax highlighting is represented by one shared lazy-load promise. Load
failure resolves to unavailable rather than rejecting every consumer. File
language naming waits for that same load, extracts the final extension without
its dot and returns the registered language name or `unknown`; callers treat
the result as telemetry/presentation metadata rather than a correctness gate.

Physical modifier probing is macOS-only. Prewarming loads the native adapter at
most once and swallows failure. A synchronous modifier query on another
platform returns false. A macOS query dynamically loads the adapter and returns
its result; callers use it only where escape protocols cannot expose the held
modifier, never as a general permission or keybinding source.

- **TERM-PRES-016 — Lazy highlighter.** Every caller shares one load result;
  unavailable highlighting leaves uncolored content and `unknown` metadata.
- **TERM-PRES-017 — Native modifier scope.** Native probing augments ambiguous
  Apple Terminal input only and is a deterministic false on other platforms.

## Acceptance scenarios

- **TERM-WID-A01 — Component nesting.** Render styled nested text in a box,
  mutate its text and wrap mode, and verify one remeasure/paint. Attempt raw
  text directly under the layout root and verify a bounded construction error.
- **TERM-WID-A02 — Button parity.** Focus a default button; activate it with
  Enter, Space and click; verify one callback per event, the 100 ms active
  state, unrelated-key propagation and timer cleanup on unmount.
- **TERM-WID-A03 — Alternate symmetry.** Mount an alternate screen with mouse
  tracking, verify entry/clear/home/mouse ordering before its first frame, then
  unmount and verify selection clear, mouse disable and main-buffer restore.
- **TERM-WID-A04 — Scroll burst.** Apply fractional positive and negative
  deltas in one input batch; verify flooring, arithmetic cancellation, one
  scheduled paint, bounded drain and no blank virtual interval.
- **TERM-WID-A05 — Same-pass anchor.** Grow content and request an element
  anchor in the same commit; verify the resolved top and content height come
  from that layout generation and manual wheel input cancels the anchor.
- **TERM-WID-A06 — Clock ownership.** Subscribe two passive animations and
  verify no interval. Add one keep-alive subscriber, verify one shared interval
  and identical tick times, blur/focus to double/restore cadence, then remove it
  and verify no live timer.
- **TERM-WID-A07 — Fixed ANSI leaf.** Supply two already wrapped ANSI lines at
  declared width 40; verify one 40×2 measured leaf and byte-preserving output.
  Supply no lines and verify no node.
- **TERM-WID-A08 — Selection convenience.** Render newline, spacer and ordinary
  and from-left-edge no-select boxes; verify ordinary layout semantics and that
  copied fullscreen text omits exactly the marked cells/leading gutter.
- **TERM-WID-A09 — Error degradation.** Render errors with readable,
  unreadable and unparsable stack origins; verify the message always appears,
  optional excerpt is bounded, origin line is highlighted and raw frames remain.
- **TERM-PRES-A01 — Color precedence.** Exercise VS Code xterm.js at level 2,
  explicit no-color, tmux with and without the truecolor escape hatch, and
  verify the boost/clamp order and stable final level.
- **TERM-PRES-A02 — Clear profiles.** Compare POSIX, Windows Terminal,
  versioned VS Code on Windows, mintty and legacy Windows sequences; only the
  legacy profile omits scrollback erase and uses legacy cursor home.
- **TERM-PRES-A03 — Image cells.** Export ANSI text containing indexed color,
  truecolor, bold, a wide grapheme, a combining mark and all four shade blocks;
  verify parsed spans, dimensions, alpha blends and portable PNG bytes.
- **TERM-PRES-A04 — Recorder resume.** Record output and resize, cross all
  three flush thresholds, resume another session identity, then clean up;
  verify v2 ordering, `0600`, no lost pre-rename event, restored stdout and no
  live listener/timer.
- **TERM-PRES-A05 — Apple recovery table.** Test absent marker, absent path,
  missing file, successful import, nonzero import and thrown import. Verify
  result, marker retention/clearing and exact manual-recovery path.
- **TERM-PRES-A06 — Optional adapters.** Fail the lazy highlighter import and
  macOS modifier prewarm; verify ordinary text/input continue, one cached
  unavailable result, `unknown` language and false modifier on non-macOS.
- **TERM-PRES-A07 — Feature key timing.** Select external, internal production
  and internal development profiles, applying the development environment
  value after module load; verify one correct key and the lazy environment read.
- **TERM-PRES-A08 — Terminal metadata.** Set ANSI-styled title on Windows and
  POSIX, transition tab status idle→busy→null, and run without capability;
  verify stripping, platform routing, multiplexer wrapping, one clear and safe
  no-op degradation.
- **TERM-PRES-A09 — Notification protocols.** Emit each notification, raw bell,
  running/error/indeterminate/completed progress and out-of-range percentages;
  verify target framing, Kitty correlation, raw BEL, clamping and clear states.
- **TERM-PRES-A10 — Stable vocabulary.** Resolve glyphs on macOS and another
  platform, then spinner configuration in missing, replace-empty,
  replace-nonempty and append modes; verify cell-safe platform substitution,
  fallback preservation, stable concatenation, completion verbs and the exact
  empty-content fallback.

## Non-normative provenance

Evidence was specified from retained host components and clock/scroll hooks,
terminal constants and color/clear helpers, ANSI image conversion, terminal
recording, desktop-terminal backup helpers, syntax-highlighting integration and
the native modifier adapter. Names, frameworks, file paths and bundled-font
encoding are provenance only; the contracts above are language-neutral.
