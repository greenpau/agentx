# Versioned Observability Wire Catalog

This catalog is normative for the generated event records that cross the
observability serialization boundary. It restates the wire vocabulary without
requiring a particular protocol compiler, object system, or implementation
language. Event admission, redaction, buffering, and sink authority remain
governed by the main observability contract.

## Contents

1. [Common codec rules](#common-codec-rules)
2. [Authentication and environment records](#authentication-and-environment-records)
3. [User metadata assembly](#user-metadata-assembly)
4. [Internal event](#internal-event)
5. [Experiment assignment event](#experiment-assignment-event)
6. [Evolution, failure, and disabled behavior](#evolution-failure-and-disabled-behavior)
7. [Acceptance scenarios](#acceptance-scenarios)

## Common codec rules

**OBS-WIRE-001 — Optional-field model.** Every field in this catalog is
wire-optional. Decoding a missing or explicit null scalar produces its scalar
default: empty string, zero, or false. A missing nested record or timestamp is
absent; a missing repeated field is an empty list. Consumers must not infer
that a default value was explicitly supplied.

**OBS-WIRE-002 — JSON names and unknown fields.** JSON uses the exact
snake-case names below. Unknown input fields are ignored and are not retained
when the record is emitted again. Adding an optional field is therefore
forward-compatible for an older reader but not lossless through that reader.
Never use this codec as an unknown-field-preserving proxy.

**OBS-WIRE-003 — Scalar conversion.** A present string, Boolean, or number is
converted to the corresponding declared scalar before storage. Integer-valued
numeric fields are rounded when emitted as JSON. The generated compatibility
codec does not independently enforce identifier ranges, timestamp range, JSON
string payload shape, or semantic enum membership; validate those at event
admission.

**OBS-WIRE-004 — Timestamp JSON.** A timestamp accepts an already parsed time,
an RFC-3339/ISO-style string accepted by the platform time parser, or an object
with `seconds` since the Unix epoch and non-negative `nanos`. Object conversion
computes `seconds * 1000 + nanos / 1,000,000` milliseconds. JSON output is a UTC
ISO string. The portable timestamp record declares seconds in years 0001–9999
and nanos in 0–999,999,999, but the compatibility codec itself does not reject
out-of-range values; an interoperable implementation validates them at ingress
or explicitly documents compatibility coercion.

**OBS-WIRE-005 — Partial construction.** Constructing from a partial record
recursively fills the same defaults as decoding. It rejects undeclared fields
at the typed API boundary where the host language supports that check. Runtime
JSON still follows `OBS-WIRE-002`.

## Authentication and environment records

**OBS-WIRE-010 — `PublicApiAuth`.** Authentication context is server/API
injected and contains:

| JSON field | Type | Default |
| --- | --- | --- |
| `account_id` | integer number | `0` |
| `organization_uuid` | string | empty |
| `account_uuid` | string | empty |

These fields are sensitive identifiers. Client event producers do not invent
them from prompts or arbitrary settings, and ordinary diagnostic rendering
does not expose them.

**OBS-WIRE-011 — `GitHubActionsMetadata`.** The nested CI record contains
optional string identifiers `actor_id`, `repository_id`, and
`repository_owner_id`, each defaulting to empty. It is meaningful only when the
containing environment identifies a GitHub Actions execution, but the codec
does not enforce that conditional.

**OBS-WIRE-012 — `EnvironmentMetadata`.** The environment record has these
exact optional fields:

| Field group | JSON fields | Types |
| --- | --- | --- |
| Runtime | `platform`, `node_version`, `terminal`, `package_managers`, `runtimes`, `version`, `arch`, `version_base`, `build_time`, `vcs`, `platform_raw` | strings |
| Runtime flags | `is_running_with_bun`, `is_ci`, `is_claubbit`, `is_github_action`, `is_agentx_action`, `is_agentx_ai_auth`, `is_agentx_remote`, `is_conductor`, `is_local_agent_mode` | Booleans |
| GitHub Action | `github_event_name`, `github_actions_runner_environment`, `github_actions_runner_os`, `github_action_ref` | strings |
| GitHub identity | `github_actions_metadata` | `GitHubActionsMetadata`, absent by default |
| Remote placement | `remote_environment_type`, `agentx_container_id`, `agentx_remote_session_id`, `coworker_type` | strings |
| Linux/WSL | `wsl_version`, `linux_distro_id`, `linux_distro_version`, `linux_kernel` | strings |
| Deployment | `deployment_environment` | string |
| Tags | `tags` | repeated strings, empty by default |

The field name `node_version` and flag `is_running_with_bun` are compatibility
wire names, not a requirement that an implementation use either runtime.

## User metadata assembly

**OBS-USER-001 — Nonblocking email state.** Keep email discovery in three states: `not-fetched`, `fetching(completion-handle)`, and `settled(optional-email)`. Early initialization starts at most one fetch, awaits it, publishes the optional result, clears the in-flight handle, and invalidates memoized user projections. Synchronous metadata reads never launch or wait for a subprocess; before initialization settles they use an immediately available OAuth/internal value or omit email.

**OBS-USER-002 — Cache reset and race.** Login, logout, or account switch resets email to `not-fetched`, drops the stored in-flight reference, and clears both core-user and repository-email memoization. The specified reset does not cancel or generation-fence a fetch already being awaited by the initializer; that older fetch can later repopulate email and clear core memoization after the account change. Preserve this observable race for strict compatibility or add a generation token as a documented privacy hardening.

**OBS-USER-003 — Core projection.** The core record contains the durable anonymous device identifier, current session identifier, application version, normalized analytics platform, optional email, optional OAuth organization/account identifiers, and optional user-type environment label. Include organization/account data only from the authentication subsystem's currently active OAuth account; cached profile data does not prove that OAuth is the active credential.

**OBS-USER-004 — Analytics enrichment.** The ordinary core projection omits subscription type, rate-limit tier, and first-token time. The feature-evaluation/analytics projection requests them explicitly. Parse first-token time from durable global configuration only when subscription type is present and the timestamp is valid; an invalid date is omission, not zero.

**OBS-USER-005 — Email precedence and privacy.** Email precedence is active OAuth account, then an internal employee-only creator environment transformed to the corporate address form, then an asynchronous repository `user.email` lookup for internal users only. External/API users never run the repository fallback. A successful repository command requires exit zero and nonempty output, then trims it; failure yields absence and is memoized for the process generation. Email remains sensitive and is admitted only to eligible sinks under `OBS-004` and `OBS-WIRE-A04`.

**OBS-USER-006 — CI metadata.** Only when the standard truthy parser accepts the GitHub Actions indicator, attach optional actor, actor ID, repository, repository ID, owner, and owner ID environment values. Preserve absence per field. Do not infer CI identity from a repository checkout or forward unrelated environment variables.

**OBS-USER-007 — Projection memoization.** Memoize independently by the enrichment flag so ordinary and analytics projections cannot contaminate one another. Session/auth/email changes must use the declared reset path; a cache hit is never authority to retain a prior account after that reset.

**OBS-WIRE-013 — `SlackContext`.** The nested collaboration context contains
`slack_team_id` string, `is_enterprise_install` Boolean, `trigger` string, and
`creation_method` string. Event-specific Slack measurements remain in the
parent event's additional metadata rather than extending this common record.

## Internal event

**OBS-WIRE-020 — `AgentXCodeInternalEvent`.** The internal analytics event has
these exact optional fields:

| Field group | JSON fields | Types/defaults |
| --- | --- | --- |
| Event identity | `event_name`, `event_id` | strings/empty |
| Time | `client_timestamp`, `server_timestamp` | timestamps/absent |
| Session/request context | `session_id`, `parent_session_id`, `device_id`, `entrypoint`, `client_type`, `agent_sdk_version`, `is_interactive` | strings/empty except Boolean false |
| Model/profile | `model`, `user_type`, `betas` | strings/empty |
| Environment | `env` | `EnvironmentMetadata`/absent |
| Process and variable metadata | `process`, `additional_metadata` | JSON-encoded strings/empty |
| Injected authentication | `auth` | `PublicApiAuth`/absent |
| Benchmark | `swe_bench_run_id`, `swe_bench_instance_id`, `swe_bench_task_id` | strings/empty |
| User/contact | `email` | string/empty; sensitive |
| Agent/team | `agent_id`, `agent_type`, `team_name` | strings/empty |
| Extension attribution | `skill_name`, `plugin_name`, `marketplace_name` | strings/empty |
| Collaboration | `slack` | `SlackContext`/absent |

`process` and `additional_metadata` are strings containing JSON, not arbitrary
nested wire maps. Validate, bound, and redact before serialization; a receiver
does not execute or recursively trust their content.

**OBS-WIRE-021 — Internal-event defaults and privacy.** A decoded record with
missing scalars contains compatibility defaults and may emit those defaults on
a subsequent JSON conversion. Sink routing must still apply privacy and
cardinality policy field by field. Generated-schema presence never authorizes
`email`, account/organization IDs, repository IDs, remote session/container
IDs, process measurements, or free-form metadata for a destination.

## Experiment assignment event

**OBS-WIRE-030 — `GrowthbookExperimentEvent`.** An experiment exposure contains:

| JSON field | Type/default | Meaning |
| --- | --- | --- |
| `event_id` | string/empty | deduplication identity |
| `timestamp` | timestamp/absent | exposure time |
| `experiment_id` | string/empty | experiment tracking key |
| `variation_id` | integer number/0 | `0` control, positive values variants |
| `environment` | string/empty | evaluation environment |
| `user_attributes` | JSON-encoded string/empty | bounded attributes at exposure |
| `experiment_metadata` | JSON-encoded string/empty | bounded experiment metadata |
| `device_id` | string/empty | device identity |
| `auth` | `PublicApiAuth`/absent | API-injected authentication context |
| `session_id` | string/empty | session identity |
| `anonymous_id` | string/empty | unauthenticated identity |
| `event_metadata_vars` | JSON-encoded string/empty | event-library metadata variables |

**OBS-WIRE-031 — Exposure validation.** Before export, require an admissible
experiment key and variation, apply exposure deduplication, validate each
JSON-encoded string as bounded data, and enforce privacy eligibility for user,
device, session, anonymous, and injected authentication identifiers. Codec
success alone is not event admission.

## Evolution, failure, and disabled behavior

**OBS-WIRE-040 — Evolution.** Add only optional fields to these record versions.
A discriminator or semantic reinterpretation requires a new versioned record
or an explicit migration. Older readers may drop new fields under
`OBS-WIRE-002`; callers that require lossless forwarding must use another
envelope or preserve the original bytes separately.

**OBS-WIRE-041 — Malformed input.** A malformed timestamp, wrong nested record,
invalid encoded-JSON string, non-finite number, or policy-forbidden identifier
is rejected or sanitized by the admission layer with bounded diagnostics. It
never affects the semantic action that produced the observation.

**OBS-WIRE-042 — Disabled exporter.** When generated event export is absent,
build-excluded, opted out, or sink-disabled, do not construct a sensitive
payload solely for discard. Local authoritative usage and semantic session
events continue unchanged.

## Acceptance scenarios

### `OBS-WIRE-A01` — Sparse decode

Decode an empty internal-event object. All scalar fields take compatibility
defaults, nested records and timestamps remain absent, and no consumer treats
an empty identifier as authenticated or explicitly supplied.

### `OBS-WIRE-A02` — Unknown optional field

Decode an experiment event containing one future field, then emit JSON. Known
fields survive with their documented conversion; the future field is absent.
The adapter reports that this is a lossy old-reader path rather than claiming
unknown-field preservation.

### `OBS-WIRE-A03` — Timestamp forms

Decode the same instant from an accepted UTC string and from seconds/nanos,
then emit both as the same UTC ISO representation. Reject or diagnose an
out-of-range nanos value at admission even though the compatibility codec can
arithmetically convert it.

### `OBS-WIRE-A04` — Sensitive field routing

Create an internal event containing email, injected auth, repository identity,
remote session identity, and free-form additional metadata. An ineligible sink
receives none of those fields; the semantic turn and local usage accounting are
identical to a run with no exporter.

### `OBS-WIRE-A05` — Integer conversion

Decode a numeric account or variation value, emit JSON, and verify the declared
integer field is rounded. Non-finite and semantically invalid values fail
admission rather than becoming unrestricted analytics labels.

### `OBS-WIRE-A06` — Disabled build

Build without the generated exporter. No generated payload, auth record, or
disk retry record is produced. The same user request has identical model,
tool, transcript, cost, and exit outcomes.

### `OBS-WIRE-A07` — User metadata refresh

Read core metadata before email initialization, then initialize with a failing repository command and read again; both reads remain nonblocking and omit email. Reset after an OAuth account switch, return a new OAuth email, and verify both ordinary and analytics memoized projections use the new account while only the analytics projection contains subscription/rate-limit/valid-first-token fields. Repeat outside GitHub Actions and verify no CI record is emitted.

## Non-normative provenance

The field vocabulary and compatibility conversion rules were specified from
the generated protocol records for internal client events, experiment
exposures, public API authentication context, and the portable timestamp type.
Those generated files are evidence only; the contracts above are the
language-neutral implementation source.
