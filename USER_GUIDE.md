# AgentX User Guide

AgentX is a local software-engineering agent. It sends your requests and selected context to the configured model, can inspect and modify the current repository through controlled tools, and stores resumable session history locally. You can use it from a terminal or through the AgentX extension for Visual Studio Code.

## Before you begin

AgentX requires:

- Go 1.26 or newer to compile and install the binary.
- Access to the configured Azure OpenAI deployment.
- A private `auth.json` in the AgentX application home.
- VS Code 1.95 or newer for the editor extension.

## Configure authentication

AgentX stores application-owned state in `~/.agentx/` by default. Set the
public `AGENTX_HOME` environment variable to an absolute path when the whole
application home must live elsewhere. This is the only supported application-
home override. Blank values are treated as unset; a nonblank value must be an
absolute, non-root path, and an invalid override fails rather than selecting
the default. AgentX selects the application-home path once, before it inspects
command-line arguments. Credential loading pins that home while reading
`auth.json`; session and project-memory paths are derived from the same frozen
selection. Existing user plugins, output styles, and MCP configuration retain
their operating-system user-configuration root; `AGENTX_HOME` does not relocate
those extension sources.
Regardless of its basename or location, the selected application home and all
of its descendants remain protected control data. Placing `AGENTX_HOME` inside
a workspace does not make credentials, sessions, transcripts, task state,
tool results, or project memory readable or editable through broad workspace
permissions or bypass mode.
Before and after permission evaluation, before a tool can execute, AgentX
rechecks the frozen home and `sessions/` directory identities. If either
pathname was renamed or replaced—or its private mode changed on a supported
POSIX platform—AgentX denies pending and future tool use and asks you to
restart it. This check detects a sustained identity change; it is not an
atomic lock over every later descendant filesystem operation.

Every invocation creates the application home and its `sessions/` child before
command-line parsing. On supported POSIX platforms, AgentX establishes and
rechecks owner-only permissions and requires and rechecks current-user
ownership. On Windows it enforces direct-directory and stable-identity checks,
but cannot yet establish or prove owner-only DACL protection. Before full
command-line parsing, `auth.json` must exist even for malformed input,
`--help`, `--version`, and `--mcp-server`.
Informational and standalone MCP invocations check that the file exists but do
not construct a model client. A model-backed invocation strictly validates the
file before it discovers extensions, creates a persistent session, or makes a
network request.

The default layout begins as:

```text
~/.agentx/
├── auth.json
└── sessions/
```

When `AGENTX_HOME` selects another location, substitute that effective path for
`~/.agentx` in the examples below.

Create `~/.agentx/auth.json` with this exact versioned,
provider-discriminated shape:

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

The complete file must be valid UTF-8 JSON no larger than 64 KiB. `version`
must be the integer `1`, and `provider` must be
`"azure_openai"`. The `azure_openai` object must contain all five shown string
fields. `endpoint` is the Azure OpenAI resource URL, `model` is AgentX's
logical model identity, `deployment` is the Azure deployment sent to the
Responses API, `api_key` is the subscription key, and `api_version` is the
Azure API version selector. Endpoint, model, deployment, and API key must be
nonempty; an empty API-version string selects the default v1 route. Unknown or
duplicate fields, trailing JSON, unsupported versions/providers, wrong types,
and missing required values are rejected.

The endpoint must be an absolute HTTPS URL without user information, a query,
or a fragment. Model and deployment values are each limited to 256 UTF-8
bytes. The API key is limited to 16 KiB and cannot contain whitespace or unsafe
control/formatting characters. A nonempty API version is limited to 128 UTF-8
bytes. Model, deployment, and API-version values likewise reject unsafe
control/formatting characters.

`auth.json` is the sole model credential source. Keep it outside repositories,
never replace the placeholder with a secret in committed examples, and do not
paste its contents into prompts or diagnostics. Rotate the key if it is ever
exposed or committed.

On Unix-like systems, make the directories owner-only and the file readable
and writable only by its owner:

