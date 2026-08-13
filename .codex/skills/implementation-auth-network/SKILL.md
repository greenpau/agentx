---
name: implementation-auth-network
description: Implement credential-source selection, OAuth and cloud-provider authentication, API client construction, proxy and TLS behavior, streaming and non-streaming requests, retries, fallback, and file transfer. Use when implementing the model/account network boundary or diagnosing transport and authentication behavior.
---

# Implementation Authentication and Network

## Preserve provider and credential isolation

Authentication selects an authorized principal; networking carries bounded requests for that principal. Keep first-party OAuth, direct API keys, managed relay credentials, AWS, Azure, and Google credentials behind provider adapters. Never let credentials, provider-specific headers, or refresh behavior leak across adapters.

Use the [architecture diagram](assets/architecture.drawio) to inspect credential selection and request recovery. Use the [strict provider-registry diagram](assets/provider-registry-v2.drawio) to trace complete-registry validation, deterministic selection, credential-safe catalog publication, and the durable provider-binding gate. Read [credential and provider contract](references/credential-provider.md) for auth-source precedence, trust, OAuth, secure storage, managed organization checks, and cloud adapters. Read [request, stream, and retry contract](references/request-stream-retry.md) for client construction, request metadata, streaming state, timeouts, retryability, capacity fallback, and cancellation. Read [proxy, TLS, and file transfer](references/proxy-tls-files.md) for proxy precedence, bypass rules, certificates, Unix relays, and bounded file operations. Requirement identifiers `AUTH-*`, `NET-*`, `RETRY-*`, `TLS-*`, and `FILEAPI-*` are stable implementation anchors.

Distinguish the broader target contracts in those references from the current
standalone Go profile. Its Azure transport implements one streaming route with
bounded same-provider retries before the first provider event. It does not
install a non-streaming fallback, source-aware 529/model fallback, fast mode,
persistent unattended retry, or the specialized API-version-mismatch
classifier. Never present a contract-only recovery path as current runtime
behavior; consult the conformance profile when a task depends on availability.

## Request workflow

1. Strictly validate the complete provider registry, then select exactly one API provider from startup configuration before constructing session or transport state. Preserve the explicit-selector, singleton, default, no-fallback, and durable-binding rules in `AUTH-046`.
2. Determine the credential source without executing untrusted helpers; after workspace trust, resolve or refresh the selected credential through its provider adapter. In the standalone Go Azure OpenAI registry, use only the versioned application-home `auth.json` contract in `AUTH-045`–`AUTH-047`; workspace trust and bare mode do not select a different model credential source.
3. Construct a fresh client when credentials or connection health require it. Attach only headers supported by the selected provider and destination.
4. Build the request from normalized messages, prompt sections, tools, model,
   limits, and provider-compatible beta fields. For qualified native media,
   perform the only provider-egress materialization of immutable
   session-store bytes and complete decoded/encoded/final-request preflight
   before opening transport. Admission and recovery may independently ask the
   store to verify its own identities and digests without exposing bytes.
5. Consume the response stream into explicit message and usage events while watchdogs, cancellation, and incomplete-stream detection remain active.
6. Classify failures without adding endpoint, deployment, configured API
   version, URL/query, headers, body, or credential as runtime-owned diagnostic
   fields. Credential-redact and bound provider prose before surfacing it. In
   the standalone profile, publish only safe WARN retry diagnostics with
   attempt, delay, model, and session correlation; do not claim
   route-family/version-source fields or a structured SDK retry event that the
   runtime does not emit. The current generic provider-error path can still
   retain a nonsecret configured API-version token echoed by provider prose.
7. Retry a byte-identical preflighted streaming request only while no provider
   event has been accepted and the retry count/window permit it. Once any
   provider event is observed, treat later failure as terminal. Never switch
   provider profiles, models, or routes, and do not issue a non-streaming
   fallback in the current standalone profile.
8. Normalize the terminal response or error and retain correlation identifiers without logging secrets.

## Invariants

