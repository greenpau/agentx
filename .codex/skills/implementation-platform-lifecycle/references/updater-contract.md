# Updater Protocol and Legacy Global Installer

## Contents

1. Scope and vocabulary
2. Public data contracts
3. Minimum and maximum version policy
4. User minimum-version floor
5. Update lock protocol
6. Installation-prefix authorization
7. Version discovery
8. Internal version history
9. Local per-user package installer
10. Global-package mutation
11. Composition with update surfaces
12. Failure and security boundaries
13. Acceptance scenarios
14. Provenance

## Scope and vocabulary

This document specifies the shared update-policy helpers and the legacy global-package installer. It is deliberately narrower than the native binary installer and the local per-user package installer. Those mechanisms consume the version-policy decisions but retain their own download, artifact-validation, link-swap, rollback, and garbage-collection contracts.

The **running version** is the immutable build version of the current process. Installing another version never changes the code executing in that process. The **target version** is a registry or object-store result after channel and policy filtering. The **release channel** is `latest` or `stable`. The **user type** is either the internal `ant` identity or any external identity.

The updater is a platform service, not a permission bypass. It may mutate only the installation selected by installation diagnosis, and only after the mechanism-specific authorization checks succeed. See [updater-state-machine.drawio](../assets/updater-state-machine.drawio).

## Public data contracts

**UPD-001 — Install status.** A legacy global install returns exactly one of `success`, `no_permissions`, `install_failed`, or `in_progress`. Registry lookup failures are represented before installation as an absent version, not as an install status.

**UPD-002 — Updater result.** A presentation-facing result contains `version`, which is a string or absent value; `status`, which is an install status; and an optional ordered list of notification strings. A successful install result names the installed target even though the running process remains on its former version.

**UPD-003 — Distribution tags.** A distribution-tag result always has two fields, `latest` and `stable`. Each is independently a version string or absent value. A malformed registry response makes both absent. Object-store discovery obtains the two fields independently and may therefore return one present and one absent.

**UPD-004 — Maximum-version configuration.** The remote maximum-version object may contain independent `external` and `ant` version caps and independent `external_message` and `ant_message` explanations. Missing, empty, malformed, or unavailable configuration means no cap and no explanation.

**UPD-005 — Exact versus semantic equality.** Compatibility floors and caps use semantic-version precedence, which ignores build metadata. The explicit update command also compares the registry string to the running version string exactly, so a build with the same semantic precedence but different build metadata is still a distinct available build. Automatic update eligibility uses semantic greater-than-or-equal comparison and therefore does not replace a running build solely because build metadata differs.

**UPD-006 — Semantic comparison profile.** Use the runtime's native semantic-version ordering when available; otherwise use a standards-compatible semantic-version library in loose parsing mode. Greater-than, greater-than-or-equal, less-than, less-than-or-equal, range satisfaction, and three-way order must share that profile. Malformed operands are not silently coerced into a valid version.

## Minimum and maximum version policy

**UPD-010 — Startup minimum-version gate.** Outside the test profile, startup synchronously awaits dynamic configuration named `tengu_version_config`, with `{minVersion: "0.0.0"}` as the unavailable-field default. A nonempty configured floor that is semantically greater than the running version prints a user-facing update requirement, identifies both versions, instructs the user to run the update command, and initiates synchronous graceful shutdown with exit status 1.

**UPD-011 — Minimum-gate fail open.** Failure to fetch or interpret the dynamic minimum-version configuration is logged and startup continues. The gate is intentionally a service-controlled compatibility guard, not a local security boundary. The test profile bypasses both the lookup and exit.

**UPD-012 — Identity-selected maximum.** Fetch dynamic configuration named `tengu_max_version_config` with an empty-object default. Select `ant` and `ant_message` only when the exact user-type environment value is `ant`; all other or missing values select `external` and `external_message`. Empty strings are treated as absent. Fetch failure is logged and returns the empty object.

**UPD-013 — Cap application.** When a discovered target is semantically above the selected maximum, replace the target with the maximum. If the running version is already semantically at or above that maximum, do not downgrade it automatically and do not install. Preserve the originally discovered channel version for diagnostics where the surface supports it.

**UPD-014 — Cap explanation.** The maximum-version explanation is retrieved through the same identity selection as the cap. It is presentation text for a known-issue warning; it does not itself enable, disable, or choose an update.

## User minimum-version floor

**UPD-020 — Floor source.** Read `minimumVersion` from the already resolved initial settings snapshot. Absence or an empty value means no user floor.

