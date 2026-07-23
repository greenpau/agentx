# Managed policy, synchronization, and reload contract

This document is normative for managed authority selection, remote policy services, settings synchronization, and live configuration change handling. `POL-*` identifiers are stable implementation anchors.

## Contents

- [Whole-source managed policy selection](#whole-source-managed-policy-selection)
- [Platform-managed sources](#platform-managed-sources)
- [Managed settings file and fragments](#managed-settings-file-and-fragments)
- [Remote managed settings](#remote-managed-settings)
- [Remote policy limits](#remote-policy-limits)
- [Settings synchronization](#settings-synchronization)
- [Change detection and coherent reload](#change-detection-and-coherent-reload)
- [Failure matrix](#failure-matrix)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Whole-source managed policy selection

`POL-001` — Managed policy is a whole settings object selected from ordered authorities. It is not a deep merge of all available authorities.

`POL-010` — Evaluate candidate authorities in this order and select the first nonempty object that parses and passes the complete settings schema:

1. validated remote managed settings;
2. macOS managed preferences or Windows machine policy;
3. managed settings file assembled from base plus fragments;
4. Windows current-user policy, only when machine policy and managed file are absent.

An invalid higher candidate is diagnosed and selection continues. An empty candidate means no policy at that authority. Preserve origin as one of `remote`, `plist`, `hklm`, `file`, or `hkcu`.

`POL-011` — Do not combine a valid remote object with local managed fragments. Once remote wins, it is the complete `policySettings` source for that generation.

`POL-012` — Remote managed settings may be security-checked against the active policy before replacement. If a new payload introduces a forbidden dangerous change, reject the entire payload and retain the prior valid policy.

## Platform-managed sources

`POL-020` — On macOS, query managed preferences from domain `com.agentx.agentxcode`, convert the property-list value to JSON with a 5,000 ms bound, and validate the resulting object. Failure or absence allows fallback.

`POL-021` — Begin the managed-device query early and cache it. Before effective settings are first published, await its bounded result so policy cannot arrive immediately after ungoverned startup. Refresh the platform-managed query every 30 minutes and drive normal policy reload on change.

`POL-022` — On Windows, read `Settings` from `SOFTWARE\Policies\AgentXCode`. Accept machine-wide values only from string or expandable-string registry types. Current-user policy is a final fallback and is considered only when machine-wide policy and managed files are absent.

`POL-023` — Platform APIs are optional. Unsupported platforms and unavailable management utilities contribute no candidate and do not crash ordinary startup.

## Managed settings file and fragments

`POL-030` — The managed base file is `managed-settings.json` in the platform's managed configuration location. The sibling fragment directory is `managed-settings.d`.

`POL-031` — Assemble the file candidate as follows:

1. start with the base object, or empty if absent;
2. enumerate only non-hidden regular files or symbolic links in the fragment directory;
3. sort fragment names lexicographically using a deterministic byte/case policy;
4. parse and validate each fragment object;
5. merge valid fragments in order using recursive object merge, with each later fragment replacing scalar conflicts;
6. validate the fully assembled settings object.

Malformed fragments are reported with their path. They do not become partially effective. An implementation must choose one explicit behavior—exclude only the malformed fragment before final validation, matching the specified behavior—rather than discarding unrelated valid fragments.

`POL-032` — Hidden files, directories, sockets, and unrelated filesystem entries are ignored. Resolve symlinks only under the managed-location security policy; never follow an untrusted fragment into ordinary project content.

## Remote managed settings

`POL-040` — Remote managed settings are available only for eligible first-party authenticated accounts and managed builds. Fetch from the product's managed-settings service using the selected first-party base URL and current inference credential.

`POL-041` — Network response schema:

```text
{
  uuid: nonempty string,
  checksum: nonempty string,
  settings: complete settings object
}
```

Reject unknown top-level shapes that would make versioning ambiguous; validate `settings` with the same full policy schema as local managed sources.

`POL-042` — Timing and retry constants:

| Property | Value |
| --- | --- |
| per-request timeout | 10 seconds |
| retries after first attempt | 5 |
| startup wait budget | 30 seconds |
| refresh interval | 1 hour |
| cache file | configuration cache `/remote-settings.json` |
| cache mode | owner read/write only (`0600` equivalent) |

`POL-043` — A valid cache is read before or concurrently with the network request and may unblock startup. Network refresh continues in the background. Persist cache atomically, flush durable data, and never expose partial JSON.

`POL-044` — HTTP semantics:

| Response | Effect |
| --- | --- |
| `200` | validate, security-check, persist, and apply if changed |
| `304` | retain cache/current value |
| `204` or `404` | authoritative absence; clear stale cache and remote candidate |
| authentication failure | do not retry as a transient request |
| timeout/transport/5xx after retries | retain last validated cache/current value |

`POL-045` — A changed valid remote payload triggers the same coherent policy reload pipeline as a managed file edit. Do not mutate only the fields noticed by the polling service.

## Remote policy limits

`POL-050` — Policy limits are separate from managed settings. Fetch `${FIRST_PARTY_BASE_URL}/api/agentx/policy_limits` only for an eligible first-party API key or first-party inference OAuth account in a team or enterprise context.

`POL-051` — Response schema:

```text
{
  restrictions: {
    <feature-name>: { allowed: boolean }
  }
}
```

Unknown restrictions are retained for forward compatibility but only known consumers enforce them. An absent key means allowed.

`POL-052` — Use the same 10-second timeout, five retries after the first attempt, one-hour polling interval, 30-second startup wait, and owner-only cache permissions as remote settings. Store in `/policy-limits.json`.

`POL-053` — Compute a cache validator by recursively sorting object keys, serializing compact JSON, hashing with SHA-256, and sending `If-None-Match: "sha256:<hex>"`.

`POL-054` — HTTP semantics:

| Response | Effect |
| --- | --- |
| `200` | validate and atomically cache restrictions |
| `304` | use cache unchanged |
| `404` | use empty restrictions and remove stale cache |
| authentication failure | no retry; use a valid stale cache if present |
| other exhausted failure | use valid stale cache; if none, use domain default |

`POL-055` — With no cache and no response, restrictions generally fail open because an unknown feature is allowed. A consumer may explicitly define an essential-only privacy default; the specified feedback consumer defaults to disallowed in that narrow state. Document each such exception beside the consuming feature.

## Settings synchronization

`POL-060` — Cross-device settings sync is distinct from managed policy. It is available only to eligible first-party OAuth inference sessions under its feature gates and never outranks local source precedence.

`POL-061` — Endpoint and response schema:

```text
GET/PUT /api/agentx/user_settings
{
  userId: string,
  version: value identifying revision,
  lastModified: ISO-8601 timestamp,
  checksum: MD5 digest string,
  content: { entries: { <allowed-key>: string } }
}
```

`POL-062` — Synchronizable keys are an explicit allowlist:

- the user settings file and user instruction file;
- repository-hash-qualified project-local settings and local instruction file for the current repository.

Reject unexpected keys, non-string values, and any file over 500 KiB. Never interpret a server-supplied key as an arbitrary path.

`POL-063` — Fetch first, then upload only allowlisted entries whose local string differs from the remote string. Do not propagate deletions. This is a convergence aid, not a general filesystem mirror.

`POL-064` — Use a 10-second request timeout and three retries. Mark downloaded writes as internal before atomic persistence, then reset settings/instruction caches through the central invalidation path.

`POL-065` — Synchronization failure is nonfatal and fail-open for local work. Retain local files, emit diagnostics without content, and retry only on the next scheduled/explicit sync boundary.

## Change detection and coherent reload

`POL-070` — Watch settings, managed fragments, and other registered configuration inputs using a platform watcher plus bounded polling fallback. Constants:

| Property | Value |
| --- | --- |
| stability window before read | 1,000 ms |
| polling cadence when active | 500 ms |
| internal-write suppression lifetime | 5,000 ms |
| deletion handling | grace period sufficient for atomic replace |

`POL-071` — Coalesce add/change/unlink bursts by canonical path and generation. A transient unlink followed by replacement within the grace period is one change, not a reset-to-empty generation followed by another generation.

`POL-072` — Internal writers register canonical path and expected time before flush. The detector consumes matching events once the write is observed. Expire stale marks after five seconds so a later user edit cannot be suppressed.

`POL-073` — External change pipeline:

```text
filesystem evidence
  -> stability/debounce
  -> read and validate candidate snapshot
  -> emit ConfigChange hooks with source and optional path
  -> if any blocking result: keep current generation
  -> central cache reset
  -> atomically publish new settings generation
  -> update dependent services and emit one change notification
```

`POL-074` — `ConfigChange` hook exit code 2 or equivalent structured block suppresses application of the candidate. It does not rewrite or delete the external file. Diagnostics must make the divergence between disk and active state visible.

`POL-075` — Perform one central reset before fanout. Individual subscribers must not each reread different source versions. A subscriber either accepts the published generation or retains its prior startup-only value.

`POL-076` — Typical hot-reload consumers include environment projection, permission rules, sandbox settings, hooks, extension enablement/removal, certificate/proxy caches, and prompt/skill listings. Their owning skills define whether additions take effect immediately or on session rebuild.

`POL-077` — Watcher failure degrades to polling. Polling failure retains the current generation and reports degraded reload; it does not repeatedly publish empty configuration.

## Failure matrix

| Failure | Active behavior | Recovery |
| --- | --- | --- |
| remote malformed payload | retain valid cache/previous or fall through | next poll |
| local managed base malformed | try next policy authority if available | next file change/startup |
| one fragment malformed | exclude fragment, report path, validate remainder | next file change |
| cache malformed | ignore cache; do not delete until authoritative response | network fetch/manual repair |
| hook blocks reload | retain active generation | next external change |
| watcher unavailable | poll | restart watcher or continue polling |
| sync server unavailable | local files remain authoritative | later sync |

## Acceptance scenarios

**POL-A01 — Whole-source fallback.** Remote settings are malformed, macOS managed preferences are valid, and managed files also exist. The plist object is the entire policy source; managed files are not merged beneath it.

**POL-A02 — Managed fragment order.** Base managed settings define an object and two lexical fragments override different fields. The final file candidate applies base, fragment A, then fragment B and reports per-fragment diagnostics.

**POL-A03 — Cached startup and late refresh.** Startup has a valid remote cache but the request exceeds 30 seconds. Startup uses the cache, continues, and applies a later valid changed response through one `ConfigChange` generation.

**POL-A04 — Policy-limit validator.** A policy-limit cache has a valid ETag hash and server returns 304. No cache rewrite or feature-state churn occurs.

**POL-A05 — Watch coalescing and internal suppression.** An atomic user settings editor emits unlink/add/change. The detector waits for stability and publishes one generation. The same editor's registered internal write produces no external reload hook.

**POL-A06 — Hook-blocked reload.** A `ConfigChange` hook blocks a managed fragment edit. Disk retains the administrator's edit, active policy remains prior, and the user receives a clear divergence diagnostic.

**POL-A07 — Sync path containment.** Sync returns one allowlisted changed entry and one arbitrary path key. The arbitrary key is rejected; the allowlisted file is written internally and cannot trigger a duplicate sync/reload loop.

## Non-normative provenance

Reference behavior was specified from settings source loaders and writers, managed-settings platform adapters, remote managed settings and policy-limit pollers, settings synchronization, change detection, policy-difference checks, and hook integration under `utils/settings/`, `services/remoteManagedSettings/`, `services/policyLimits/`, and `services/settingsSync/`. Paths and symbol names are provenance only.
