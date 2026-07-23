# Native Installer, Version Locks, and Package-Manager Coexistence

## Contents

1. Boundary and public records
2. Platform and directory layout
3. Version resolution and source selection
4. Artifact acquisition and verification
5. Installation and activation
6. Update policy and single-flight behavior
7. Per-version lock protocols
8. Installation diagnostics
9. Retention and crash cleanup
10. Migration and coexistence cleanup
11. Package-manager detection
12. Failure and compatibility boundaries
13. Acceptance scenarios
14. Provenance

## Boundary and public records

The native installer manages immutable version binaries plus one user-facing executable entry. It is separate from the legacy global-package installer, the local package installer, and operating-system package managers. Use [native-installer-state-machine.drawio](../assets/native-installer-state-machine.drawio) for the mutation order.

**NINST-001 — Installation result.** A native update result contains `latestVersion` as a string or absent value, `wasUpdated` as a boolean, optional `lockFailed`, and optional `lockHolderPid`. A lock failure returns absent `latestVersion` from the public facade even though the internal attempt knows the selected target. An already-current, floor-skipped, or cap-skipped successful attempt reports the selected/discovered version and is considered successful by the internal update result.

**NINST-002 — Setup message.** Installation checks and alias cleanup return ordered messages with text, `userActionRequired`, and type `path`, `alias`, `info`, or `error`. Diagnostics report; they do not repair PATH or execute the displayed shell command.

**NINST-003 — Immutable running image.** Installing or activating a version changes the entry used by a future process. The current process continues from its original executable. A process running a managed version may hold that version against cleanup for its lifetime.

## Platform and directory layout

**NINST-010 — Platform key.** Normalize all operating systems other than native Windows and macOS to Linux. Support only `x64` and `arm64`; reject every other architecture. Form `<os>-<arch>`, except Linux in a musl environment forms `linux-<arch>-musl`. The binary name is `agentx.exe` when the platform key begins with `win32`, otherwise `agentx`.

**NINST-011 — Directory separation.** Resolve permanent versions under `<data-home>/agentx/versions`, disposable downloads under `<cache-home>/agentx/staging`, version locks under `<state-home>/agentx/locks`, and the active executable under the user's local bin directory. Respect XDG data/cache/state overrides; default respectively to `.local/share`, `.cache`, and `.local/state` beneath home. The user-bin default is `.local/bin` beneath home.

**NINST-012 — Version paths.** Before use, recursively create version, staging, lock, and executable-parent directories. The installed version path is the version string as one child of the versions directory; staging is the same child of staging. Create a zero-length placeholder at a missing version path. A zero-length file is never a valid installed binary but supplies a lockable path for the mtime-lock implementation.

**NINST-013 — Binary plausibility.** A candidate is possible only when stat says regular file, size is nonzero, and execute access succeeds. Any stat/access error means false. This is a plausibility check, not a product signature or self-test; integrity is established at download time.

## Version resolution and source selection

**NINST-020 — Direct-version grammar.** Accept `v?MAJOR.MINOR.PATCH` with an optional hyphen followed by one or more non-whitespace characters. Strip one leading `v`. The `99.99.*` namespace is reserved for source-level smoke-test builds and is rejected in every shipped profile unless the explicit test-version build feature is present.

**NINST-021 — Channel grammar.** Any selector not matching the direct-version grammar must be exactly `stable` or `latest`; otherwise fail with guidance naming those channels. Internal users resolve the corresponding registry tag through the private package registry. External users read the channel pointer from the binary object store.

**NINST-022 — Internal version query.** Query `npm view <native-package>@<tag> version --prefer-online --registry <private-registry>` with a 30-second bound and preserved failure output. A nonzero result logs failure telemetry with latency/source/exit code and throws an error containing standard error. Success logs latency/source and returns trimmed standard output.

**NINST-023 — External version query.** Fetch `<base>/<channel>` as text with a 30-second HTTP timeout and optional basic authentication. Success logs latency and returns trimmed body. Failure records latency, optional HTTP status, and whether the error text includes `timeout`, logs a wrapped error naming the URL, and throws.

## Artifact acquisition and verification

**NINST-030 — Owned staging.** Artifact download begins only inside a per-version update lock or the explicit lockless mode. Remove the chosen staging path recursively and forcefully before use, then create it. Lockless mode appends process identifier and current time to the base staging path so concurrent downloads do not share a directory.

