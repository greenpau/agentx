# Web, MCP, LSP, and external-data contracts

## Trust boundary

Treat URLs, redirects, DNS answers, search snippets, page contents, MCP descriptors, schemas, annotations, progress, resource URIs, content blocks, and protocol metadata as untrusted. Separate four decisions: the capability is discoverable, its input is structurally valid, policy permits the requested destination/action, and the transport succeeds. One does not imply another.

## `WebFetch`

Input contains `url` and a prompt describing extraction/transformation.

- Accept only HTTP(S), with a URL no longer than 2,000 characters. Reject embedded credentials and malformed or non-public-looking hostnames.
- Check preapproved exact URLs/hosts first, then compose domain-scoped allow/ask/deny policy. Redirects require revalidation; authorization for one host is not authority for another.
- Use a 60-second request ceiling, at most 10 same-host redirects, and a 10 MiB response ceiling.
- Cache eligible successful fetches for 15 minutes with a 50 MiB cache budget. Cache keys must include every request property that changes observable content or authority.
- Convert supported public text to a model-usable form and cap transformed markdown at 100,000 characters. Persist binary content and return a safe path/metadata rather than decoding arbitrary bytes as text.
- Return an explicit blocked, timeout, HTTP, redirect, content-type, size, or transform error. Never render fetched instructions as trusted system policy.

The specified descriptor is concurrency-safe and read-only. Its `openWorld` annotation is absent/false even though transport is networked; destination permission remains mandatory and must not be inferred from that annotation.

## `WebSearch`

Input includes a query of at least two characters and optionally either `allowed_domains` or `blocked_domains`, never both. Normalize domains without broadening them. Invoke at most eight provider searches for one tool call.

Return concise results with title, URL, support text, and provider citations/commentary. Keep citation identity attached through projection. Distinguish no results, provider refusal, rate limit, authentication failure, malformed provider output, and transport error. The descriptor is concurrency-safe, read-only, and deferrable; exposure depends on selected provider and runtime availability.

## Deferred discovery with `ToolSearch`

Input is a query and optional `max_results`, default 5.

- `select:A,B` requests exact canonical names or aliases and preserves the requested order after canonicalization.
- Otherwise support keyword scoring and `+required` terms. Match search hints, descriptions, canonical names, and aliases without treating external text as executable syntax.
- Return tool-reference blocks, not calls. Zero matches may include pending-MCP connection context but must not fabricate a descriptor.
- Always-loaded descriptors bypass deferral. Discovery never bypasses profile or deny filtering.

## MCP tool naming and descriptors

Construct canonical names as `mcp__<normalized-server>__<normalized-tool>`. Preserve raw `serverName` and `toolName` in protocol metadata for transport and permission matching. An SDK compatibility mode may display the original name without the prefix, but permission rules still match the fully qualified identity.

For each discovered descriptor:

1. Sanitize the search hint to one line and truncate the model-visible description to 2,048 characters.
2. Validate the external JSON Schema before registry insertion; reject unsupported or unsafe schema constructs explicitly.
3. Interpret annotations conservatively: missing `readOnlyHint`, `destructiveHint`, or `openWorldHint` means false for read-only and true-risk defaults through the shared descriptor policy. Concurrency follows the proven read-only annotation unless a stronger local contract exists.
4. Preserve `alwaysLoad` only from a trusted local configuration decision, not arbitrary tool text.
5. Enforce server-prefix and tool-specific deny rules before exposure; built-ins win name collisions.
6. On invocation, validate input locally, run common permission/hook policy, call the currently connected server, normalize progress and output, and retry at most once for a recognized session-expiry refresh.
7. Preserve optional `_meta` and `structuredContent` as extension fields only after size/type validation. Every accepted call still gets one terminal model result.

## MCP authentication and resources

`mcp__<server>__authenticate` has a 10,000-character cap. It reports already-authenticated, unsupported, user-action-required, success, cancellation, and failure distinctly. It may open an external OAuth flow only through the platform/user-interaction contract.

`ListMcpResourcesTool` accepts an optional server name. Query eligible servers independently; one server failure is included as a scoped error while other results survive. Cache listings and invalidate on list-changed events, connection close, or configuration changes.

`ReadMcpResourceTool` requires exact `server` and `uri`. Require a connected server with resource-read support. Return normalized text content; decode and persist binary/blob content using a safe extension derived from validated media type, then return its path and metadata. Reject unknown servers, unknown resources, invalid base64, oversized content, and unsupported block types explicitly.

## `RemoteTrigger`

Support actions `list|get|create|update|run`. `list` and `get` are read-only; mutations are writes. Validate trigger IDs against a word/hyphen identifier contract and bodies as bounded records. Require the remote-trigger build gate, runtime enrollment, authenticated first-party account, and managed policy allowing remote sessions. Send the specified protocol beta marker `ccr-triggers-2026-01-30`. Normalize HTTP status and response JSON; redact credentials and transport headers.

## External-family acceptance cases

- **WX-A01:** A permitted URL redirecting to a private or unapproved host is stopped before the second request.
- **WX-A02:** Search input containing both allow and block lists is rejected structurally; no provider request occurs.
- **WX-A03:** A malicious MCP description cannot set system instructions, alter permission rules, or become an always-load decision.
- **WX-A04:** A missing MCP read-only annotation schedules sequentially and cannot inherit a prior version's classification.
- **WX-A05:** One MCP server failing during resource listing does not erase successful resources from another; the failure remains visible and attributable.
- **WX-A06:** A binary MCP resource persists with bounded validated bytes and returns a stable saved-path result across resume.
- **WX-A07:** A stale MCP tool accepted before disconnect receives an unavailable terminal result with the original tool-use ID.
- **WX-A08:** Remote trigger `run` is denied when managed policy disables remote sessions even if account and feature gates pass.

## Non-normative provenance

The specified evidence included web fetch/search implementations, MCP descriptor adapters and resource tools, LSP schemas, and a remote-trigger client. These are provenance only; external protocol libraries and transport implementations are replaceable.
