# Model and Provider Catalog Contract

This reference defines the language-neutral model-selection subsystem used by the query runtime. It covers the current compatibility catalog, alias and provider resolution, policy allowlists, model options, capability predicates, context and output limits, validation, deprecation, fallback, and delegated-model inheritance. “Must” denotes reference-compatible behavior. Values described as server data are deliberately not promoted into permanent client constants.

## Table of contents

- [Responsibility and data boundaries](#responsibility-and-data-boundaries)
- [Provider selection and compatibility catalog](#provider-selection-and-compatibility-catalog)
- [Canonicalization and display](#canonicalization-and-display)
- [Selection precedence and alias resolution](#selection-precedence-and-alias-resolution)
- [Administrative allowlist](#administrative-allowlist)
- [Provider overrides and Bedrock discovery](#provider-overrides-and-bedrock-discovery)
- [Availability axes and extended context](#availability-axes-and-extended-context)
- [Capability predicates](#capability-predicates)
- [Context and output limits](#context-and-output-limits)
- [Model-picker options](#model-picker-options)
- [Validation, errors, and deprecation](#validation-errors-and-deprecation)
- [Delegated, skill, teammate, and auxiliary models](#delegated-skill-teammate-and-auxiliary-models)
- [Overload fallback](#overload-fallback)
- [Cost attribution](#cost-attribution)
- [Acceptance scenarios](#acceptance-scenarios)
- [Provenance](#provenance)

## Responsibility and data boundaries

**MOD-001 — One resolution pipeline.** Treat a model setting, a concrete provider model identifier, a canonical family/version, a display label, and an API wire value as different data. A caller may carry the original setting for persistence and display while sending a resolved provider identifier. Never use a display label as a provider identifier.

**MOD-002 — Catalog snapshot versus service data.** The static compatibility catalog below is the exact client snapshot. Additional picker entries, internal model profiles, account eligibility, and internal capability metadata are service-controlled data. Validate their documented shapes and cache semantics, but do not invent or permanently hard-code service-returned model identifiers.

**MOD-003 — Independent availability axes.** A model may be present in the client catalog yet unusable because of provider deployment, account entitlement, organization policy, context entitlement, build/runtime feature gates, authentication, or current service availability. Conversely, a custom provider deployment may be usable without appearing in the static catalog. Keep these axes independent.

**MOD-004 — Session initialization boundary.** Resolve the provider before initializing provider model strings. The reference does not support changing provider selection after catalog initialization: provider predicates are read dynamically, but the initialized model-string table is not rebuilt. An implementation should either prohibit that mutation or preserve the resulting compatibility limitation; it must not silently mix providers.

## Provider selection and compatibility catalog

**MOD-010 — Provider precedence.** Select exactly one API provider using the runtime's standard truthy environment parser. Bedrock wins over Vertex, Vertex wins over Foundry, and Foundry wins over the first-party API. If no provider selector is true, select first-party. Conflicting provider selectors do not produce an error.

**MOD-011 — First-party endpoint predicate.** An unset custom base URL is first-party. Otherwise parse it as a URL and compare the complete host exactly with `api.agentx.com`; internal users may also use `api-staging.agentx.com`. A port, sibling subdomain, invalid URL, or other host is not first-party. This predicate affects internal capability discovery and is separate from the provider selector.

**MOD-012 — Static compatibility catalog.** Store the catalog as one version-by-provider relation. The first-party column is also the canonical override key. Preserve these exact current values:

| Version key | First-party / canonical ID | Bedrock ID | Vertex ID | Foundry ID |
| --- | --- | --- | --- | --- |
| Haiku 3.5 | `agentx-3-5-haiku-20241022` | `us.agentx.agentx-3-5-haiku-20241022-v1:0` | `agentx-3-5-haiku@20241022` | `agentx-3-5-haiku` |
| Haiku 4.5 | `agentx-haiku-4-5-20251001` | `us.agentx.agentx-haiku-4-5-20251001-v1:0` | `agentx-haiku-4-5@20251001` | `agentx-haiku-4-5` |
| Sonnet 3.5 | `agentx-3-5-sonnet-20241022` | `agentx.agentx-3-5-sonnet-20241022-v2:0` | `agentx-3-5-sonnet-v2@20241022` | `agentx-3-5-sonnet` |
| Sonnet 3.7 | `agentx-3-7-sonnet-20250219` | `us.agentx.agentx-3-7-sonnet-20250219-v1:0` | `agentx-3-7-sonnet@20250219` | `agentx-3-7-sonnet` |
| Sonnet 4 | `agentx-sonnet-4-20250514` | `us.agentx.agentx-sonnet-4-20250514-v1:0` | `agentx-sonnet-4@20250514` | `agentx-sonnet-4` |
| Sonnet 4.5 | `agentx-sonnet-4-5-20250929` | `us.agentx.agentx-sonnet-4-5-20250929-v1:0` | `agentx-sonnet-4-5@20250929` | `agentx-sonnet-4-5` |
| Sonnet 4.6 | `agentx-sonnet-4-6` | `us.agentx.agentx-sonnet-4-6` | `agentx-sonnet-4-6` | `agentx-sonnet-4-6` |
| Opus 4 | `agentx-opus-4-20250514` | `us.agentx.agentx-opus-4-20250514-v1:0` | `agentx-opus-4@20250514` | `agentx-opus-4` |
| Opus 4.1 | `agentx-opus-4-1-20250805` | `us.agentx.agentx-opus-4-1-20250805-v1:0` | `agentx-opus-4-1@20250805` | `agentx-opus-4-1` |
| Opus 4.5 | `agentx-opus-4-5-20251101` | `us.agentx.agentx-opus-4-5-20251101-v1:0` | `agentx-opus-4-5@20251101` | `agentx-opus-4-5` |
| Opus 4.6 | `agentx-opus-4-6` | `us.agentx.agentx-opus-4-6-v1` | `agentx-opus-4-6` | `agentx-opus-4-6` |

Adding or removing a version must update this relation, canonical IDs, canonical-to-version lookup, display names, defaults, capability predicates, option lists, fallback suggestions, deprecation metadata, output limits, and prices as one compatibility change.

**MOD-013 — Provider client boundary.** Provider selection chooses the corresponding transport/authentication adapter and the provider column above. Transport credentials, endpoints, proxying, and region discovery belong to the authentication/network contract. Model selection supplies the provider-specific identifier and must not reach through that boundary to reinterpret credentials.

## Canonicalization and display

**MOD-020 — Override reversal first.** Before canonicalizing a concrete identifier, compare it exactly against each active model-override value. If one matches, replace it with that override's canonical first-party key. This comparison is case-sensitive and exact; the first matching entry wins. If initial settings are not available yet, leave the identifier unchanged.

**MOD-021 — Ordered canonicalization.** Lowercase the result of MOD-020, then match the most specific known version before a broader family. The order is Opus 4.6, 4.5, 4.1, 4; Sonnet 4.6, 4.5, 4; Haiku 4.5; Sonnet 3.7, Sonnet 3.5, Haiku 3.5; then AgentX 3 Opus, Sonnet, and Haiku. Return the corresponding date/provider-neutral name. If no known version matches, extract the leading AgentX family token when possible; otherwise return the lowercased input unchanged.

**MOD-022 — Canonicalization is descriptive.** Canonicalization does not prove existence, entitlement, or API compatibility. Substring matching intentionally recognizes dated first-party IDs, provider-prefixed IDs, and Vertex suffixes. An arbitrary Foundry deployment name may remain unknown even when it routes to a known model.

**MOD-023 — Wire normalization.** Immediately before constructing a model API request, remove every case-insensitive `[1m]` or `[2m]` marker from the selected identifier. Context selection and beta/header selection occur before or alongside this projection. Never persist the stripped value back over the user's setting merely because the provider wire omits the marker.

**MOD-024 — Public display.** Known concrete catalog values have stable labels: `Opus 4`, `Opus 4.1`, `Opus 4.5`, `Opus 4.6`, `Sonnet 3.5`, `Sonnet 3.7`, `Sonnet 4`, `Sonnet 4.5`, `Sonnet 4.6`, `Haiku 3.5`, and `Haiku 4.5`; supported `[1m]` variants append a 1M-context qualifier. Unknown public values display verbatim. A public author/attribution name is `AgentX ` plus the known label, or `AgentX (` plus the exact unknown identifier plus `)`.

**MOD-025 — Marketing-name boundary.** Marketing names may be derived from canonical IDs for first-party, Bedrock, and Vertex. Return no marketing name on Foundry because deployment identifiers are user-defined and need not reveal the backing model. This affects friendly custom-option labels and “newer version available” hints, not request routing.

**MOD-026 — Internal display masking.** Internal service-provided models may have an alias, label, and concrete identifier. For an otherwise unknown internal model, mask all but the first three characters of the first dash-delimited codename segment, preserve later segments, and append `[1m]` when effective. Public catalog names remain unmasked.

## Selection precedence and alias resolution

**MOD-030 — User-setting precedence.** Resolve the main model setting in this order:

1. live session override, including a `/model` change or the startup `--model` value;
2. startup main-thread agent model when no explicit startup model exists and the agent did not request `inherit`;
3. `AGENTX_MODEL`;
4. merged settings `model`;
5. built-in account/provider default.

The runtime stores items 1 and 2 in the same session-override slot. A present null override means “use the built-in default” and blocks lower-precedence environment/settings values. A truthy selected value rejected by the administrative allowlist is ignored, and selection falls through to the built-in default rather than terminating startup.

**MOD-031 — Alias vocabulary.** The advertised aliases are `sonnet`, `opus`, `haiku`, `best`, `sonnet[1m]`, `opus[1m]`, and `opusplan`. Alias comparison during parsing is case-insensitive after trimming. The parser recognizes a trailing `[1m]` on any base alias even when that combined spelling is not advertised, but picker validation treats only the advertised list as automatically valid.

**MOD-032 — Alias meanings.** Resolve aliases against the current provider catalog and tier defaults:

- `sonnet` selects the default Sonnet, retaining a parsed trailing `[1m]`;
- `opus` selects the default Opus, retaining a parsed trailing `[1m]`;
- `haiku` selects the default Haiku, retaining a parsed trailing `[1m]` even though Haiku is not declared 1M-capable;
- `best` selects the default Opus and discards a parsed 1M marker;
- `opusplan` selects the default Sonnet outside the runtime plan-mode rule, retaining a parsed trailing `[1m]` in the parsed concrete setting.

For non-alias custom identifiers, preserve original case and surrounding-internal spelling after trimming. Normalize only a trailing `[1m]` marker to lowercase.

**MOD-033 — Tier defaults.** A tier-specific environment default overrides all built-in provider values. Without it:

- default Opus is Opus 4.6 on every provider;
- default Sonnet is Sonnet 4.6 on first-party and Sonnet 4.5 on Bedrock, Vertex, and Foundry;
- default Haiku is Haiku 4.5 on every provider;
- the small/fast auxiliary model is `AGENTX_SMALL_FAST_MODEL` when present, otherwise default Haiku.

**MOD-034 — Main default by account.** Internal users use the server-configured internal default when supplied, otherwise default Opus with `[1m]`. Max and Team Premium subscribers use default Opus, with `[1m]` when the merged-Opus predicate is true. Pro, Team Standard, Enterprise, first-party API/pay-as-you-go, and third-party users use default Sonnet. Resolve this setting through MOD-032 before transport.

**MOD-035 — Merged Opus predicate.** Merged Opus 1M is false when global 1M disable is active, for Pro subscribers, or outside first-party. It is also false for a AgentX Cloud subscriber whose subscription type is unknown. Otherwise it is true. This predicate is reused by defaults, options, and migration; do not replace it with a Max-only test.

**MOD-036 — Runtime plan-mode substitution.** Select a runtime model on every query iteration from the base main-loop model and current permission mode:

- if the selected user setting is exactly `opusplan`, permission mode is plan, and the latest assistant usage does not exceed 200,000 tokens, use default Opus without adding `[1m]`;
- if the selected user setting is exactly `haiku` and permission mode is plan, use default Sonnet;
- otherwise use the base main-loop model unchanged.

When an `opusplan` plan already exceeds 200,000 tokens, stay on its parsed base Sonnet model so a switch to 200K Opus does not invalidate the active context. Provider IDs or non-exact spellings that merely belong to Haiku/Opus do not trigger these substitutions.

**MOD-037 — Session override display and persistence.** Keep the base model and any temporary session-only model distinct. A user picker selection clears a temporary plan/remote session model. Changing the base model to a non-null value persists that exact setting in user settings and updates the live override; changing it to null removes the user setting and sets an explicit default override. A model picker embedded for another setting may opt out of this global settings write.

## Administrative allowlist

**MOD-040 — Default semantics.** An absent `availableModels` setting allows every user-specified model. An empty list blocks every user-specified model but never removes the picker’s null-valued Default option. Consequently, a denied saved/CLI model falls back to the built-in default even when the default's concrete identifier is not listed.

**MOD-041 — Normalization and override handling.** Before matching, reverse an exact model override using MOD-020, trim the candidate and entries, and compare lowercase values. Do not otherwise canonicalize provider prefixes. A family wildcard can match a provider-form identifier by substring, while a version shorthand generally cannot match a provider prefix unless the candidate was first reversed through a configured override.

**MOD-042 — Family wildcards.** `opus`, `sonnet`, and `haiku` are family wildcard entries. A wildcard matches when the candidate contains the family token or when a candidate alias resolves to a concrete identifier containing it.

**MOD-043 — Family narrowing.** A family wildcard is disabled whenever the same allowlist contains any non-family entry whose text contains that family followed by end-of-entry or `-`. Thus `['opus', 'opus-4-5']` allows only the specific Opus 4.5 prefix, not every Opus. The scan does not require the family token to begin the entry; preserve this compatibility behavior.

**MOD-044 — Direct and alias matching.** Accept a direct normalized equality unless it is a narrowed family wildcard. For a non-family alias such as `best` or `opusplan`, support bidirectional matching: resolve a candidate alias and compare with entries, and resolve an alias entry and compare with the candidate. Family aliases use MOD-042 instead.

**MOD-045 — Version-prefix matching.** For each non-alias, non-family entry, compare it with the candidate or candidate alias resolution as a start prefix ending at the candidate end or immediately before `-`. Also try adding `agentx-` to an entry that lacks it. This makes `opus-4-5` and `agentx-opus-4-5` match dated builds but not `opus-4-50`.

**MOD-046 — Full-ID compatibility edge.** Although the settings description calls a full model ID exact, the implemented rule is MOD-045 for every non-alias entry. Therefore a listed full ID also matches that text followed by another dash-delimited suffix. Preserve this in compatibility tests or document a deliberate policy-tightening divergence.

**MOD-047 — Enforcement points.** Apply the allowlist before startup user selection, custom-model validation, and picker-option display. The null Default option bypasses it. Known aliases are not exempt. Never issue a validation network request for an allowlist-denied value.

## Provider overrides and Bedrock discovery

**MOD-050 — Settings override shape.** `modelOverrides` is a string-to-string settings object. Only keys exactly equal to a canonical first-party ID in MOD-012 take effect; unknown keys and empty values are ignored. An effective value replaces the provider-derived identifier for that version on every catalog read. The settings schema does not validate that the replacement is a valid identifier or belongs to the active provider.

**MOD-051 — Bedrock profile discovery.** Bedrock asynchronously lists all pages of system-defined inference profiles from the same region used by the Bedrock client. Keep profiles whose identifiers contain `agentx`. For each static version, select the first returned profile whose identifier contains that version's canonical first-party ID; if none matches, retain the static Bedrock ID.

**MOD-052 — Initialization timing.** Non-Bedrock catalogs initialize synchronously. Bedrock starts one serialized, memoized profile fetch and, while it is pending, serves static Bedrock IDs plus settings overrides. A caller that needs stable picker values must await catalog initialization. A fetch error or empty profile list settles the catalog to static IDs; it does not block startup or schedule an automatic retry in that process.

**MOD-053 — Override order.** Apply settings overrides after both static provider selection and Bedrock profile discovery, including while discovery is pending. Thus an administrative override wins over a discovered inference profile.

**MOD-054 — ARN extraction.** For an identifier beginning with `arn:`, treat the text after its last `/` as the effective model/profile ID. If no slash exists, leave it unchanged. Non-ARN identifiers are unchanged.

**MOD-055 — Bedrock region prefix.** Recognize only `us`, `eu`, `apac`, and `global` when immediately followed by `.agentx.` in the effective ID. Applying a prefix replaces an existing recognized prefix, adds it to a foundation ID beginning `agentx.`, and leaves all other formats unchanged.

**MOD-056 — Discovery authentication boundary.** Profile discovery uses the configured Bedrock endpoint/region and proxy. Skip request signing when Bedrock auth skipping is enabled. Otherwise, a Bedrock bearer token suppresses credential refresh; absent a bearer token, use refreshed AWS credentials when available. Failure is reported diagnostically and converted to the static catalog behavior in MOD-052.

## Availability axes and extended context

**MOD-060 — Availability decision.** Evaluate model presence, provider support, administrative allowlist, account entitlement, runtime feature gate, context entitlement, and successful validation separately. Picker presence is a convenience projection, not proof that a request will succeed.

**MOD-061 — One-million-context access.** Global 1M disable denies both Opus and Sonnet access. A AgentX Cloud subscriber is eligible only when cached extra-usage status is known and provisioned: null disabled-reason or `out_of_credits` counts as provisioned; missing cache and every explicit not-provisioned, organization/seat/member/group disabled or zero-limit, no-limits, service-disabled, and unknown reason deny access. Non-subscriber API/pay-as-you-go users are eligible unless globally disabled.

**MOD-062 — Extended-context support.** The declared public support predicate is canonical Sonnet 4 family or Opus 4.6. It returns false under global 1M disable. The local context-window resolver nevertheless honors an explicit `[1m]` marker before checking this support predicate, so an unsupported custom/Haiku spelling can locally report 1,000,000 and still be rejected by its provider.

**MOD-063 — Upgrade hint.** Offer a 5× context upgrade only when the stored user setting is exactly `opus` or `sonnet` and the corresponding access predicate succeeds. Warning context emits the matching `/model …[1m]` command; tip context emits the corresponding Opus/Sonnet 1M message. Defaults, concrete IDs, and other aliases receive no hint.

**MOD-064 — Server/account boundaries.** The client does not discover general external-account model entitlements. First-party bootstrap may provide additional picker options, 1M access uses cached account status, and `/model` may probe a custom value. A successful static alias resolution must still tolerate a provider 404 or account denial at request time.

## Capability predicates

**MOD-070 — Third-party override protocol.** For non-first-party providers only, each of the pinned default Opus, Sonnet, and Haiku identifiers may have a comma-separated supported-capabilities string. Apply an override only when the candidate equals that pinned model case-insensitively and the capabilities variable is defined. A listed token means true; a defined list that omits it means false; no matching pin/list means “no override.” Supported tokens are `effort`, `max_effort`, `thinking`, `adaptive_thinking`, and `interleaved_thinking`. Cache results by lowercased model plus capability for the process lifetime.

**MOD-071 — Thinking support.** MOD-070 wins. A service-resolved internal model supports thinking. Otherwise first-party and Foundry support every model whose canonical name is not AgentX 3; Bedrock and Vertex support canonical Sonnet 4 or Opus 4 only.

**MOD-072 — Adaptive thinking support.** MOD-070 wins. Opus 4.6 and Sonnet 4.6 support adaptive thinking. Any other known Opus, Sonnet, or Haiku does not. An unknown identifier defaults true on first-party and Foundry and false on Bedrock and Vertex.

**MOD-073 — Effort support.** A global always-enable switch wins. Then MOD-070 wins. Opus 4.6 and Sonnet 4.6 support effort; every other known Opus, Sonnet, or Haiku does not. Unknown identifiers default true only on first-party. Maximum effort is narrower: MOD-070 may decide it, otherwise only Opus 4.6 and service-resolved internal models support it.

**MOD-074 — Effort application.** Resolve applied effort as environment override, then live app-state value, then model default. Environment `unset` or `auto` means send no effort. If the result is `max` on a model without maximum-effort support, send `high`. Numeric effort is internal-only and is never persisted. External settings persist `low`, `medium`, or `high`; internal settings may also persist `max`.

**MOD-075 — Effort defaults.** Internal service model metadata may specify a default level or numeric value, and the internal default-model config may specify its own default. Externally, Opus 4.6 defaults to medium for Pro; Max and Team subscribers also default to medium when the rollout config is enabled. When the ultrathink feature is active, other effort-capable models default to medium. Otherwise omit the API effort value, which displays as high. If an effort-capable request has no explicit value, still include the effort beta/header.

**MOD-076 — Interleaved thinking.** MOD-070 wins. Foundry supports it for every identifier; first-party supports it for canonical non-AgentX-3 models; Bedrock and Vertex support canonical Opus 4 or Sonnet 4. A global interleaved-thinking disable prevents sending the beta/header even when support is true.

**MOD-077 — Structured output.** Only first-party and Foundry may enable it, and only for Sonnet 4.5/4.6, Opus 4.1/4.5/4.6, and Haiku 4.5. Bedrock and Vertex return false regardless of model. When a structured output format is present and the predicate is true, add the structured-output beta/header if absent.

**MOD-078 — Context management.** Foundry supports every identifier. First-party supports canonical non-AgentX-3 identifiers. Bedrock and Vertex support canonical AgentX 4 Opus, Sonnet, or Haiku. This predicate controls the context-management beta/header; the actual body still depends on active context-management strategy.

**MOD-079 — Auto mode.** Auto mode exists only when its build feature is included. External users must be on first-party and canonical Opus 4.6 or Sonnet 4.6. A service allow-models list can force-enable an exact raw ID or canonical name, but cannot bypass the external provider restriction. Internal users deny AgentX 3 and public AgentX 4.0–4.5 families and allow other identifiers. Without the build feature, always return false.

**MOD-080 — Fast mode.** Fast mode is model-compatible only when its global disable is off and the parsed effective model contains Opus 4.6. Actual availability additionally requires first-party, surface eligibility, an enabled organization/account status or accepted cache fallback, user opt-in, and no active cooldown. Switching to an incompatible model turns live fast mode off without persisting that automatic downgrade.

**MOD-081 — Advisor models.** When the advisor feature exists, public base and advisor models are restricted to Opus 4.6 or Sonnet 4.6; internal users bypass those family checks. Store advisor identifiers without context markers. A configured advisor remains stored but inactive when the current base model does not support the advisor tool.

**MOD-082 — Other beta decisions.** Vertex receives the web-search beta/header for AgentX 4 Opus, Sonnet, or Haiku; Foundry receives it for every deployed model. Tool-search uses one header for Bedrock/Vertex and another for first-party/Foundry. First-party-only experimental betas include Foundry unless separately excluded, and a global experimental-beta disable wins. These decisions do not add models to the catalog.

**MOD-083 — Native attachment qualification.** The current native attachment
profile is enabled only for the Azure/OpenAI Responses adapter when the logical
model is exactly `gpt-5.6-sol` and the configured API selector is exactly
empty, `v1`, or `preview`. All other providers, logical model names, and API
selectors are text-only and reject native media before transport. This is a
closed local advertisement and preflight qualification, not deployment
introspection or proof that every deployment behind an allowed selector accepts
the advertised modalities.

**MOD-084 — Qualified media matrix.** A qualified request accepts only
`image/png`, `image/jpeg`, and `application/pdf` through the native immutable
attachment store. Images map to Responses `input_image`; PDF maps to
`input_file`. PNG/JPEG remain subject to `IQ-013` decode/re-encode,
dimension, pixel, and no-resize rules. `application/pdf` means only the
conservative `IQ-013` classic-xref/catalog/page-tree subset; it does not claim
object/xref streams, incremental updates, active/forms/embedded content,
OCR/conversion, or a page count above the configured bound. Audio, SVG, GIF,
WebP, URLs, arbitrary binary, and every other MIME are unsupported.
Capability advertisement must expose this exact matrix and must be absent for
a text-only profile.

**MOD-085 — Provider evidence boundary.** The request forms follow the
official [Azure Responses API image and file input
schema](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/responses).
Loopback request tests prove local construction and zero-call preflight.
Because Azure deployments and their vision/PDF eligibility vary independently,
every release artifact and intended deployment/profile must retain a separate
installed-runtime PNG/JPEG/PDF qualification before it claims real-provider
support. The current worktree's one-profile result is recorded as
[non-normative environment-scoped evidence](../../implementation-conformance-audit/references/native-attachment-production-qualification.md);
it does not qualify another artifact, deployment, selector, model, or platform.

## Context and output limits

**MOD-090 — Context-window order.** Resolve the effective input context in this order:

1. a positive leading base-10 integer from the internal-only maximum-context override (a numeric prefix is accepted even when later characters remain);
2. an effective explicit `[1m]` marker: 1,000,000;
3. internal first-party capability cache `max_input_tokens` when at least 100,000, capped to 200,000 under global 1M disable when the server value is larger;
4. an active 1M beta/header on a declared 1M-capable model: 1,000,000;
5. the Sonnet 4.6 1M experiment when client data contains the exact string `true` and the model has no explicit marker: 1,000,000;
6. service-provided internal model context window;
7. 200,000.

The internal maximum override takes precedence even over explicit 1M and global-disable handling.

**MOD-091 — Capability-cache shape.** Internal dynamic capability discovery is enabled only for internal users on the first-party provider and an approved first-party host. Skip refresh under essential-traffic-only privacy. Accept a list of records containing string `id` and optional numeric `max_input_tokens` and `max_tokens`; strip all unknown/internal-only fields before disk persistence.

**MOD-092 — Capability-cache persistence.** Persist the sorted list plus a timestamp in a private user-only cache file. Sort by descending identifier length, then stable lexical order. Reads validate the complete file and return no capabilities on any read/parse/schema failure. The timestamp is recorded but not used for expiry. Exact case-insensitive ID match wins; otherwise use the first case-insensitive identifier contained in the candidate. A successful unchanged refresh avoids a write; refresh failure keeps existing cache and never fails startup.

**MOD-093 — Output-limit table.** Determine model default and upper maximum output tokens from the canonical family in this order:

| Family/version | Default | Upper limit |
| --- | ---: | ---: |
| Opus 4.6 | 64,000 | 128,000 |
| Sonnet 4.6 | 32,000 | 128,000 |
| Opus 4.5, Sonnet 4/4.5, Haiku 4.5 | 32,000 | 64,000 |
| Opus 4/4.1 | 32,000 | 32,000 |
| AgentX 3 Opus | 4,096 | 4,096 |
| AgentX 3 Sonnet | 8,192 | 8,192 |
| AgentX 3 Haiku | 4,096 | 4,096 |
| AgentX 3.5 Sonnet/Haiku | 8,192 | 8,192 |
| AgentX 3.7 Sonnet | 32,000 | 64,000 |
| Unknown | 32,000 | 64,000 |

An internal service model's explicit default/upper values replace this table, with 32,000/64,000 fallbacks. An internal capability-cache `max_tokens` of at least 4,096 replaces the upper limit and clamps the default down to it.

**MOD-094 — Request output limit.** A rollout may cap the default reservation to the smaller of the table default and 8,000; this does not change the upper limit and participates in the query runtime's one-time 64,000 escalation contract. A positive leading base-10 integer from the environment override replaces the default, is capped to the model upper limit, and falls back to the default when no positive numeric prefix exists. Legacy budgeted thinking is at most one token below the effective output maximum.

## Model-picker options

**MOD-100 — Option record.** A picker option has a persisted value, label, short description, and optional model-facing description. Null means built-in Default. Option order is semantic and must be stable. Provider-specific option values may be aliases or concrete provider IDs as described below.

**MOD-101 — Base option matrix.** Build options in this order:

| Profile | Ordered options after Default |
| --- | --- |
| Internal | service-provided internal entries; merged Opus 4.6 1M; Sonnet 4.6; Sonnet 4.6 1M; Haiku 4.5 |
| Max or Team Premium subscriber | separate Opus 4.6 1M only when merge is off and access is on; Sonnet 4.6; Sonnet 4.6 1M when access is on; Haiku 4.5 |
| Other AgentX Cloud subscriber | Sonnet 4.6 1M when access is on; merged Opus 4.6 1M when merge is on, otherwise Opus 4.6 and then Opus 4.6 1M when access is on; Haiku 4.5 |
| First-party API/pay-as-you-go | Sonnet 4.6 1M when access is on; merged Opus 4.6 1M when merge is on, otherwise Opus 4.6 and optional Opus 4.6 1M; Haiku 4.5 |
| Bedrock, Vertex, or Foundry pay-as-you-go | custom pinned Sonnet when configured, otherwise explicit provider Sonnet 4.6 and optional 1M; custom pinned Opus when configured, otherwise legacy-labelled Opus option, explicit provider Opus 4.6, and optional 1M; custom pinned Haiku when configured, otherwise provider default Haiku |

Default is always first. Access checks in the table are MOD-061. Prices and extra-usage wording are presentation metadata and do not change values.

**MOD-102 — Third-party custom tier options.** Show a custom pinned tier only on a non-first-party provider and only when that tier's default-model environment value is present. Its value remains the family alias, its label/description use optional custom display environment values, and its detailed description includes the actual pinned identifier. A pinned Sonnet or Opus identifier containing `[1m]` is described as extended context.

**MOD-103 — Third-party legacy-label edge.** With no custom third-party Opus pin, the compatibility list includes an option labelled “Opus 4.1”/legacy whose value is the `opus` alias. The current alias resolves to provider Opus 4.6, so that legacy-labelled entry and the explicit Opus 4.6 entry can resolve to the same model. Preserve this observable behavior or document a deliberate UI correction.

**MOD-104 — Additional options.** Append, in order:

1. one environment custom option when present and no existing value is exactly equal;
2. server-bootstrap additional options in server order, each only when its value is not already exactly equal;
3. the current user setting, otherwise the initial setting, when still absent.

The server bootstrap shape is `{model, name, description}` transformed to option value/label/description. Fetch it only on first-party, outside essential-traffic-only mode, with usable profile-scoped OAuth or API key; use a five-second timeout and one OAuth refresh/retry. Cache both client data and additional options on disk only when changed. Fetch/validation failure leaves the prior cache untouched.

**MOD-105 — Current custom rendering.** Add `opusplan` with its dedicated plan-mode label, first-party `opus` and `opus[1m]` with their ordinary friendly options, a known concrete public model with marketing name and an upgrade hint when its version differs from the current family alias, or an unknown value as a verbatim Custom model.

**MOD-106 — Final policy filtering.** After assembling all options, filter every non-null value through the administrative allowlist. Always retain Default. An embedded picker may additionally append the currently active value for display even when the ordinary option builder filtered it; that display-only current entry does not relax validation of a new selection.

## Validation, errors, and deprecation

**MOD-110 — Validation order.** Validate an explicitly entered custom model in this order:

1. trim; reject empty;
2. enforce administrative allowlist;
3. accept an advertised alias case-insensitively;
4. accept an exact environment custom-option value as user-prevalidated;
5. accept an exact process-cache hit;
6. issue a live probe.

The process cache stores successful exact trimmed strings only. It is not scoped by provider or credential and has no expiry; the allowlist check still runs before a cache hit.

**MOD-111 — Probe contract.** A live probe sends the entered identifier without alias parsing, with one maximum output token, zero retries, query source `model_validation`, and one user text block `Hi` marked ephemeral for prompt caching. Success records the exact value as valid. Never mutate the user's stored model during a probe.

**MOD-112 — Validation errors.** Return structured invalid results with these classes:

- empty: model name cannot be empty;
- policy denial: name is not in available models;
- not found/404: model not found, with MOD-114 third-party suggestion when available;
- authentication: check API credentials;
- connection: check network connection;
- API body with not-found type and a model-specific message: model not found;
- other API failure: include the provider error message;
- unknown failure: unable to validate plus the normalized error text.

Unknown failures fail closed. Do not cache failures.

**MOD-113 — Picker command checks.** `/model default` selects null without probing. Before accepting a non-default value, enforce the allowlist. Reject an Opus `[1m]` spelling when both Opus 1M access and merge are unavailable. Reject Sonnet 1M without access for the `sonnet[1m]` and Sonnet 4.6 `[1m]` spellings, but preserve the compatibility exception for explicit Sonnet 4.5 1M. Advertised aliases skip the live probe; other input uses MOD-110. A picker-list selection is trusted as an assembled option and does not repeat the live probe.

**MOD-114 — Third-party unavailable suggestion.** On Bedrock, Vertex, or Foundry, suggest provider Opus 4.1 for an Opus 4.6 identifier, provider Sonnet 4.5 for Sonnet 4.6, and provider Sonnet 4 for Sonnet 4.5. Match hyphenated or underscore family/version spellings case-insensitively. First-party gives no version suggestion. Use the same chain for validation 404s and request-time unavailable-model guidance.

**MOD-115 — Request-time unavailable model.** A provider 404 reports that the model may not exist or be accessible and directs interactive users to `/model` and noninteractive users to `--model`; third-party errors include MOD-114 and the deployment/provider context. A Bedrock error mentioning model ID uses equivalent picker guidance. A AgentX Cloud subscriber receiving a 400 invalid-model response for a non-custom Opus model receives the plan-specific Opus availability message.

**MOD-116 — Deprecation is warning-only.** Match deprecated model keys as case-insensitive substrings, then consult the active provider's retirement date. Return no warning when the provider date is null. A warning never blocks, remaps, or validates a model.

| Deprecated family | First-party | Bedrock | Vertex | Foundry |
| --- | --- | --- | --- | --- |
| AgentX 3 Opus | January 5, 2026 | January 15, 2026 | January 5, 2026 | January 5, 2026 |
| AgentX 3.7 Sonnet | February 19, 2026 | April 28, 2026 | May 11, 2026 | February 19, 2026 |
| AgentX 3.5 Haiku | February 19, 2026 | no warning | no warning | no warning |

The user-visible warning identifies the friendly model name, retirement date, and recommendation to switch.

**MOD-117 — Legacy Opus remap.** Unless explicitly disabled, first-party parsing silently remaps exact Opus 4/4.1 legacy identifiers (`agentx-opus-4-20250514`, `agentx-opus-4-1-20250805`, `agentx-opus-4-0`, and `agentx-opus-4-1`) to current default Opus while retaining a parsed `[1m]`. Third-party providers pass them through. This runtime remap is separate from deprecation warnings.

**MOD-118 — Settings migrations.** Startup migration rewrites only user-settings ownership, never project/local/policy ownership:

- first-party legacy Opus strings in MOD-117 become `opus` when remapping is enabled;
- eligible `opus` user settings become `opus[1m]`, or are removed when that is the built-in default;
- eligible first-party Pro/Max/Team Premium explicit Sonnet 4.5 settings become `sonnet` or `sonnet[1m]`;
- internal removed aliases map to current Opus aliases and may enable fast mode.

Each migration is idempotent and leaves CLI/session-only values unchanged.

## Delegated, skill, teammate, and auxiliary models

**MOD-120 — Subagent default and precedence.** An omitted subagent model defaults to `inherit`. Resolve an effective subagent model in this order:

1. `AGENTX_SUBAGENT_MODEL`, parsed as a user model;
2. model explicitly supplied by the invoking agent tool;
3. agent definition model;
4. `inherit`.

The environment override bypasses parent-tier and Bedrock-prefix inheritance. A tool-specified value is constrained to the advertised model-alias vocabulary by its tool contract.

**MOD-121 — Bare tier match.** For tool or agent settings exactly equal to bare `opus`, `sonnet`, or `haiku` case-insensitively, canonicalize the parent. If it belongs to that tier, inherit the parent's exact identifier rather than resolving the alias to the provider default. `best`, `opusplan`, and `[1m]` forms do not tier-match.

**MOD-122 — Inherit semantics.** `inherit` applies runtime main-loop substitution using the parent's exact identifier, current permission mode, and the session's exact selected-user-setting predicate, with “exceeds 200K” treated false. This allows `opusplan` behavior only when the session setting itself is exactly `opusplan`. It is not the same as reparsing the parent setting.

**MOD-123 — Bedrock subagent region.** Extract a recognized cross-region prefix from the parent. After resolving a tool/agent alias on Bedrock, apply the parent prefix unless the original child specification already has its own recognized prefix. Preserve an explicitly prefixed child to avoid changing data residency. Non-Bedrock providers and unrecognized formats receive no prefix transformation.

**MOD-124 — Skill override context carry.** If the current model has effective `[1m]`, a skill's model override lacks `[1m]`, and the parsed target supports 1M, append `[1m]` to the skill setting. Do not append it to Haiku/unsupported targets. Preserve a skill's explicit marker. This is context carry, not parent-tier exact-ID inheritance.

**MOD-125 — Teammate tri-state default.** Teammate default configuration has three states:

- absent: use hard-coded provider-aware Opus 4.6 directly from MOD-012, without Bedrock discovery or settings model overrides;
- null: follow the leader's exact model, falling back to hard-coded Opus 4.6 when no leader model exists;
- string: parse that setting through the main alias parser.

An invocation model overrides all three. Invocation/agent-frontmatter `inherit` means leader exact model, then the tri-state fallback. Unlike ordinary subagents, the absent teammate default does not inherit the leader.

**MOD-126 — Internal model profile.** Internal service configuration may provide a default model, default effort, prompt suffix, switch callout, and model records containing alias, concrete identifier, label, description, context window, output limits, effort defaults, and always-on-thinking. It is ignored for external users. Resolve a record by exact alias equality or by finding its concrete identifier case-insensitively inside the supplied model. If configuration is unavailable, an internal alias falls through unchanged so the normal API error path exposes the stale/missing config.

**MOD-127 — Always-on-thinking auxiliary rule.** An internal model marked always-on-thinking cannot receive a disabled-thinking request in the safety classifier. Omit the disable flag and add 2,048 output-token headroom so adaptive thinking cannot consume the classifier's short textual verdict budget. Other classifier models explicitly disable thinking.

**MOD-128 — Auxiliary model rules.** Side queries that request the small/fast model use MOD-033. Hooks may explicitly supply a model; otherwise their own contract chooses small/fast or Haiku. Advisor selection follows MOD-081. These auxiliary choices do not change the main session model or persist a `/model` setting.

## Overload fallback

**MOD-130 — Fallback input.** Automatic model fallback is an explicit noninteractive option. `default` resolves immediately to the current built-in default. Reject only the raw case where both explicit main and fallback option strings are identical; differently spelled values that later resolve equal are not rejected by this check.

**MOD-131 — Trigger.** Count eligible consecutive overload responses. Background/non-foreground sources fail promptly. After three overloads, trigger the configured fallback only when the all-primary-model override is present or the caller is a non-subscriber using a built-in non-custom Opus model. Without a configured fallback, continue or terminate according to ordinary bounded/persistent retry policy.

**MOD-132 — Transition.** On trigger, terminalize partial tool uses, clear the failed attempt's assistant fragments, reset streaming-tool executor state, replace the current model in the query tool context, strip model-bound thinking signatures for internal compatibility, publish a warning naming old and new display models, and retry coherent history. Do not rewrite the saved base user setting merely because one turn fell back.

**MOD-133 — Streaming safety.** Model fallback shares the duplicate-side-effect constraint of non-streaming recovery: disable it or prove the failed streaming attempt cannot still execute non-idempotent tools. The query runtime's recovery contract remains authoritative for tool terminalization and cleanup.

## Cost attribution

**MOD-140 — Canonical price lookup.** Attribute usage by canonical model name, not provider identifier. Prices are US dollars per million tokens except web search, which is per request:

| Tier | Input | Output | Cache write | Cache read | Web search |
| --- | ---: | ---: | ---: | ---: | ---: |
| Sonnet 3.5/3.7/4/4.5/4.6 | 3 | 15 | 3.75 | 0.30 | 0.01 |
| Opus 4/4.1 | 15 | 75 | 18.75 | 1.50 | 0.01 |
| Opus 4.5/4.6 standard | 5 | 25 | 6.25 | 0.50 | 0.01 |
| Opus 4.6 fast | 30 | 150 | 37.50 | 3.00 | 0.01 |
| Haiku 3.5 | 0.80 | 4 | 1.00 | 0.08 | 0.01 |
| Haiku 4.5 | 1 | 5 | 1.25 | 0.10 | 0.01 |

Use the response's actual speed field to select Opus 4.6 fast pricing, not the user's requested fast-mode state.

**MOD-141 — Unknown-price fallback.** When canonical lookup fails, emit unknown-model-cost evidence and use the current built-in default model's tier. If even that tier is unknown, use standard Opus 4.5/4.6 pricing. Do not fail the query because price metadata is missing.

**MOD-142 — Bedrock application profile cost.** For a Bedrock application inference profile, query its profile metadata and use the first backing model ARN's final path segment for cost classification. Empty/malformed/failing metadata returns no backing model and falls through to ordinary unknown-price handling. This lookup affects cost only, not request routing.

## Acceptance scenarios

### Provider, catalog, and aliases

**MOD-A01 — Conflicting provider selectors.** Enable Bedrock, Vertex, and Foundry selectors together. Verify Bedrock is selected, its catalog column is used, and no ambiguity prompt appears. Initialize the catalog, mutate selectors, and verify the process rejects/requires restart or exhibits the explicitly documented unsupported mixed-state behavior rather than silently rebuilding part of the catalog.

**MOD-A02 — Provider defaults.** With no tier overrides, verify `sonnet` resolves to Sonnet 4.6 first-party and Sonnet 4.5 on each third-party provider, while `opus` resolves to Opus 4.6 and `haiku` to Haiku 4.5 everywhere. Add a tier environment override and verify it wins without changing the static table.

**MOD-A03 — Account default.** Exercise internal, Max, Team Premium, Pro, Team Standard, Enterprise, first-party API, and third-party profiles. Verify MOD-034 including merged `[1m]` and the unknown-subscription fail-closed case.

**MOD-A04 — Alias edge spellings.** Parse mixed-case/whitespace aliases, `sonnet[1m]`, `best[1m]`, `haiku[1m]`, and `opusplan[1m]`. Verify parser behavior in MOD-031/032, but verify only advertised combined aliases bypass live validation.

**MOD-A05 — Plan substitution.** Use exact `opusplan` outside plan, inside plan below 200K, and inside plan above 200K; then use exact `haiku` and a concrete Haiku provider ID in plan. Verify only the exact settings switch and that over-200K Opus Plan remains on its parsed Sonnet base.

### Allowlist and provider mapping

**MOD-A06 — Empty and absent allowlist.** With the setting absent, accept an arbitrary custom model. With an empty list, reject the same value, hide every non-default option, and still run the built-in Default without a policy error.

**MOD-A07 — Narrowed family.** Evaluate `['opus']`, then `['opus', 'opus-4-5']`, against Opus 4.5, Opus 4.6, alias `opus`, and `best`. Verify wildcard behavior, narrowing, alias resolution, and segment-boundary rejection of Opus 4.50.

**MOD-A08 — Full-ID suffix edge.** Put one dated full ID in the allowlist. Verify exact input and a dash-suffixed extension both match under compatibility semantics, while a non-dash continuation does not. A hardened exact-only implementation must mark this as a deliberate policy divergence.

**MOD-A09 — Override reversal.** Map canonical Opus 4.6 to a provider ARN. Verify the ARN is displayed/routed as the override, canonicalizes back to Opus 4.6 by exact equality, and can match a canonical allowlist entry. Change case in the ARN and verify reversal no longer occurs.

**MOD-A10 — Bedrock discovery.** Return several paginated system profiles with two candidates for one canonical model. Verify the first service-order match wins, static values fill gaps, settings overrides win afterward, and interim reads before the awaited initializer use static-plus-overrides. Repeat with fetch failure and verify static settlement with no startup failure or second automatic fetch.

**MOD-A11 — Region inheritance.** Give a Bedrock parent an `eu` profile, then spawn an alias child, an explicitly `us`-prefixed child, a foundation-ID child, and an arbitrary custom child. Verify parent `eu` is added/replaced only where MOD-123/055 allows and never overwrites the child's explicit `us` residency.

### Availability and capabilities

**MOD-A12 — Extended-context gates.** Test an API-key user, a subscriber with missing extra-usage cache, null reason, out-of-credits, and organization-disabled reason. Verify access outcomes. Then set global disable and verify access/support/marker handling is disabled except an internal positive max-context override still controls local context calculation.

**MOD-A13 — Capability-cache recovery.** Seed a valid private cache with overlapping IDs, malformed unknown fields, and a timestamp. Verify exact then longest-substring matching, stripped persisted fields, no timestamp expiry, and static fallback on malformed-file read or refresh failure.

**MOD-A14 — Provider capability matrix.** For known AgentX 3, Haiku 4.5, Sonnet 4.5, Sonnet 4.6, Opus 4.1, Opus 4.6, and an unknown custom ID, evaluate thinking, adaptive thinking, effort, maximum effort, interleaved thinking, structured output, and context management on all four providers. Verify MOD-071–078, then add an exact third-party pinned override and verify explicit true and explicit false both win.

**MOD-A14A — Native attachment capability.** Cross the provider, logical
model, and API-selector values in `MOD-083`. Verify only the exact qualified
combinations advertise the version-1 image/PDF capability and construct exact
loopback media requests; all other combinations omit capability metadata,
remain compatible with legacy text, reject media explicitly, and make zero
provider calls. This is local configuration/preflight evidence, not remote
deployment qualification.

**MOD-A14B — Installed-runtime native attachment qualification.** Install or
otherwise execute the exact candidate artifact with the intended
provider/deployment/profile and a selector allowed by `MOD-083`. Submit a
representative normalized PNG, JPEG, conservative multi-page PDF,
attachment-only turn, and mixed ordered request through public CLI and
stream-JSON routes. Verify model-grounded responses, stable typed replay,
resume after source removal, fork-owned media, privacy, bounded cleanup, and
zero unintended calls for locally rejected input. Record artifact digest,
logical model, selector class, media fixtures, request count, outcomes, skips,
and cleanup without endpoint, deployment, credentials, source/runtime paths,
provider bodies, base64, prompts, or raw model output. Repeat for every release
artifact, deployment, selector, and native platform claimed. A provider
rejection/quarantine claim requires a deliberately induced closed
media-specific rejection; a successful media run cannot stand in for it.

**MOD-A15 — Output limits.** Verify every row of MOD-093, then apply a server maximum below the default, the 8K reservation rollout, invalid/negative/too-large environment overrides, and legacy thinking. Verify clamping order and the one-token thinking margin.

### Options, validation, and lifecycle

**MOD-A16 — Option matrix and deduplication.** Generate each profile in MOD-101. Add an environment custom option, duplicated and unique bootstrap options, and a current custom model. Verify stable order, exact-value first-wins deduplication, friendly current rendering, and final allowlist filtering with Default retained.

**MOD-A17 — Third-party legacy option.** On a third-party provider without a custom Opus pin, select the legacy-labelled Opus 4.1 option and the explicit Opus 4.6 option. Verify both effective resolutions, including the compatibility duplicate described by MOD-103.

**MOD-A18 — Validation short circuits.** Validate empty, allowlist-denied, known alias, exact environment custom option, cached success, and arbitrary custom input. Verify only the last case probes the network and failures never enter the valid cache.

**MOD-A19 — Validation error mapping.** Make the probe return 404, authentication, connection, typed not-found body, generic API, and unknown errors on first-party and third-party. Verify MOD-112/114 messages and no fallback suggestion on first-party.

**MOD-A20 — Validation-cache scope edge.** Successfully validate one exact custom identifier, change credentials/provider, and validate the same string. Verify the compatibility cache reuses success after the still-current allowlist check. A provider/credential-scoped cache is an allowed hardening only when documented as a divergence.

**MOD-A21 — Deprecation versus remap.** Select each deprecated family on every provider and verify provider-specific warning dates/nulls. Separately parse a first-party legacy Opus 4.1 ID with remap enabled and disabled, then on third-party. Verify warning, runtime remap, and migration are independent operations.

### Delegation, fallback, and accounting

**MOD-A22 — Subagent precedence.** Supply all four subagent model sources, then remove them one by one. Verify environment, tool, agent, inherit precedence; bare matching preserves the parent's exact deployment; non-bare aliases re-resolve; and inherit applies runtime plan substitution.

**MOD-A23 — Skill and teammate inheritance.** Invoke an Opus skill from Opus 1M and a Haiku skill from the same session; verify only Opus carries 1M. Exercise absent, null, and string teammate defaults with and without a leader and invocation `inherit`; verify MOD-125 rather than ordinary subagent semantics.

**MOD-A24 — Overload fallback.** In an eligible foreground non-subscriber Opus query, return three overloads with a configured fallback. Verify one model transition, coherent history, terminal partial tools, warning, signature cleanup where applicable, and no persisted base-model rewrite. Repeat for a background query, subscriber, custom model, and no fallback and verify no automatic switch.

**MOD-A25 — Cost resolution.** Attribute standard and fast Opus 4.6 responses, every catalog price tier, a Foundry custom deployment, a reversed override, and a Bedrock application profile whose first backing ARN is known. Verify canonical pricing, response-speed selection, profile lookup, and unknown fallback evidence.

**MOD-A26 — Bootstrap service boundary.** Return invalid bootstrap option data, time out, deny profile scope, disable nonessential traffic, and use a third-party provider. Verify no network-derived option mutation. Then return a valid first-party response and verify changed-only persistence without treating its model IDs as new static aliases.

## Provenance

These contracts were specified from the model catalog and its direct consumers: provider/config/alias/default/allowlist/option/validation/deprecation/capability/profile modules, context/thinking/effort/beta/fast-mode predicates, startup and app-state model selection, Bedrock discovery, API request construction and errors, agent/skill/teammate selection, bootstrap-provided model options, migrations, retry fallback, and cost attribution. Source paths and implementation-language symbols are provenance only; the `MOD-*` requirements and `MOD-A*` scenarios above are the standalone implementation contract.
