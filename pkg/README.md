# AgentX Go packages

Every directory below `pkg/` is importable by other Go modules on the supported Unix and Windows build targets. This makes the implemented runtime available for composition and testing without the Go `internal` import restriction. Unsupported operating systems may expose portable leaf packages, but the complete application does not claim to build there.

Importability is not a promise that every exported identifier is a stable, untrusted-client SDK. These packages currently expose the runtime's trusted-host composition surface; compatibility follows the [runtime conformance profile](../.codex/skills/coding-directives/references/runtime-conformance.md), and APIs may still evolve with those contracts.

Callers must preserve the same authority boundaries as the root application:

- Validate untrusted wire and stored data with the owning package before use.
- Route model-requested effects through tool validation, permission, sandbox, hook, and result-normalization contracts; public visibility is never authorization.
- Do not derive trust, policy bypass, protected paths, provider credentials, or executable extension configuration directly from untrusted input.
- Treat configuration, MCP environments, transcripts, task output, and diagnostic data as potentially sensitive. Do not reflect, log, or serialize credentials; `config.Azure.APIKey` is deliberately excluded from JSON.
- Use `config.LoadProviderRegistry` or `config.LoadProviderRegistryAtRoot` for
  trusted-host preselection of provider profiles. Their descriptors omit Azure
  routing and credentials, but callers must still apply the returned complete
  `CredentialSanitizer` to the fully framed output. Use `config.Load` only when
  selecting and constructing one model-backed runtime profile.
- Do not confuse `ProviderRegistry.MarshalJSON` with the external discovery
  wire protocol. The package-native projection has no top-level protocol
  version and emits `ProviderDescriptor` fields such as `type` and
  `reasoning.default_effort`; it is useful only inside a trusted Go composition
  that preserves the sanitizer boundary. External tools and editor hosts use
  `agentx --list-providers --output-format json`, whose standalone public
  schema is version `1` and whose closed descriptors use compatibility fields
  such as `providerType`, `supportedReasoningEfforts`, and
  `reasoning.defaultEffort`. Neither projection contains literal endpoint URLs.
- Hold the documented session lock while mutating durable session state, and preserve package-specific shutdown and cleanup ordering.
- Treat heuristic scanners as defense in depth, not as proof that arbitrary content is safe.

Signal acquisition and first-request shutdown ownership live in `pkg/signals`. A process must share one `signals.ProcessShutdown` across its monitors and declare exactly one semantic SIGINT owner; the package rejects duplicate or conflicting ownership. Start the process monitor before a composed print monitor and stop it after the print monitor so OS delivery remains continuously owned; an early process stop returns `signals.ErrMonitorOrder` without disarming delivery and may be retried after print stops. Cancellation and force-exit callbacks must be nonblocking, non-reentrant lifecycle leaves and must never invoke a monitor stop function. Generic staged cleanup, process, and filesystem lifecycle primitives remain in `pkg/platform`.

The isolated `pkg/testing` package owns the test-profile-only `TestingPermission` capability. It is registered only when an immutable environment snapshot explicitly contains `NODE_ENV=test`; conflicting values fail closed. The capability accepts exactly `{}`, is read-only and concurrency-safe, always requires the ordinary permission round trip with `Run test?`, and has no production registry footprint.
