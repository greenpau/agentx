# Interactive Updater Presentation

## Ownership and shared protocol

This adapter selects and presents an automatic-update mechanism. It owns transient `checking`, discovered-version, result, and banner state. It does not own version policy, update locking, package mutation, native artifact integrity, global configuration, or installation diagnosis. Implement those through the platform updater contract and native/local installer ports; see the updater protocol routed by the platform-lifecycle skill.

**REPL-UPD-001 — One-time mechanism resolution.** On mount, optionally skip installation detection when the build gate for skipping disabled update detection is active and auto-updates are disabled. Otherwise diagnose the currently executing installation once. Render no updater content until both “is native” and “is package-manager-owned” decisions are known. Route package-manager-owned first, native second, and every other diagnosis to the legacy package updater.

**REPL-UPD-002 — Scheduling and live guard.** Each selected updater checks once after mount and every 1,800,000 milliseconds thereafter. A mutation-capable updater reads the current shared `isUpdating` value at invocation time; it must not rely on the value captured when the periodic callback was created. If already updating, return without discovery or mutation. Keep the callback identity stable so a state change does not retrigger the mount check.

**REPL-UPD-003 — Legacy package flow.** In eligible production profiles, resolve channel from the initial settings snapshot with latest as default, query the registry, read the global disable decision, apply the server maximum, record running and discovered versions, and update only when enabled, target exists, running is semantically below target, and the user floor permits it. Before a local/global package mutation, remove a native symlink only when configuration does not declare the native mechanism. Diagnose the actual running installation: local uses the local installer, global uses the global installer, development performs no mutation, unexpectedly native performs no mutation, and unknown uses `installMethod == local` as the sole local fallback; otherwise it chooses global.

**REPL-UPD-004 — Legacy completion.** Set shared checking true immediately before mutation and false after mechanism completion or an explicitly handled development/native early return. Emit success telemetry only for `success`; every other normalized status emits failure telemetry. Include running/target version, elapsed duration, whether local migration was used, and diagnosed installation type. Publish one presentation result `{version: target, status}` after a normalized installer outcome. A thrown unnormalized dependency failure follows the outer UI error boundary; implementations should preserve the shared-state cleanup obligation rather than strand the checking flag.

**REPL-UPD-005 — Native flow.** If already checking, in test/development, or globally disabled, return. Otherwise set checking, record check-start telemetry, retrieve maximum-version policy, and when the running version is above the maximum load an explanatory warning with a fixed fallback explanation. Invoke the native installer for the selected channel. Lock contention is a silent check skip with a contention metric and no presentation result. Record current/latest display versions after non-contention completion. Publish success only when the native result says a replacement occurred; “already current” records an up-to-date metric without a result.

**REPL-UPD-006 — Native failure taxonomy.** A thrown native error is logged, normalized to `{version: absent, status: install_failed}`, and classified for telemetry by case-sensitive substring priority: `timeout`; `Checksum mismatch`; `ENOENT` or `not found`; `EACCES` or `permission`; `ENOSPC`; `npm`; `network`, `ECONNREFUSED`, or `ENOTFOUND`; otherwise unknown. Emit one boolean field for each known class. Always clear checking in a finally-equivalent path, including contention, success, current, and failure.

**REPL-UPD-007 — Package-manager-owned flow.** This route never installs. Unless globally disabled, concurrently obtain the selected channel and package-manager identity, fetch the channel pointer from the distribution object store, apply the maximum, refuse an automatic downgrade when running is at/above that maximum, apply the user floor, and retain only an update-available boolean plus manager identity. Repeat on the same 30-minute schedule.

**REPL-UPD-008 — Package-manager guidance.** When an update exists, present exactly one manager-owned instruction: Homebrew uses `brew upgrade agentx-code`, winget uses `winget upgrade AgentX.AgentXCode`, apk uses `apk upgrade agentx-code`, and all other identities use generic package-manager update guidance. Never execute that instruction. Verbose mode additionally prints the immutable running version.

**REPL-UPD-009 — Result visibility.** The legacy package view is hidden until it has either a result or an active check with both running and target display versions. Native is visible only for a maximum-version warning, a result with a version, or an active check with both display versions. Package-manager guidance is visible only when update-available is true. These are presentation rules and produce no transcript message.

**REPL-UPD-010 — Status text.** While checking, legacy says `Auto-updating…` and native says `Checking for updates`. A new successful legacy semantic version says the update is installed and restart is required; native says restart to update. Legacy `install_failed` or `no_permissions` recommends doctor plus a local or global manual command based on local-install evidence. Native `install_failed` recommends the status command. Contention has no error banner because the periodic check will retry.

**REPL-UPD-011 — Once-per-semantic-version notice.** Initialize the last-notified value to the running version's major/minor/patch triple using loose parsing. Strip prerelease and build metadata from every installed version the same way. Return a notice only when this triple differs from the last-notified triple, then synchronously remember it. Repeated results for the same triple do not repeat the success banner.

**REPL-UPD-012 — Maximum warning eligibility.** Native maximum-policy text is stored whenever the running version exceeds the cap, but the rollback warning is displayed only for the internal user profile. External builds keep that text nonvisible. The warning offers the explicit safe rollback command; it never invokes rollback automatically.

**REPL-UPD-013 — Non-authoritative state.** Version-display fields, `isUpdating`, update-available, manager identity, error banners, and last-notified semantic version are component state. They are neither transcript events nor model context. Authoritative installed state comes from the installer and subsequent process/diagnostic observations.

## Acceptance scenarios

### `REPL-UPD-A01` — Periodic callback races an install

The 30-minute timer fires after checking was set true. The callback reads the live flag and returns before lookup or mutation. After completion clears it, the next timer may check; the mount effect does not repeat merely because the flag changed.

### `REPL-UPD-A02` — Package manager cannot be self-mutated

Diagnosis returns package-manager-owned and the object-store target is newer. The UI shows the manager-specific command, never calls a native/local/global installer, emits no model-visible message, and repeats discovery after 30 minutes.

### `REPL-UPD-A03` — Native lock contention

The native installer reports lock contention. The view emits the contention metric, clears checking in finalization, publishes no failed result or banner, and relies on the next scheduled check.

### `REPL-UPD-A04` — Same release notice twice

Two successful results name builds whose metadata differs but whose major/minor/patch triple is identical. The first differing triple produces one restart banner; the second produces none. Installing the next patch produces a new banner.

### `REPL-UPD-A05` — Disabled detection gate

Auto-updates are disabled and the build gate says to skip diagnosis. Mechanism state remains unresolved and the wrapper renders nothing. It neither misclassifies the install nor starts periodic mutation.

### `REPL-UPD-A06` — Native checksum failure

Native installation throws an error containing `Checksum mismatch`. The view logs the error, emits checksum=true and every other classification boolean false, publishes absent-version/install-failed, clears checking, and shows the status-command recovery hint without touching transcript state.
