---
name: implementation-terminal-engine
description: Implement the retained-mode terminal renderer and interactive input stack, including layout, frame diffing, ANSI lifecycle, keyboard and mouse parsing, focus, selection, prompt editing, keybindings, Vim mode, paste handling, and durable prompt history. Use for terminal rendering, editor behavior, shortcut resolution, input protocol, or history implementation.
---

# Implementation Terminal Engine

## Objective

Build a responsive terminal substrate whose rendering and input behavior are independent of the agent session policy. The engine receives a presentation tree and terminal events; it emits bounded terminal updates and normalized UI events.

See the [terminal architecture diagram](assets/architecture.drawio) for the rendering, input, editor, and persistence boundaries, the [flex-layout diagram](assets/flex-layout.drawio) for dirty propagation, generation-aware caching, measure/layout branching, flex redistribution, absolute positioning, and pixel rounding, and the [component and integration lifecycle diagram](assets/components-and-integration-utilities.drawio) for alternate-screen symmetry, scroll ownership, shared-clock wakeups, recording, export, and terminal-preference recovery.

## Load references by task

- Read [rendering-pipeline.md](references/rendering-pipeline.md) to implement layout, frame production, screen diffing, cursor control, resize, fullscreen, synchronized output, and rendering acceptance tests.
- Read [flex-layout-engine.md](references/flex-layout-engine.md) to implement the retained flex tree, units and edge precedence, measurement, wrapping, flex distribution, absolute layout, caching, rounding, and explicit compatibility gaps.
- Read [text-layout-and-capabilities.md](references/text-layout-and-capabilities.md) to implement grapheme width, wrapping, truncation, ANSI styling, hyperlinks, bidirectional text, tab stops, and response-driven terminal capability discovery.
- Read [input-events.md](references/input-events.md) to implement byte tokenization, key/mouse/paste/terminal-response decoding, propagation, focus, scrolling, and selection.
- Read [prompt-editing.md](references/prompt-editing.md) to implement cursor editing, submission, prompt modes, configurable keybindings, chords, Vim, paste ingestion, and history persistence/navigation.
- Read [components-and-integration-utilities.md](references/components-and-integration-utilities.md) to implement retained presentation primitives, alternate-screen and scrolling lifecycles, the shared animation clock, stable glyph/spinner vocabulary, color and clear compatibility, ANSI image export, recording, desktop-terminal recovery, lazy highlighting, and native modifier probing.

## Core contracts

- **TERM-001 — Retained rendering.** Reconcile a persistent presentation tree, calculate layout, render a logical screen, compare it with the previous screen, then emit the smallest safe terminal update.
- **TERM-002 — Commit ordering.** Complete layout during the commit phase and schedule paint after layout effects so cursor and focus declarations from the same input event appear in that paint.
- **TERM-003 — Stream-safe input.** Parse arbitrary byte chunks incrementally. Never assume one read equals one key, paste, mouse event, or terminal response.
- **TERM-004 — Event ownership.** Deliver normalized events through ordered handlers with prevent-default and immediate-propagation semantics. A consumed chord or modal action must not leak into the text editor.
- **TERM-005 — Grapheme editing.** Cursor offsets, deletion, movement, wrapping, selection, and viewport calculations operate on displayed grapheme boundaries while preserving the original text value.
- **TERM-006 — UI/durable split.** Cursor, focus, selection, chord, paste-assembly, and history-navigation state are transient. Prompt history is append-safe durable data and remains separate from the conversation transcript.
- **TERM-007 — Recovery paint.** Resize, resume, external output contamination, or invalid previous-screen assumptions force a safe repaint instead of applying an unsafe incremental diff.

## Implementation workflow

1. Implement the screen cell, style, hyperlink, cursor, and terminal-capability contracts.
2. Implement retained layout and full-frame rendering before optimizing incremental diffs.
3. Add the incremental input tokenizer and normalized event dispatcher.
4. Add focus, mouse hit-testing, scrolling, and selection over the same screen coordinates used by rendering.
5. Add the grapheme-aware editor and then layer history, paste, keybindings, and Vim behavior over it.
6. Validate byte-level and screen-level golden scenarios from the references.

## Boundary rules

- Keep model messages, permissions, tools, and task policy outside this skill.
- Accept presentation-ready nodes and callbacks from the interactive controller.
- Return normalized user actions; do not directly mutate the semantic transcript.
- Treat terminal capability detection as advisory. Unsupported features degrade to safe full redraw, ordinary keyboard input, or no mouse/focus reporting.

## Non-normative provenance

Evidence came from the reference `ink/`, `keybindings/`, `vim/`, prompt input components and hooks, cursor utilities, and prompt-history module. These locations do not prescribe package layout or implementation language.
