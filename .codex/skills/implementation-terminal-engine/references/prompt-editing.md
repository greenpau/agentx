# Prompt Editing, Keybindings, Paste, and History

## Contents

1. [Editor state and cursor rules](#editor-state-and-cursor-rules)
2. [Submission and navigation](#submission-and-navigation)
3. [Kill, yank, undo, and prompt modes](#kill-yank-undo-and-prompt-modes)
4. [Configurable keybindings](#configurable-keybindings)
5. [Vim state machine](#vim-state-machine)
6. [Paste ingestion](#paste-ingestion)
7. [Durable prompt history](#durable-prompt-history)
8. [History navigation and search](#history-navigation-and-search)
9. [Acceptance scenarios](#acceptance-scenarios)
10. [Non-normative provenance](#non-normative-provenance)

## Editor state and cursor rules

The editor owns text value, cursor offset, wrapped position, viewport window, display mask/highlights, optional ghost text, transient paste state, prompt mode, undo/buffer state, and Vim mode. The caller owns semantic submission.

- **TERM-ED-001 — Grapheme cursor.** Movement and character deletion stop at grapheme boundaries. Display columns account for wide and combining characters.
- **TERM-ED-002 — Controlled value.** Notify the caller only when text changes; update cursor independently when only position changes.
- **TERM-ED-003 — Viewport.** When wrapped content exceeds the configured visible-line limit, show the window around the cursor and report the viewport's source offsets.
- **TERM-ED-004 — Ghost text.** Render ghost text only when its recorded insertion position still equals the current cursor, preventing a stale one-frame suggestion.
- **TERM-ED-005 — Raw cleanup.** Strip terminal styling from inserted text, normalize carriage returns, and defensively process raw delete characters leaked by SSH or multiplexers.

## Submission and navigation

- **TERM-ED-006 — Plain Enter.** Submit the current original value.
- **TERM-ED-007 — Backslash Enter.** When multiline input is enabled and the previous character is backslash, replace the continuation with a newline and record that the behavior was used.
- **TERM-ED-008 — Modified Enter.** Shift+Enter or terminal-meta+Enter inserts a newline. On terminals unable to encode Shift+Enter, an OS modifier probe may provide the same behavior.
- **TERM-ED-009 — Coalesced SSH Enter.** A multi-character read ending in exactly one carriage return inserts the preceding text and submits, except when the preceding character is backslash. Lone or embedded carriage returns become newlines.
- **TERM-ED-010 — Vertical navigation.** Up/down first move within wrapped display lines, then logical lines. Only when neither movement is possible do they invoke history navigation.
- **TERM-ED-011 — Fullscreen reservation.** Page Up/Down and wheel are no-ops in the editor while fullscreen scrolling owns them.
- **TERM-ED-012 — Mode marker.** Inserting a recognized mode marker at the beginning may switch prompt mode while keeping the editor's visible value separate from any hidden display prefix.

## Kill, yank, undo, and prompt modes

Support character/token deletion, word and line movement, start/end of line, kill-before, kill-after, kill-word, yank and yank-pop.

- Consecutive kill commands accumulate into one kill-ring item in directionally correct order.
- Any non-kill key ends kill accumulation.
- Yank inserts the current kill-ring item. Yank-pop replaces only the immediately preceding yank and ends on any unrelated key.
- Undo restores text, cursor and associated pasted-content map together.
- Escape handling may clear or double-press-cancel input, but must be disabled when a modal/keybinding context owns Escape because listener registration order otherwise lets both fire.
- Prompt-mode buffers preserve text, cursor and pasted contents when moving among ordinary prompt, shell, and other supported modes.
- Leaving a special mode with empty input returns to ordinary prompt mode before triggering global cancellation.

## Configurable keybindings

Configuration contains context blocks. Each binding maps a keystroke or whitespace-separated chord to a known action, `command:<identifier>`, or `null` to unbind.

Recognized contexts include Global, Chat, Autocomplete, Confirmation, Help, Transcript, HistorySearch, Task, ThemePicker, Settings, Tabs, Attachments, Footer, MessageSelector, DiffDialog, ModelPicker, Select, and Plugin.

- **TERM-KEY-001 — Parse aliases.** Normalize ctrl/control; alt/opt/option/meta; cmd/command/super/win; Esc; Return; Space; and arrow glyph aliases.
- **TERM-KEY-002 — Terminal modifiers.** Treat alt and meta as one logical terminal modifier. Keep super distinct when extended keyboard reporting supplies it. Do not expose function modifier as configurable.
- **TERM-KEY-003 — Merge order.** Load defaults first and append user bindings. The last matching binding wins. `null` is a real winning result that consumes/unbinds the key.
- **TERM-KEY-004 — Validation.** Report malformed keystrokes/blocks, invalid context or action, duplicate raw JSON keys, duplicate effective bindings, non-rebindable and platform-reserved shortcuts, and unmodified printable bindings that the editor would necessarily consume.
- **TERM-KEY-005 — Safe fallback.** Missing or unreadable user configuration uses defaults. Invalid configuration retains the last valid cached/default bindings and surfaces warnings without disabling input.
- **TERM-KEY-006 — Hot reload.** Watch the user file, replace the merged registry atomically, and notify the UI of new validation issues.
- **TERM-KEY-007 — Active contexts.** Resolve only among registered handler contexts, explicitly active UI contexts, and Global. Effective winner remains the last matching binding in merged order.
- **TERM-KEY-008 — Chord preference.** If an input is a live prefix of any longer non-null chord, enter pending state even if it exactly matches a shorter binding.
- **TERM-KEY-009 — Null prefix shadow.** A later null override for a longer chord removes that chord from prefix consideration.
- **TERM-KEY-010 — Chord timeout.** Cancel an incomplete chord after 1,000 ms. Escape or an invalid continuation also cancels. Swallow the invalid continuation rather than reinterpret it independently.
- **TERM-KEY-011 — Wheel exception.** A wheel event cannot begin a chord, but during a pending chord it cancels like another invalid continuation.
- **TERM-KEY-012 — Consumption.** A synchronous handler may return false to allow propagation. Any other return, including a pending asynchronous result, consumes the event.

## Vim state machine

Vim starts in INSERT mode unless the caller explicitly restores another supported mode.

State is either INSERT with accumulated inserted text or NORMAL with one command-parser state: idle, count, operator, operator-count, operator-find, operator-text-object, find, `g`, operator-`g`, replace, or indent.

- **TERM-VIM-001 — Unconfigurable Escape.** Escape from INSERT always enters NORMAL. Record accumulated inserted text for dot-repeat and move one grapheme left unless already at offset zero or immediately after a newline.
- **TERM-VIM-002 — Normal cancellation.** Escape in NORMAL resets any pending command to idle.
- **TERM-VIM-003 — Submit parity.** Enter delegates to ordinary editor submit/newline handling in either mode.
- **TERM-VIM-004 — Counts.** Parse decimal counts, treating zero as a motion when no count has begun. Clamp counts to 10,000. Operator and motion counts multiply.
- **TERM-VIM-005 — Persistent register.** Preserve unnamed register content and linewise flag, last find command, and last repeatable change across mode switches.
- **TERM-VIM-006 — Dot repeat.** Replay insert, operator, find/text-object operator, replace, delete-character, case-toggle, indent, join, or open-line without recording the replay as a new change.
- **TERM-VIM-007 — Idle arrows.** At idle, arrows delegate to ordinary editor navigation and history fallback. During a command, map arrows to Vim motions.
- **TERM-VIM-008 — Literal states.** Backspace/Delete map to motions only when a motion is expected; they must not become literal replacement/find characters.
- **TERM-VIM-009 — Composition.** Operators compose with motions, finds, line operations, `g` motions, and inner/around text objects. Update text, cursor and register as one semantic edit.

## Paste ingestion

Detect a paste when bracketed-paste metadata is set, a chunk exceeds 800 characters, a prior paste chunk is pending, or the input resembles one or more image paths.

- Assemble chunks until 100 ms of quiet.
- Use a synchronous pending flag so a paste and Enter delivered in one input batch cannot submit the old editor value.
- Remove orphaned focus-event tails from assembled paste data.
- Strip ANSI escapes and normalize line endings/tabs before inserting text.
- An empty bracketed paste on macOS triggers clipboard-image inspection when image support is enabled.
- Debounce clipboard-image inspection by 50 ms.
- Split multiple image paths on newlines and on spaces preceding an absolute Unix or drive-qualified Windows path.
- Read candidate images concurrently. Convert valid images to attachments; preserve non-image lines as text. A vanished temporary screenshot may fall back to clipboard image data.
- Text longer than 800 characters becomes a visible numbered placeholder and an out-of-line paste record. Shorter text inserts directly.
- Paste IDs are positive integers unique within a prompt. Expand text placeholders by original offsets from right to left so placeholder-looking content inside a paste is never recursively expanded. Image placeholders remain text markers plus attachment blocks.

## Durable prompt history

History is distinct from the conversation transcript. Store newline-delimited records containing display input, restorable text paste metadata, timestamp, project identity and optional session identity.

- **TERM-HIST-001 — Scope/order.** Read newest first and filter to the current project. Within the 100-record scan window, yield current-session entries before other sessions so concurrent sessions do not interleave arrow history.
- **TERM-HIST-002 — Permissions.** Create and append the history file with owner-only mode `0600`.
- **TERM-HIST-003 — Locking.** Ensure the file exists, acquire an append lock with a 10-second stale threshold and three retries beginning at 50 ms, append complete JSON lines, then release.
- **TERM-HIST-004 — Buffered write.** Submission adds to an in-memory queue and starts a nonblocking flush. Retry queued follow-up writes at most five times with a 500 ms delay; cleanup performs a final flush.
- **TERM-HIST-005 — Corruption tolerance.** Skip malformed lines. Missing history is an empty history; other read errors may surface diagnostically.
- **TERM-HIST-006 — Paste storage.** Keep text paste content of at most 1,024 characters inline. Store larger text by content hash in a separate paste store, writing that blob asynchronously. Do not store images in prompt history.
- **TERM-HIST-007 — Semantic undo.** Removing the most recent submission pops it from the pending queue. If already flushed, remember its timestamp in a session-local skip set so restored-on-interrupt input does not appear twice.

## History navigation and search

Arrow navigation loads entries in chunks of ten and coalesces concurrent requests. Synchronous cursor/index refs ensure rapid keypresses do not observe stale UI state.

- On first Up, save current draft, cursor, prompt mode and paste map.
- When starting from shell mode, keep a shell-only filter fixed until navigation resets.
- Up moves to older entries and places the cursor at the start for the first recalled entry; Down moves newer and restores the saved draft at index zero.
- Exhaustion leaves the current recalled value and rolls back the speculative index.
- Reset clears cache, fixed filter and navigation hint.

Reverse search:

- Save original input/cursor/mode/paste map when search begins.
- Stream history lazily and explicitly close the generator on reset to release the file handle.
- Restart from newest when the query changes and abort any stale search.
- Match the query's last occurrence, deduplicate by display string, restore mode and paste map, and place the cursor at the match within the mode-clean value.
- Exhaustion keeps the last match but marks failure.
- Accept keeps the match, cancel restores the original draft, and execute submits the original or matched entry.

## Acceptance scenarios

- **TERM-ED-A01 — Grapheme edit.** Edit combining and double-width graphemes;
  verify movement/deletion never splits them.
- **TERM-ED-A02 — Vertical precedence.** Press Up within multiline wrapped
  input; verify cursor movement precedes history recall.
- **TERM-ED-A03 — Coalesced submit.** Deliver `text\r` in one SSH chunk; verify
  text inserts and submits once.
- **TERM-KEY-A01 — Longer chord.** Configure a two-key chord whose first key
  also has a single-key action; verify the longer chord waits and wins.
- **TERM-KEY-A02 — Null shadow removal.** Null-unbind that longer chord; verify
  the first key's single action fires without chord wait.
- **TERM-VIM-A01 — Count composition.** Enter Vim `3d2w`; verify effective
  count six and one repeatable edit record.
- **TERM-ED-A04 — Paste-before-submit.** Paste a long string followed by Enter
  in the same event-loop batch; verify assembly precedes submission.
- **TERM-ED-A05 — Mixed attachments.** Paste two image paths and one ordinary
  line; verify two attachments plus preserved text.
- **TERM-HIST-A01 — Large-paste recall.** Submit a large paste, restart history
  and recall it; verify the content-hash record restores placeholder mapping.
- **TERM-HIST-A02 — Interrupt deduplication.** Interrupt before response and
  restore the prompt; verify history contains it only once.
- **TERM-HIST-A03 — Search generation.** Search rapidly changing queries;
  verify stale asynchronous matches never overwrite the newest query.

## Non-normative provenance

Evidence was specified from the reference cursor/editor hooks, prompt input components, input-mode and paste helpers, keybinding parser/resolver/validator/provider, Vim transitions/operators/motions/text objects, history store, paste store and history-search hooks. These paths and symbols are not normative.
