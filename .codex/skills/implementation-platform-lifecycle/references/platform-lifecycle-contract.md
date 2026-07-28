# Platform and Lifecycle Contract

## Contents

1. Boundary and capability model
2. Filesystem and path operations
3. Processes, output, and cancellation
4. Timers, signals, locks, and temporary resources
5. Terminal and desktop integrations
6. Notification and sleep prevention
7. Installation and update mechanics
8. Graceful shutdown
9. Failure containment
10. Acceptance scenarios
11. Provenance

## Boundary and capability model

**PLAT-001 — Replaceable port.** Put filesystem, process, clock/timer, signal, terminal, desktop, secure-storage primitive, executable discovery, networking socket, and OS-notification operations behind replaceable interfaces. Domain code consumes structured results rather than runtime-specific exceptions.

**PLAT-002 — Capability state.** Distinguish unsupported platform, dependency absent, permission denied, temporarily unavailable, malformed configuration, operation failed, and operation cancelled. Cache a probe only for the period in which its inputs cannot change.

**PLAT-003 — Supported profile.** Detect macOS, Windows, WSL, Linux, and unknown separately. A feature may support a subset; general platform detection must not make the entire client unavailable on another profile.

**PLAT-004 — Portable failure.** Platform-specific failures remain within the integration unless the requested semantic operation cannot proceed safely. Diagnostic reporting itself is best effort.

**PLAT-005 — Immutable build identity.** Resolve one process-wide build identity
before ordinary surface and runtime initialization. A source-controlled release
version is always available when linker metadata is absent; non-empty linker
values may override the release version, Git branch and commit, build user, and
build date. The resolved semantic version is immutable for the process and is
shared by session metadata, structured/SDK initialization, MCP server identity,
interactive startup, and diagnostics. Version-only output may add the resolved
Git and build facts to its banner, but downstream semantic version fields
contain only the version. In the standalone Go profile, version-only execution
still performs the common `GCFG-PATH-006` private-directory bootstrap and
requires `auth.json` to exist under `AUTH-045`; it does not parse credential
contents or require provider setup, workspace configuration, or session
construction.

## Filesystem and path operations

**PLAT-010 — Unified operations.** Provide async and explicitly justified sync forms for read, range read, reverse-line read, append, write, exclusive create, stat/lstat, realpath, rename, unlink, directory traversal, mkdir/rmdir, chmod, symlink, readlink, access, and file descriptor operations. Test domain services against an injected implementation.

**PLAT-011 — Path forms.** Normalize separators and Unicode where required while preserving the caller's original path for display. Authorization-sensitive callers receive every relevant representation: lexical absolute path, deepest existing physical ancestor, and fully resolved physical path when it exists.

**PLAT-012 — Atomic create.** When a new file requires a mode, create it exclusively with that mode in one operation. Do not create broadly and narrow permissions later. Sensitive directories use owner-only access; sensitive files use owner read/write. In the standalone Go application-home adapter, pin child inspection, creation, opening, and chmod to an identity-verified opened parent root. Recheck the textual parent after the descriptor-relative operation so a rename fails the acquisition without mutating a replacement path. Supported POSIX adapters prove effective-user ownership on the opened descriptor before chmod, then require that ownership and zero group/world mode bits on every later verification; a privileged process must not chmod another user's pre-existing directory before rejecting it. Windows retains directory type, no-follow, and stable-identity evidence but does not claim owner-only DACL enforcement from synthesized `FileMode`. Apply [PORT-FS-017 through PORT-FS-021](portable-runtime-services.md#native-owned-directory-primitives) for the standalone native owned-directory inspection, creator-exclusive acquisition, detach, sync, and strict cleanup variants.

**PLAT-013 — Atomic replace.** Write replacement content to a same-filesystem temporary file, flush as required, set the intended mode, and rename. A failed write leaves the former target intact. Preserve unknown parseable fields when the owning data contract requires it.

**PLAT-014 — Append discipline.** Use a single ordered writer per append log. Handle partial writes and closed descriptors. A successful append means the bytes were accepted by the local persistence layer; stronger durability requires an explicit flush contract.

**PLAT-015 — Range semantics.** Range and tail reads are byte-bounded and report omitted bytes. Missing optional output files read as empty only when their owning task contract says so; general file reads report absence.

**PLAT-016 — Directory traversal.** Treat directory entries as untrusted names, do not follow symlinks implicitly during destructive cleanup, and continue past per-entry failures while reporting aggregate errors.