**NINST-031 — Internal package integrity.** For an internal user, derive the platform package as `<native-package>-<platform>`. Query its `dist.integrity` from the private registry with a 30-second bound and reject nonzero or empty output. Materialize an isolated package manifest and lockfile that pins both the umbrella package and platform optional dependency and embeds that integrity. Run `npm ci --prefer-online --registry <private-registry>` in staging with a 60-second bound. Return acquisition type `npm` only after zero exit.

**NINST-032 — External manifest.** Fetch `<base>/<version>/manifest.json` as JSON with a ten-second timeout and optional authentication. Select the exact platform key under `platforms`; absence is terminal. Obtain its checksum, form `<base>/<version>/<platform>/<binary-name>`, and never choose a caller-controlled filesystem name from the manifest.

**NINST-033 — Binary download bounds.** Download the binary as bytes with a five-minute total timeout and a separate stall timer. Arm the stall timer before the request and reset it on every progress callback. The default stall threshold is 60 seconds; a test-only environment override is numeric-or-default. Always clear the timer after success or failure.

**NINST-034 — Selective retry.** Attempt a binary download at most three times. Retry only when the HTTP adapter classifies cancellation from the owned stall-abort controller, sleeping one second between attempts. Do not retry HTTP failure, total timeout not classified as owned cancellation, checksum mismatch, filesystem failure, or permission failure.

**NINST-035 — Checksum before write.** Hash the complete response bytes with SHA-256 and compare the lowercase hex digest exactly to the manifest checksum. On mismatch, write nothing and throw. On match, write the staging binary and set mode `0755`. Acquisition telemetry distinguishes manifest fetch, platform absence, download success, timeout-like failure, and checksum mismatch.

**NINST-036 — Source dispatch.** Shipped internal profiles use the private package route and return `npm`; external profiles use the public binary route and return `binary`. The test-version build feature may instead acquire an access token with an explicit cloud-CLI argument vector, use it as a bearer header for the sentinel bucket, and return `binary`; that branch must not exist in ordinary shipped artifacts.

## Installation and activation

**NINST-040 — Same-filesystem atomic install.** Copy the staged binary to `<install-path>.tmp.<pid>.<time>` beside the final version, set `0755`, then rename to the final version path. This copy-then-rename permits cache and data homes on different filesystems while keeping the final replace atomic. Best-effort remove the temporary file on failure and preserve the former final version until rename succeeds.

**NINST-041 — Package extraction.** For `npm` acquisition, enumerate the scoped package directory and select the first entry whose name begins with the native platform-package prefix. Require its `cli` child, atomically install it, then recursively force-remove staging. Missing package, missing binary, and atomic-move failure have distinct telemetry stages and all propagate.

**NINST-042 — Direct-binary install.** For `binary` acquisition, require `<staging>/<binary-name>`, atomically install it, then recursively force-remove staging. Missing staged binary and atomic-move failure are distinct terminal stages.

**NINST-043 — Install decision.** Download/install only when the selected version is not already a possible version binary or force-reinstall is true. Otherwise retain the version file and only activate it. Return whether a new file installation occurred, not whether the active entry changed.

**NINST-044 — Windows activation.** On Windows, the active entry is a copied executable. Ensure its parent exists. If an existing active file and target have equal byte size, treat activation as unchanged without content hashing. Otherwise rename the old active file to `agentx.exe.old.<time>`, copy the target to the active path, and best-effort unlink the old file. If copy fails, restore the old name; if restoration also fails, throw a restoration error whose cause is the copy error. First install copies directly and maps missing source to an explicit error. Activation returns false rather than throwing after its outer logged failure.

**NINST-045 — POSIX activation.** Ensure the active parent exists. If an existing symlink resolves lexically to the selected target, report unchanged. Otherwise attempt to unlink the existing entry; a failure is logged but activation still attempts replacement. Create a uniquely named temporary symlink and rename it to the active path; best-effort remove the temporary on failure. Because the specified sequence unlinks an observed old entry before temporary creation, there is a short missing-entry window despite the final rename being atomic; parity-sensitive tests must model that window.