**UPD-021 — Skip predicate.** Skip a target precisely when `target >= minimumVersion` is false under semantic-version comparison. Equality is allowed. On a skip, emit a debugging record naming target and floor; do not mutate settings.

**UPD-022 — Stable-channel downgrade protection.** The floor exists so a user switching to the stable channel can retain a newer running build until stable catches up. It is not a request to upgrade to the floor, and it never authorizes an explicit downgrade.

## Update lock protocol

**UPD-030 — Lock identity.** The legacy global updater lock is the literal child `.update.lock` of the resolved application configuration home. Its payload is the decimal process identifier with no surrounding metadata. A lock is fresh for strictly less than 300,000 milliseconds according to its modification time.

**UPD-031 — First observation.** Acquisition first stats the lock. A fresh lock returns `false`. Absence proceeds to exclusive creation. Any other stat failure is logged and returns `false`; inability to inspect a lock is never interpreted as permission to update.

**UPD-032 — Stale-lock recheck.** A lock observed as stale is statted again immediately before deletion. If the second observation is now fresh, acquisition returns `false`. If still stale, unlink it. If it disappeared between observations, proceed to creation. Any other recheck or unlink failure is logged and returns `false`. This second stat prevents a stale-lock contender from deleting a fresh lock installed by another contender.

**UPD-033 — Exclusive creation.** Create the lock with create-new/exclusive semantics and the current process identifier as its complete text. If the configuration directory is absent, create that directory recursively and retry exclusive creation once. An already-existing lock at either create attempt means contention and returns `false`. Every other create or directory error is logged and returns `false`.

**UPD-034 — Ownership-aware release.** Release reads the current lock text and unlinks it only when the text exactly equals the releasing process identifier. Absence is success. A foreign, malformed, or replaced lock is left untouched. Other read or unlink errors are logged, and release remains best effort.

**UPD-035 — Mutation lifetime.** Successful acquisition encloses alias cleanup, environment checks, permission checks, package-manager execution, and configuration recording in one `try/finally`-equivalent lifetime. Release runs after every return and every thrown failure in that region. A process crash may leave a lock until the five-minute stale threshold.

**UPD-036 — Contention result.** Failed acquisition produces status `in_progress`, logs an updater error, and emits a lock-contention event containing the current process identifier and running version. It performs no alias cleanup, prefix lookup, install, or configuration write.

**UPD-037 — Lock limitations.** The payload proves only textual process ownership for release; acquisition does not perform a liveness probe and does not include host, boot, or start-time identity. Consequently, the five-minute age rule—not identifier liveness—is the crash-recovery mechanism. An implementation must preserve that observable policy unless deliberately versioning the protocol.

## Installation-prefix authorization

**UPD-040 — Prefix command.** Determine whether the process is running under the Bun-compatible runtime. For that runtime execute `bun pm bin -g`; otherwise execute `npm -g config get prefix`. Use an explicit program/argument vector and the user's home directory as working directory so project-local package-manager configuration cannot influence discovery.

**UPD-041 — Prefix normalization.** A nonzero prefix command logs failure and yields an absent prefix. A successful command yields trimmed standard output. An empty trimmed result is absent. Standard error is not a prefix.

**UPD-042 — Writable-prefix decision.** Test write access on the returned prefix itself. Success returns `{hasPermissions: true, npmPrefix: prefix}`. Denial logs an insufficient-permissions updater error and returns `{false, prefix}` so diagnostics can still show the path. Discovery failure or an unexpected exception returns `{false, absent}` after logging.

**UPD-043 — Authorization timing.** Prefix write access is checked after alias cleanup and WSL/runtime compatibility checks but before invoking the global installation command. It is the legacy installer's only filesystem permission preflight; a later package-manager failure remains `install_failed`.

## Version discovery

**UPD-050 — Channel-to-tag mapping.** Map stable to the registry tag `stable`; map latest to `latest`. Execute `npm view <package>@<tag> version --prefer-online` with an explicit argument vector, a five-second abort timeout, and the user's home directory as working directory.

**UPD-051 — Single-version result.** A zero-exit registry query returns trimmed standard output. A nonzero exit logs the exit code and whichever standard streams are nonempty, then returns an absent version. Timeout, spawn, and adapter failures must likewise become an absent lookup result at this boundary rather than an install attempt.

**UPD-052 — Registry tag query.** For diagnostics, execute `npm view <package> dist-tags --json --prefer-online` under the same home-directory and five-second constraints. Parse one object. Accept a tag only when its value is a string; missing or differently typed values become absent independently. A nonzero exit or parse failure returns both tags absent.

