---
name: coding-directives
description: Go coding standards and implementation directives for the terminal-first AgentX client. Use when creating, modifying, or reviewing Go code, runtime architecture, conformance status, package boundaries, configuration, constructors, errors, concurrency, security, persistence, protocol types, adapters, or tests.
---

# Coding Directives

## Establish the contract first

Implement the behavioral contracts reachable from `AGENTS.md`. Read the narrowest applicable `implementation-*` skill before coding. The Go source, routed contracts, and tests are the project authority.

Preserve observable inputs, outputs, state transitions, ordering, concurrency, permission decisions, persistence, recovery, and user-visible failures.

Read [runtime-architecture.md](references/runtime-architecture.md) when changing package ownership, dependency direction, lifecycle composition, trust boundaries, or supported surfaces. Read [runtime-conformance.md](references/runtime-conformance.md) when changing availability, compatibility status, executable evidence, or an operational/partial/contract-only/unavailable boundary. Update these references alongside the source and tests they describe.

## Package boundaries

Organize packages by behavioral owner:

- Keep bootstrap, settings, session state, query/model orchestration, capability execution, transcript continuity, task management, extension discovery, distributed execution, presentation adapters, and operational services as explicit boundaries.
- Keep the semantic session engine independent of terminal, headless, SDK, bridge, remote, and MCP presentation or transport adapters.
- Keep commands, model-callable tools, and durable tasks as distinct contracts even when one workflow uses all three.
- Put policy with the enforcing domain. UI code may render an approval request but must not decide permission truth; transport code may relocate execution but must not redefine tool or message semantics.
- Keep interfaces small and consumer-owned at boundaries. Prefer direct concrete dependencies inside a package.
- Do not add cross-package shortcuts when a contract, registry, adapter, or dispatcher already owns the interaction.

Avoid an undifferentiated `utils` package. Place stateless helpers with the domain whose vocabulary and invariants they serve.

## Design and structure

Use cohesive structs with methods for identity-bearing or stateful concepts such as sessions, turns, registries, tasks, transcripts, permission evaluators, model streams, transports, and renderers. Use functions for stateless parsing, normalization, formatting, and validation.

Favor composition and explicit dependency injection. Avoid mutable package globals except immutable constants and deliberately process-wide registries initialized before concurrent use. Make ownership, lifetime, and cleanup visible in constructors and APIs.

Model state machines explicitly. Define initial, active, terminal, invalid, cancellation, timeout, and recovery transitions. Represent identifiers with dedicated types where confusing two identifier classes could corrupt correlation or ownership.

Export only what another package must use. Keep methods focused on one logical operation. Split types when they own unrelated lifetimes, policies, or failure scopes.

## Configuration and construction

Treat configuration loading, migration, validation, normalization, and runtime construction as separate phases when their failure or provenance differs.

- Preserve scope, precedence, source attribution, managed-policy authority, and the difference between compile inclusion, feature gating, eligibility, platform support, configuration, and health.
- Parse external data into wire/config types, validate it, derive normalized runtime values, and construct live services only after required invariants hold.
- Prefer constructors returning `(*Type, error)` or `(Interface, error)` when dependencies, configuration, or resources can fail.
- Reject missing required dependencies before starting background work or acquiring resources.
- Make partially acquired resources safe to close, and make cleanup idempotent.
- Do not use a cached `validated` flag when validation depends on mutable inputs; prefer immutable normalized configuration.

Use named constants for protocol markers, stable statuses, defaults, limits, and configuration keys. Do not hide behaviorally significant values in literals.

## Context, concurrency, and cancellation

Accept `context.Context` as the first parameter for blocking, remote, permission-sensitive, streamed, or task-owned operations. Do not store request contexts in long-lived structs.

Use structured concurrency: the owner of goroutines owns their cancellation, result collection, and cleanup. Every started goroutine must have a bounded termination path. Avoid fire-and-forget work unless the task contract assigns it a durable identity, retrievable output, explicit cancellation, and completion notification.

Preserve accepted request order as the authoritative identity, pairing, and barrier sequence even when concurrency-safe work overlaps. Within one declared safe group, terminal results may be published in observable completion order; never lose their accepted tool-use identity or source parent. Serialize unsafe mutations and declared barriers. Ensure every accepted tool-use identifier receives exactly one terminal result, including denial, malformed input, cancellation, timeout, sibling failure, and specified interruption.

