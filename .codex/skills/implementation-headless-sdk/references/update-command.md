# Explicit Update Command

## Contents

1. [Scope and output contract](#scope-and-output-contract)
2. [Diagnosis and configuration reconciliation](#diagnosis-and-configuration-reconciliation)
3. [Package-manager-owned installations](#package-manager-owned-installations)
4. [Native installation path](#native-installation-path)
5. [Local and global package paths](#local-and-global-package-paths)
6. [Status and shutdown mapping](#status-and-shutdown-mapping)
7. [Specified compatibility edges](#specified-compatibility-edges)
8. [Acceptance scenarios](#acceptance-scenarios)
9. [Provenance](#provenance)

## Scope and output contract

This is an explicit local maintenance command, not a model turn and not the passive interactive updater. It writes human-oriented progress to ordinary standard output, failures to standard error, records updater telemetry, refreshes generated shell completions after verified mutation, and exits through the graceful-shutdown contract. It does not emit SDK/NDJSON records.

**CLI-UPD-001 — Initial report.** Record an update-check event, print the immutable running version, resolve `autoUpdatesChannel` from initial settings with latest as default, print the selected channel, then run the installation diagnostic before any update lookup or mutation.

**CLI-UPD-002 — Diagnostic visibility.** When more than one installation is found, print a warning followed by every type/path in diagnostic order and mark the actually running entry. Print every diagnostic warning with its proposed fix; do not suppress PATH warnings or convert warnings into failure by themselves.

**CLI-UPD-003 — Human channels.** Status/progress and successful results use ordinary output; lookup/install errors use standard error. Debug records may contain method and registry-command descriptions but cannot change the exit outcome. All terminal exits use graceful shutdown so cleanup and terminal restoration retain their platform contracts.

## Diagnosis and configuration reconciliation

**CLI-UPD-010 — Missing install method.** Read one global-configuration snapshot. If its install method is absent and diagnosis is not package-manager-owned, map `npm-local → local`, `native → native`, `npm-global → global`, and every other type to unknown; persist that method while preserving the remaining configuration and report the change.

**CLI-UPD-011 — Development rejection.** A development installation prints a nonfatal-looking warning but terminates the command with status 1 before version discovery or mutation.

**CLI-UPD-012 — Reality wins.** Outside package-manager ownership, when the captured configuration has a nonunknown expected method and the normalized running type differs, print both, state that the running installation will be updated, persist the normalized running method, and continue with reality. Normalize local/global/native/development/unknown explicitly; preserve another diagnostic spelling when no mapping exists.

**CLI-UPD-013 — Snapshot timing.** The command retains the configuration object captured before its own reconciliation writes. Later branches that consult this local snapshot do not automatically reread the newly stored method. A standalone implementation must preserve this intra-command ordering or deliberately version a fix.

## Package-manager-owned installations

**CLI-UPD-020 — No self-mutation.** When diagnosis says package-manager-owned, identify the manager and never invoke native, local, or global installers. Homebrew, winget, and apk each query the selected channel through the package registry; when target exists and running is semantically below it, show running-to-target and the exact manager command. Otherwise print that the product is current and exit 0.

**CLI-UPD-021 — Manager commands.** Homebrew guidance is `brew upgrade agentx-code`; winget is `winget upgrade AgentX.AgentXCode`; apk is `apk upgrade agentx-code`. Pacman, deb, rpm, mise, asdf, and unknown do not receive a guessed command; instruct the user to use their package manager and exit 0.

**CLI-UPD-022 — Lookup ambiguity.** In the three specialized manager branches, an absent lookup result takes the same displayed “up to date” path as a target not newer than running. This is a specified false-negative compatibility behavior, not proof that the installation is current.

## Native installation path

**CLI-UPD-030 — Forced native call.** For an actually native installation, invoke the native installer with the selected channel and force-reinstall true. This bypasses native maximum, floor, already-current, and public single-flight gates but retains version resolution, artifact integrity, locking, activation, and verification rules.

**CLI-UPD-031 — Native contention.** When the result says lock failure, print that another process is running, include its PID when present, ask the user to try again, and exit 0. Contention is not an install success but is deliberately nonerror for this command.

**CLI-UPD-032 — Native result.** An absent latest version without the contention branch prints failure and exits 1. If latest version string exactly equals running, print current. Otherwise print success from running to latest and regenerate completion cache. Both successful branches exit 0.

**CLI-UPD-033 — Native thrown failure.** Catch native exceptions, print a fixed install-failure line, the string form of the error, and doctor guidance, then exit 1. Do not falsely persist a command-level success after partial native mutation.

## Local and global package paths

**CLI-UPD-040 — Native-entry cleanup guard.** Before the package-based path, remove the native installed entry only when the command's captured configuration does not say native. The removal helper itself preserves entries heuristically identified as package-managed.

**CLI-UPD-041 — Registry failure detail.** Query the selected package tag. An absent result prints failure plus registry/network/proxy/firewall causes, conditionally notes an unpublished internal/custom package, suggests debug, a manual view command using configured-or-identity-default package, and registry authentication, then exits 1.

**CLI-UPD-042 — Exact current comparison.** If discovered version string exactly equals the running version, print current and exit 0. Otherwise announce it as a new version and proceed even when semantic precedence would classify it as equal build metadata or as an older selected-channel version.

**CLI-UPD-043 — Mechanism routing.** Actual npm-local selects the local installer; npm-global selects the global installer. Unknown diagnosis probes the fixed local installation and reports the fallback choice. Any other type prints that it cannot be updated and exits 1. Report the chosen local/global method before mutation.

**CLI-UPD-044 — Local invocation.** Invoke the local installer with the selected channel, allowing that mechanism to preserve its channel semantics and status normalization.

**CLI-UPD-045 — Global invocation mismatch.** Invoke the global installer without passing the discovered version or selected channel. Its package specification is therefore the bare configured package and the package manager applies its default distribution tag. Nevertheless, the command's success text names the previously discovered channel version. This specified mismatch can install a different version than the one reported, especially on stable; preserve it for compatibility tests or expose a deliberate protocol-versioned correction.

## Status and shutdown mapping

**CLI-UPD-050 — Success.** On normalized success, print running-to-discovered success, regenerate the completion cache, and exit 0 after the shared final shutdown call.

**CLI-UPD-051 — No permission.** Print insufficient permission. Local guidance enters the fixed per-user local directory and updates the configured package. Global guidance recommends repairing permissions or using elevated execution and also offers native installation. Exit 1.

**CLI-UPD-052 — Install failure.** Print failed install. Local receives its manual directory/package command; global receives native-install guidance. Exit 1.

**CLI-UPD-053 — In progress.** Print that another instance is updating and ask the user to wait, then exit 1. This differs intentionally from native contention's status 0.

**CLI-UPD-054 — Completion-cache timing.** Regenerate completion cache only after native/local/global mutation reports success and the discovered target differs from the running version. Do not refresh for current, lookup failure, contention, permission failure, or install failure.

## Specified compatibility edges

**CLI-UPD-060 — Explicit downgrade exposure.** The package path uses exact inequality, not `target > running`; a stable target older than running can be announced and sent to the local installer. Forced native similarly bypasses floor/cap. The global bare-package mismatch may instead install registry latest. These are explicit command behaviors, not automatic-update policy.

**CLI-UPD-061 — Diagnostic authority.** Multiple installations and configuration mismatch are warnings/reconciliation evidence. The command mutates only the diagnosed running mechanism except for explicitly scoped cleanup helpers. It never updates every discovered installation.

**CLI-UPD-062 — No structured contamination.** Even when invoked from a binary that otherwise supports structured modes, the standalone update command uses its declared human output and exits before a semantic headless session. It never starts the query engine merely to report maintenance progress.

## Acceptance scenarios

### `CLI-UPD-A01` — Stable global mismatch

Settings select stable, lookup returns `2.5.0`, and running is `2.4.0` under npm-global. The command announces `2.5.0`, invokes the global installer with the bare package spec, and the fake package manager records that it selected default latest `2.7.0`. On zero exit the command still reports `2.5.0`. The test records this divergence explicitly.

### `CLI-UPD-A02` — Package-manager lookup outage

Diagnosis is Homebrew and registry lookup fails. No installer runs; the specified branch prints “up to date” and exits 0. The conformance report marks this as lookup ambiguity rather than verified currency.

### `CLI-UPD-A03` — Native contention

Forced native returns lock failure with PID 42. The command names PID 42, asks to retry, refreshes no completions, and exits 0. An otherwise equivalent global `in_progress` status exits 1.

### `CLI-UPD-A04` — Unknown resolved local

Diagnosis is unknown, local-install evidence exists, and target differs. The command warns that type is unknown, states local fallback, calls the local installer with the selected channel, and maps its status without touching the global prefix.

### `CLI-UPD-A05` — Same precedence, different metadata

Running is `3.0.0+old` and discovered is `3.0.0+new`. Exact strings differ, so the explicit command proceeds and reports an update even though passive automatic comparison would not.

### `CLI-UPD-A06` — Reconciled snapshot remains old

The captured configuration has no method; diagnosis sets native and persists it. Later in the same invocation, an injected path reaches a check against the captured snapshot and still observes absence. An implementation that silently rereads must declare the compatibility correction.

## Provenance

The contract implements the update/upgrade command's observable routing, text classes, configuration writes, installer arguments, status mapping, and exit behavior. Parser implementation, terminal color library, and source module names are not normative. The referenced platform updater/native-installer contracts own mutation and verification internals.
