# Credential and provider contract

This document defines provider selection, credential-source precedence, helper execution, OAuth lifecycle, managed organization enforcement, and cloud-provider adapters. `AUTH-*` identifiers are normative and stable.

## Contents

- [Provider selection](#provider-selection)
- [Bearer-token source precedence](#bearer-token-source-precedence)
- [API-key source precedence](#api-key-source-precedence)
- [Credential helpers](#credential-helpers)
- [Application-owned Azure credential file](#application-owned-azure-credential-file)
- [First-party OAuth authorization](#first-party-oauth-authorization)
- [OAuth refresh concurrency and 401 recovery](#oauth-refresh-concurrency-and-401-recovery)
- [Managed organization enforcement](#managed-organization-enforcement)
- [AWS Bedrock adapter](#aws-bedrock-adapter)
- [Azure Foundry adapter](#azure-foundry-adapter)
- [Google Vertex adapter](#google-vertex-adapter)
- [Failure and recovery](#failure-and-recovery)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Provider selection

`AUTH-001` — Select one model API provider before constructing a client:

| Provider | Selection and credential family |
| --- | --- |
| first-party | direct API key, bearer token, or AgentX account OAuth |
| AWS Bedrock | AWS credential chain, optional bearer token, managed refresh command |
| Azure OpenAI | versioned application-owned `auth.json` API key |
| Azure Foundry | Foundry API key or Azure identity token provider |
| Google Vertex | Google application credentials/token and project/region |

Conflicting provider selectors fail startup or follow one explicit documented precedence. Never attach first-party OAuth/key credentials to a cloud-provider endpoint.

`AUTH-002` — Keep provider selection, selected model, region/resource/project, credential source, and base URL as attributed bootstrap facts. Changing provider requires a client/context rebuild and clears provider-specific credential caches.

`AUTH-003` — First-party authentication is disabled when bare mode is active, a cloud provider is selected, or an external first-party credential source intentionally takes precedence. Managed OAuth contexts have special fallback restrictions in `AUTH-012`.

## Bearer-token source precedence

`AUTH-010` — Outside bare mode, determine first-party bearer token source in this exact order:

1. `AGENTX_AUTH_TOKEN`, except in managed OAuth context;
2. `AGENTX_OAUTH_TOKEN`;
3. OAuth token from inherited file descriptor, or the approved managed relay disk fallback;
4. configured `apiKeyHelper`, except in managed OAuth context;
5. AgentX account inference OAuth token from secure storage/keychain;
6. none.

Return both `hasToken` and source identity. Detection of helper configuration must not execute it.

`AUTH-011` — Bare mode is API-key-only. Bearer-token detection recognizes only an `apiKeyHelper` supplied by explicit flag settings; ignore OAuth environment variables, descriptor OAuth, user keychain, and normal settings.

`AUTH-012` — A managed OAuth context is a remote/relay or desktop-spawned session whose launcher supplies OAuth. It must not fall back to a user's normal settings `apiKeyHelper`, settings environment API key/token, or local keychain variant not supplied by the managed context.

`AUTH-013` — For an AgentX-only Unix relay, OAuth enablement follows the launcher's placeholder OAuth signal. Local settings cannot flip the auth shape because the relay injects the real credential and expects matching protocol headers.

`AUTH-014` — Treat every nonempty selected model credential as an exact secret literal at process-owned egress boundaries. Build one immutable union for each sink before replacement; never chain independently selected markers. Before constructing any streaming sanitizer, prove that the union has a set-safe terminal marker; a nonempty union without one is a composition error, not a successfully constructed stream that silently suppresses safe bytes. Inspect raw and canonical JSON encodings as well as every decoded key, string leaf, scalar spelling, and duplicate object-member occurrence; a last-write-wins map projection is not safety evidence for an earlier duplicate member. At model-provider request construction, reject any complete credential literal found in the exact final URL, decoded URL or query fields, or canonical non-auth header block before `HTTPClient.Do`. The intended Azure API-key header may contain the selected key exactly, including duplicate membership in the frozen union, but fails composition if another union member is embedded in its value. Cross-field permutation inspection accepts at most five separately framed values; a larger record fails closed before attempting attacker-controlled factorial ordering. After projection, construct and validate the exact physical emission—including escapes, keys, values, structural separators, wrappers, length/framing bytes, and line terminators—so no later append can reconstruct a literal after the check. Malformed model tool arguments whose escaped meaning cannot be inspected must be replaced before persistence with a credential-independent, schema-invalid argument projection that still receives exact-one terminal settlement; never retain their raw spelling for replay. Sanitize before persistence, authority parsing, truncation, framing, hooks, progress, and model continuation. Reapply the complete sanitizer after any normalization that replaces, removes, or joins bytes—such as unsafe-control replacement—because normalization can construct a credential absent from the raw input. An error wrapper that retains a raw secret-bearing cause only to preserve `errors.Is` classification must either discard every other cause capability or implement fail-closed formatting for ordinary, detailed, and Go-syntax verbs; a safe `Error` method and absent `Unwrap` method do not make `%#v` safe. When no set-safe replacement exists at a bounded nonstreaming seam, carry explicit suppression through downstream adapters so they do not synthesize prose around an empty result. Capability calls may receive bounded projection behavior and maximum lookahead, but never a reflectable credential set or raw literal.

`AUTH-015` — Provider diagnostics and model-client identity renderings are complete-value credential boundaries. Parse `Retry-After` immediately into private scheduling state and discard its raw wire spelling before constructing an error, retry observation, callback value, or terminal wrapper. Apply the complete credential union to the fully composed `ProviderError`, retry observation, and retry-exhaustion message after labels and separators are present; ordinary, detailed, quoted, string, and Go-syntax formatting must all use that safe projection rather than traverse private or structured fields. Retry callbacks receive a detached observation whose mutation cannot alter active retry classification or delay, while the terminal wrapper may retain a safe unwrap chain for `errors.Is`/`errors.As`. Diagnostic formatting for a configured model client is generic: endpoint, model, deployment, API version, and other configured identity fields are not rendered because any may equal or contain the API key.

## API-key source precedence

`AUTH-020` — Bare API-key resolution is:

1. `AGENTX_API_KEY` environment value;
2. explicit flag-settings `apiKeyHelper` result;
3. none.

Never read keychain, account config, approval lists, or ordinary settings in bare mode.

`AUTH-021` — Ordinary API-key resolution:

1. if invocation explicitly prefers third-party/direct authentication and environment key exists, use it;
2. in CI/test, use key from inherited file descriptor, then environment key; if neither API key nor OAuth token is present, fail clearly;
3. outside CI, use environment key only when its normalized fingerprint is in the user's approved external-key list;
4. use API key from inherited file descriptor;
5. if helper is configured, it owns the source even when its asynchronous cache is still cold—return helper source with no key rather than falling through;
6. use login-managed key from durable account config/platform keychain;
7. none.

An environment-specific managed homespace may suppress `AGENTX_API_KEY` in favor of its console-managed key.

`AUTH-022` — API-key source tags are `AGENTX_API_KEY`, `apiKeyHelper`, `/login managed key`, and `none`. Never log or persist the raw key in ordinary configuration diagnostics; approval records use a normalized nonreversible identifier where supported.

## Credential helpers

`AUTH-030` — `apiKeyHelper` is code-bearing configuration. If sourced from project or local settings in an interactive session, execute only after workspace trust. Bare mode consults only explicit flag settings.

`AUTH-031` — Helper subprocess uses a shell because the setting is a command, has a 10-minute timeout, and requires successful exit plus nonempty trimmed stdout. Stderr is diagnostic. Do not append shell syntax or expose output in logs.

`AUTH-032` — Default helper cache lifetime is five minutes; a nonnegative integer override may change milliseconds. Invalid override is diagnosed and defaults to five minutes.

`AUTH-033` — Helper cache is stale-while-revalidate:

- fresh value returns immediately;
- stale value returns immediately and one background refresh starts;
- cold cache deduplicates concurrent executions and callers await one result;
- settings/auth reset increments an epoch so an orphaned old helper completion cannot overwrite new state.

`AUTH-034` — A stale refresh failure retains the working stale value and refreshes its timestamp to avoid hammering. A cold failure caches a noncredential sentinel so callers do not fall through to OAuth unexpectedly. User sees the helper error; the sentinel is never sent as a real secret to an incompatible provider.

## Application-owned Azure credential file

`AUTH-044` — Treat a selected plaintext credential file as a credential
container before parsing it. Require a bounded regular non-symlink file, one
filesystem link, stable identity and size across the complete read, and
owner-only access evidence. POSIX adapters reject any group or world permission
bit. An adapter that cannot prove ownership and access control from
authoritative platform metadata must reject credential-file use; synthesized
portable mode bits are not DACL evidence. When the application home has
already been identity-pinned, retain an opened root for that exact directory
through every credential lstat, open, reread, and final identity check. Open
the literal child descriptor-relative to that root; never re-resolve the
credential through the mutable application-home pathname.

`AUTH-045` — The standalone Go Azure OpenAI profile has exactly one model
credential source: the literal `auth.json` child of the application home
resolved by `GCFG-PATH-001G`. After `GCFG-PATH-006` bootstrap and before full
command-line parsing, every invocation requires that path to exist, including
malformed input, help, version, and the standalone MCP tool host. Non-model
surfaces perform only the existence gate: they do not parse credentials or
construct a provider client. Both that gate and a model-backed read use the
descriptor-pinned home required by `AUTH-044`, then reverify the frozen textual
home identity before proceeding. A missing or unusable direct-file diagnostic
must name the expected path, include the stable guide URL
`https://github.com/greenpau/agentx/blob/main/USER_GUIDE.md`,
and show this credential-independent placeholder shape:

```json
{
  "version": 1,
  "provider": "azure_openai",
  "azure_openai": {
    "endpoint": "https://your-resource.openai.azure.com",
    "model": "gpt-5.6-sol",
    "deployment": "gpt-5.6-sol",
    "api_key": "replace-with-your-secret",
    "api_version": "preview"
  }
}
```

A model-backed start reads at most 64 KiB under `AUTH-044` and strictly decodes
one UTF-8 JSON object with no trailing value. The top level has exactly
`version`, `provider`, and `azure_openai`; `version` is integer `1`;
`provider` is the exact string `azure_openai`; and `azure_openai` has exactly
the five string fields shown above. Endpoint, model, deployment, and API key
are nonempty; an empty API-version string selects the provider's default v1
route. Reject unknown or duplicate members at either level, unsupported
versions/providers, and wrong types. Reject unpaired JSON surrogate escapes
without rejecting a valid literal or escaped U+FFFD replacement character.
Require an absolute HTTPS endpoint with no user information, query, or
fragment. The loopback-only HTTP exception is a separately explicit
direct-constructor test seam that application configuration loading never
enables. Model and deployment are each at most 256 UTF-8 bytes. The API key
is at most 16 KiB, has no Unicode whitespace, surrounding HTTP-header
whitespace, control, format, line, or paragraph characters. A nonempty API
version is at most 128 UTF-8 bytes; model, deployment, and API version reject
the same unsafe control/format/line/paragraph character classes. Reject any
invalid endpoint or model/deployment mapping before extension discovery, persistent session
materialization, or provider construction. The selected `api_key` immediately
joins the immutable redaction union. There is no
`.env.production`, arbitrary `--env-file`, or process-environment credential
fallback, even when those legacy sources contain a complete coherent bundle.

## First-party OAuth authorization

`AUTH-040` — Interactive OAuth uses authorization-code grant with PKCE S256 and unpredictable state. Support automatic loopback callback and manual code flow. Authorization parameters include client ID, response type, exact redirect URI, requested scopes, code challenge, state, and optional organization, login hint, login method, or inference-only mode.

`AUTH-041` — Inference-only flow requests only the inference scope. Full account login requests the registered full scope set. A stored token qualifies for model inference only when its scopes include the inference scope.

`AUTH-042` — Token exchange and token refresh are JSON requests bounded to 15 seconds. Exchange validates state/redirect correlation. Refresh uses returned refresh token when present or retains the old one, computes absolute expiration, parses space-separated scopes, and may request the current full AgentX account scope set so older grants can expand under server policy.

`AUTH-043` — Store OAuth tokens in secure platform storage, with a protected credentials-file fallback where required. Durable account profile metadata is separate from the secret token record. Access token, refresh token, authorization code, and verifier never enter transcript or model context.

## OAuth refresh concurrency and 401 recovery

`AUTH-050` — Deduplicate ordinary refresh checks within the process. Before checking expiration, compare protected credentials-file modification time and clear memory/keychain caches if another process changed it.

`AUTH-051` — Expired-token refresh:

1. check cached token and required inference scope;
2. reread secure storage asynchronously;
3. create configuration directory if needed;
4. acquire cross-process directory lock;
5. if locked, retry at most five times after 1–2 seconds jitter;
6. after lock, reread again and adopt another process's fresh token;
7. refresh and atomically store;
8. clear read caches and release lock in `finally`.

`AUTH-052` — Concurrent API 401/revoked-token handlers are deduplicated by failed access token. Clear caches, reread storage, and if the stored token differs, use it. Otherwise force refresh even when local expiry says the token is fresh.

`AUTH-053` — Refresh failure rereads storage once more before returning failure; another process may have succeeded during the failed request. Do not delete a usable token solely because one refresh request failed.

`AUTH-054` — Environment/descriptor tokens without refresh tokens cannot be refreshed. Return an authentication error naming the source class without echoing token content.

## Managed organization enforcement

`AUTH-060` — If managed policy specifies `forceLoginOrgUUID`, first-party OAuth must be authoritatively verified against the profile endpoint before use. Cached user-writable organization metadata is not sufficient.

`AUTH-061` — Refresh token if needed, fetch profile using current access token, and compare exact organization UUID. If profile cannot be fetched or lacks required scope, fail closed with remediation. A token from the wrong organization is rejected with source-appropriate guidance.

`AUTH-062` — An AgentX Unix relay is exempt locally because the local side already performed organization validation and the remote holds only a placeholder. Cloud-provider auth is outside first-party organization enforcement.

## AWS Bedrock adapter

`AUTH-070` — Select AWS region from model-specific small/fast override when applicable, otherwise normal AWS region resolution. Use standard AWS credential provider chain unless auth is explicitly skipped for a trusted proxy/test context.

`AUTH-071` — `AWS_BEARER_TOKEN_BEDROCK` selects bearer API-key auth, sets Authorization, and disables request signing. Otherwise refresh/retrieve AWS access key, secret, and session token; never mix the two modes.

`AUTH-072` — A configured `awsAuthRefresh` from project/local settings requires trust. Before running, test caller identity; invoke refresh only when credentials are unusable. Bound interactive refresh command to three minutes and stream sanitized progress to auth status.

`AUTH-073` — On AWS credential-provider error or Bedrock 403, clear AWS credential/INI caches, construct a fresh provider client, and retry only under common retry bounds.

`AUTH-074` — Treat paths and aliases that select host AWS model credentials, including `AWS_SHARED_CREDENTIALS_FILE`, as model-credential material at child and extension boundaries. An MCP or hook expansion cannot receive the path directly or through a benignly renamed variable merely because the path string is not itself an access key.

## Azure Foundry adapter

`AUTH-080` — Endpoint derives from configured resource or explicit Foundry base URL. If `AGENTX_FOUNDRY_API_KEY` exists, use SDK API-key behavior. Otherwise use Azure default credential to obtain token for `https://cognitiveservices.azure.com/.default`.

`AUTH-081` — A skip-auth mode may install an empty token provider only for an explicitly trusted proxy/test environment. Do not silently fall back from failed Azure identity to first-party credentials.

## Google Vertex adapter

`AUTH-090` — Resolve model-specific region first, then global cloud region, configured default, and final supported fallback. Require project identity from explicit Vertex/project discovery.

`AUTH-091` — Before client creation, run configured GCP auth refresh when credentials are expired unless skip-auth is explicitly enabled. Google auth uses cloud-platform scope.

`AUTH-092` — Avoid metadata-server project discovery when explicit project/environment/credential-file facts already determine the project. A configured AgentX Vertex project may be a last-resort project fallback.

`AUTH-093` — On Vertex 401 or known Google credential-refresh/load errors, clear GCP caches, construct a new client, and retry under common bounds. Never reuse AWS/first-party credential paths.

## Failure and recovery

| Failure | Behavior |
| --- | --- |
| no credential in interactive first-party session | route to login/key setup surface |
| no credential in CI/bare | fail clearly; no interactive fallback |
| helper untrusted | do not execute; source remains visible |
| secure storage read failure | report auth unavailable; do not log file contents |
| refresh lock exhausted | retain current token state and return bounded auth failure |
| org cannot be verified | fail closed when policy requires it |
| cloud provider auth failure | clear only that provider cache and bounded retry |

## Acceptance scenarios

**AUTH-A01 — Managed credential isolation.** Managed desktop has user's settings helper and a launcher OAuth token. Launcher OAuth wins; helper never executes.

**AUTH-A02 — CI source precedence.** CI has API key descriptor and environment key. Descriptor wins. CI with OAuth token but no API key lets API-key lookup return none without throwing.

**AUTH-A03 — Stale helper fanout.** Helper cache is stale and three requests arrive. All receive stale value immediately; one refresh runs. If it fails, stale value remains.

**AUTH-A04 — Cross-process refresh race.** Two processes refresh an expired OAuth token. Lock winner writes fresh credentials; loser rereads after lock and skips refresh.

**AUTH-A05 — Managed organization fail-closed.** Policy requires organization A but profile fetch fails. Authentication fails closed even if local account metadata says A.

**AUTH-A06 — Cloud credential retry isolation.** Bedrock 403 clears AWS caches and retries with a fresh Bedrock client; no AgentX account OAuth header is attached.

**AUTH-A07 — Credential-file alias isolation.** The host configures `AWS_SHARED_CREDENTIALS_FILE` and a benignly named variable with the same path. MCP configuration expansion rejects both values without exposing the path, while an unrelated explicit server-owned credential remains usable.

**AUTH-A08 — Exact-literal egress closure.** Configure one session credential plus one provider credential whose independently chosen replacement markers would recreate each other. Exercise ordinary output, structured JSON aliases/scalars, metadata, progress, truncation at every byte boundary, persistence, hooks, and model continuation. Construct a bounded union containing the conventional mask plus every guard candidate and verify session composition rejects it before Azure, interactive, task, transcript, or structured sinks start; no pre-closed sanitizer may turn safe output into a false success. Use credentials equal to the canonical separator sequence between two otherwise safe JSON leaves and to a safe JSON suffix plus its physical line terminator; verify the exact final framed bytes fail closed rather than reconstructing either literal after body-only validation. Configure Azure endpoint host, path, decoded API-version query, and User-Agent aliases plus one contributed credential embedded within the selected Azure key; construction and the final pre-dispatch guard reject each with zero transport calls, while an exact duplicate of the selected key in the frozen union remains valid. Submit duplicate object members whose earlier escaped value decodes to a credential but whose later value is safe; every occurrence is inspected, and no last-write-wins normalization can authorize retention of the original raw bytes. Submit a malformed tool argument whose unfinished escape decodes toward a credential; persistence, replay, and continuation retain only the safe schema-invalid projection while the call still receives one terminal result. Wrap a secret-bearing classified error at both the engine and application operational boundaries and verify `%v`, `%+v`, and `%#v` expose only the sanitized message while `errors.Is` still succeeds and `errors.Unwrap` returns nil. Configure credential `a�b`, supply error text `a\x01b`, and verify unsafe-control normalization is followed by redaction in both the returned error and turn-result record. No semantic or encoded output contains either literal; bounded nonstreaming guard exhaustion produces explicit suppression without synthetic fallback text, and no capability context contains a reflectable credential value.

**AUTH-A09 — Unverifiable credential-file access.** A Windows build without
native owner/DACL inspection is given an otherwise regular `auth.json`. A
model-backed start is unavailable and fails before parsing it; operator ACL
configuration alone cannot make the current adapter accept it. A POSIX build accepts mode `0600`
and rejects mode `0640`, symlinks, and hard-linked aliases.

**AUTH-A10 — Provider diagnostic composition closure.** Configure one credential equal to a provider-error label plus separator and adjacent message, another equal to an invalid raw `Retry-After` value, and model/deployment identities equal to or containing the API key. Force one retry and format the provider error, retry callback value, retry-exhaustion wrapper, and model client through `%s`, `%v`, `%+v`, and `%#v`. No rendering contains a credential or configured identity, the raw header is absent, and changing the callback's detached structured error does not change the second attempt or terminal retry classification. `errors.As` still reaches the safe structured provider error through the terminal wrapper.

**AUTH-A11 — Missing application credential.** Start with an isolated missing
application home and invoke one malformed argument form plus one interactive,
headless, help-only, version-only, and standalone-MCP form. Each process first
leaves the private application home and `sessions/` child present, then exits
nonzero because `auth.json` is absent. The malformed form reports the
credential prerequisite rather than usage until a direct regular `auth.json`
exists. Structured stdout remains empty; the diagnostic contains the resolved
path, stable GitHub guide URL, and placeholder object but no credential. No
provider, extension generation, persistent session child, or MCP request loop
starts.
Replace the missing child in turn with a directory and a direct symlink; both
remain fail-closed and retain the same guide, expected path, and placeholder
shape without reading a target.

**AUTH-A12 — Strict schema and no legacy fallback.** With private
`auth.json`, accept the exact version-1 Azure object and construct one client
from its values. Independently reject an unknown field, duplicate field,
second JSON value, unsupported version/provider, wrong type, empty
endpoint/model/deployment/API key, insecure file, and oversized file before
provider or persistent-session construction. Verify an empty API-version string
selects the v1 default. Pin the original application-home root, rename that
directory, and put a different valid `auth.json` at the old pathname: the
credential loader reads only the original descriptor-rooted child, while the
application boundary rejects the changed textual home identity. Repeat with a
valid `.env.production` and a complete Azure process environment while
`auth.json` is missing or invalid; neither legacy source changes the failure.

## Non-normative provenance

Reference behavior was specified from authentication source resolvers, helper cache, secure storage/keychain adapters, OAuth service/client, account profile enforcement, cloud credential refreshers, and provider-specific API client construction under `utils/auth*`, `services/oauth/`, and API/provider utilities. Paths and symbols are provenance only.