Use channels for ownership transfer or event streams, mutexes for shared in-memory invariants, and atomics only for simple independent values. Document buffer sizes, backpressure, channel-closing ownership, and whether cancellation may discard queued data.

## Errors and recovery

Return errors; do not panic in runtime code. Panics are acceptable only for impossible programmer invariants during initialization or test-helper setup, and should not be used for untrusted input or operational failure.

Wrap boundary failures with `%w` so callers can inspect causes. Add concise operation and identity context without including secrets. Use typed or sentinel errors only when callers make a behavioral decision from the category; do not create a global catalog for incidental strings.

Treat denial, cancellation, timeout, retry exhaustion, unavailable capability, malformed protocol input, and partial recovery as normal modeled outcomes. Bound retries and include backoff/cancellation behavior. Recovery must never invent success or automatically replay an uncertain side effect.

## Security and external input

Treat model output, MCP data, hooks, plugins, files, environment variables, network responses, and restored transcripts as untrusted input.

- Validate syntax and semantic preconditions before authorization and execution.
- Compose permission decisions from mode, scoped rules, managed policy, tool checks, hooks, path/command analysis, sandbox availability, and user choice.
- Fail closed for unknown capabilities, invalid schemas, unsafe paths, unavailable required isolation, or ambiguous specified mutations.
- Never log or persist access tokens, API keys, passwords, cookies, private keys, OAuth secrets, raw authorization headers, or full credential/config structs.
- Use restrictive file modes and atomic or append-safe writes for sensitive or durable state.
- Keep telemetry optional, privacy-filtered, and outside correctness paths.

## Protocol and persistence types

Separate domain types from wire, storage, and presentation projections. Use explicit version or discriminator fields for durable and externally exchanged records. Define unknown-field behavior, framing, ordering, correlation, size limits, and forward/backward compatibility at each boundary.

Treat the transcript as append-safe event history, not a screen buffer. Do not persist ephemeral progress or silently expose UI state to the model. Preserve enough evidence to reconcile interrupted calls without replaying uncertain mutations.

For JSON, YAML, or other serialized Go structs, use stable snake_case field names and `omitempty` only when omission and an explicit zero value have identical protocol meaning.

## Tests

Add focused table-driven unit tests beside the owning package and boundary-level tests for cross-package contracts. Prefer standard-library test facilities unless an existing project dependency materially improves clarity.

Cover:

- success plus malformed, unknown, denied, cancelled, timed-out, and unavailable cases;
- ordering, correlation, serialization barriers, concurrent completion, and backpressure;
- partial streams, interrupted writes, corrupt tails, retries, reconnection, and idempotent cleanup;
- configuration precedence, migration, policy overrides, and disabled feature combinations;
- secret redaction and the absence of ephemeral UI data from durable/model-visible history.

Use deterministic clocks, random sources, identifiers, transports, and filesystems behind narrow interfaces when tests need control. Prefer `httptest`, temporary directories, in-memory fakes, and fault injection over live services. Run race-enabled tests for packages that own concurrency.

## Go style and completion

Run `gofmt` on edited Go files and `go test ./...`; run `go test -race ./...` when concurrency behavior changes. CI must compile both production and `_test.go` files for every supported Windows architecture, and cross-build both Darwin architectures so build-tagged platform contracts cannot silently rot. Cross-compilation is compile evidence, not native runtime evidence. Keep imports in standard-library, third-party, and local-module groups. Use side-effect imports only for deliberate registration and explain non-obvious cases.

Write useful exported comments and document non-obvious security, ordering, durability, and cancellation decisions. Avoid comments that merely restate identifiers.

Before declaring work complete:

1. Trace the implementation to the owning contract IDs and acceptance scenarios.
2. Confirm failures remain scoped to their owning domain.
3. Confirm cancellation and cleanup reach all owned resources.
4. Confirm disabled or unavailable behavior is explicit.
5. Update the owning implementation skill whenever implementation work reveals a missing contract.
6. Use `implementation-conformance-audit` for changes that affect implementation coverage, routing, or architectural contracts.