```sh
mkdir -p ~/.agentx/sessions
chmod 700 ~/.agentx ~/.agentx/sessions
chmod 600 ~/.agentx/auth.json
```

On Windows, restrict the application home and `auth.json` to the current user.
The current standalone Go profile does not yet implement the native owner/DACL
inspection required to prove that protection, so it fails closed before
reading `auth.json`; model-backed startup is unavailable on Windows until that
adapter exists.

If `auth.json` is missing—or the selected child is a directory or symbolic
link rather than a direct regular file—AgentX exits without starting a model
or persistent session. The error includes the expected path, the placeholder
JSON shape above, and this stable guide link:
<https://github.com/greenpau/agentx/blob/main/USER_GUIDE.md>.

## Install the AgentX binary

Compile and install the latest published source with Go:

```sh
go install github.com/greenpau/agentx@latest
```

To install the exact source in a checked-out repository, run this from its root:

```sh
go install .
```

`go install` compiles AgentX and writes the executable to `GOBIN` when configured. Otherwise, it uses `$(go env GOPATH)/bin` on Unix-like systems and `%USERPROFILE%\go\bin` on Windows. Add that directory to `PATH`, then verify the installation:

```sh
agentx --version
agentx --help
```

Re-run `go install .` after updating a local checkout to replace the installed binary with the newly compiled version.

## Use AgentX in a terminal

Change to the repository you want AgentX to work in, then start an interactive session:

```sh
agentx
```

Enter a request at the prompt, for example:

```text
Explain how configuration is loaded and identify the relevant tests.
```

AgentX streams the response and shows tool activity. If a requested operation needs approval, review the exact operation and choose whether to allow or deny it.

### Enable repository instructions and skills

Workspace-defined behavior is disabled unless you explicitly trust the workspace:

```sh
agentx --trust-workspace
```

Trust enables the repository's `AGENTS.md`, its root `.codex/skills` hierarchy, and project `.agentx` plugins, hooks, output styles, and MCP configuration. AgentX discovers skills only from the active repository's root `.codex/skills`; it does not load user-global, plugin-provided, remote, bundled, nested-repository, or additional-directory skills.

Only trust repositories whose configuration and executable extension files you have reviewed.

### Run a one-shot request

Use `--print` for scripts or a single noninteractive request:

```sh
agentx --print "summarize the repository architecture"
agentx --print --trust-workspace "review the current changes"
```

Choose an output format when another program will consume the result:

```sh
agentx --print --output-format text "explain this project"
agentx --print --output-format json "summarize test coverage"
agentx --print --input-format stream-json --output-format stream-json
```

Structured stdout contains protocol records only; diagnostics are written separately to stderr.
Cost fields are `null` when the configured deployment has no authoritative price;
numeric `0` is reserved for a known zero cost.

### Troubleshoot a turn

Successful turns do not write routine lifecycle records at the default INFO
threshold. Enable DEBUG diagnostics to write one correlated start and terminal
record for each model-backed turn, together with detailed troubleshooting
context:

```sh
agentx --trust-workspace --print --output-format text --debug \
  "investigate this repository" 2>agentx-debug.log
```

DEBUG adds session construction, model-iteration, stream, retry, tool, usage,
timing, and terminal-state metadata. WARN and ERROR conditions can still appear
without debug. For a persistent session, the durable session record is not the
diagnostic stream: `transcript.jsonl` stores the accepted user event as the turn
start, provider usage under the same turn ID, and one terminal `turn_result`
when finalization succeeds. Diagnostics do not include prompts, model text,
tool arguments or results, file contents, request headers or bodies, or
configured credentials. stdout remains reserved for the requested text, JSON,
or NDJSON result.

### Control reasoning and turn limits

The default reasoning effort is `high`. Supported values are `none`, `low`, `medium`, `high`, `xhigh`, and `max`:

```sh
agentx --effort medium
agentx --print --effort xhigh --max-turns 20 "investigate this failure"
```

