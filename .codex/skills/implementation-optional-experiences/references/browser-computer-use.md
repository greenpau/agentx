# Browser and Computer-Use Contract

## Contents

1. [Scope](#scope)
2. [Shared boundary and state model](#shared-boundary-and-state-model)
3. [Browser adapter architecture](#browser-adapter-architecture)
4. [Computer-use adapter architecture](#computer-use-adapter-architecture)
5. [Failure mapping](#failure-mapping)
6. [Acceptance scenarios](#acceptance-scenarios)
7. [Non-normative provenance](#non-normative-provenance)

## Scope

This reference specifies two separately gated open-world adapters:

- browser automation through a Chromium extension and an MCP-compatible server; and
- direct desktop automation through native macOS capture, application, keyboard, and pointer services.

Both adapters join the ordinary tool registry and query lifecycle. Neither is a privileged alternate agent loop. Their external processes, native libraries, bridges, sockets, permissions, and operating-system state are optional resources that must fail within the adapter boundary.

The skill's [architecture diagram](../assets/architecture.drawio) supplies the common gate-to-adapter-to-cleanup flow. The two-page [browser and computer-use lifecycle diagram](../assets/browser-computer-use.drawio) specializes the transport and exclusive-control paths described below.

## Shared boundary and state model

**OPT-BR-001 — Independent identities.** Reserve the canonical normalized server names `agentx-in-chrome` and `computer-use`. Apply ordinary MCP name normalization before comparison, but never allow one adapter's tools, configuration, prompt, or lifecycle state to masquerade as the other.

**OPT-BR-002 — Registry contribution.** An enabled adapter contributes a server configuration, a bounded allowed-tool set, tool-specific rendering, and optional system-prompt material. Registration does not imply that the external extension, native dependency, or operating-system permission is currently usable.

**OPT-BR-003 — Layered gates.** Evaluate build inclusion, product feature, account and subscription eligibility, managed policy, platform, runtime dependency, user preference, and live connection independently. A failure before registration hides the adapter; a failure after registration returns a typed unavailable or disconnected result.

**OPT-BR-004 — Ordinary safety boundary.** Browser and desktop calls still pass through canonical tool resolution, input validation, hooks, permission policy, cancellation, result normalization, transcript persistence, and presentation. Adapter-provided permission modes may strengthen or specialize this boundary but cannot silently override managed denial.

**OPT-BR-005 — UI state separation.** Tracked tab identifiers, clickable links, hidden-application sets, overlays, connection status, and enter/exit notifications are presentation or live application state. They enter model context only through an explicit normalized tool result or prompt section.

**OPT-BR-006 — Abort ownership.** The query turn owns the adapter's abort signal. Disconnect, global Escape, Ctrl+C, sibling cancellation, stream abort, and shutdown converge on idempotent adapter cleanup before the next owner is allowed to act.

## Browser adapter architecture

The browser path is:

```text
tool registry and prompt
  -> MCP browser server
  -> authenticated bridge WebSocket OR secure local socket
  -> native-messaging host
  -> paired Chromium extension
  -> selected tab
```

### Discovery, setup, and registration

**OPT-BR-010 — Supported browser matrix.** Discover Chromium-family installations in this priority order: Chrome, Brave, Arc, Edge, Chromium, Vivaldi, then Opera. Preserve per-platform application names, executable candidates, profile roots, native-messaging manifest locations, and Windows current-user registry keys. Arc has no Linux candidate; Opera uses roaming rather than local Windows application data.

**OPT-BR-011 — Extension discovery.** Inspect every supported browser data root and its profile directories for an installed extension. A public build recognizes only the production extension identity; authorized internal or development builds may additionally recognize development and internal identities. Treat unreadable profiles as absent and continue scanning other candidates.

**OPT-BR-012 — Portable probe.** Keep extension detection independent of the interactive terminal so the same probe can serve terminal and IDE surfaces. The probe returns structured presence and browser/profile evidence, not a side effect.

**OPT-BR-013 — Setup transaction.** Browser setup determines the platform command, creates any required launcher wrapper, writes a native-host manifest to every applicable browser location, and registers Windows manifests under the current user. Create directories before writing. A partial per-browser failure is reported while installation continues where safe; unsupported platforms fail explicitly.

**OPT-BR-014 — Manifest contract.** The native-host manifest identifies the host executable, declares standard input/output transport, and allowlists the recognized extension origins. Because a manifest executable field cannot contain arguments, use a dedicated launcher or executable entrypoint rather than embedding shell syntax.

**OPT-BR-015 — Manifest permissions.** On Unix-like systems, make host launchers executable and keep local communication directories private. Never create a world-writable native host, socket directory, or manifest path.

**OPT-BR-016 — Allowed browser tools.** Map the adapter's browser-tool catalog into canonical MCP tool names at registration time. The functional catalog includes JavaScript execution, page reading, find, form input, computer interaction, navigation, resize, GIF creation, image upload, page-text extraction, tab context, tab creation, plan update, console messages, network requests, shortcut listing, and shortcut execution. Unknown names use generic MCP rendering and safety rules.

**OPT-BR-017 — Permission-mode validation.** Accept only `ask`, `skip_all_permission_checks`, or `follow_a_plan` as an adapter permission-mode override. Reject an unknown value with a diagnostic and proceed with the ordinary default; never interpret an arbitrary string as a permissive mode.

### Local native-message transport

**OPT-BR-020 — Framing.** Encode every local native message as a four-byte unsigned little-endian payload length followed by one UTF-8 JSON value. Apply the same frame shape on standard input/output and each local socket connection.

**OPT-BR-021 — Frame limit.** The payload length must be from 1 through 1,048,576 bytes. A zero-length or oversized frame is a protocol violation: log only safe metadata, close the offending peer, and do not attempt to resynchronize within its payload.

**OPT-BR-022 — Incremental decoder.** Buffer arbitrary input chunks. Do nothing until at least four length bytes exist; then do nothing until the complete declared payload exists. Decode all complete frames in order and retain the incomplete suffix for the next chunk. End-of-stream may process a final complete buffered frame but must not invent missing bytes.

**OPT-BR-023 — JSON-RPC routing.** Correlate requests and responses by their protocol identifier. Broadcast extension-originated notifications to connected MCP clients, and route each response to the client that owns the corresponding outstanding request. Unknown, duplicate, or already-settled identifiers are contained and logged without corrupting other requests.

**OPT-BR-024 — Connection isolation.** Maintain a buffer and outstanding-request ownership for each MCP client. A malformed client destroys only that connection. Client close removes its mappings so a late extension response cannot be delivered to a future client.

**OPT-BR-025 — Secure endpoint.** On Unix-like systems, create a per-user socket directory with mode `0700`, create a process-specific socket, and set its mode to `0600` after binding. On Windows, use the defined per-user named-pipe scheme. Scan known socket paths so the extension can find live hosts.

**OPT-BR-026 — Stale endpoint recovery.** Before listening, recognize legacy endpoint shape, remove endpoints whose encoded process is no longer alive, and leave live peers untouched. On shutdown close clients and server, remove only the process-owned socket, and remove its directory only if empty.

**OPT-BR-027 — Process signals.** Native-host termination, input close, fatal protocol error, or configured process signal invokes the same idempotent shutdown path and flushes bounded diagnostics before exit.

### Bridge, pairing, prompt, and presentation

**OPT-BR-030 — Transport selection.** Use an authenticated browser bridge only when the relevant feature/profile enables it. Local-development mode selects a local unencrypted loopback endpoint; staging selects the staging secure endpoint; ordinary enabled use selects the production secure endpoint. Otherwise use the secure native socket path.

**OPT-BR-031 — Bridge authentication.** Supply stable account identity and a current OAuth access token through callbacks rather than persisting them in server configuration. Empty or expired credentials produce authentication status, not anonymous privileged access.

**OPT-BR-032 — Pairing persistence.** When an extension pairs, persist its device identifier and display name only when either value changes. Reuse the stored device identifier on reconnect. Display only a short identifier fragment in diagnostics.

**OPT-BR-033 — Safe bridge telemetry.** Forward numeric and boolean metadata where allowed, but allowlist string fields. Page content, arbitrary errors, URLs, tool input, and user data must not leak into telemetry under an unconstrained metadata key.

**OPT-BR-034 — Connection failures.** Distinguish missing installation, not-running extension, account mismatch, authentication failure, disconnected tool call, and unsupported platform. Give the user a bounded remediation message while returning a normalized tool error to the model.

**OPT-BR-035 — Initial tab context.** At the start of a browser-automation session, obtain fresh tab context. Reuse an existing tab only when the user explicitly asks; otherwise create a new tab. On closed, invalid, or stale tab identifiers refresh context before retrying.

**OPT-BR-036 — Tab provenance.** Track at most 200 tab identifiers observed in tool inputs or trusted results. Use only tracked numeric identifiers when constructing a focus-tab link. Once full, stop admitting new identifiers rather than evicting provenance and accidentally blessing an unrelated tab.

**OPT-BR-037 — Prompt loading.** When deferred tool search is enabled, require the model to load each browser tool before invocation. When browser behavior is packaged as a runtime skill, instruct the model to invoke that skill before loading tools. Do not simultaneously claim that unavailable tools are callable.

**OPT-BR-038 — Browser conduct.** Tell the model to remain within the user's browser task, avoid blocking JavaScript modal dialogs, respect login and sensitive-action boundaries, and stop for user direction when the page presents ambiguity or consequential choices.

**OPT-BR-039 — Browser rendering.** Render known tools with concise action labels and safe secondary fields. Show a focus-tab affordance only for a valid tracked tab. Large page/tool results use the shared MCP result truncation and expansion contract rather than custom transcript mutation.

## Computer-use adapter architecture

The desktop path is:

```text
feature and subscription gates
  -> tool catalog plus permission wrapper
  -> exclusive session lock and Escape tap
  -> native executor
     -> display capture / application preparation
     -> pointer / keyboard / clipboard
  -> normalized result and screenshot
  -> turn-end restoration and lock release
```

### Gates, dependencies, and coordinate contract

**OPT-BR-050 — Default-disabled gate.** Desktop control is disabled unless its runtime configuration explicitly enables it. External rollout additionally requires an eligible subscription; an authorized internal profile may use a distinct eligibility path. A protected internal-development environment may force disablement unless explicitly overridden.

**OPT-BR-051 — Partial configuration.** Merge a dynamic partial configuration over safe defaults. Defaults include disabled top-level enablement, native screenshot filtering, guarded clipboard behavior, animated motion, pre-action application hiding, automatic display targeting, and pixel coordinates. Missing dynamic fields never become accidental falsy or permissive values.

**OPT-BR-052 — Frozen coordinate mode.** Read the coordinate mode once for the session or first use, then keep it stable for tool descriptions, validation, scaling, clicks, drags, regions, and screenshots. A mid-session flag refresh cannot make the model and executor use different coordinate systems.

**OPT-BR-053 — Platform and loader gate.** Direct desktop execution requires the supported desktop platform and loadable native capture and input modules. Delay module loading until the adapter is enabled or a call requires it. A load failure returns explicit unavailable state and leaves the core runtime usable.

**OPT-BR-054 — Terminal identity.** Resolve the containing terminal's application identity from the operating-system bundle identifier, with a bounded table of known terminal fallbacks. Use a sentinel host identity if unavailable. Exempt the terminal from hiding and exclude it from screenshots wherever the native capture API permits.

### Exclusive ownership

**OPT-BR-060 — Lock record.** Store desktop-control ownership in a dedicated `computer-use.lock` record under the private application configuration root. The record contains session identity, process identity, and acquisition time.

**OPT-BR-061 — Atomic acquisition.** Acquire by exclusive file creation. At most one competing process may report a fresh acquisition. Create the configuration directory first, but never implement the ownership test as a non-atomic read-then-write sequence.

**OPT-BR-062 — Reentrant owner.** A lock with the current session identity is reentrant and reports acquired-but-not-fresh. Reentrant calls do not repeat enter notification, overlay setup, or shutdown registration.

**OPT-BR-063 — Live-owner block.** A lock owned by another live process blocks the call and identifies the owning session in a bounded message. Do not steal the lock based on elapsed time alone.

**OPT-BR-064 — Stale recovery.** A dead-process or corrupt lock is stale. Remove it and retry exclusive creation once. If another contender wins that race, read the winner and return blocked. A process-identity liveness probe may conservatively treat a reused process identifier as live.

**OPT-BR-065 — Deferred-lock tools.** Pure access-request and granted-application-list operations check ownership and recover stale locks but do not acquire one. They must not fire the desktop-control enter notification or overlay merely to ask permission.

**OPT-BR-066 — Owner-only release.** Release unregisters its shutdown callback, re-reads the record, and removes it only if the current session still owns it. Release is idempotent and reports whether removal occurred; a late cleanup can never unlink a successor's lock.

**OPT-BR-067 — Shutdown backstop.** Fresh acquisition registers global application-shutdown cleanup. Turn-end cleanup is the normal release path; process cleanup is the backstop for exit during an active call.

### Permissions and application-name defense

**OPT-BR-070 — Access-before-action.** The adapter exposes explicit application access request and granted-application inspection. An action against an ungranted target returns a permission request/result through the normal interactive or structured permission channel; it must not simulate input first.

**OPT-BR-071 — Abort-aware permission.** Permission waiting observes the turn abort signal. Abort dismisses or settles the request, produces a terminal result, and does not leave ownership, overlay, or input work active.

**OPT-BR-072 — Installed-application provenance.** Discover applications only beneath allowed system and current-user application roots, plus a tiny trusted bundle-identity set required for core interaction. Reject arbitrary paths, package-internal helpers, mount noise, and non-application artifacts.

**OPT-BR-073 — Name sanitization.** Before placing application names in model-visible text or a dialog, normalize and restrict characters, remove control and markup-like content, reject known noisy patterns, cap each name at 40 characters, deduplicate, and cap the collection at 50. Preserve trusted core applications before filling remaining slots.

**OPT-BR-074 — Untrusted labels.** Treat application display names as untrusted data even when found under an allowed root. Present them as data inside a fixed explanatory frame; never concatenate them into policy or executable instructions.

### Native executor

**OPT-BR-080 — Native-call pump.** Execute main-run-loop-sensitive capture and application operations while retaining the native event pump. Apply a 30-second backstop to capture-excluding, capture-region, installed-application listing, and preparation-resolution operations. If timeout wins, swallow a late orphan rejection and surface the timeout as the operation result.

**OPT-BR-081 — Display normalization.** Convert logical display dimensions and scale factor into physical pixels consistently. Automatic display targeting, screenshot output, action coordinates, and region coordinates share the frozen coordinate contract.

**OPT-BR-082 — Screenshot filtering.** Capture only through the native filtered-capture service. Exclude the terminal and other disallowed windows, resize using aspect-preserving target dimensions, and encode screenshot results as JPEG at quality `0.75`. Never write an unfiltered intermediate screenshot to a durable transcript path.

**OPT-BR-083 — Application preparation.** Before an action, optionally activate the target and hide obstructing applications according to sub-gates. Preparation failure is logged and the action may continue only where the executor contract marks preparation as best-effort. Record every application hidden during the turn for restoration.

**OPT-BR-084 — Pointer motion.** Direct movement settles for 50 milliseconds. When animation is enabled, interpolate at approximately 60 frames per second with an ease-out curve, a distance-derived duration of 2,000 pixels per second, and a 0.5-second cap; moves shorter than 30 milliseconds may be direct.

**OPT-BR-085 — Input validation.** Validate coordinates, button, click count, scroll amount/direction, key names, modifier combinations, repeat count, text, and hold duration before invoking native input. Release every pressed modifier or key in a `finally`-equivalent cleanup, including partial press failure, timeout, and abort.

**OPT-BR-086 — Clipboard guard.** For clipboard-based multiline input, save readable prior clipboard state when possible, write bounded tool text, paste, and restore according to the clipboard guard. Clipboard failure returns a typed input error and must not expose previous clipboard contents to the model.

**OPT-BR-087 — Expected Escape hole-punch.** Register global physical Escape as an abort control on fresh ownership. Immediately before the model synthesizes a bare Escape, notify the native event tap so that event passes through without cancelling the turn. Modified Escape sequences are ordinary input unless explicitly classified otherwise.

**OPT-BR-088 — Escape degradation.** If the global event tap cannot register, commonly because accessibility permission is missing, keep desktop tools available under their other gates, log a warning, and rely on Ctrl+C or the surface's normal cancellation control.

**OPT-BR-089 — Escape pump lifetime.** Retain the native event pump while the Escape tap is registered. Unregister the tap before releasing the ownership lock; registration, notification, and unregistration are idempotent at their public boundary.

### Turn-end restoration and rendering

**OPT-BR-090 — Cleanup entrypoints.** Invoke the same cleanup after natural model stop, abort during model streaming, abort during tool execution, and process shutdown. Gate the dynamic native import so turns that never used desktop control perform an in-memory no-op.

**OPT-BR-091 — Unhide first.** Restore every application recorded as hidden during the turn before releasing exclusive ownership. Clear the hidden set after the restoration attempt so stale presentation state does not affect a later turn.

**OPT-BR-092 — Bounded unhide.** Wait no more than five seconds for application restoration. The underlying native request may finish later; timeout only stops blocking turn cleanup. Log restoration failure without preventing Escape unregistration or owner lock release.

**OPT-BR-093 — Release despite teardown failure.** Catch failure from Escape unregistration and continue to owner-only lock release. A native cleanup error cannot strand the lock and permanently block later desktop sessions.

**OPT-BR-094 — Enter/exit notification.** A fresh acquisition may notify that desktop control began. Send the completion notification only when owner release actually removes the lock. Reentrant calls and duplicate cleanup do not repeat notifications.

**OPT-BR-095 — Tool presentation.** Present desktop actions, target application, coordinates, key/text summaries, access state, and screenshots through tool-specific UI backed by normalized results. Redact long or sensitive text, never render hidden clipboard contents, and keep overlays out of the model transcript.

## Failure mapping

| Failure | Model-visible result | User-visible handling | Required cleanup |
| --- | --- | --- | --- |
| Browser extension absent | unavailable with install guidance | concise setup path | close attempted transport |
| Extension disconnected mid-call | disconnected tool error | reconnect/account guidance | settle outstanding request |
| Invalid browser frame | protocol error for owning peer | safe diagnostic | destroy only that peer |
| Desktop gate or dependency absent | unavailable | eligibility/platform explanation | no lock or native input |
| Another desktop session owns lock | busy/blocked | identify owning session safely | none; preserve other's lock |
| OS permission denied | denied or access-required | normal approval/access dialog | release local ownership if acquired |
| Escape or Ctrl+C | cancelled | surface cancellation state | unhide, unregister, release |
| Native operation timeout | bounded operation error | progress stops | release pressed input and turn resources |
| Cleanup native call fails | original result plus diagnostic | non-blocking warning | continue remaining cleanup |

## Acceptance scenarios

**OPT-BR-100 — Split frame decoding.** Deliver two bytes of a browser length prefix, then the remaining prefix and part of the JSON, then the suffix plus a second full frame. The decoder emits exactly two messages in order only after each is complete.

**OPT-BR-101 — Oversized browser frame.** A peer declares 1,048,577 bytes. That peer closes without allocation of the declared payload; other connected clients and the extension connection remain usable.

**OPT-BR-102 — Private socket recovery.** Start after an earlier host crashed. A dead process socket is removed, a live sibling socket is retained, the new directory and endpoint have private modes, and shutdown removes only the new endpoint.

**OPT-BR-103 — Stale browser tab.** A prior-session tab identifier is offered to a navigation request. The adapter refuses to bless it, refreshes tab context, and uses a current tab only under the user's stated intent.

**OPT-BR-104 — Bridge credential expiry.** The secure bridge rejects an expired token. The call becomes an authentication/disconnection result, recommends account reconnection, and neither logs nor persists the token.

**OPT-BR-105 — Competing desktop sessions.** Two processes attempt fresh acquisition concurrently. Exactly one creates the lock and sends enter notification; the other returns blocked without hiding an app, registering Escape, or producing input.

**OPT-BR-106 — Stale desktop owner race.** Two processes observe a dead owner. Both may unlink or retry, but exclusive creation selects one winner; the loser reads the winner and never reports acquired.

**OPT-BR-107 — Deferred permission inquiry.** `request_access` runs while no owner exists. It checks stale state and opens the normal access flow but creates no lock and sends no desktop-control enter or exit notification.

**OPT-BR-108 — Physical and synthetic Escape.** Physical Escape aborts an active turn and triggers restoration. A model-requested bare Escape first signals expected input, reaches the target application, and does not abort. Registration failure still leaves Ctrl+C able to cancel.

**OPT-BR-109 — Hidden-app abort.** Preparation hides two applications and native input then hangs. Cancellation records a terminal tool result, attempts both unhide operations for at most five seconds, clears hidden state, unregisters Escape, and releases only the current session's lock.

**OPT-BR-110 — Native timeout with pressed keys.** A multi-key operation passes the 30-second backstop after pressing one modifier. The surfaced result is timeout and the modifier is released despite the orphaned native promise.

**OPT-BR-111 — Terminal privacy.** A screenshot is taken while the controlling terminal overlaps the target. The capture excludes the terminal, never serializes its pixels as an intermediate artifact, and returns the filtered JPEG at the configured quality.

**OPT-BR-112 — Malicious application label.** Discovery yields overlong names, control characters, instruction-like markup, duplicates, and applications outside allowed roots. Model-visible and dialog lists contain only sanitized unique allowed names, each no longer than 40 characters, with at most 50 entries.

**OPT-BR-113 — Cleanup failure containment.** Application unhide and Escape unregistration both throw. Cleanup records bounded diagnostics, still executes owner-only lock release, sends exit notification only if release succeeds, and the next session can acquire control.

**OPT-BR-114 — Desktop gate and loader matrix.** Exercise default
disabled, ineligible external subscription, authorized internal profile,
unsupported platform, enabled supported platform and native dependency load
failure. Verify registration/use-time visibility separately, frozen safe
defaults, lazy load, explicit unavailable results and no lock/input/overlay in
every disabled or failed case.

## Non-normative provenance

Evidence was consolidated from the browser-extension setup, portable detection, native host, MCP adapter, prompt and rendering areas; the desktop gate, lock, run-loop, application-name, executor, permission wrapper, cleanup, setup and rendering areas; and their query stop/abort integration points. Names here identify protocol values only where interoperability requires them. Current module names and implementation language are not implementation requirements.
