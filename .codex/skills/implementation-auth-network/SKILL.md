---
name: implementation-auth-network
description: Implement credential-source selection, OAuth and cloud-provider authentication, API client construction, proxy and TLS behavior, streaming and non-streaming requests, retries, fallback, and file transfer. Use when implementing the model/account network boundary or diagnosing transport and authentication behavior.
---

# Implementation Authentication and Network

## Preserve provider and credential isolation

Authentication selects an authorized principal; networking carries bounded requests for that principal. Keep first-party OAuth, direct API keys, managed relay credentials, AWS, Azure, and Google credentials behind provider adapters. Never let credentials, provider-specific headers, or refresh behavior leak across adapters.

Use the [architecture diagram](assets/architecture.drawio) to inspect credential selection and request recovery. Read [credential and provider contract](references/credential-provider.md) for auth-source precedence, trust, OAuth, secure storage, managed organization checks, and cloud adapters. Read [request, stream, and retry contract](references/request-stream-retry.md) for client construction, request metadata, streaming state, timeouts, retryability, capacity fallback, and cancellation. Read [proxy, TLS, and file transfer](references/proxy-tls-files.md) for proxy precedence, bypass rules, certificates, Unix relays, and bounded file operations. Requirement identifiers `AUTH-*`, `NET-*`, `RETRY-*`, `TLS-*`, and `FILEAPI-*` are stable implementation anchors.

## Request workflow

1. Select exactly one API provider from startup configuration and validate its mandatory environment and account inputs.
2. Determine the credential source without executing untrusted helpers; after workspace trust, resolve or refresh the selected credential through its provider adapter. In the standalone Go Azure OpenAI profile, use only the versioned application-home `auth.json` contract in `AUTH-045`; workspace trust and bare mode do not select a different model credential source.
3. Construct a fresh client when credentials or connection health require it. Attach only headers supported by the selected provider and destination.
4. Build the request from normalized messages, prompt sections, tools, model,
   limits, and provider-compatible beta fields. For qualified native media,
   perform the only provider-egress materialization of immutable
   session-store bytes and complete decoded/encoded/final-request preflight
   before opening transport. Admission and recovery may independently ask the
   store to verify its own identities and digests without exposing bytes.
5. Consume the response stream into explicit message and usage events while watchdogs, cancellation, and incomplete-stream detection remain active.
6. Classify failures. In DEBUG, retain the safe provider status/code, request ID, route family, version-source class, attempt, and retry decision without emitting the endpoint, deployment, exact configured API version, URL/query, headers, body, or credential. Refresh credentials or disable a stale connection pool when appropriate, then retry with bounded backoff or persistent heartbeats according to source and policy.
7. If a recoverable stream fails before a coherent terminal message, optionally issue a bounded non-streaming fallback without duplicating already-started side effects.
8. Normalize the terminal response or error and retain correlation identifiers without logging secrets.

## Invariants

- Bare mode is hermetic: no OAuth, user keychain, or ordinary settings credentials.
- The standalone Go Azure OpenAI profile requires application-home `auth.json`
  to exist on every invocation, before full command-line parsing, and strictly
  parses it only for model-backed startup. It is the profile's sole model
  credential source. Both the presence gate and strict read remain
  descriptor-relative to the frozen application-home identity; pathname
  replacement cannot redirect them. Model-backed use is limited to platforms
  where the credential adapter can prove owner-only file access; the current
  Windows adapter fails closed before reading because native DACL verification
  is unavailable.
- Managed remote or desktop OAuth contexts never fall back to a user's local settings key or helper.
- Authentication refresh is deduplicated within a process and locked across processes; another process's fresh token wins a race.
- Explicit user cancellation is never retried or silently converted to transport failure.
- A background auxiliary request does not amplify a 529 capacity incident; foreground and safety-critical requests follow the documented retry policy.
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
- An `ECONNRESET` retry can disable keep-alive and construct a fresh client without changing the logical request.
- An Azure response that explicitly rejects the configured API version is one nonretryable configuration failure. It preserves safe provider correlation and remediation, does not rewrite `auth.json`, and does not retry or switch routes behind the operator's back.
- `retry-after` overrides exponential delay; ordinary backoff starts at 500 ms, adds up to 25% jitter, and caps at 32 seconds.
- A stream with no `message_start`, or with no completed content and no stop reason, uses the documented fallback instead of reporting false success.
- A 500 MiB-plus upload is rejected after reading and before network transmission, and a download path attempting traversal is rejected before directory creation.