The configured model is deployment-backed. A `--model` value must match
`azure_openai.model` in `auth.json`; AgentX will not silently route a different
logical model name to the deployment.

### Choose a permission mode

```sh
agentx --permission-mode default
agentx --permission-mode acceptEdits
agentx --permission-mode plan
agentx --permission-mode dontAsk
```

- `default` asks whenever policy requires approval.
- `acceptEdits` pre-authorizes eligible file edits, while all other safety checks remain active.
- `plan` allows analysis and planning without mutation.
- `dontAsk` denies operations that would require interactive approval.

You can further restrict capabilities with startup rules:

```sh
agentx --allowed-tools 'Read,Glob,Grep'
agentx --disallowed-tools 'Bash,Write,Edit'
```

Denials and mandatory safety checks take precedence over broad allow rules.
Bash remains approval-sensitive, and protected paths such as
`~/.agentx/auth.json`, `.git`, a workspace `.agentx/`, and `.codex/` do not
become readable or writable merely because a broad rule was allowed. The user
application home `~/.agentx/` and a repository's workspace-extension
`.agentx/` directory are different trust domains. If AgentX reports that its
home identity changed, stop modifying that directory, restore it if
appropriate, and restart AgentX before using tools again.

### Use bare mode

Bare mode suppresses implicit repository instructions, skills, plugins, MCP configuration, memory, and output styles:

```sh
agentx --bare
```

Use this for a minimal session when you do not want workspace customization loaded.

### Resume, continue, and fork sessions

AgentX persists sessions by default under
`<application-home>/sessions/<workspace-hash>/<session-id>/`. The workspace
hash keeps `--continue` and session discovery scoped to the selected workspace;
the session identifier names one private session directory. Project memory is
stored separately and remains project-scoped rather than being inferred from
or copied into a session directory.

```sh
agentx --continue
agentx --resume SESSION_ID
agentx --resume SESSION_ID --fork-session
```

- `--continue` opens the latest eligible session.
- `--resume` opens a specific session.
- `--fork-session` creates a new session from the selected durable history.
- `--no-session-persistence` uses a temporary, nonresumable headless session:
  it writes no transcript, cannot combine with resume/continue/fork, and does
  not load or expose project memory.

### List and delete native sessions

Use the provider-free management flags to inspect or delete native AgentX
sessions without starting a model connection or semantic session. Both
operations require `--cwd`; AgentX normalizes that workspace and scopes the
operation to its local session partition.

```sh
agentx --list-sessions --cwd WORKSPACE [--output-format text|json]
agentx --list-sessions --cwd WORKSPACE --session-page-size 100 \
  [--session-page-token TOKEN] --output-format json

agentx --delete-session SESSION_ID --session-revision REVISION \
  --cwd WORKSPACE [--output-format text|json]
```

List pages default to 100 entries and accept sizes from 1 through 500. When
more entries remain, pass the returned opaque `next_page_token` as
`--session-page-token`. Each listed session includes an opaque `revision`;
deletion requires that exact value so a changed or replaced target returns
`stale` instead of deleting the wrong directory.

Text output is intended for people and includes session ID, update time, and
revision. JSON writes exactly one versioned object to stdout, with diagnostics
on stderr. List status is one of `ok`, `stale`, or `store_unsafe`. Delete
status is one of `deleted`, `not_found`, `stale`, `session_locked`,
`delete_incomplete`, or `store_unsafe`; non-success outcomes remain
machine-readable even when the process exits nonzero. `session_locked` means
another process owns the session lock. `delete_incomplete` means cleanup is
still pending and retained data has not been reported as deleted.

Deletion removes only the selected directory from AgentX's local native
session store. It is not secure media erasure and does not delete backups,
remote copies, project memory, worktrees, authentication or configuration,
fork descendants, or any AgentX VS Code extension presentation cache.

AgentX discovers sessions and project memory only in the current application
home. It does not scan, migrate, or delete data from another layout or
directory. Back up and move any such data manually before relying on it, while
preserving owner-only directory and file permissions.

