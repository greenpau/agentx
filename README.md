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
  "version": 2,
  "providers": [
    {
      "id": "sol-5.6",
      "type": "azure_openai",
      "default": true,
      "capabilities": {
        "reasoning": {
          "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "high"
        }
      },
      "azure_openai": {
        "endpoint": "https://your-resource.openai.azure.com",
        "model": "gpt-5.6-sol",
        "deployment": "gpt-5.6-sol",
        "api_key": "replace-with-your-secret",
        "api_version": "preview"
      }
    }
  ]
}
```

Model-backed starts strictly parse this version-2 provider-profile document as
their sole model credential source. The old version-1 object is not supported
and is not migrated automatically. A sole provider is selected implicitly. If
there are several providers, set `"default": true` on exactly one profile or
select an exact profile ID at startup with `--provider ID`; an invocation with
several profiles and no selector or default fails with instructions for adding
`"default": true` to one provider entry. More than one configured default is
invalid.

Discover configured provider profiles before starting a session with:

```sh
agentx --list-providers --output-format json
```

This command strictly validates the complete registry but does not select a
provider, require a default, inspect a workspace, create a session, or contact
Azure. It therefore succeeds for a valid multi-provider registry with no
default and lets a host choose a returned provider ID. A profile may route to
a distinct model endpoint, but the returned profile is metadata—not the
literal endpoint URL. The result contains provider IDs, types, logical models,
effective-default state, and operator-declared reasoning capabilities. It
deliberately excludes endpoint URLs, deployments, API-version selectors, keys,
and route bindings.

The useful standalone discovery grammar is
`agentx --list-providers [--output-format text|json]`; the required selector
and optional output option may occur in either order and each may appear only
once. A final bare `--` terminator is accepted, but no prompt or other option
(including `--help` or `--version`) is valid. The JSON response uses public
discovery schema version `1`; that is independent of the required version `2`
auth-file schema. Automation should buffer stdout, use it only after exit
status `0`, require top-level integer `version: 1`, and discard buffered stdout
on every nonzero exit. See the User Guide for the exact camelCase descriptor
shape.

For example, two Azure model endpoints can be configured independently:

```json
{
  "version": 2,
  "providers": [
    {
      "id": "sol-5-6",
      "type": "azure_openai",
      "default": true,
      "capabilities": {
        "reasoning": {
          "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "high"
        }
      },
      "azure_openai": {
        "endpoint": "https://sol-resource.openai.azure.com",
        "model": "gpt-5.6-sol",
        "deployment": "sol-5-6",
        "api_key": "replace-with-sol-secret",
        "api_version": "preview"
      }
    },
    {
      "id": "terra-5-6",
      "type": "azure_openai",
      "capabilities": {
        "reasoning": {
          "efforts": ["low", "medium", "high", "xhigh"],
          "default_effort": "medium"
        }
      },
      "azure_openai": {
        "endpoint": "https://terra-resource.openai.azure.com",
        "model": "gpt-5.6-terra",
        "deployment": "terra-5-6",
        "api_key": "replace-with-terra-secret",
        "api_version": "preview"
      }
    }
  ]
}
```

Each profile's reasoning list is authoritative for AgentX startup and live
effort validation; `default_effort` must be one of that profile's declared
`efforts`. These fields are operator-maintained declarations. AgentX does not
probe Azure to verify that a deployment actually supports them, so keep each
profile aligned with its deployment.
On Unix, make `auth.json` owner-readable and owner-writable only. Across
supported runtime paths, all configured Azure model credentials are redacted
from output, ordinary child environments, model context, and transcripts.
Explicit MCP-provider environment values are scoped to that provider's
subprocess, and configured provider credential values are scrubbed from its
model-facing results. The current Windows build fails closed before reading
`auth.json` because native owner/DACL verification is not implemented.

Provider responses are untrusted even when transport authentication succeeds. Structural IDs, names, phases, discriminators, request IDs, and opaque reasoning state are rejected before persistence or execution if they reflect the credential or contain unsafe controls; text and structured error fields are redacted across streaming chunks and adjacent field boundaries. The provider-neutral engine repeats these checks so a custom model adapter cannot bypass the Azure boundary.

## Install and run

Go 1.26 or newer is required.

```sh
go install github.com/greenpau/agentx@latest
agentx --version
agentx --list-providers --output-format json
```

From a checked-out copy of this repository, install the current source instead:

```sh
go install .
```

Ensure the Go installation directory is on `PATH`. By default it is `$(go env GOPATH)/bin` on Unix-like systems and `%USERPROFILE%\go\bin` on Windows, unless `GOBIN` is configured.

```sh
agentx
agentx --print "inspect this repository"
agentx --print --attachment screenshot.png "explain this screenshot"
agentx --print --attachment before.jpg --attachment after.png \
  "compare these images in argument order"