**PLAT-017 — Duplicate paths.** Compare normalized absolute and physical forms with platform-appropriate case rules. Do not assume that textual inequality means different files.

**PLAT-018 — Temporary ownership.** Generate unpredictable names inside an explicit narrow root. The creator records whether the file or directory should be deleted, persisted, or transferred. Cleanup never targets an unresolved environment value, broad root, home directory, or wildcard.

**PLAT-019 — Cache cleanup.** Age-based cleanup defaults to 30 days unless the setting overrides it. A zero cleanup period follows the owning persistence contract rather than being reinterpreted. Failure to delete one item does not stop unrelated cleanup.

## Processes, output, and cancellation

**PLAT-020 — Structured process result.** Return stdout bytes/text, stderr bytes/text, exit code, terminating signal, timeout/cancel flag, start/end time, and spawn error independently. A nonzero exit is an ordinary result unless the caller declares it exceptional.

**PLAT-021 — No shell by default.** Execute an explicit program and argument vector unless the capability contract intentionally invokes a shell. Never implement an argument vector by string concatenation.

**PLAT-022 — Environment.** Build child environments from an allowlisted/inherited base plus deliberate overlays. Proxy, certificate, remote, and credential-related variables use the owning auth/network contract. A child must not inherit secrets or runtime flags merely because the parent has them.

**PLAT-023 — Working directory.** Resolve the cwd before spawn and report a missing/inaccessible cwd separately from executable absence. A remote or worktree adapter translates cwd explicitly.

**PLAT-024 — Abort ownership.** A process launched for an abortable operation listens to that owner's cancellation token. Remove listeners after exit. Prefer process-group or tree termination when the operation can create descendants; escalation from graceful termination to force kill is bounded and documented.

**PLAT-025 — Output pressure.** Read stdout and stderr concurrently, bound retained memory, stream progress with backpressure, and keep draining even if the presentation consumer is slow. Disk-backed output follows task or tool-result limits.

**PLAT-026 — Broken pipes.** Writes to stdout/stderr swallow the platform's closed-pipe condition when the consumer intentionally exited. Other write errors remain diagnostic. Structured streams never intermix console output.

**PLAT-027 — Process liveness.** A signal-zero style probe means “a process with this identifier may exist,” not proof of identity. Stale-lock recovery combines liveness with ownership metadata and age.

## Timers, signals, locks, and temporary resources

**PLAT-030 — Abort-responsive delay.** A delay clears its timer and resolves or rejects according to an explicit abort policy. Optional timers are unreferenced so they do not keep the process alive.

**PLAT-031 — Timeout wrapper.** Returning a timeout outcome while an asynchronous operation is still running does not cancel that operation. Use an abort-capable owned operation when continued work would be unsafe. Always release the timer after either the operation or timeout outcome wins.

**PLAT-032 — Combined cancellation.** A combined token reacts to either parent or a timeout, exposes one signal, and returns a cleanup operation that removes listeners and timers immediately.

**PLAT-033 — Event signal.** A listener-only signal stores no state. Subscribing returns an unsubscribe operation; emission iterates current listeners; reset removes all listeners. Use a real store when consumers need snapshots.

