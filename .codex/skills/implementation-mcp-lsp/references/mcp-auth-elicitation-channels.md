# MCP authentication, elicitation, and channels contract

This document defines OAuth, extended authorization, elicitation, and server-initiated channel delivery. `MCPOAUTH-*` and `CHANNEL-*` identifiers are normative.

## Contents

1. [OAuth configuration and state](#oauth-configuration-and-state)
2. [Callback listener](#callback-listener)
3. [Token lifecycle and step-up authorization](#token-lifecycle-and-step-up-authorization)
4. [Elicitation lifecycle](#elicitation-lifecycle)
5. [Channel capability model](#channel-capability-model)
6. [Security and failure behavior](#security-and-failure-behavior)
7. [Acceptance scenarios](#acceptance-scenarios)
8. [Non-normative provenance](#non-normative-provenance)

## OAuth configuration and state

`MCPOAUTH-001` — Keep MCP OAuth credentials separate per canonical server identity, authorization server, client identity, and user/account. Store access/refresh tokens and dynamic client secrets in secure storage, never ordinary project configuration.

`MCPOAUTH-002` — OAuth configuration may specify client ID, callback port, HTTPS authorization-server metadata URL, and extended-authorization (`xaa`) enablement. Discover protected-resource and authorization-server metadata only from policy-valid HTTPS origins; validate issuer/endpoints before use.

`MCPOAUTH-003` — Authorization requests use PKCE and unpredictable state. Bound metadata, registration, token, and refresh requests to 30 seconds and redact tokens, authorization codes, verifiers, secrets, and sensitive headers from logs.

`MCPOAUTH-004` — Acquire an interprocess credential lock with at most five retries before mutating shared token/client-registration state. After lock acquisition, reread storage so another process's successful grant wins the race.

## Callback listener

`MCPOAUTH-010` — If configured callback port is valid and available, use it. Otherwise select a random port:

- ordinary range 49152–65535;
- Windows compatibility range 39152–49151;
- at most 100 attempts;
- final compatibility fallback port 3118.

Bind loopback only, validate state exactly, accept one terminal callback, and close listener on success, error, cancel, or timeout.

`MCPOAUTH-011` — When no browser/listener is available, expose the authorization URL and use the active surface's structured/manual completion protocol. Never accept an authorization code without its correlated state/request identity.

## Token lifecycle and step-up authorization

`MCPOAUTH-020` — Before a request, use a nonexpired access token. If refresh is possible, deduplicate refresh and atomically replace credentials; on invalid grant clear only unusable MCP credentials for that server and enter `needs-auth`.

`MCPOAUTH-021` — Cache `needs-auth` detection/status for 15 minutes to avoid repeated browser prompts. Explicit user retry invalidates this cache.

`MCPOAUTH-022` — On HTTP 403 `insufficient_scope`, parse and validate requested scopes, perform bounded step-up authorization, and cache the resulting scope-specific grant. Do not discard or overwrite the still-usable base grant.

`MCPOAUTH-023` — A step-up grant omits refresh behavior where required by the authorization-server contract, so refreshing a base token cannot silently claim elevated scope. Reissue the affected MCP request only after the new grant is stored and request identity remains active.

`MCPOAUTH-024` — Extended authorization (`xaa`) is a separate optional flow with a 30-second network bound and explicit identity-provider login protocol. Failure returns `needs-auth`/authorization error; it cannot fall back to sending unauthenticated sensitive requests.

## Elicitation lifecycle

`MCPOAUTH-030` — Elicitation is a server request for user-provided form data or URL-mediated action. Treat message, requested schema, URL, and identifiers as untrusted protocol input.

`MCPOAUTH-031` — Processing order:

1. validate server capability, request schema, mode, URL, and identifiers;
2. emit `Elicitation` hooks; a structured hook may accept/decline/cancel within authority;
3. if unresolved, route to interactive UI or SDK structured-control request;
4. validate accepted content against requested schema;
5. emit `ElicitationResult` hooks, which may override according to hook contract;
6. return exactly one accept/decline/cancel response to the server.

`MCPOAUTH-032` — For validation error `-32042`, provide bounded correction feedback and retry at most three times. After the third failed correction, decline/cancel with explicit protocol error; never loop indefinitely.

`MCPOAUTH-033` — URL-mode elicitation opens/displays only policy-valid URL. Correlate browser/SDK completion to elicitation ID and send the documented completion notification. Disconnect/cancel returns cancel exactly once.

`MCPOAUTH-034` — Hooks cannot bypass schema validation or URL policy. `Elicitation` exit 2 means deny; `ElicitationResult` exit 2 converts result to decline.

## Channel capability model

`CHANNEL-001` — A channel is server-initiated content/events admitted into a session. Connection or MCP notification support alone does not authorize channel delivery.

`CHANNEL-002` — Admission requires every gate in order:

1. server advertises the channel capability;
2. runtime/build enables channels;
3. OAuth grant includes required scopes;
4. managed policy permits channels (team/enterprise defaults off unless `channelsEnabled=true` or equivalent);
5. session configuration enables the channel;
6. source is an eligible marketplace/plugin identity where required;
7. effective channel plugin/server allowlist permits it;
8. per-method/channel permission is granted.

Any failed gate rejects or ignores delivery with source-attributed status.

`CHANNEL-003` — Organization-provided `allowedChannelPlugins` replaces, not appends to, the ordinary remote feature/ledger allowlist. Development entries may have an explicit per-entry bypass for testing. A nondevelopment server kind never becomes allowed solely through plugin allowlist matching.

`CHANNEL-004` — Channel permissions enumerate exact protocol capability and notification methods. Never authorize with a broad local regular expression over arbitrary method names.

`CHANNEL-005` — Normalize channel content into safe model/user messages. Escape XML-like wrappers and metadata; metadata keys follow strict identifier grammar. Preserve server, channel, event identity, timestamp/order, and provenance without accepting executable markup.

`CHANNEL-006` — Apply queue bounds, ordering, duplicate suppression, cancellation, and backpressure. A disconnected or disabled channel cannot enqueue into a later unrelated session.

## Security and failure behavior

`MCPOAUTH-040` — Never include MCP tokens, client secrets, codes, PKCE verifier, or raw auth metadata in model context, transcript, plugin environment, or ordinary diagnostics.

`MCPOAUTH-041` — Validate redirect origin, issuer, authorization endpoint, token endpoint, and protected resource relationship to prevent confused-deputy grants. A server cannot redirect OAuth to an unrelated unapproved host.

`MCPOAUTH-042` — Authentication cancellation affects only the pending server/request. Other MCP connections remain active.

`CHANNEL-010` — Malformed, oversized, unauthorized, or duplicate channel notifications are dropped with bounded diagnostics. They do not disconnect ordinary MCP tools unless the protocol itself is compromised.

`CHANNEL-011` — On policy/settings change, reconcile subscriptions: stop newly disallowed delivery, drain/drop queued items according to identity, and require fresh admission for re-enable.

## Acceptance scenarios

1. **MCPOAUTH-A01 — Refresh lock.** Two processes detect expired MCP token. One acquires the lock and refreshes; the second rereads and uses it without a second refresh.
2. **MCPOAUTH-A02 — Scope step-up.** Server asks for elevated scope after a 403. Base token remains stored; a scope-specific grant is obtained and only the affected active request retries.
3. **MCPOAUTH-A03 — Callback exhaustion.** Callback-port selection collides 100 times. Runtime attempts fallback 3118, and failure there terminates with needs-auth rather than binding a non-loopback wildcard.
4. **MCPOAUTH-A04 — Elicitation retry ceiling.** Elicitation content fails schema three times. The fourth form prompt never appears; server receives terminal decline/error.
5. **CHANNEL-A01 — Managed gate.** A channel-capable server is connected and allowlisted, but managed team policy has not enabled channels. Tools work; channel events are rejected.
6. **CHANNEL-A02 — Organization replacement.** Organization `allowedChannelPlugins` is present. It replaces the ordinary ledger, so an entry allowed only by the old ledger is rejected.

## Non-normative provenance

Reference behavior was specified from MCP OAuth and port selection, extended authorization, elicitation handlers/validators, channel allowlist/permission/notification services, connection manager, hook integration, and SDK control transport under `services/mcp/` and MCP utilities. Paths and symbols are provenance only.
