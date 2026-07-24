# AgentX conformance profile

This matrix records the observable standalone profile implemented by the Go runtime and its VS Code workspace adapter. “Operational” means a runtime route is constructed and tested. “Partial” means a useful runtime route exists but named specified behaviors are deliberately absent. “Contract-only” means state and protocol rules exist for composition or diagnostics but no backend is fabricated. “Unavailable” is a supported, explicit build result—not an implicit success stub.

## Evidence boundary

The Go implementation is a substantial local runtime, not a claim that every specified AgentX behavior has reached parity. The repository-local Ruby audits validate the skill-routing graph and contract structure. They do **not** trace each normative skill contract to Go or VS Code source or execute it. Go implementation evidence comes from package tests; editor-adapter evidence comes from the TypeScript/unit, real-binary handshake, Extension Host, production-bundle, and VSIX checks named below. Partial, contract-only, and unavailable rows are known scope boundaries, not fabricated coverage. A future per-contract traceability audit would need to map each stable contract identifier to implementation, executable test, or an explicit unsupported decision.

The editor tests do not contact the production model endpoint. Unit and
Extension Host tests do not submit a model turn. The offline integration smoke
builds the actual AgentX binary but sends only the initialization control under
a synthetic version-1 `auth.json` and an isolated temporary application home.

## Canonical lifecycle evidence