**NINST-046 — Activation verification.** Before activation, silently remove the active path only when it is an empty directory; ignore missing/not-directory/not-empty and log other errors. After activation, run binary plausibility against the active entry. If invalid, fail with active path, whether the version source still exists, and a write-permission hint. A false activation return is therefore not accepted as success unless the existing active entry independently validates.

## Update policy and single-flight behavior

**NINST-050 — Policy order.** Resolve target first. Unless forced, apply the server maximum. If target exceeds maximum and running is already at/above maximum, record a cap-skip and return success with the originally discovered target; never downgrade. Otherwise replace target with maximum. Next, skip work only when target string exactly equals the running version and both the stored version and active entry validate. Then apply the user's minimum-version floor.

**NINST-051 — Force semantics.** Force-reinstall bypasses maximum and minimum policy, bypasses already-current short-circuit, and best-effort unlinks the per-version lock before reacquiring it. Force does not bypass architecture, channel/direct-version grammar, artifact integrity, filesystem errors, or activation verification.

**NINST-052 — Process single flight.** Non-forced public installs share one in-process asynchronous completion handle regardless of selector; a second call joins the first operation even if it requested another channel/version. Clear the slot after either success or failure. Forced calls bypass this single-flight slot and may overlap.

**NINST-053 — Lockless update gate.** When the lockless-update environment flag is truthy, use unique staging, rely on final-file and symlink/copy operations, and propagate errors without a per-version lock. Otherwise use the selected PID or mtime lock with three retries.

**NINST-054 — Successful configuration.** After internal update success—including already-current and policy-skip outcomes—set configuration to native only when it was not already native, preserve other fields, disable the legacy updater, and mark that disablement as native protection. Start old-version cleanup asynchronously without awaiting it. Return `wasUpdated: true` for every internal success, so this field expresses successful update resolution rather than strictly “new bytes installed.”

**NINST-055 — Lock-failure projection.** Failure to acquire the update lock logs latency and optional holder PID and returns internal `success: false, lockFailed: true`. The public facade projects absent latest version, false updated, and the lock metadata. It does not change configuration or start cleanup.

## Per-version lock protocols

**NINST-060 — Lock selection.** An explicit truthy/falsy environment override selects PID locks; otherwise a cached feature decision with false default selects them. The fallback is an mtime-heartbeat directory lock. All lock paths are `<state-home>/agentx/locks/<version-basename>.lock`.

**NINST-061 — PID lock record.** A PID lock is JSON containing numeric `pid`, nonempty version basename, nonempty executable path, and acquisition time. Empty, unreadable, unparsable, or missing required fields is inactive. PIDs 0 and 1 are never active; signal-zero success is the primary liveness check.

**NINST-062 — PID reuse defense.** The current process identifier is trusted. For another live PID, query its command. Empty/unavailable command is conservatively treated as matching. Otherwise the lowercased command must contain either `agentx` or the lowercased expected executable path. A different command makes the lock stale. A lock older than two hours triggers another PID liveness check but remains active when the PID is still visible.

**NINST-063 — PID write and verification.** Write indented JSON with flush to a unique temporary path and rename it over the lock, then reread and require the current process identifier. Release rereads and unlinks only its own identifier. The protocol does not use exclusive creation: two initially unheld contenders can each rename and can both briefly observe ownership if their verify operations interleave before the later overwrite. Treat this as a specified best-effort race limitation, not a strict distributed mutex guarantee.

**NINST-064 — Operation lock retries.** A PID operation lock attempts `retries + 1` times. With retries, exponential delays start at one second and cap at five seconds; without retries, the unused nominal bounds are 100–500 milliseconds. The mtime lock receives the same retry count/bounds and a seven-day stale threshold. Compromise callbacks log nonfatal diagnostics. Operation errors propagate; release runs in finalization.

**NINST-065 — Lifetime lock.** Lock the current version only when the process executable path text includes the managed versions directory. PID lifetime locks register release on process exit, SIGINT, and SIGTERM. Mtime lifetime locks use zero retries and register async release in the global cleanup registry. Acquisition failure, missing version, or compromised lock is nonfatal, which means cleanup protection is weakened but the session continues.

**NINST-066 — Stale PID cleanup.** Enumerate `.lock` entries. Remove legacy directory locks unconditionally when PID locking is active. Remove file locks only when inactive by the PID/command test. Ignore per-entry failures; missing lock directory yields zero. Diagnostics list valid parsed locks with version, PID liveness, executable, acquisition date, and path.

