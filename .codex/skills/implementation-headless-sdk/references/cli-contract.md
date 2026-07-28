# Command-Line and Noninteractive Surface Contract

## Contents

1. [Early dispatch](#early-dispatch)
2. [Interaction-mode inference](#interaction-mode-inference)
3. [Prompt acquisition](#prompt-acquisition)
4. [Option families](#option-families)
5. [Validation rules](#validation-rules)
6. [Native session-management path](#native-session-management-path)
7. [Initialization ordering](#initialization-ordering)
8. [Output and exit behavior](#output-and-exit-behavior)
9. [Failure behavior](#failure-behavior)
10. [Acceptance scenarios](#acceptance-scenarios)
11. [Non-normative provenance](#non-normative-provenance)

## Early dispatch

Parse mode-defining arguments before loading the general runtime so special entrypoints can avoid unrelated initialization and establish environment latches before dependent modules evaluate.

The standalone Go profile has one deliberately earlier process prerequisite:
perform the private application-home bootstrap in `GCFG-PATH-006` before
the full command-line parser. Immediately afterward, perform the `AUTH-045`
`auth.json` existence gate before full parsing or dispatching any surface,
including malformed input. These two bounded filesystem steps do not authorize
general configuration loading, credential parsing, provider setup, workspace
discovery, or session construction.

Preserve this precedence:

1. Version-only invocation; after the shared bootstrap and credential-file
   existence gate, print the resolved product build-identity banner with no
   normal initialization or credential parsing.
2. Profiler entry.
3. Build-only system-prompt dump.
4. Browser/native/computer-use MCP host modes.
5. Daemon worker.
6. Remote-control aliases.
7. Daemon.
8. Background-session operations such as list, logs, attach, kill, or background start.
9. Template operations; force process exit after the terminal UI completes because handles may remain.
10. Environment runner.
11. Self-hosted runner.
12. Worktree plus terminal-multiplexer execution.
13. Rewrite update/upgrade flags into the update command.
14. Bare-mode environment latch.
15. Start early input capture and load the normal main entrypoint.

- **CLI-001 — Fast-path isolation.** A fast path initializes only the services
  it requires and uses its own output contract. The standalone Go
  `GCFG-PATH-006` directory bootstrap and `AUTH-045` existence gate are common
  process prerequisites rather than surface services; help, version, and
  standalone MCP perform them but do not parse `auth.json`, construct a model
  provider, discover workspace extensions, or create a persistent session.
- **CLI-002 — Position-sensitive commands.** Special assistant and SSH forms are recognized only in their documented argument position so ordinary prompt text containing those words is not rerouted.
- **CLI-003 — Deep links.** Recognized application URI forms are handled before full session initialization; interactive direct-connect links may be rewritten into pending connection state, while headless links route to the appropriate noninteractive command.

## Interaction-mode inference

The process is noninteractive if print mode, initialization-only mode, or an SDK URL is selected, or stdout is not a terminal. Record interaction mode and client/entrypoint identity before telemetry, configuration error display, output sink selection, or terminal setup.

- **CLI-004 — Renderer exclusion.** Never create the terminal rendering root in noninteractive mode. Its console interception would contaminate stdout.
- **CLI-005 — SIGINT ownership.** Interactive main installs its own cancellation behavior. Print/headless mode owns SIGINT and performs query abort plus structured/text shutdown without double handling.
- **CLI-006 — Question behavior.** External client identity may change default question-preview or permission behavior, but it does not change semantic messages.

## Prompt acquisition

If stdin is a terminal, use the positional prompt. If stdin is nonterminal and the process is not the standalone MCP host:

- Streaming JSON input exposes stdin as an asynchronous record stream.
- Text input waits at most 3,000 ms for the first stdin data. A timeout warns and continues rather than hanging indefinitely.
- Once data begins, collect it to end-of-file.
- If positional prompt and stdin text both exist, join them with exactly one newline.

- **CLI-007 — Input mode coupling.** Streaming JSON input is valid only with streaming JSON output.
- **CLI-008 — Empty prompt.** A new noninteractive session requires input unless a valid resume or SDK transport can supply it.
- **CLI-009 — Encoding.** Treat stdin text as UTF-8 and preserve content after normal line-ending handling; do not interpret it as terminal key input.

## Option families

The externally meaningful option grammar includes:

| Family | Contract |
| --- | --- |
| Interaction | print, bare/simple presentation, input format, output format, verbose streaming, partial events, hook events |
| Session | continue, resume, fork, explicit session ID, resume at message, rewind files, disable persistence |
| Native management | list sessions, delete one revision, workspace, bounded page size, opaque page token |
| Model/limits | primary and fallback model, effort/thinking, max turns, max budget, structured-output schema/retry limit |
| Prompt context | replace system prompt or file, append system prompt or file, setting sources, extra directories |
| Capabilities | base/allowed/disallowed tools, permission mode, permission-prompt tool, agents, plugins, plugin directories, MCP configs |
| Files | input/downloaded files and session-token requirements |
| Remote/SDK | SDK URL, replay input, auth status, bridge/direct/SSH/remote configuration |
| Diagnostics | debug, startup/cost information, version, profiler and build-only modes |

Output formats:

- `text`: print the final result string and newline; limit/errors use concise human-readable stderr/text behavior.
- `json`: nonverbose emits only the terminal result object; verbose emits an aggregate array of retained session events.
- `stream-json`: emit each selected event as one JSON line; requires verbose mode.

## Validation rules

- **CLI-010 — Format constraints.** Stream input requires stream output. Replay requires both directions to stream. Partial assistant events require print plus stream output. Disabling persistence is print-only; in the standalone Go profile it selects a temporary nonresumable session, conflicts with resume/continue/fork, and disables project-memory loading and commands.
- **CLI-011 — SDK normalization.** Supplying an SDK URL forces print, verbose, stream input and stream output.
- **CLI-012 — Prompt source exclusivity.** A direct replacement system prompt conflicts with replacement-from-file; append text conflicts with append-from-file. File read errors identify the failing source before execution.
- **CLI-013 — Model fallback.** Fallback model must differ from the primary model.
- **CLI-014 — Session identity.** Explicit session ID conflicts with continue/resume unless forking. Validate identifier format and absence before creating a new session, except when a trusted server-side SDK transport supplies its own tagged identity.
- **CLI-015 — Resume/rewind.** Resume-at and file rewind require the corresponding resume/session context and a valid message checkpoint. Rewind-only exits after reporting its result.
- **CLI-016 — File authorization.** Downloading session files requires the corresponding session token/authorization.
- **CLI-017 — Multiplexer.** Tmux/multiplexer mode requires worktree mode, a supported platform, and an available executable.
- **CLI-018 — Teammate identity.** Team name, agent name and agent identifier are all present or all absent.
- **CLI-019 — MCP composition.** Parse each CLI MCP item as inline JSON first, otherwise as a file. Accumulate all parse errors. Later configurations override earlier entries deterministically. Reject reserved names and apply managed policy filters.
- **CLI-020 — Managed restrictions.** Enterprise/managed MCP configuration may forbid dynamic SDK servers or strict-mode combinations. Report filtered/forbidden entries rather than silently enabling them.

## Native session-management path

- **CLI-024 — Provider-free native management.** After the common frozen
  application-home bootstrap, `auth.json` presence gate, full option
  validation, and normalized absolute-workspace selection, dispatch the
  `CLIG-033` list/delete modes to one runtime-owned native-session service.
  Return before full credential parsing, model/provider or query-engine
  construction, transcript-store opening, extension/MCP/tool discovery,
  semantic-session creation, workspace-partition creation for an empty list,
  and project-memory creation. A present malformed auth document therefore
  permits management, while a missing document retains the common bootstrap
  failure. Never accept application-home, workspace-hash, session-directory,
  or transcript paths from the caller.
- **CLI-025 — Native-management projection.** Text mode emits only the bounded
  human inventory/deletion result. JSON mode emits exactly one versioned object
  on stdout and sends diagnostics only to stderr. Inventory JSON contains only
  status, minimal session identity/update/revision fields, and an optional
  opaque continuation token; deletion JSON contains only its closed status and
  session identifier. Neither projection exposes transcript or prompt text,
  title/topic/tool data, filesystem paths, workspace hash, or application-home
  information. Non-success closed outcomes remain machine-readable on stdout
  even when process status is nonzero.
- **CLI-026 — CLI/control separation.** Native list/delete selectors do not
  infer print/headless mode, start prompt acquisition, or enter the duplex SDK
  runner. The first implementation requires only this provider-free CLI path;
  it exposes no `list_sessions` or `delete_session` control. Add such controls
  only with an asynchronous input-reader-safe design that specifies
  correlation, permission-response and interrupt races, cancellation, timeout,
  result ordering, and shutdown settlement.

## Initialization ordering

For ordinary noninteractive execution:

1. Complete `GCFG-PATH-006`, cross the `AUTH-045` presence gate, and only then
   run the full CLI parser. Help/version dispatch and ordinary option validation
   follow parsing. Model-backed execution completes ordinary validation before
   the strict `AUTH-044`/`AUTH-045` read and provider construction.
2. Apply explicitly supplied setting sources early enough to affect initialization.
3. Initialize configuration, identity, network and policy without rendering dialogs.
4. On configuration error, write a clean error to stderr and shut down with status 1.
5. Establish permission context before assembling tools.
6. Complete setup before any cwd-dependent work.
7. Treat noninteractive project use as already trust-authorized by invocation; apply the full environment afterward.
8. Begin local MCP configuration reads early, but do not run unapproved work before setup/policy.
9. Build a non-rendering application-state store and manually attach settings/state subscriptions normally provided by UI components.
10. Run required setup and session-start hooks in their documented order.
11. Start the headless runner without requiring a terminal component tree to keep the process alive.

Regular headless MCP connections required for turn one are awaited. Account-hosted connector discovery is bounded to 5,000 ms; on timeout, proceed while background connection updates may affect later turns.

## Output and exit behavior

- Install a stdout protocol guard before the first structured record.
- Sandbox unavailability is fatal when policy says fail-if-unavailable; otherwise produce a conspicuous warning through the active output contract.
- The terminal result is the final semantic result even if task/session lifecycle events are emitted afterward.
- Text and JSON modes write their final aggregate/result before initiating shutdown; this ordering does not turn a backpressure-free stdout write into durable-delivery acknowledgement.
- On normal input EOF, process status follows the last retained ordinary result: error yields 1 and success yields 0. A later successful SDK turn therefore supersedes an earlier interrupted error for final status.
- An SDK `interrupt` control does not exit. Print-mode SIGINT is a distinct process-lifecycle path: abort active work, request graceful shutdown with status 0, and do not require the ordinary result to finish first.
- Initialization and protocol validation failures exit nonzero. `cancel_async_message` produces only its control response and does not by itself select an exit status.
- Cleanup runs even when the top-level headless invocation is launched without awaiting it directly; the event loop remains alive through active streams/tasks.

- **CLI-021 — Exit authority.** Do not derive process status from the most recently emitted control, task, state, or cancellation record. Normal completion consults the last retained `result`; explicit signal/shutdown paths retain their own stated status. When a post-initialization error is projected through an opaque credential sanitizer, snapshot only exact identities from a bounded, cycle-deduplicated, panic-contained standard unwrap graph; never invoke callback-owned `Is`, `As`, or repeated/stateful `Error` behavior. A host-owned surface may contribute its already sealed trusted standard-library leaf classifications without exposing the callback wrapper that produced them. Preserve only the classification identity needed for exit policy without exposing a generic raw-cause traversal. Reprojecting an already sanitized error, including beneath a cleanup join, prioritizes its private sealed classifications within the same fixed bound and never reopens or reformats the raw cause. In particular, a sanitized empty-prompt usage failure remains status 2 and cancellation remains classifiable even though consumers cannot unwrap the credential-bearing cause.
- **CLI-022 — Startup writer isolation.** Assemble version, help, and first-byte-timeout records before invoking the host writer. Contain writer panics, replace writer-owned errors with fixed local failures without inspecting or retaining their error graph, and treat a nil-error short write as an explicit failure.
- **CLI-023 — Text input callback isolation.** Invoke host `Read` and `Close`
  callbacks behind a fixed-error boundary. Preserve only exact `io.EOF`;
  replace every other error, invalid byte count, or panic without formatting,
  classifying, or retaining the host error. Start `Close` asynchronously and
  bound the join so cancellation and first-byte timeout cannot wait behind a
  broken callback. Any abandoned callback goroutine owns no session state.

## Failure behavior

- Parse/validation/configuration errors occur before any JSON stdout record when possible.
- Missing or invalid required model authentication fails before structured
  protocol initialization; missing `auth.json` also fails informational and
  standalone-MCP surfaces before their ordinary output.
- In structured mode, runtime failure emits one terminal error result when the protocol is initialized, then exits nonzero.
- Diagnostics never appear as unframed stdout in JSON modes.
- A stdin first-byte timeout warns and continues; a malformed stream record is fatal.
- Unknown/malformed MCP config entries do not partially initialize a rejected server set.

## Acceptance scenarios

1. Run version-only for both a source-default build and a linker-stamped build
   with a present but malformed `auth.json`; verify the corresponding
   build-identity banner prints and credential contents are never parsed.
   Remove `auth.json` and repeat; both runs fail with the `AUTH-A11`
   diagnostic and print no banner.
2. Pipe no data into text mode; verify a warning after 3 seconds and no indefinite block.
3. Supply positional and stdin prompts; verify newline joining and one user workload.
4. Request stream input with text output; verify fail-fast validation and no protocol records.
5. Use an SDK URL without explicit formats; verify forced print/verbose/stream settings.
6. Provide conflicting system-prompt text/file options; verify an explicit error before session creation.
7. Give three MCP entries with two errors; verify both errors are reported and no partial invalid configuration runs.
8. Time out account-hosted MCP at 5 seconds; verify turn one proceeds and a later turn can see a late connection.
9. Produce a runtime error after stream initialization; verify one structured error result and nonzero status.
10. Interrupt one SDK turn, then complete another successfully and close input; verify the process remains alive after interrupt and normal EOF exits 0 from the later result.
11. Send SIGINT during print-mode execution; verify graceful shutdown requests status 0 even if no ordinary result completes.
12. Produce an empty-prompt usage failure after session construction with a configured credential reflected in its diagnostic, then repeat with a classified cancellation or writer failure. Verify the returned projection contains no credential, offers no generic raw-cause traversal, retains usage status 2 or the original cancellation classification, and still runs session cleanup.
13. Run version-only, help-only, and first-byte-timeout paths with writers that return hostile errors, short-write, or panic. Verify no callback error method is invoked, every panic is contained, and each path returns only its fixed local writer classification.
14. Run text input with an uncomparable error whose `Error`, `Is`, and `Unwrap`
methods panic, an invalid read count, a panicking `Read`, and a blocking or
panicking `Close`. Verify fixed failure, zero host error-method calls, prompt
non-admission, and bounded cancellation/timeout.

## Non-normative provenance

Evidence was specified from the reference bootstrap entrypoint, top-level command parser, initialization entrypoint, main mode-selection logic, headless runner setup and MCP startup integration. Option spellings and paths are provenance; the independent rules above are normative.