- Bare mode is hermetic: no OAuth, user keychain, or ordinary settings credentials.
- The standalone Go Azure OpenAI registry requires application-home `auth.json`
  to exist on every invocation, before full command-line parsing. It strictly
  parses the version-2 provider registry for both model-backed startup and
  standalone provider discovery; the latter deliberately performs no profile
  selection or provider I/O. It is the registry's sole model credential
  source. Both the presence gate and
  strict read remain descriptor-relative to the frozen application-home
  identity; pathname replacement cannot redirect them. Model-backed use is
  limited to platforms where the credential adapter can prove owner-only file
  access; the current Windows adapter fails closed before reading because
  native DACL verification is unavailable.
- Validate every configured profile before selection. Select by exact explicit
  provider ID, singleton, or one declared default; never infer a provider from
  the logical model and never fall back to another profile after a request
  failure. Provider discovery validates the same complete registry but does
  not select a profile, so a valid multi-provider registry with no default can
  be enumerated before launch. Persist and verify the selected provider
  binding before resume.
- Treat each profile's operator-declared reasoning efforts as its authoritative
  subset. Enforce that subset during startup, live effort changes, durable
  restore, and provider request construction, but do not describe it as remote
  deployment introspection.
- Freeze every configured provider API key, including unselected keys, into the
  complete credential union before composing extension credentials or opening
  shared output, persistence, model, task, or SDK boundaries. Public provider
  catalogs contain identity and capability metadata only.
- Managed remote or desktop OAuth contexts never fall back to a user's local settings key or helper.
- Authentication refresh is deduplicated within a process and locked across processes; another process's fresh token wins a race.
- Explicit user cancellation is never retried or silently converted to transport failure.
- In profiles that install source-aware capacity handling, a background
  auxiliary request does not amplify a 529 incident. The standalone profile
  has no auxiliary/model fallback and follows its ordinary bounded retry rules.
- Proxy, mTLS, and custom CA behavior applies consistently to supported HTTP stacks, but an AgentX-only Unix relay must never capture MCP or arbitrary web traffic.
- Downloads and uploads validate paths, sizes, status classes, concurrency, and cancellation before changing workspace state.
- Native user attachments are not public Files API uploads. Their provider
  request body and data URLs are provider-stream-owned, never observable
  payloads, copied byte-identically across bounded transport attempts, and
  cleared at terminal settlement. A known unsupported/tampered/over-limit set
  makes zero network calls.

## Verification checks

- In CI, a descriptor-provided key outranks the environment key, while a configured OAuth token causes API-key lookup to return none rather than fail.
- Concurrent expired-token requests produce one refresh, and a token refreshed by another process is adopted without a second refresh.
- A retryable transport/open failure before the first provider event reuses the
  same selected provider, client configuration, and byte-identical request;
  the standalone profile does not currently disable keep-alive or construct a
  replacement client as a retry side effect.
- A configured Azure API-version selector is sent literally and never
  rewritten. The standalone profile does not yet recognize the provider's
  version-mismatch prose as a special nonretryable configuration class, so
  ordinary status and `x-should-retry` rules apply and bounded safe provider
  prose can echo that nonsecret selector. It still never edits `auth.json` or
  switches providers/routes.
- A singleton provider needs no explicit default. With several providers, an exact `--provider` wins; otherwise exactly one `default: true` is required. Unknown selectors, multiple defaults, and an unselected registry with no default fail before provider construction.
- `--list-providers --output-format json` advertises every credential-free
  descriptor and exact reasoning subset without selecting a provider or
  constructing a session. A fieldless correlated `initialize` response
  publishes the same descriptors with exactly one selected profile, while the
  startup `system/init` event contains only that selected profile and active
  requests remain fixed to it.
- `retry-after` overrides the 32-second ordinary exponential-delay cap only
  when the directed wait fits within the standalone two-minute total retry
  window; ordinary backoff starts at 500 ms and adds up to 25% jitter.
- An open, timeout, watchdog, or incomplete-stream failure before the first
  provider event may start another bounded streaming attempt. After the first
  provider event it is terminal; there is no non-streaming fallback.
- A 500 MiB-plus upload is rejected after reading and before network transmission, and a download path attempting traversal is rejected before directory creation.