**UPD-053 — Object-store pointer.** Native/package-manager discovery may fetch `<distribution-bucket>/<channel>` as text with a five-second HTTP timeout. Trim the response body. Network, timeout, status, decoding, or other failures are logged for debugging and return an absent version.

**UPD-054 — Parallel object-store tags.** Obtain latest and stable object-store pointers concurrently and return both outcomes after both settle through their failure-normalizing helper. Failure of one pointer does not erase the other.

**UPD-055 — Untrusted version text.** The discovery helpers return trimmed text without independently proving semantic-version shape. Consumers subject it to the shared semantic comparator; malformed operands follow that comparator's failure path and cannot authorize mutation. When a target reaches installation, pass it only as one argument inside an explicit package specification. Never interpolate it into a shell command.

## Internal version history

**UPD-060 — Eligibility.** Version history is available only when the exact user type is `ant`; every other identity receives an empty list without a registry request.

**UPD-061 — Package choice.** Query the native package identifier when the build supplies one, otherwise the ordinary package identifier. This avoids advertising versions for which the internal native artifact does not exist.

**UPD-062 — History query.** Execute `npm view <selected-package> versions --json --prefer-online` from the user's home directory with a 30-second abort timeout. A nonzero exit logs code and nonempty standard error and returns an empty list.

**UPD-063 — History order and bound.** Parse the response, apply sequence `slice(-limit)`, then reverse those entries. Do not semantically resort them. Thus a well-formed registry string array is returned in registry-order newest-first with at most `limit` entries. The specified boundary does not validate individual element types; JSON or shape failure while slicing/reversing returns an empty list, while malformed elements in an otherwise reversible array cross through to the caller as a compatibility quirk.

## Local per-user package installer

**UPD-LOCAL-001 — Lazy path resolution.** Resolve the local installation as the literal child `local` of the application configuration home at call time, not module/bootstrap time, because an entrypoint may set the configuration-home override before first use. The wrapper command is its child `agentx`.

**UPD-LOCAL-002 — Running-local heuristic.** Classify the current process as local only when its script/argument-one path contains the literal forward-slash fragment `/.agentx/local/node_modules/`. Missing argument means false. This specified textual heuristic is not a realpath or Windows-separator check.

**UPD-LOCAL-003 — Exclusive seed files.** Create a seed file only with exclusive create-if-missing semantics. Already-exists returns false and preserves all existing bytes and mode; every other error propagates to the environment setup boundary.

**UPD-LOCAL-004 — Environment layout.** Recursively create the local directory. Exclusively seed `package.json` with name `agentx-local`, version `0.0.1`, and private true. Exclusively seed the `agentx` wrapper with a POSIX shell header and an `exec` of `<local>/node_modules/.bin/agentx` forwarding every argument. Request mode `0755`, and when newly created apply `0755` again because the creation mode is subject to the user's umask. Existing, even malformed, seed files are never overwritten or repaired. Any setup error is logged and returns false.

**UPD-LOCAL-005 — Version specification.** A present nonempty explicit version wins. Otherwise stable maps to the `stable` tag and latest maps to `latest`. Form one package argument `<configured-package>@<spec>`.

**UPD-LOCAL-006 — Package invocation.** After successful setup, execute `npm install <package-spec>` from the local installation directory with a one-million-byte output buffer and the process helper's ordinary timeout. Use an explicit argument vector and do not request global mutation.

**UPD-LOCAL-007 — Status mapping.** Zero exit preserves all global configuration fields, sets `installMethod` to local, and returns success. Exit code 190 returns in-progress. Every other nonzero exit logs standard error and returns install-failed. Setup failure, spawn/adapter exception, or configuration exception is logged and normalized to install-failed.

**UPD-LOCAL-008 — Existence probe.** Local installation exists exactly when the fixed `node_modules/.bin/agentx` child passes a filesystem access probe. It does not validate file type, content, execute permission, package version, or wrapper health; every access failure means false.

**UPD-LOCAL-009 — Shell-name heuristic.** Read the shell environment string and return zsh when it contains `zsh`, otherwise bash when it contains `bash`, otherwise fish when it contains `fish`, otherwise unknown. This order matters for unusual composite paths and is used only to choose guidance/config paths.

## Global-package mutation

**UPD-070 — Ordered preflight.** After lock acquisition, remove obsolete command aliases from shell configuration files before checking WSL compatibility or prefix permissions. Enumerate the known shell configuration paths in their deterministic map order. For each readable file, filter only recognized AgentX command aliases and rewrite only when at least one was found. Missing files are skipped. A read/filter/write failure for one file is debug-logged and does not stop the remaining files or installation.