**PLAT-034 — Lock discipline.** Lock protocols retain owner, filesystem identity, and freshness evidence appropriate to the domain. Acquisition is exclusive or verified after write; release is owner-aware and idempotent. Stale thresholds are explicit whenever a protocol permits stale breaking. Corrupt locks fail safe or follow a documented recovery path. Apply [PORT-LOCK-001 through PORT-LOCK-004](portable-runtime-services.md#native-session-lease) to the standalone native session lease; that lease uses retained filesystem and operating-system lock identity rather than stale-lock breaking.

**PLAT-035 — Signal mapping.** Interactive SIGINT, print-mode SIGINT, SIGTERM, and SIGHUP have surface-specific handlers. Normal SIGTERM and SIGHUP exit statuses preserve conventional signal-derived codes 143 and 129. Do not let two handlers race the same shutdown.

**PLAT-036 — Orphan terminal detection.** On non-Windows TTY sessions, check terminal readability/writability every 30 seconds without keeping the process alive. Skip a check during high-priority scroll draining. A revoked terminal initiates the same bounded shutdown as hangup.

## Terminal and desktop integrations

**PLAT-040 — Terminal capabilities.** Detect terminal family and supported escape protocols conservatively. Unsupported sequences must be harmless; terminal rendering contracts decide when to enable them.

**PLAT-041 — External editor.** Parse a configured editor into executable and arguments without executing shell syntax. Classify GUI/editor families only to form their documented goto-line/column arguments. Failure to open returns false or a structured error and leaves the prompt intact.

**PLAT-042 — Browser/path open.** Validate URLs and paths, select the platform opener, avoid shell interpolation, and report inability to open rather than claiming success.

**PLAT-043 — Clipboard/media.** Validate image type and size before attachment creation. Temporary conversions and screenshots have explicit owners and cleanup. Clipboard unavailability degrades to a hint or error, not a session crash.

**PLAT-044 — Hyperlinks.** Emit terminal hyperlinks only when supported and when the target has passed the relevant path/URL validation. Plain-text fallback remains useful.

## Notification and sleep prevention

**PLAT-050 — Notification order.** Run notification hooks first, then route through the configured terminal channel. Channels include automatic selection, terminal-specific protocols, bell, disabled, and no method. Channel errors return an error method and do not fail the completed semantic work.

**PLAT-051 — Automatic notification.** Select by detected terminal. Terminal-specific queries are best effort. An unavailable method does not silently invoke an unrelated desktop command.

**PLAT-052 — Sleep ownership.** Sleep prevention is reference counted. The first owner starts it; the last stops it; forced cleanup resets all ownership.

**PLAT-053 — macOS sleep process.** Use an idle-sleep-only inhibitor with a 300-second self-expiry and restart it every four minutes while owners remain. Do nothing on other platforms. The child is unreferenced and force-cleaned; spawn/exit failure is nonfatal.

## Installation and update mechanics

Apply the detailed `UPD-*` protocol for channel policy and the legacy global-package mechanism, and the detailed `NINST-*` protocol for native artifact verification, activation, per-version locking, rollback, retention, and package-manager coexistence. The `PLAT-*` rules below are their shared platform invariants.

**PLAT-060 — Update lock.** Serialize legacy global-package mutation with an exclusive lock stale after five minutes, rechecking age immediately before breaking a candidate stale lock and releasing only an owned lock. Native per-version mutation follows the rollout-selected `NINST-*` PID or mtime protocol, including its documented best-effort PID overwrite race; do not falsely model the two lock formats as one strict mutex.

**PLAT-061 — Installation detection.** Determine package/native installation and writable prefix before choosing an update mechanism. Do not mutate a system-wide install without proven permission.

**PLAT-062 — Version selection.** Compare semantic versions, distribution tags, channel, minimum/maximum policy, skipped versions, and architecture/platform compatibility before download or install.

**PLAT-063 — Mutation boundary.** Download to a temporary location, validate the expected artifact, install using an explicit program/argument vector, and preserve a usable previous installation on failure where the mechanism permits it.

**PLAT-064 — Background update.** An automatic update may run noncritically, but it uses the applicable process and filesystem concurrency guards and never contaminates structured output. It may replace the entry used by a future launch while the current process continues on its immutable running image. User-visible restart guidance follows a verified successful install; known lock races remain explicit under `NINST-*` rather than being denied by prose.

## Graceful shutdown

**PLAT-070 — Idempotent entry.** The first shutdown call latches the exit code/reason and owns cleanup. Later calls return without starting another sequence. A synchronous facade stores the shared in-flight completion handle and guarantees a fallback terminal reset/exit if that sequence fails.

**PLAT-071 — Failsafe budget.** Arm an unreferenced failsafe before async cleanup. Its duration is the larger of five seconds or the SessionEnd hook budget plus 3.5 seconds.

**PLAT-072 — Terminal first.** Disable mouse tracking, unmount the alternate screen once, drain input, disable extended keyboard/focus/bracketed-paste protocols, show the cursor, and clear owned progress/status/title state before slow async work. Writes are synchronous best effort because a dead terminal may reject them.

**PLAT-073 — Resume hint.** For an interactive persistent TTY with an actual session file, print one resume command on the main screen. Prefer the custom title with safe quoting; otherwise use the session ID.

**PLAT-074 — Critical cleanup.** Run registered cleanup functions concurrently before hooks and telemetry. Bound this phase to two seconds. A failing function does not prevent other registered cleanup.

**PLAT-075 — SessionEnd hooks.** Run hooks after critical cleanup under one overall abort/timeout budget. Per-hook timeouts cannot exceed the overall budget. Hook errors and timeout do not prevent exit.

**PLAT-076 — Final observers.** Report startup performance and the cache-eviction hint before shutting observers down. Bound analytics shutdown to 500 milliseconds. Lost analytics is preferable to a hanging exit.

**PLAT-077 — Final message and exit.** Write a requested final diagnostic to stderr after terminal restoration and before forced exit. Drain the actual input stream immediately before exit. If ordinary exit fails because the terminal is gone, force-kill the process; tests may intercept this behavior.

**PLAT-078 — Cleanup registry.** Registration is set-like and returns unregistration. Shutdown snapshots the current set and awaits all callbacks concurrently. Callbacks must therefore be independently idempotent and must not rely on ordering.

**PLAT-079 — Opaque shutdown failures.** Classify cleanup and shutdown callback failures only from exact sentinels, shutdown-owned context state, and package-sealed snapshots. Never invoke callback-owned `Error`, `Is`, `As`, or `Unwrap` behavior while projecting them. Unknown failures receive fixed diagnostics, and a blocking error method cannot delay later shutdown phases or process exit.

## Failure containment

**PLAT-080 — Partial availability.** A missing browser, editor, notifier, updater dependency, sleep tool, or terminal capability is local unavailability. Return a bounded result and preserve the session.

**PLAT-081 — Durable warning.** Failure to flush authoritative session state is materially different from failure to flush telemetry. Surface the former as lost resumability when the surface can still report it.

**PLAT-082 — No destructive fallback.** A failed atomic replace, symlink creation, cleanup, updater step, or path resolution never falls back to a broader destructive target.

## Acceptance scenarios

### `PLAT-A01` — Signal during a write

SIGTERM arrives while transcript and task-output writers have queued data. One shutdown sequence restores the terminal, flushes registered critical writers within two seconds, bounds hooks and analytics, retains exit code 143, and leaves no child process or held lock owned by the session.

### `PLAT-A02` — Dead terminal

The TTY becomes unreadable without SIGHUP. The orphan check notices it, begins shutdown, ignores EIO while resetting terminal state, drains the correct overridden input stream, and force-exits if normal exit cannot flush.

### `PLAT-A03` — Symlink and parent-replacement race

A target path changes from a regular file to a symlink between validation and exclusive create. The operation fails without following it and without writing a broader target. The permission layer receives enough path evidence to report the denial.
Repeat while an acquired application-owned parent is renamed and replaced
after its child was observed missing. Descriptor-relative creation may affect
only the originally acquired parent; the replacement receives no child or
chmod, and final textual-parent verification reports the identity change.

### `PLAT-A04` — Output backpressure

A child emits stdout and stderr faster than the UI consumes them. Both streams continue draining, retained memory remains bounded, durable task output preserves the configured cap, cancellation terminates the owned process tree, and the final result identifies signal/cancellation distinctly.

### `PLAT-A05` — Optional OS features absent

On an unsupported Linux terminal, browser open, desktop notification, clipboard image read, and sleep prevention are unavailable. Each returns an unavailable/no-method outcome; the local model/tool turn and transcript succeed unchanged.

### `PLAT-A06` — Hanging cleanup

One registered cleanup callback never resolves, and another returns an error whose `Unwrap` blocks forever. Other callbacks run concurrently. Critical cleanup times out after two seconds, error projection invokes none of the returned error's methods, later bounded phases run while the global failsafe remains armed, and the process exits no later than the declared failsafe budget with the terminal restored.

### `PLAT-A07` — Build metadata stamping

Build the entrypoint once without linker values and once with distinct release,
branch, commit, build-user, and build-date values. Give each isolated
application home a present but malformed `auth.json`: the first binary reports
the source-controlled version without parsing credential contents, and the
second binary's version banner reports the injected facts. Remove `auth.json`
and verify both binaries stop with `AUTH-A11` before printing a banner.
Configure an isolated application process with a distinct semantic version and
rich banner; verify repeat configuration cannot replace it, version-only output
uses the banner, and semantic projections read the version without copying the
banner. Missing optional linker values retain their declared source fallback or
remain absent; they never erase the release version.

## Provenance

Non-normative evidence was surveyed in the portable filesystem/process/path utilities, terminal and desktop adapters, notification and sleep services, installer/updater mechanisms, cleanup registry, and graceful-shutdown implementation. This contract restates required behavior independently of those modules and their implementation language.
