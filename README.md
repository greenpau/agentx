# AgentX

AgentX is a standalone Go implementation of the terminal-first agent runtime described by this repository. It is an orchestration client—not a language model. The root [`main.go`](main.go) selects a user surface and delegates to a shared session engine that composes context, streams the deployment-backed model, authorizes tools, persists recovery evidence, and continues until the turn is terminal.

For installation and day-to-day terminal and VS Code workflows, see the [AgentX User Guide](USER_GUIDE.md).

This repository currently delivers the operational local-agent core, not every optional product surface. Retained terminal rendering, delegated teams, remote placement, provider OAuth, marketplace installation, and other optional planes remain explicitly unavailable. The repo-local [runtime conformance profile](.codex/skills/coding-directives/references/runtime-conformance.md) separates operational, partial, contract-only, and unavailable behavior so source presence is never mistaken for a working route.

AgentX owns one application home. `AGENTX_HOME` is the sole supported absolute
path override; otherwise the home is `~/.agentx`.
Every invocation creates the application home and its `sessions/` child with
private permissions on supported Unix platforms before parsing command-line
arguments. The selected physical home is frozen for bootstrap. Before full CLI
parsing, every surface—including malformed input, `--help`, `--version`, and
the standalone MCP tool host—requires `<application-home>/auth.json` to exist
as a direct regular file. A missing or unusable child is a startup error that points to the
[authentication instructions in the GitHub user guide](https://github.com/greenpau/agentx/blob/main/USER_GUIDE.md#configure-authentication)
and prints this placeholder shape:

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

Model-backed starts strictly parse this versioned, provider-discriminated
document as their sole model credential source. On Unix, make `auth.json`
owner-readable and owner-writable only. Across supported runtime paths, the
Azure model credential is redacted from output, ordinary child environments,
model context, and transcripts. Explicit MCP-provider environment values are
scoped to that provider's subprocess, and configured provider credential values
are scrubbed from its model-facing results. The current Windows build fails
closed before reading `auth.json` because native owner/DACL verification is not
implemented.

Provider responses are untrusted even when transport authentication succeeds. Structural IDs, names, phases, discriminators, request IDs, and opaque reasoning state are rejected before persistence or execution if they reflect the credential or contain unsafe controls; text and structured error fields are redacted across streaming chunks and adjacent field boundaries. The provider-neutral engine repeats these checks so a custom model adapter cannot bypass the Azure boundary.

## Install and run

Go 1.26 or newer is required.

```sh
go install github.com/greenpau/agentx@latest
agentx --version
```

From a checked-out copy of this repository, install the current source instead:

```sh
go install .
```

Ensure the Go installation directory is on `PATH`. By default it is `$(go env GOPATH)/bin` on Unix-like systems and `%USERPROFILE%\go\bin` on Windows, unless `GOBIN` is configured.

```sh
agentx
agentx --print "inspect this repository"
agentx --print --output-format json "summarize the architecture"
agentx --print --input-format stream-json --output-format stream-json
agentx --mcp-server
```

Useful controls include `--effort high`, `--permission-mode plan`, `--allowed-tools`, `--disallowed-tools`, `--resume`, `--continue`, `--fork-session`, and `--no-session-persistence`. Project `AGENTS.md`, repository-local `.codex/skills`, and workspace `.agentx/` plugins, hooks, output styles, and MCP configuration are ignored until `--trust-workspace` is explicit. The private user application home is not that workspace extension directory and never becomes project-controlled merely because a workspace is trusted. Skills are never loaded from user configuration, plugins, remote providers, or nested repositories. Run `agentx --help` for the current contract.

The default reasoning effort is `high`; accepted values for `gpt-5.6-sol` are `none`, `low`, `medium`, `high`, `xhigh`, and `max`. A `--model` override is rejected unless it matches the deployment-backed model in `auth.json`, preventing a logical model label from silently routing to a different Azure deployment.

The bidirectional NDJSON adapter remains live for correlated controls and can queue `now`, `next`, and `later` user records while a turn is active. It does not splice a newly arrived prompt into the active recursive model/tool turn: `now` first cancels that turn, then the accepted record runs as the next serialized turn. MCP integration is stdio-only; image and audio result blocks are validated but become inert text/metadata placeholders rather than model attachments, and trusted project MCP configuration does not yet have a separate approval persisted against its exact configuration fingerprint. Resume, fork, and compaction are useful but similarly bounded; see the conformance profile for their explicit compatibility gaps.

## Visual Studio Code extension

[greenpau/agentx-vscode-extension](https://github.com/greenpau/agentx-vscode-extension) maintains the companion Visual Studio Code adapter as a separate repository. Its extension identifier is `greenpau.agentx`.

The binary remains authoritative for the model loop, capabilities, permission policy, session lock, transcript, recovery, and tool results. The extension keeps only a bounded, redacted, lossy presentation cache and never uses it as model context. Adding editor context contributes a workspace-relative path and optional range rather than silently copying file contents, so AgentX must still use its ordinary `Read` capability and path policy. Restricted Mode keeps the chat explanation visible but blocks AgentX process launch and workspace-defined launch settings until VS Code trusts the workspace. The webview uses a closed content-security policy, has no network or workspace-resource access, renders untrusted values as text, and delegates validated `https` links to the extension host. Extension-owned launches request a kill-on-close Windows Job Object and use an owned Unix process group so bounded shutdown covers descendant processes.

See the extension repository's [README](https://github.com/greenpau/agentx-vscode-extension#readme) and [host protocol](https://github.com/greenpau/agentx-vscode-extension/blob/main/docs/PROTOCOL.md) for development commands, packaging, its exact security boundary, and current limitations.

## Safety model

Every model-requested side effect crosses one capability boundary:

```text
resolve → validate → hooks → permission/path/shell policy → execute → normalize → persist
```

Denied, malformed, cancelled, timed-out, unavailable, and interrupted calls receive terminal tool results just like successful calls. Read-only concurrency is conservative; mutations and skill-scope changes are scheduling barriers. Bash always requires explicit authorization even when static analysis recognizes a read-only command. Protected credential and configuration files, protected descendants reached through recursive search, out-of-scope paths, symlink/hardlink substitutions, shell redirections, and dangerous removals cannot inherit automatic read or edit permission.
The complete selected application-home subtree is protected even when an
`AGENTX_HOME` override places it inside the active workspace or gives it a
basename other than `.agentx`. AgentX rechecks the frozen home and `sessions/`
directory identities before and after permission evaluation, before a tool can
execute. Once a rename, replacement, or supported-POSIX privacy change is
detected, pending and future tool use is denied until AgentX is restarted.

When session persistence is enabled, history is append-only JSONL below `<application-home>/sessions/<workspace-hash>/<session-id>/` with restrictive permissions. Standalone project memory is separate from session transcripts and keyed by the selected absolute workspace; linked worktrees therefore do not currently share it. User input and accepted tool calls are durable before the corresponding network request or side effect. Recovery never replays an uncertain call: a fully unresolved response-identified assistant/tool group leaves the live projection, while a missing member of a retained mixed group receives only an in-memory interrupted result. Forks copy durable raw evidence, never promote those derived results, and remain hidden behind a completion marker until the destination batch is durable. Forking is not a single transaction across the independent source and destination stores, but a crash-partial destination cannot be selected by `--continue` or explicit resume.

## Architecture and verification

See the repo-local [runtime architecture](.codex/skills/coding-directives/references/runtime-architecture.md) for package ownership and lifecycle details, and the [runtime conformance profile](.codex/skills/coding-directives/references/runtime-conformance.md) for the implemented/disabled profile and executable evidence map.

All implementation packages are importable below [`pkg/`](pkg/README.md). That is a trusted-host composition surface, not an authorization bypass or a frozen public SDK; package consumers must preserve the documented validation, permission, secret-handling, persistence, and shutdown contracts.
Signal ownership is isolated in `pkg/signals`; test-profile-only capabilities are isolated in `pkg/testing` and are omitted unless `NODE_ENV=test` is explicit.

```sh
go test ./...
go test -race ./...
go test ./... -shuffle=on -count=3
go vet ./...
```

Network tests bind loopback-only `httptest` servers; they never call the production Azure endpoint.

The repository-local Ruby architecture audits validate the implementation-skill hierarchy. They do not, by themselves, prove that every contract has an executable Go implementation; the conformance profile and Go tests are the implementation evidence.