## Installation diagnostics

**NINST-070 — Check eligibility.** Return no setup messages when installation checks are disabled, the running build is development, or neither force, actual native execution, nor configured native method applies.

**NINST-071 — Active-entry checks.** Report a missing local-bin directory. On Windows require a possible copied binary. On POSIX call readlink directly: absence reports missing command; a link to an invalid target reports that target; not-a-link or other readlink failure falls back to validating the regular file. This ordering avoids access/readlink time-of-check races.

**NINST-072 — PATH comparison.** Resolve every PATH entry and compare it with the resolved active-parent path; use case-insensitive equality on Windows and exact equality elsewhere. Ignore unresolvable entries. Missing Windows PATH produces graphical environment-variable guidance. Missing POSIX PATH chooses the current shell's configuration file when known and emits a command that appends `.local/bin` and sources that file.

## Retention and crash cleanup

**NINST-080 — Deferred best effort.** Cleanup first yields one asynchronous turn and contains ordinary per-entry failures. It must not block successful install completion or session startup.

**NINST-081 — One-hour artifacts.** On Windows, attempt every `agentx.exe.old.<digits>` deletion regardless of age and ignore files still in use. Remove staging children and version-directory temp files matching `.tmp.<pid>.<time>` only when their stat modification time is older than one hour. Ignore concurrent absence and individual errors.

**NINST-082 — Candidate versions.** Read the versions directory once and stat each non-temp entry at most once. Keep only regular files. On non-Windows, reject nonempty files with no execute bit; Windows skips mode-bit filtering. Record name, resolved path, and modification time.

**NINST-083 — Protected set.** Protect the actual running executable when inside versions, the valid target of the active symlink, and every version with an active selected lock. Lock-check failure under the mtime mechanism is treated as not active. Protected versions never consume the two-entry retention allowance.

**NINST-084 — Retention selection.** From unprotected candidates, sort by modification time newest-first and retain the first two. Delete every remaining candidate concurrently, reacquiring its version operation lock immediately around unlink. Track deleted, lock-failed, and error counts independently. Consequently total stored versions may exceed two because every protected version plus two unprotected versions remains.

## Migration and coexistence cleanup

**NINST-090 — Safe active-entry removal.** Before switching away from native, lstat the active entry and resolve a symlink when present. Treat targets ending in `.js` or containing `node_modules` as package-managed and leave them untouched. Otherwise unlink as native. Missing is success; other errors are logged. This heuristic may classify an arbitrary non-package executable as native, so callers must invoke it only inside an already selected migration path.

**NINST-091 — Alias cleanup.** For each known shell configuration in deterministic order, filter recognized AgentX aliases and rewrite only changed files. Each removal returns an action-required alias message instructing the current shell to unalias. Per-file failure logs and returns a non-action-required error message; later files continue.

**NINST-092 — Package uninstall order.** During migration, always attempt global removal of the public package first, then the configured package when present and different, then recursively remove the fixed per-user local-install directory. Count each successful package removal and local-directory removal independently; collect errors and warnings without aborting later steps.

**NINST-093 — npm uninstall fallback.** Invoke `npm uninstall -g <package>` as an explicit argument vector from the current working directory. Zero exit is removed. Registry not-found is a silent non-removal. Other stderr is an error except `ENOTEMPTY`, which triggers manual executable-only cleanup after querying global prefix. On Windows remove the three known launchers from the prefix; elsewhere remove only `<prefix>/bin/agentx`. Never recursively delete the package directory; success carries a warning naming the leftover node-modules path.

## Package-manager detection

**NINST-100 — Detection result and precedence.** Return one of Homebrew, winget, mise, asdf, pacman, apk, deb, rpm, or unknown. Evaluate in exactly that order and memoize the aggregate result. Path recognizers use the actual executable path with argv-zero fallback.

**NINST-101 — Path-owned managers.** Homebrew matches a `Caskroom` path only on macOS/Linux/WSL, deliberately excluding npm installed under a Homebrew prefix. Winget matches its Packages or Links directory only on Windows. Mise and asdf match their installs-directory patterns case-insensitively on any platform.