| Lifecycle boundary | Go owner | Executable evidence |
| --- | --- | --- |
| Pre-CLI application-home bootstrap | `pkg/app`, `pkg/config`, `pkg/platform` | `AGENTX_HOME`/deprecated `AGENTX_STATE_DIR`/default precedence, blank/relative/root/symlink rejection, frozen home and `sessions/` creation, POSIX privacy, pinned-parent races, missing-auth surface tests, and latched post-rename tool denial |
| Early mode and option selection | `main.go`, `pkg/cli`, `pkg/app` | CLI cross-option, stdin, exit, and surface tests |
| Credential/configuration normalization | `pkg/config` | strict version/provider/schema, duplicate/unknown/trailing/oversize rejection, legacy-fallback denial, Unix mode-bit enforcement, Windows identity/link checks, redaction, endpoint, model/effort tests |
| Extension/capability generation | `pkg/extensions`, `pkg/mcp`, `pkg/tool` | precedence, cycle, trust, discovery, schema, and registry tests |
| Session creation/recovery | `pkg/sessionlock`, `pkg/transcript`, `pkg/app` | application-home/workspace-hash placement, exclusive lock, truncation/corruption, unresolved-call, workspace-scoped continue/resume/fork tests |
| Context assembly | `pkg/prompt`, `pkg/memory`, `pkg/compact` | ordered sections, non-truncating instruction snapshots with explicit hard ceilings, recall, threshold, projection tests |
| Model stream | `pkg/model`, `pkg/engine` | exact Azure request, strict SSE lifecycle/correlation, cross-boundary credential-reflection rejection/redaction, retry, timeout, cancellation tests |
| Capability boundary | `pkg/tool`, `pkg/permission`, `pkg/sandbox` | validation, deny-first policy, approvals, path/shell analysis, hooks, scheduling, exact settlement tests |
| Durable work | `pkg/task` | lifecycle, output identity, state rollback, restart reconciliation, process-tree cancellation tests |
| Presentation/protocol | `pkg/protocol`, `pkg/surface`, `pkg/app` | canonical-event validation and closed SDK NDJSON/control/race tests |
| VS Code workspace adapter | [standalone extension repository](https://github.com/greenpau/agentx-vscode-extension) | protocol/client/presentation/security unit tests; real-binary offline init handshake; trusted and Restricted Mode VS Code 1.95.3 Extension Host activation, command-registration, and trust-state tests; separate extension-host/webview production bundles |
| Shutdown | `main.go`, `pkg/signals`, `pkg/platform`, runtime `Close` methods | atomic print-handler routing plus globally first-signal latching, conventional exit codes, exact-once second-signal/failsafe escalation, enforced monitor stop order, joined completion, idempotence, bounded phases, flush, and cleanup tests |

## Supported build profile

| Domain | Status | Boundary |
| --- | --- | --- |
| Application-home bootstrap | Operational | First nonblank `AGENTX_HOME`, deprecated `AGENTX_STATE_DIR`, then `~/.agentx`; the selected override must be absolute and non-root. One physical home is frozen before full CLI parsing, its direct `sessions/` child is created through a pinned parent root, and every invocation requires `auth.json` to exist, including malformed input. Credential existence checks and reads stay descriptor-relative to that root and reject a changed textual identity. The complete selected home is a mandatory protected capability subtree even inside an approved workspace; the interactive, headless, and standalone-MCP tool executors verify the home and `sessions` identities before and after permission evaluation, invalidate pending approval, and latch a process-lifetime tool denial after a detected sustained displacement. Restoring the inode does not clear the latch, while the guard does not claim atomicity against a rename after authorization returns to execution. Effective-user ownership and owner-only mode are enforced on supported POSIX platforms; Windows DACL privacy is not implemented. The standalone selector performs platform lexical cleaning but not Unicode NFC normalization |
| Azure OpenAI Responses API using the `auth.json` model `gpt-5.6-sol` | Operational on supported POSIX platforms | Streaming, encrypted-reasoning replay, function calls, provider-metadata validation, cross-chunk/part/error-field credential redaction, bounded retry/watchdog, usage, and context limits |
| Authentication and provider selection | Partial | One strict version-1, provider-discriminated application-home `auth.json` Azure subscription-key source is supported, with no dotenv, `--env-file`, or process credential fallback. The literal child is opened through the frozen home descriptor, not by re-resolving a mutable pathname. POSIX files require effective-user ownership and private mode bits. Windows model-backed authentication is unavailable: regular-file, non-symlink, single-link, stable-identity, and bounded-read code exists, but every load fails before reading because native owner/DACL verification is absent. OAuth, Bedrock, Vertex, first-party account login, and provider/model fallback are unavailable |
| Settings, policy, and migration | Partial | Pre-CLI application-home selection plus the fixed `auth.json` contract and early CLI are operational; multi-scope settings files, managed/remote policy, migrations, synchronization, and live reload are unavailable |
| Context and instruction discovery | Partial | Trusted ancestor `AGENTS.md` snapshots, prompt precedence, skills, memory, date/environment, tools, and explicit prompt files are operational; managed/user/local `AGENTX.md`, rules/includes/conditions, lazy attachment, cache invalidation, instruction audit hooks, Git snapshot, and >40k status warnings are unavailable |
| Interactive line REPL | Partial | Commands, streaming answer text, approvals, questions, cancellation; deliberately not a retained-mode TUI and has no 30-second orphan-terminal detector |
| Headless text and aggregate JSON | Operational | Shared engine and deterministic exit behavior. Recognized slash input is routed before model context: only descriptor-opted-in `/compact` and `/cost` (`/usage`) run here, while other known or valid-unknown commands fail locally with zero model turns |
| Bidirectional stream JSON SDK | Partial | Closed public union, FIFO correlated controls, dedupe/replay, permission races, and a bounded `now`/`next`/`later` queue are operational. A `now` record cancels and becomes the next serialized turn; it is not injected into the active recursive turn. Initialize-time hook/MCP/prompt/agent/schema injection and live environment/model/mode mutation are unavailable |
| VS Code workspace extension | Partial | Workspace-host Activity Bar chat, incremental streaming text, tool/result correlation, permission and question controls, stop/follow-up behavior, workspace-scoped new/continue/resume/fork, editor references, diagnostics, completion notifications, Restricted Mode gating, owned process-tree cleanup, and allowlisted target VSIX construction are operational. The binary owns semantic and durable state; the extension cache is bounded, redacted, lossy, and non-authoritative. Text-only input, sessions observed by this extension rather than an authoritative inventory/replay, restart-bound settings, no IDE MCP/LSP bridge, and no native runtime qualification for every cross-built target are explicit limits |
| Core file/search/shell/question/task tools | Operational | Rooted text-file profile; all side effects cross composed authorization. Shell analysis recognizes a conservative closed Bash subset and asks on unsupported or ambiguous syntax; foreground and background Bash preserve the selected sandbox command factory; no PowerShell command tool is registered |
| Test-only permission capability | Operational | `pkg/testing` owns `TestingPermission`; exact `NODE_ENV=test` enablement, strict empty input, mandatory approval, success mapping, concurrency classification, and zero production registry footprint are tested |
| Durable sessions | Partial | Persistent state lives below `<application-home>/sessions/<workspace-hash>/<session-id>/`; new/resume/continue/fork, append-only transcript, exact session lock, newest-leaf projection, workspace scoping, response-group restoration, fully-unresolved response-group omission, mixed-group in-memory settlement, and completion-gated fork publication are operational. Legacy graph variants, live rewind, tombstone/snip replay, and general sidechain editing remain unavailable; source and destination are independent stores rather than one cross-store transaction |
| Local background processes and work/todo state | Operational | Durable records and output, bounded polling/cancellation; no unsafe process-handle reacquisition |
| Skills and output styles | Operational | Trusted root `.codex/skills` discovery only, literal arguments, enforced deny-only tool scopes |
| Plugin component loading and hooks | Partial | Trusted manifest discovery plus contributed styles/hooks/MCP and the explicitly reachable hook subset; plugin skills are ignored, and there is no marketplace installer, dependency installer, or unsupported hook-event backend |
| MCP stdio client | Partial | Explicit stdio config, lifecycle, bounded discovery/tool calls, generation-fenced reconnects, ordinary composed permission checks, and provider-scoped result redaction are operational. Image/audio blocks become placeholders, resources/prompts have no integrated model-facing adapter, and trusted project definitions lack a separately persisted fingerprint-bound approval |
| Standalone MCP stdio tool host | Operational | Performs the common application-home and `auth.json` existence gates, then reuses core schema, application-home identity guard, permission, execution, task, and result contracts without parsing credentials or constructing a model client |
| Persistent project memory | Conditional | Attributed bounded local memory is stored outside the session directory at `<application-home>/projects/<workspace-hash>/memory`, with configured-credential redaction and heuristic secret rejection; authoritative transcript remains separate. The standalone hash uses the selected absolute workspace, so linked worktrees do not share memory and the broader canonical-repository/override contract is unavailable. Bare and nonpersistent sessions do not create or load memory. Unix owner-only mode bits are enforced; Windows model-backed startup is already unavailable before memory construction |
| Context compaction | Partial | Automatic/manual durable deterministic excerpt projections retain authoritative transcript evidence and safe response/tool boundaries. Complete specified semantic summaries, compact-history rewriting, team synchronization, and consolidation are unavailable |
| Team memory, auto-dream consolidation, and memory synchronization | Unavailable | Local attributed project memory does not imply cross-agent or remote memory services |
| VS Code completion notifications | Partial | The workspace extension can use VS Code notifications according to its local focus setting after a projected terminal result; this is presentation state, not a durable task or general operating-system notifier service |
| Sleep prevention, updater, and general desktop/browser integration | Unavailable | The VS Code adapter does not fabricate these operating-system services or an AgentX binary update channel |
| Signal and shutdown lifecycle | Partial | Early process-level signal acquisition with conflict-checked raw-print versus interactive semantic SIGINT ownership, SIGTERM/SIGHUP codes, one global first request across all monitors, exact-once second-signal/failsafe escalation, joined completion, concurrent critical cleanup, and SessionEnd ordering are operational; orphan-TTY polling and retained-terminal restoration/hints are unavailable |
| External telemetry and authoritative cost accounting | Unavailable | Local privacy-filtered diagnostics and token usage are operational; no remote sink or deployment price table is configured |
| OS sandbox | Conditional | Bounded macOS backend probe; explicit unavailable/unsupported status elsewhere; semantic authorization always remains active |
| Distributed identity, delivery, replay gate, and transport registry | Contract-only | No remote transport factory is registered in the local profile |
| Remote bridge/cloud/SSH/direct-connect/teleport | Unavailable | No network placement is implied by contract-state presence |
| Delegated agents and teams | Unavailable | No worker backend, mailbox, or worktree-placement implementation is advertised |
| Retained-mode terminal renderer, Vim/keybinding stack | Unavailable | Line REPL remains the selected portable surface |
| HTTP/SSE/WebSocket MCP, OAuth, server-initiated elicitation/channels, and LSP | Unavailable | Stdio request/response MCP is the only configured external-provider transport; unsupported server requests receive an explicit protocol error |
| Voice, browser/computer use, notebook, assistant viewer | Unavailable | Feature profile reports build exclusion; the bounded VS Code workspace adapter does not imply these optional experiences |

## Verification gates

The handoff gate is:

```sh
git diff --check
test -z "$(gofmt -l *.go $(find pkg -name '*.go' -type f -print))"
go vet ./...
go test ./...
go test -race ./...
go test ./... -shuffle=on -count=3
go test -count=1 -v ./pkg/signals ./pkg/testing
GOOS=linux GOARCH=amd64 go build ./...
GOOS=windows GOARCH=amd64 go build ./...
ruby .codex/skills/implementation-conformance-audit/scripts/build_source_coverage.rb --check
ruby .codex/skills/implementation-conformance-audit/scripts/build_contract_scenario_coverage.rb --check
ruby .codex/skills/implementation-architecture/scripts/generate_drawio.rb --check
ruby .codex/skills/implementation-architecture/scripts/enhance_custom_drawio.rb --check
ruby .codex/skills/implementation-conformance-audit/scripts/audit_architecture.rb
```

Tests use isolated application homes, synthetic version-1 `auth.json`
documents, and loopback-only mock servers. No conformance command sends a
request to the production Azure deployment.
Linux and Windows commands are compilation evidence from the current host, not native runtime-conformance claims.
The standalone [VS Code extension repository](https://github.com/greenpau/agentx-vscode-extension) owns its Node, Extension Host, offline protocol, and VSIX verification gates. Its Extension Host command downloads the pinned VS Code 1.95.3 test runtime; headless Linux additionally needs Xvfb and Electron runtime libraries. Those tests prove activation, command registration, manifest restriction, and trust-state behavior; they do not submit a user prompt or prove native remote-extension-host behavior. Extension packaging proves cross-build and VSIX construction for the six declared targets, while native release qualification still requires installation and execution on every target platform.