**UPD-071 — WSL guard.** When using npm, reject a package manager resolved from a Windows path while running inside WSL. Log the condition, emit the dedicated diagnostic event with the running version, print remediation that installs Linux Node/npm and fixes path order, and return `install_failed`. The Bun-compatible runtime bypasses this npm-specific test.

**UPD-072 — Permission outcome.** If prefix authorization does not prove write access, return `no_permissions`. Do not invoke a package manager and do not record a new installation method.

**UPD-073 — Package specification.** A present, nonempty explicit version produces `<package>@<version>`; absence produces the bare configured package identifier. Choose Bun when running under its compatible runtime, otherwise npm. Execute `<manager> install -g <package-spec>` as an explicit argument vector from the user's home directory. Project `.npmrc` and `.bunfig.toml` files must not affect the invocation.

**UPD-074 — Install failure.** A nonzero package-manager result is logged as an updater error containing captured standard output and standard error and returns `install_failed`. It does not update installation metadata. This boundary has no built-in retry and no internal timeout; its caller or process lifetime supplies any broader bound.

**UPD-075 — Successful metadata.** After a zero-exit install, transactionally update global configuration by preserving every other field and setting `installMethod` to `global`, then return `success`. Configuration-write failure propagates as a failure after the package mutation; the lock is still released. An implementation should surface that split outcome rather than falsely claiming the old executable remains installed.

**UPD-076 — No in-process replacement.** Success means a future process will resolve the updated installation. The current process continues executing its original image, and interactive presentation says to restart before applying the update.

## Composition with update surfaces

**UPD-080 — Mechanism selection.** Diagnose the installation actually executing before update. Native installation uses the native binary updater; package-manager installation reports manager-specific guidance; local package installation uses the local installer; global package installation uses this legacy installer. Development builds do not self-update. An unknown type may fall back to explicit installation evidence, never an arbitrary destructive path.

**UPD-081 — Automatic eligibility order.** An automatic check first rejects an already-running check, unsupported build profile, or disabled updater; discovers the channel target; applies the server maximum; records versions for diagnostics; and installs only when the running version is semantically below target and the user floor does not skip it. The interactive updater checks at mount and every 30 minutes.

**UPD-082 — Concurrent guards.** The presentation-level in-progress flag prevents duplicate work in one UI instance, while the filesystem lock prevents concurrent global installs across processes. The periodic callback must read the current in-progress value rather than a captured stale value. Neither guard substitutes for the other.

**UPD-083 — Explicit command semantics.** The explicit update command diagnoses multiple installations and configuration mismatches, shows actionable warnings, handles package-manager-owned installs without mutation, routes native/local/global mechanisms, maps every install status to a distinct exit path, refreshes completion caches only after verified success, and exits through graceful shutdown.

**UPD-084 — Maximum is not rollback.** Automatic checks never downgrade a running version already at or above a server maximum. A known-issue warning may offer a separate explicit safe rollback command, whose authorization and native-installer semantics are outside this legacy installer.

## Failure and security boundaries

**UPD-090 — Home-directory isolation.** Every package-manager discovery and mutation runs from the user home directory specifically to avoid repository-controlled package-manager configuration. This reduces project-level registry redirection; credential, proxy, certificate, and home-level registry policy remain the auth/network layer's responsibility.

**UPD-091 — Fail-closed mutation.** Ambiguous lock state, an unwritable prefix, incompatible WSL npm, unknown installation ownership, or failed package execution cannot be promoted to success. Lookup failure is nonfatal to the running session but cannot trigger an install.

**UPD-092 — Bounded discovery, explicit unbounded install.** Registry and object-store discovery have five-second bounds; internal history has a 30-second bound. The legacy global install itself has no updater-local timeout. Cancellation or shutdown must therefore be supplied by the outer process lifecycle if required.

**UPD-093 — Best-effort auxiliaries.** Debug logging, analytics, alias cleanup per file, lock release logging, and maximum-config retrieval do not by themselves crash an otherwise usable session. Configuration persistence after a successful package mutation is not merely auxiliary because it determines future mechanism selection.

**UPD-094 — Secret discipline.** Diagnostics may record version strings, selected mechanism, duration, status, and process identifier. Do not record registry credentials, environment contents, package-manager configuration contents, or arbitrary shell-file contents.

## Acceptance scenarios

### `UPD-A01` — Two contenders and stale replacement

