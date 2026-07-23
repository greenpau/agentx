---
name: implementation-interactive-repl
description: "Implement the interactive terminal session adapter: setup dialogs, REPL application state, guarded prompt dispatch, command queues, local and remote session adapters, cancellation, message projection, streaming display, fullscreen transcript, overlays, and dialog focus. Use for interactive conversation or terminal UI behavior above the renderer."
---

# Implementation Interactive REPL

## Objective

Coordinate user input, the shared session engine, transient UI state, and terminal presentation without allowing rendering state to become authoritative session state.

See the [interactive architecture diagram](assets/architecture.drawio) for dispatch, queue, session, and presentation relationships. Use the [dialog arbitration diagram](assets/dialog-arbitration.drawio), [product workflow state-machine diagram](assets/product-workflow-state-machines.drawio), [agent, extension, and task workflow diagram](assets/agent-extension-task-workflows.drawio), [integration and ambient-presentation diagram](assets/integration-ambient-presentation.drawio), and [updater presentation state machine](assets/updater-presentation.drawio) for the specialized workflows below.

## Load references by task

- Read [session-controller.md](references/session-controller.md) to implement setup sequencing, application state, session adapters, query lifecycle, cancellation, shutdown, and remote-view behavior.
- Read [prompt-dispatch.md](references/prompt-dispatch.md) to implement the generation-aware dispatch guard, input normalization, local commands, queued priorities, batching, notifications, and race closure.
- Read [message-dialog-presentation.md](references/message-dialog-presentation.md) to implement message normalization, grouping, streaming projection, fullscreen and scroll behavior, dialog arbitration, overlays, search, and transcript separation.
- Read [interactive-controls-and-rich-content.md](references/interactive-controls-and-rich-content.md) to implement reusable selectors, tabs, wizards, fuzzy pickers, workspace search, Markdown, tables, code, diffs, themes, and responsive dialog content.
- Read [product-workflows.md](references/product-workflows.md) to implement exact dialog priority, settings/status/usage, selectors, onboarding and trust, statistics and diagnostics presentation, feedback, and conversation discovery/resume workflows.
- Read [agent-extension-task-workflows.md](references/agent-extension-task-workflows.md) to implement agent authoring and management, plugin and MCP management, correlated elicitation, background task dialogs, team views, and their asynchronous fault states.
- Read [integration-and-ambient-presentation.md](references/integration-and-ambient-presentation.md) to implement authentication and external handoff screens, IDE and LSP presentation, remote-control and teleport views, read-only hook and skill browsers, notifications, tips, local viewers, and presentation fault isolation.
- Read [updater-presentation.md](references/updater-presentation.md) to route automatic update checks among native, global/local package, and operating-system package-manager installations without making presentation state authoritative.

## Core contracts

- **REPL-001 — Presentation adapter.** The REPL observes and drives a semantic session; it does not redefine messages, tools, permissions, tasks, or persistence.
- **REPL-002 — Guarded dispatch.** Reserve a turn synchronously before any asynchronous input expansion. Use a generation-aware guard so stale cleanup cannot end newer work.
- **REPL-003 — Queue determinism.** Dispatch queued work only while idle, in `now`, `next`, then `later` order. Preserve command boundaries and do not merge task notifications into ordinary user batches.
- **REPL-004 — State separation.** Keep durable transcript/application domain state distinct from component-local input, overlay, focus, scroll, animation, and speculative presentation state.
- **REPL-005 — Explicit model visibility.** Local views and ephemeral progress remain UI-only. Translate them into a typed message only when the feature contract explicitly makes them model-visible.
- **REPL-006 — Immediate cancellation.** Abort the active query, invalidate its generation, settle queued work, close or dismiss pending UI, and invoke the active local/remote adapter's cancellation path.
- **REPL-007 — Nonblocking first paint.** Render the session shell while bounded optional initialization continues, but await required startup hooks before the first model request.

## Implementation workflow

1. Establish application state and the active semantic session adapter.
2. Complete setup/trust dialogs before any untrusted project execution.
3. Render the initial shell and attach input, notification, dialog, and session subscriptions.
4. Route input through local-command handling, guarded normalization, queueing, or immediate query dispatch.
5. Project streamed semantic events into message state and derived presentation rows.
6. Arbitrate approvals and overlays without losing queued input or terminal focus.
7. On exit, cancel active work, settle callbacks, flush durable state, unmount the terminal root, and restore terminal modes.

## Boundary rules

- Delegate byte parsing, layout, frame diffing, editor mechanics, keybindings, and prompt history to the terminal-engine contract.
- Delegate model/tool/task semantics to their owning runtime skills.
- Treat remote sessions as alternate semantic adapters; never run local tools merely because remote messages are displayed locally.

## Non-normative provenance

Evidence came from the reference REPL launcher, REPL and prompt components, interactive helpers, message renderers, screen/dialog launchers, UI contexts, and session hooks. Names and paths are provenance only.