agentx --print --attachment report.pdf
agentx --print --output-format json "summarize the architecture"
agentx --print --input-format stream-json --output-format stream-json
agentx --mcp-server
```

Useful controls include `--provider terra-5-6`, `--effort high`,
`--permission-mode plan`, `--allowed-tools`, `--disallowed-tools`, `--resume`,
`--continue`, `--fork-session`, and `--no-session-persistence`. Project
`AGENTS.md`, repository-local `.codex/skills`, and workspace `.agentx/`
plugins, hooks, output styles, and MCP configuration are ignored until
`--trust-workspace` is explicit. The private user application home is not that
workspace extension directory and never becomes project-controlled merely
because a workspace is trusted. Skills are never loaded from user
configuration, plugins, remote providers, or nested repositories. Run
`agentx --help` for the current contract.

Routine model-backed turn lifecycle records are DEBUG diagnostics. Pass `-d`
or `--debug` to emit turn-correlated lifecycle records plus session,
model-iteration, stream, retry, tool, usage, timing, and terminal-state metadata
to stderr; retry warnings carry session and model identity. Without debug,
successful turns do not emit routine lifecycle records; WARN and ERROR
conditions remain eligible. For persistent sessions, the accepted user event,
provider usage, and terminal turn result are instead durable session evidence
in `transcript.jsonl`. Diagnostic records omit prompts, model text, tool
arguments and results, file contents, headers, bodies, and configured
credentials. stdout retains the selected text or structured output contract, so
troubleshooting output can be captured separately with `2>agentx-debug.log`.

Reasoning capabilities belong to the selected provider profile. Its
`default_effort` applies unless `AGENTX_REASONING_EFFORT` or `--effort`
overrides it, and every effective value must appear in that profile's
`efforts` list. Use `--provider ID` to choose an endpoint; `--model`, when
supplied, must match that selected profile's deployment-backed logical model
and never reroutes to another endpoint. Changing providers requires a new
AgentX process.

### Native input attachments

Headless input accepts repeatable explicit `--attachment PATH` arguments with
optional prompt text. Attachment-only turns and mixed ordered text/media are
supported. The closed first matrix is PNG (`image/png`), JPEG (`image/jpeg`),
and PDF (`application/pdf`): at most 8 files, 20 MiB per file, 40 MiB per
message/model request, 8,192 pixels on either image dimension, 20,000,000
image pixels, and 100 PDF pages. A session admits at most 100,000 durable
committed attachment manifests and 512 MiB of unique blobs; its independent
in-process terminal upload-attempt ledger is also capped at 100,000 accepted
upload lifecycle IDs. Unsupported formats—including audio, SVG, GIF, WebP,
URLs, and arbitrary binary—fail explicitly.

AgentX snapshots only caller-selected regular single-link files, rejects
symlinks and replacement/growth/truncation races, verifies media from bytes,
normalizes images by decode/re-encode with metadata removal, and validates PDFs
with a conservative decoded-name parser, a complete offset-verified classic
xref table, and a consistent catalog/page tree. PDF encryption, active/action
content, annotations, forms/XFA, embedded content, object/xref streams, and
incremental updates are rejected; AgentX does not execute or decompress stream
content, OCR, convert, or sanitize arbitrary PDF semantics. Blobs are
immutable, content-addressed, owner-private session data capped at 512 MiB.
Transcripts contain manifests only; they never contain bytes, base64, or
original absolute paths. Resume reuses verified blobs, fork copies them into
the destination, and native session deletion removes the local store but is
not secure erasure of backups or remote copies.

Stream-JSON clients must first observe the advertised version-1
`input_capabilities`, then use correlated bounded
`attachment_import` begin/chunk/commit/abort records and reference only the
committed manifest from a typed user message. Capability absence means
text-only. Capability source entries are scoped: `file_path` applies only to
the initial CLI prompt and `stream_json_v1` to per-turn structured imports.
The exact schema, 256 KiB decoded chunk limit, 120-second upload timeout, 8 MiB
NDJSON record ceiling, acknowledgements, and queue semantics are in the
[wire contract](.codex/skills/implementation-headless-sdk/references/sdk-wire-protocol.md#versioned-user-content-and-attachment-import)
and [User Guide](USER_GUIDE.md#upload-attachments-over-stream-json).

The current provider qualification is deliberately narrow: Azure/OpenAI
Responses, logical model exactly `gpt-5.6-sol`, and API selector empty, `v1`,
or `preview`. Loopback tests prove exact `input_image`/`input_file` request
construction. One current-worktree profile passed representative live
PNG/JPEG/conservative-PDF, mixed-order, stream, resume, fork, compaction, and
privacy qualification; see the
[sanitized evidence](.codex/skills/implementation-conformance-audit/references/native-attachment-production-qualification.md).
That run is not a release-artifact or universal deployment/selector/platform
attestation, so each claimed release profile still requires `MOD-A14B`.
Configured non-sol endpoints remain text-only unless that exact profile is
separately qualified; neither Azure provider type nor declared reasoning
capabilities imply native-media support.
Media quarantine requires closed media-specific
status/code/parameter evidence; unrelated provider failures retain valid media,
and media-bearing provider diagnostics are replaced wholesale before they can
reach output, logs, or durable history.

The bidirectional NDJSON adapter remains live for correlated controls and can queue `now`, `next`, and `later` user records while a turn is active. It validates and reserves the whole typed message before a `now` record cancels the active turn; the accepted record then runs as the next serialized turn and is not spliced into the active recursive model/tool turn. MCP integration is stdio-only; image and audio result blocks are validated but become inert text/metadata placeholders rather than native user attachments, and trusted project MCP configuration does not yet have a separate approval persisted against its exact configuration fingerprint. Resume, fork, and compaction are useful but similarly bounded; see the conformance profile for their explicit compatibility gaps.

## Visual Studio Code extension

[greenpau/agentx-vscode-extension](https://github.com/greenpau/agentx-vscode-extension) maintains the companion Visual Studio Code adapter as a separate repository. Its extension identifier is `greenpau.agentx`.

The binary remains authoritative for the model loop, capabilities, permission policy, session lock, transcript, recovery, and tool results. The extension keeps only a bounded, redacted, lossy presentation cache and never uses it as model context. Adding editor context contributes a workspace-relative path and optional range rather than silently copying file contents, so AgentX must still use its ordinary `Read` capability and path policy. Restricted Mode keeps the chat explanation visible but blocks AgentX process launch and workspace-defined launch settings until VS Code trusts the workspace. The webview uses a closed content-security policy, has no network or workspace-resource access, renders untrusted values as text, and delegates validated `https` links to the extension host. Extension-owned launches request a kill-on-close Windows Job Object and use an owned Unix process group so bounded shutdown covers descendant processes.

The first structured `system/init` event describes only the process-selected
profile: its provider ID and type, logical model, and operator-declared
reasoning capabilities. A separate, fieldless correlated `initialize` control
request returns the complete safe `providers` catalog, including selected and
effective-default state for every profile. Neither form publishes API keys,
endpoint URLs, deployments, or API-version selectors. Provider switching is
startup-bound: a host must restart AgentX with `--provider ID`, not request a
live model mutation.

A host integration can discover profiles before launch by invoking
`agentx --list-providers --output-format json`. That provider-neutral command
works without a configured default, creates no semantic session, and returns
the same provider descriptor fields as structured `initialize`, with
`selected:false` for every entry. After choosing an ID, start the normal
stream-JSON process with `--provider ID`; its initialization catalog then marks
exactly that profile selected. This binary contract makes discovery available
to VS Code integrations; it does not claim that every release of the companion
extension already exposes a provider picker or provider setting.

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

When session persistence is enabled, history is append-only JSONL below `<application-home>/sessions/<workspace-hash>/<session-id>/` with restrictive permissions. Native attachment blobs are untrusted model input, not instruction or tool authority; their presence grants no filesystem or capability permission. Standalone project memory is separate from session transcripts and keyed by the selected absolute workspace; linked worktrees therefore do not currently share it. User input and accepted tool calls are durable before the corresponding network request or side effect. Recovery never replays an uncertain call: a fully unresolved response-identified assistant/tool group leaves the live projection, while a missing member of a retained mixed group receives only an in-memory interrupted result. Forks copy durable raw evidence, never promote those derived results, and remain hidden behind a completion marker until the destination batch is durable. Forking is not a single transaction across the independent source and destination stores, but a crash-partial destination cannot be selected by `--continue` or explicit resume.

Durable history is also provider-bound. Every record carries the provider ID,
type, logical model, and an opaque fingerprint of the noncredential route
(normalized endpoint route, deployment, and API selector). Rotating only the
profile's API key does not change that fingerprint and preserves resume/fork
eligibility. Selecting another provider, or changing the bound profile's type,
model, endpoint route, deployment, or API selector, fails before replay or
provider I/O. `--continue` chooses the latest eligible session in the
workspace; it does not prefilter by the currently selected provider, so a
binding mismatch can direct the user to restart with the recorded
`--provider ID`.

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

Network tests bind loopback-only `httptest` servers; they never call the
production Azure endpoint. Image/PDF loopback fixtures prove request
construction, not deployment eligibility.

The repository-local Ruby architecture audits validate the implementation-skill hierarchy. They do not, by themselves, prove that every contract has an executable Go implementation; the conformance profile and Go tests are the implementation evidence.

## License

AgentX is free software licensed under the [GNU General Public License, version 3 only](LICENSE) (`GPL-3.0-only`).