Process A and process B both observe a six-minute-old lock. A rechecks, unlinks, and exclusively writes its identifier. B's second stat observes A's fresh modification time and returns `in_progress` without unlinking it. A installs and releases only after confirming its exact identifier remains in the lock.

### `UPD-A02` — Lock replaced before release

The updater acquires the lock, but the file is replaced with another identifier before cleanup. Release reads the foreign payload and leaves it intact. The original updater reports its semantic install result and logs any independent cleanup diagnostics.

### `UPD-A03` — Missing configuration directory race

Two processes receive absence while creating the lock. Each may recursively create the configuration directory, but exclusive file creation has one winner. The loser receives already-exists and returns `in_progress`; neither deletes the winner's fresh lock.

### `UPD-A04` — Maximum below running version

Latest is `3.2.0`, the server cap is `3.0.0`, and the running version is `3.1.0`. The check retains `3.2.0` for diagnostics, reports that current is at or above the cap, and performs no automatic downgrade.

### `UPD-A05` — Stable channel below user floor

Stable resolves to `2.9.0`, latest/running is `3.0.0`, and `minimumVersion` is `3.0.0`. The skip predicate is true, no installer is invoked, and the floor remains unchanged until settings explicitly clear it or stable catches up.

### `UPD-A06` — Same semantic version, different build

Running is `4.1.0+oldsha` and the registry returns `4.1.0+newsha`. The explicit update command sees different strings and may install the new build. The automatic semantic comparison sees the running version at or above target and does not install solely for the metadata change.

### `UPD-A07` — Malicious project package configuration

The current repository contains package-manager configuration redirecting the registry. Prefix discovery, version lookup, tag lookup, history, and global installation all run with the home directory as cwd and explicit argument vectors, so repository-local configuration is not loaded and no shell expansion occurs.

### `UPD-A08` — WSL with Windows npm

Alias cleanup completes, then the WSL guard identifies Windows npm. The updater emits its diagnostic event and remediation, returns `install_failed`, never probes global prefix permissions, never installs, and releases its owned lock.

### `UPD-A09` — Prefix visible but unwritable

Prefix discovery succeeds and returns `/system/prefix`; write-access testing fails. The result retains that prefix for doctor diagnostics, installation returns `no_permissions`, configuration remains unchanged, and the lock is released.

### `UPD-A10` — Partial object-store outage

The latest pointer times out while stable returns `2.8.4`. Parallel tag discovery returns `{latest: absent, stable: "2.8.4"}`. A latest-channel update does not install; a stable-channel consumer may continue with the valid stable result.

### `UPD-A11` — Package install succeeds, metadata write fails

The package manager exits zero, then global-configuration persistence fails. The operation does not return `success`; it releases the lock and surfaces that installation state may have changed while mechanism metadata did not. A subsequent diagnostic reconciles executable reality with configuration.

### `UPD-A12` — Crash during installation

The process crashes after exclusive lock creation. The lock remains. Contenders return `in_progress` until its modification time reaches five minutes; a later contender performs the double stale check, removes it, and may retry. No liveness check shortens that interval.

### `UPD-LOCAL-A01` — Existing malformed wrapper

The local directory exists and its wrapper is a nonempty but malformed file. Exclusive seed creation reports already-exists, so setup returns true without overwriting or chmod repair. The package install may succeed, but invoking the wrapper remains a separate diagnostic failure; implementation must not silently repair it in this path.

### `UPD-LOCAL-A02` — Concurrent first setup

Two processes recursively create the local directory and exclusively seed both files. Each file has one create winner; already-exists is a benign false result. Neither truncates the other's bytes. Both may continue to npm, whose exit code 190 is normalized to in-progress for the loser.

### `UPD-LOCAL-A03` — Stable local update

No explicit version is provided and channel is stable. The installer passes exactly `<package>@stable` to non-global npm in the local directory. On zero exit it records local method and returns success; unlike the explicit command's global path, channel and installed tag remain aligned.

### `UPD-LOCAL-A04` — Weak existence evidence

The fixed package bin child exists but is a readable directory or invalid script. The access probe returns true and callers may choose the local route. Environment or install diagnostics must detect later failure; this helper does not claim executable validity.

## Provenance

The normative behavior above was implemented from the updater helper, its explicit command and interactive consumers, configuration migration, installation diagnostics, and native/local installer boundaries. Names of source modules and implementation-language mechanisms are intentionally omitted from the contract body: a standalone implementation must preserve the data, ordering, races, bounds, and visible outcomes using the target language's own process, filesystem, HTTP, semantic-version, and configuration abstractions.