**NINST-102 — Linux family gates.** Memoize parsing `/etc/os-release` into `ID` and space-separated `ID_LIKE`; unreadable means unknown and conservatively permits command probing. Pacman probes only Arch family, apk only Alpine, dpkg only Debian, and rpm only Fedora/RHEL/SUSE. Each ownership query is an explicit program/argument vector with five-second timeout and no project cwd; only zero exit with nonempty standard output matches.

## Failure and compatibility boundaries

**NINST-110 — No integrity fallback.** Missing integrity, missing manifest/platform, checksum mismatch, missing staged binary, or failed atomic move cannot fall back to an unverified artifact or a broader path. A previous final version survives until the atomic rename.

**NINST-111 — Known activation approximations.** Windows equal byte size is treated as equal content. POSIX target comparison resolves paths lexically, not by inode. These shortcuts may suppress a repair when bytes differ at equal size or paths alias the same inode; force-reinstall/diagnostics are the recovery route.

**NINST-112 — Force authority.** Force-reinstall deliberately attempts to remove even an active per-version lock. Expose force only through an explicit user/admin operation with its own authorization and warning; automatic update never sets it.

**NINST-113 — Secret handling.** Private-registry basic credentials and smoke-test bearer tokens live only in request configuration or child-process result handling. Never write them into manifests, lock records, telemetry, debug text, setup messages, or contract traces.

## Acceptance scenarios

### `NINST-A01` — External verified install

Latest resolves from the public channel pointer. The manifest contains the exact platform and checksum. Bytes arrive with progress before both timers, hash correctly, are written `0755`, copied beside the zero-length version placeholder, atomically replace it, and activate. Configuration becomes native/protected; cleanup begins asynchronously; the current process remains on its old image.

### `NINST-A02` — Stall then checksum failure

Attempt one receives no progress for 60 seconds and is aborted, waits one second, then retries. Attempt two completes with a wrong SHA-256. It does not retry again, writes no binary, leaves the old version and active entry usable, and emits checksum-failure evidence.

### `NINST-A03` — Windows rollback

An active executable of different size is renamed aside, but copying the selected target fails. The old file is renamed back and the install fails. If that restoration also fails, the restoration error is primary and retains the copy error as cause; diagnostics explicitly acknowledge the missing-working-entry risk.

### `NINST-A04` — PID-lock overwrite race

Two contenders both see no active lock. A renames its record and verifies before B renames and verifies. Both callbacks can execute because creation was not exclusive. The conformance test records this compatibility limitation; a strengthened implementation may require a protocol-version migration rather than silently claiming byte-for-byte lock parity.

### `NINST-A05` — Active old versions survive retention

Six version binaries exist: running, active-link target, another process-locked version, and three unprotected. Cleanup protects the first three, retains the two newest unprotected, and attempts deletion of only the oldest unprotected under a fresh operation lock.

### `NINST-A06` — Force reinstall over lock

An explicit forced operation bypasses max/floor/current checks, best-effort removes the target lock, reacquires with three retries, verifies a fresh artifact, atomically replaces the version, and reactivates it. A non-forced automatic call in the same initial state reports contention instead.

### `NINST-A07` — Package-manager ownership

The executable resides in Homebrew Caskroom while npm also exists under the Homebrew prefix. Detection returns Homebrew before probing Linux databases. Automatic presentation shows the Homebrew instruction and performs no native/global/local mutation.

### `NINST-A08` — npm migration ENOTEMPTY

Global uninstall returns `ENOTEMPTY`. Manual cleanup discovers prefix, removes only launchers, leaves node modules intact, reports success plus the exact leftover-directory warning, continues with the second package when applicable, and finally removes the fixed local installation independently.

### `NINST-A09` — POSIX activation interruption window

An old symlink points elsewhere. Activation unlinks it, then temporary-symlink creation fails. The temporary is cleaned best effort, activation returns false, final plausibility fails, and the selected version remains installed for repair even though the active command is temporarily absent.

### `NINST-A10` — Two selectors join one single flight

A non-forced latest update is in flight; a non-forced request for an explicit older version arrives. It joins the same operation and receives the latest request's outcome. After failure or success clears the slot, a later explicit request starts its own operation.

## Provenance

This contract consolidates the specified native installer, download adapters, PID and mtime version locks, platform/package-manager detection, XDG path policy, and migration cleanup. The source module graph and language primitives are non-normative; standalone implementations must reproduce the records, ordering, bounds, race windows, verification, side effects, and failure projections with target-language facilities.