AgentX never assumes an interrupted side effect succeeded and does not automatically replay an uncertain tool call during recovery.

### Useful interactive commands

Inside the terminal session, use `/help` to see the current command catalog. Common commands include:

- `/status` — show the current runtime and session status.
- `/skills` — list skills available from `.codex/skills`.
- `/tasks` — show registered background tasks.
- `/cost` — show current usage accounting.
- `/doctor` — run runtime diagnostics.
- `/compact` — compact conversation context.
- `/mcp status` — show MCP server state.
- `/mcp reload` — reload eligible MCP configuration.
- `/plugin` — inspect plugin state.
- `/memory list`, `/memory recall`, `/memory remember` — work with local memory.
- `/output-style` — inspect or select an output style.
- `/clear` — clear active model context while retaining the durable transcript.
- `/exit` — close the session cleanly.

Availability can vary by mode and build. A command reports an explicit unavailable result when its backing feature is not operational.

## Use AgentX in Visual Studio Code

The AgentX extension opens an editor-native chat backed by the same AgentX binary and session runtime.

### Install or select the binary

The extension resolves AgentX in this order:

1. The absolute path in `agentx.binaryPath`.
2. A platform-specific binary bundled in the installed VSIX.
3. `agentx` on the extension-host `PATH`.

If you are developing locally, run `go install .` from the repository root and ensure Go's binary installation directory is on the extension host's `PATH`. To locate that directory, run:

```sh
go env GOBIN
go env GOPATH
```

If `go env GOBIN` is nonempty, the binary is `<GOBIN>/agentx` (or `agentx.exe` on Windows). Otherwise, use the `bin` directory beneath `go env GOPATH`. You can set **AgentX: Binary Path** to that executable's absolute path.

Run **AgentX: Run Installation Diagnostics** from the Command Palette if the extension cannot find or start the binary.

### Trust the workspace

VS Code must trust the workspace before the extension can launch AgentX. In Restricted Mode, the AgentX view remains visible so it can explain the restriction, but no AgentX process is started and workspace-controlled launch settings are blocked.

After reviewing the repository, use VS Code's **Workspace: Manage Workspace Trust** command and trust it. The `agentx.trustWorkspaceFeatures` setting separately controls whether a trusted workspace's `AGENTS.md`, `.codex/skills`, and `.agentx` extensions are passed to AgentX.

### Open and use the chat

Select the AgentX icon in the Activity Bar or run **AgentX: Open Chat**. Enter a request in the composer and submit it. The view shows:

- Streaming assistant text.
- Tool calls and correlated results.
- Permission requests.
- Structured questions.
- Context usage and turn status.
- Queued follow-up requests.

Use **AgentX: Stop Current Turn** to cancel active work. Cancellation is sent to the AgentX runtime; closing a visual row alone does not redefine session state.

### Add editor context

AgentX offers editor and source-control commands:

- **AgentX: Add Selection Reference to Chat**
- **AgentX: Add Current File to Chat**
- **AgentX: Add Current File Problems to Chat**
- **AgentX: Explain Selection**
- **AgentX: Fix Selection**
- **AgentX: Generate Tests for Selection**
- **AgentX: Review Workspace Changes**

A file or selection adds a workspace-relative path and optional range. The extension does not silently copy the entire file into the prompt. AgentX must still read it through its ordinary tools and permission policy. Problems are included only when you explicitly add them.

### Respond to permissions and questions

For a permission request, the extension offers:

- Allow once.
- Edit the complete input and allow.
- Deny.
- Deny and stop the active turn.

There is no permanent-approval button in the current extension protocol. Edited input is treated as a complete replacement and is validated again by AgentX.

For model-generated questions, choose one or more listed options or provide free-form text when offered, then submit the response.

### Manage sessions

Use the Command Palette or chat controls:

