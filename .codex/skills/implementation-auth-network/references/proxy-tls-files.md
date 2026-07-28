# Proxy, TLS, and file-transfer contract

This document defines proxy selection, bypass, mTLS/custom CA behavior, relay scoping, connection-pool recovery, and public Files API operations. `TLS-*` and `FILEAPI-*` identifiers are normative.

## Contents

- [Proxy selection and bypass](#proxy-selection-and-bypass)
- [mTLS and certificate trust](#mtls-and-certificate-trust)
- [AgentX Unix relay and connection pooling](#agentx-unix-relay-and-connection-pooling)
- [Files API wire contract](#files-api-wire-contract)
- [Download](#download)
- [Upload](#upload)
- [Batch behavior](#batch-behavior)
- [Failure and recovery](#failure-and-recovery)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Proxy selection and bypass

`TLS-001` — Proxy environment precedence is:

1. lowercase `https_proxy`;
2. uppercase `HTTPS_PROXY`;
3. lowercase `http_proxy`;
4. uppercase `HTTP_PROXY`.

Lowercase `no_proxy` outranks uppercase `NO_PROXY` for direct checks; HTTP-stack adapters may receive the canonical selected value.

`TLS-002` — `NO_PROXY` entries split on comma or whitespace. Support:

- `*` bypass all;
- exact hostname/IP;
- leading-dot suffix matching both base and subdomains, never lookalikes;
- exact host:effective-port, with HTTPS default 443 and HTTP default 80.

Invalid request URL does not bypass proxy. Normalize hostname case.

`TLS-003` — Configure each supported HTTP stack consistently. Remove previous global interceptors/default agents before reconfiguration so settings reload does not stack handlers. Per-request bypass uses direct mTLS/CA agent when configured, otherwise removes proxy agents.

`TLS-004` — A proxy may resolve destination hostnames only when the explicit proxy-resolves-hosts switch is enabled. In that mode, local lookup returns hostname/address-family to the proxy adapter instead of performing local DNS. This is deployment behavior, not an SSRF bypass for direct HTTP hooks.

`TLS-005` — WebSocket adapters use the selected proxy agent or native proxy URL and honor the same bypass decision. AWS SDK clients receive an explicit proxy request handler and credential-provider network path.

## mTLS and certificate trust

`TLS-010` — mTLS environment inputs are:

```text
AGENTX_CLIENT_CERT             path to PEM client certificate
AGENTX_CLIENT_KEY              path to PEM private key
AGENTX_CLIENT_KEY_PASSPHRASE   passphrase
```

Read cert/key as text through the filesystem abstraction. A read failure is diagnosed and omits that component; never log contents/passphrase.

`TLS-011` — Create an mTLS agent only when client credentials or custom CA configuration exists. Enable keep-alive initially. Apply cert, key, passphrase, and CA consistently to direct HTTPS, proxy CONNECT request TLS, WebSocket TLS, and fetch/HTTP adapter formats.

`TLS-012` — Custom CA behavior:

| Inputs | Trust set |
| --- | --- |
| neither extra CA nor system-CA option | runtime default; no explicit override |
| extra CA only | bundled Mozilla roots plus extra file contents |
| system-CA option only | system roots; if runtime handles it natively, leave explicit CA unset |
| system-CA plus extra | system roots (or bundled fallback) plus extra contents |

Setting explicit CA replaces runtime defaults, so always include an appropriate base root set before appending `NODE_EXTRA_CA_CERTS`.

`TLS-013` — Memoize certificate/proxy material for performance, and clear CA, mTLS, and proxy caches when relevant environment/settings change. A failed extra-CA read is diagnosed; retain available base roots rather than returning an empty trust set.

## AgentX Unix relay and connection pooling

`TLS-020` — `AGENTX_UNIX_SOCKET` routes only first-party AgentX API client traffic through the launcher relay. MCP HTTP/SSE, web fetches, hooks, and other destinations must not inherit it because the relay fixes its upstream to the AgentX API.

`TLS-021` — On a runtime that supports native Unix fetch, first-party proxy options select the Unix socket before environment proxy. Other runtimes use their dedicated relay adapter or report unsupported; never reinterpret the socket path as an HTTP URL.

`TLS-022` — After stale pooled connection evidence (`ECONNRESET`/`EPIPE`) and its feature gate, disable fetch keep-alive for the process lifetime. Clear/rebuild the client so retry opens a fresh connection. This is sticky because the pool is known unreliable.

## Files API wire contract

`FILEAPI-001` — Default base URL precedence is `AGENTX_BASE_URL`, then application API base URL, then `https://api.agentx.com`. Files API uses OAuth bearer token plus API version `2023-06-01` and beta values equivalent to `files-api-2025-04-14,oauth-2025-04-20`.

`FILEAPI-002` — A file attachment descriptor is `{fileId, relativePath}`. A result includes file ID/path, success, optional error, and byte count. Batch results preserve input order.

`FILEAPI-003` — Shared Files API retry performs at most three total attempts, starting at 500 ms and doubling between attempts (500, 1,000 ms waits). It is separate from the model API retry policy.

`FILEAPI-004` — The public remote Files API is not the native user-attachment
import subsystem. Native CLI and stream-JSON image/PDF inputs use the
session-owned immutable attachment store and the query/model provider boundary;
they do not upload through `/v1/files`, inherit the 500 MiB Files API limit,
or gain arbitrary filesystem/remote-file authority from a `fileId` descriptor.

## Download

`FILEAPI-010` — Download `GET /v1/files/<encoded-id>/content`, response bytes, 60-second timeout. Status behavior:

| Status | Behavior |
| --- | --- |
| 200 | return bytes |
| 401 | nonretryable authentication failure |
| 403 | nonretryable access denied |
| 404 | nonretryable not found |
| other <500 | retry under Files API budget |
| 5xx/network | retry under Files API budget |

Validate/encode file ID as a protocol identifier; never allow path separators to alter endpoint.

`FILEAPI-011` — Normalize destination relative path before download. Reject any normalized traversal above workspace. Place accepted content under `<workspace>/<session-id>/uploads/<clean-relative-path>`, stripping only documented redundant session/uploads prefixes.

`FILEAPI-012` — Revalidate final path containment and symlink policy before creating parents/writing. Create parent directories only after path validation. A failed individual download returns a failure result without aborting sibling batch downloads.

## Upload

`FILEAPI-020` — Read file once before retry to avoid time-of-check/time-of-use size races. Maximum content size is exactly 500 * 1024 * 1024 bytes. Reject larger files before network transmission.

`FILEAPI-021` — Upload `POST /v1/files` as multipart form data with cryptographically random boundary:

- binary part named `file`, filename is basename of provided relative path, content type octet-stream;
- text part `purpose` with value `user_data`;
- explicit content type/boundary and content length;
- 120-second per-attempt timeout.

`FILEAPI-022` — Success is 200 or 201 with nonempty response `id`. A success without ID is retryable under the three-attempt budget. Status 401, 403, and 413 are nonretryable. Cancellation is nonretryable and returns upload-canceled failure. Network/5xx failures retry.

`FILEAPI-023` — Upload file bytes are retained only for the bounded operation and never logged. File path and relative path are sanitized in telemetry according to privacy policy.

## Batch behavior

`FILEAPI-030` — Default batch concurrency is five for uploads and downloads. Queue excess; preserve result ordering despite completion order. Cancellation prevents queued items from starting and aborts active uploads where transport supports it.

`FILEAPI-031` — Batch completion reports individual results and aggregate count/time. One file's auth error does not overwrite another file's result; a caller may choose to abort the batch on systemic auth only through an explicit higher-level policy.

`FILEAPI-032` — List/delete or future file methods require the same base URL, credential, beta/version headers, pagination bounds, identifier validation, retry classification, and cancellation. Do not infer arbitrary filesystem access from remote filename metadata.

## Failure and recovery

| Failure | Behavior |
| --- | --- |
| proxy URL malformed | explicit network configuration failure, not silent direct fallback |
| no-proxy entry malformed | entry does not match; diagnostic as appropriate |
| client cert/key unreadable | omit failed material; if server requires mTLS, connection fails clearly |
| extra CA unreadable | keep base trust roots and diagnose |
| download traversal | reject before network/write or at least before write; no directories created |
| upload over 500 MiB | local failure before network |
| partial file write | use atomic temp/write where practical; remove partial and return failure |

## Acceptance scenarios

**TLS-A01 — Proxy precedence and bypass.** Both lowercase and uppercase proxy variables exist. Lowercase HTTPS proxy wins. `.example.com` bypasses `example.com` and `a.example.com` but not `notexample.com`.

**TLS-A02 — Explicit trust composition.** Extra CA is configured without system-CA. Effective explicit trust contains bundled roots plus extra file, not only the extra certificate.

**TLS-A03 — Relay traffic containment.** AgentX Unix socket and an MCP SSE server coexist. Model API uses Unix relay; MCP connects to its own URL through ordinary proxy rules.

**FILEAPI-A01 — Download containment.** Download path `../../settings.json` is rejected and no parent directory is created. `/uploads/a.txt` normalizes to the session uploads directory without escaping it.

**FILEAPI-A02 — Upload size boundary.** Upload content is one byte over 500 MiB. No POST starts. Exactly 500 MiB is permitted subject to transport/memory limits.

**FILEAPI-A03 — Batch cancellation and order.** Five uploads run, a sixth queues, and cancellation arrives. Active operations abort when possible, queued operation never starts, and result order still matches input.

## Non-normative provenance

Reference behavior was specified from proxy, mTLS, CA-certificate, HTTP/WebSocket/AWS adapters, API client fetch options, and public Files API download/upload utilities under `utils/proxy*`, certificate utilities, and `services/api/filesApi`. Paths and symbols are provenance only.
