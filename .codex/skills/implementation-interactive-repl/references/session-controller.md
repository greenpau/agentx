# Interactive Session Controller

## Contents

1. [Responsibilities and state](#responsibilities-and-state)
2. [Interactive setup sequence](#interactive-setup-sequence)
3. [First paint and first request](#first-paint-and-first-request)
4. [Session adapters](#session-adapters)
5. [Query lifecycle](#query-lifecycle)
6. [Cancellation and shutdown](#cancellation-and-shutdown)
7. [Failure behavior](#failure-behavior)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Non-normative provenance](#non-normative-provenance)

## Responsibilities and state

The interactive controller combines durable application/session state with transient presentation state while keeping their lifetimes explicit.

**Authoritative or shared state**

- Normalized messages and session identity.
- Tool registry and permission mode.
- MCP/extension connection state.
- Tasks, teammates and durable notifications.
- File history, attribution, model/thinking settings and usage.
- Remote/bridge session metadata.

**Transient interactive state**

- Prompt text, cursor, mode, paste map and suggestions.
- Local command view and callback.
- Active query guard and abort controller.
- Focused dialog, overlays and menus.
- Streaming display accumulator.
- Scroll position, unseen divider, search and fullscreen mode.
- Animation, spinner and temporary footer state.

- **REPL-SES-001 — Single message authority.** Keep a synchronously readable message reference aligned with queued state changes so an input event cannot dispatch against a stale pre-commit transcript.
- **REPL-SES-002 — Provider boundary.** Context providers carry transient UI channels and selectors; they are not the durable transcript or task store.
- **REPL-SES-003 — One active adapter.** Exactly one semantic session adapter owns send, interrupt, permission response, and lifecycle status at a time.

## Interactive setup sequence

Create a reusable terminal root before showing setup screens, then render sequential dialogs into that root. Each dialog resolves an explicit completion callback before the next begins.

The safe interactive setup order is:

1. First-run onboarding when required.
2. Project trust unless already trusted or operating in an explicitly exempt environment.
3. After trust, latch trusted state, refresh identity/feature eligibility, and begin system-context prefetch.
4. Approve project MCP servers only when their settings source is wholly valid.
5. Approve external instruction-file inclusions.
6. Establish repository mapping only after trust.
7. Apply the full trusted environment.
8. Start deferred telemetry and policy presentation.
9. Request custom API-key approval where required.
10. Show dangerous permission-mode warning and first-use auto-mode consent where applicable.
11. Handle development-channel, browser integration and other eligible onboarding.

- **REPL-SES-004 — Trust stop.** Trust rejection sets terminal exit outcome and prevents all later project-dependent setup.
- **REPL-SES-005 — Renderer-owned errors.** Once the terminal renderer has intercepted ordinary console output, fatal interactive errors are rendered through the presentation root, then that root is unmounted and cleanup runs.
- **REPL-SES-006 — Sequential dialogs.** Do not mount overlapping setup dialogs or allow their completion callbacks to resolve out of order.

## First paint and first request

Interactive startup may begin MCP connection, plugin work and a fresh-session startup hook concurrently.

- Regular interactive MCP prefetch must not block the first render or first prompt. Servers connected later become available on later turns.
- A fresh-session startup hook may remain pending while the shell renders. The first model request must await its completion and incorporate any returned message/context.
- Resume/continue recovery owns its own startup/recovery hook path and must not run a duplicate fresh-session hook.
- Plugins and other expensive optional work may run in the background if their absence on turn one is an accepted contract.

- **REPL-SES-007 — Shell-first rendering.** Render stable application chrome and existing messages as soon as required trust/setup state exists.
- **REPL-SES-008 — Hook barrier.** Treat the startup hook promise as a request barrier, not a first-paint barrier.
- **REPL-SES-009 — Late registry.** Refresh available tools/clients at a turn boundary so late connections become usable without restarting the REPL.

## Session adapters

Local, remote, bridge, SSH/direct-connect and assistant-viewer sessions expose a common interaction surface:

- send user input
- interrupt active work
- answer permission or elicitation requests
- receive normalized messages and task/status events
- report loading/running/idle/disconnected state
- close and release subscriptions

**Local adapter**

- Sends normalized input into the shared query engine.
- Executes approved local tools through the local capability boundary.
- Treats the query guard as loading state.

**Remote/bridge adapter**

- Immediately appends the local user's visible message when the transport contract expects optimistic echo, then sends it remotely.
- Displays remote assistant and tool-result messages without executing their tools locally.
- Uses remote loading/status in addition to the local guard.
- Relays interrupt and permissions through the remote control protocol.

**Viewer-only adapter**

- Treats the REPL as presentation and input client for a remotely running perpetual session.
- May page historical events lazily.
- Never initializes a second local semantic agent loop for that session.

- **REPL-SES-010 — Adapter parity.** UI actions call adapter methods; components do not branch into independent local versus remote semantic logic.
- **REPL-SES-011 — Provenance visibility.** Deep-link or remote-origin prompts may display an origin banner without silently altering their model-visible text.

## Query lifecycle

At the controller level:

1. Acquire a dispatch reservation.
2. Normalize input and attachments.
3. Release reservation if the work resolves locally without model messages.
4. Transition the guard to running when a query actually starts.
5. Consume streamed events into message/application state.
6. End only the matching guard generation.
7. Trigger queued-work processing after returning to idle.

Streaming display updates may be coalesced to approximately one 16 ms presentation frame. This does not delay transcript/state ingestion.

An incomplete source line may remain hidden until it is safe to display. A streaming tool row uses a stable deterministic temporary identity so repeated deltas update one mounted row rather than remounting it.

- **REPL-SES-012 — Loading derivation.** Loading is true when the active adapter reports work or the local dispatch guard is reserved/running.
- **REPL-SES-013 — Deferred heavy work.** Expensive message projection may yield about every 5 ms so input and animation remain responsive without changing semantic order.

## Cancellation and shutdown

Cancellation sequence:

1. Abort the active model/query request.
2. Force-end the query guard and increment its generation.
3. Cancel or reject queued entries whose contract requires immediate settlement.
4. Clear transient loading placeholders and local modal state.
5. Invoke the active adapter's interrupt/close path.
6. Preserve explicit terminal results for accepted tool and task identifiers.
7. Restore prompt input or history state when the auto-restore-on-interrupt contract applies.

Orderly exit:

1. Stop accepting new prompt dispatch.
2. Cancel active foreground work or wait for its bounded completion as configured.
3. Resolve pending dialog/local-command callbacks.
4. Flush transcript, task and settings state.
5. Unsubscribe remote, extension, terminal and notification listeners.
6. Unmount the root and restore terminal modes.
7. Run registered process cleanup and return the selected exit status.

- **REPL-SES-014 — Stale finally safety.** A cancelled older query cannot clear a newer query's loading state.
- **REPL-SES-015 — No anonymous callbacks.** Every mounted dialog, local command and permission prompt has a cancellation/unmount settlement path.
- **REPL-SES-016 — Host callback settlement.** Invoke terminal writers outside
  presentation-state locks and mark the callback boundary explicitly. Reentrant
  prompt, publish, or finish operations fail immediately with a stable local
  error; independent sanitizer updates remain safe. Contain writer panics and
  replace writer-owned errors with fixed local failures without formatting,
  unwrapping, or retaining them. Once a streamed turn's writer fails, keep that
  turn poisoned so finish cannot report success after a partial projection.
- **REPL-SES-017 — Input callback isolation.** Normalize the terminal reader
  behind one owned pump. Preserve exact EOF, but replace every other callback
  error, invalid byte count, or panic with a fixed local input failure without
  inspecting the host error graph. Start a closable reader's `Close` callback
  asynchronously and bound pump joining so terminal shutdown cannot wait
  behind a broken host callback.

## Failure behavior

- A local command failure becomes a local error view/message and does not corrupt the semantic transcript.
- A render failure is presentation-scoped; durable messages remain recoverable.
- A remote disconnect keeps already received transcript events and enters an explicit disconnected/reconnecting state.
- Failure before trust exits without executing project-scoped integrations.
- Failure after root creation is displayed through the root and followed by terminal restoration.
- A startup hook failure follows hook policy: explicit blocking failure stops the request; nonblocking diagnostic failure is recorded and the surface remains usable.

## Acceptance scenarios

**REPL-SES-A01 — Deferred startup hook.** Delay the startup hook while rendering; verify the prompt is visible, but submitting waits for the hook before the model request.

**REPL-SES-A02 — Trust rejection.** Reject project trust; verify no project MCP, LSP, repository mapping or full environment execution follows.

**REPL-SES-A03 — Late capability.** Connect an MCP server after turn one; verify it appears on the next turn without restarting the session.

**REPL-SES-A04 — Query supersession.** Cancel query A and immediately start query B; verify A's cleanup cannot end B's guard or spinner.

**REPL-SES-A05 — Remote tool projection.** Display a remote tool-use event; verify it is rendered but never executed by the local tool runner.

**REPL-SES-A06 — Viewer disconnect.** Disconnect a remote viewer; verify existing history remains and the UI reports disconnection explicitly.

**REPL-SES-A07 — Renderer-owned fatal error.** Throw a fatal error after console patching; verify the error is visibly rendered and terminal modes are restored.

**REPL-SES-A08 — Open-dialog exit.** Exit with an open local-command dialog; verify its callback settles and no root or input listener remains.

**REPL-SES-A09 — Reentrant terminal writer.** Have a terminal writer synchronously
reenter prompt dispatch and streamed publication, return an error with hostile
error methods, and panic. Verify every nested operation fails without waiting,
the host error graph is never inspected or retained, the panic becomes a fixed
local failure, sanitizer replacement remains safe, and a partial streamed turn
cannot later finish successfully.

**REPL-SES-A10 — Hostile terminal reader.** Return an uncomparable error with
panicking error methods, an invalid count, and a panic from `Read`; separately
block or panic in `Close`. Verify the reader pump reports only its fixed
failure, never invokes host error methods, admits no input after failure, and
closes within its bound.

## Non-normative provenance

Evidence was specified from the reference interactive launcher/helpers, REPL component, session hooks, application state store, setup screens, startup-hook integration and cleanup registry. Names and file locations are provenance only.