- **AgentX: New Chat**
- **AgentX: Continue Latest Session**
- **AgentX: Resume Session by ID**
- **AgentX: Fork Session by ID**

The extension's session picker contains sessions previously observed by that extension plus a manual-ID option. It is not a complete transcript browser. AgentX owns authoritative transcript storage; the extension retains only a bounded, redacted presentation cache.

### Configure the extension

Open **AgentX: Open Settings** to change:

| Setting | Purpose |
| --- | --- |
| `agentx.binaryPath` | Absolute AgentX executable path. |
| `agentx.reasoningEffort` | Reasoning effort for newly started sessions. |
| `agentx.permissionMode` | `default`, `acceptEdits`, `plan`, or `dontAsk`. |
| `agentx.maxTurns` | Maximum recursive model turns per request. |
| `agentx.trustWorkspaceFeatures` | Enable trusted repository instructions and extensions. |
| `agentx.bare` | Disable implicit instructions, skills, extensions, MCP, and memory. |
| `agentx.outputStyle` | Select a discovered output style. |
| `agentx.allowedTools` | Startup capability allow rules. |
| `agentx.disallowedTools` | Startup capability deny rules. |
| `agentx.followUpMode` | Queue follow-ups or cancel the active turn before running the next request. |
| `agentx.composerEnterBehavior` | Choose whether Enter sends or inserts a newline. |
| `agentx.todoCodeLens` | Show AgentX actions above TODO and FIXME comments. |
| `agentx.startOnViewOpen` | Start AgentX when the chat view opens instead of on first use. |
| `agentx.historyLimit` | Bound cached presentation records per known session. |
| `agentx.maxRenderedTextBytes` | Bound text retained for a rendered message or tool payload. |
| `agentx.completionNotifications` | Control completed-turn notifications. |

Startup settings apply to the next AgentX process. Start a new chat after changing restart-bound settings.

### Diagnose extension problems

Use these commands in order:

1. **AgentX: Run Installation Diagnostics** — verify binary selection and compatibility.
2. **AgentX: Show Output** — inspect bounded extension-host diagnostics.
3. **AgentX: Copy Diagnostic Report** — copy a redacted report for support.
4. Confirm that the workspace is trusted.
5. Confirm that `<application-home>/auth.json` exists and matches the exact schema in [Configure authentication](#configure-authentication).
6. Run `agentx --version` in the same local or remote environment where the extension host runs.

For Remote SSH, Dev Containers, or WSL, the extension and AgentX binary run in
the remote workspace extension host. Install the binary and create
`~/.agentx/auth.json` in that environment, not only on the desktop host.

## Security guidance

- Never paste credentials into prompts, tool inputs, chat context, or diagnostics.
- Never commit `~/.agentx/auth.json` or another secret-bearing file.
- Review permission requests carefully, especially shell commands and writes.
- Prefer `plan` or `dontAsk` when inspecting an unfamiliar repository.
- Review `AGENTS.md`, `.codex/skills`, and the workspace `.agentx/` directory before enabling trusted workspace features.
- Use `--bare` or `agentx.bare` when repository customization is not required.
- Remember that the extension is a presentation adapter: the AgentX binary owns permissions, tools, transcripts, and recovery.

## Current limitations

- This profile accepts text input; image, audio, and arbitrary attachment blocks are not supported.
- Provider OAuth, delegated agents and teams, cloud handoff, and automatic binary updates are unavailable.
- The VS Code extension does not provide a complete session-history browser.
- Reasoning effort, permission mode, output style, allow/deny rules, bare mode, and trust loading are restart-bound in VS Code.
- MCP support is stdio-only in the current runtime profile.

For implementation boundaries and exact compatibility status, see the repo-local [runtime architecture](.codex/skills/coding-directives/references/runtime-architecture.md), [runtime conformance profile](.codex/skills/coding-directives/references/runtime-conformance.md), and the standalone extension's [VS Code host protocol](https://github.com/greenpau/agentx-vscode-extension/blob/main/docs/PROTOCOL.md).
