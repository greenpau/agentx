# AgentX User Guide

AgentX is a local software-engineering agent. It sends your requests and selected context to the configured model, can inspect and modify the current repository through controlled tools, and stores resumable session history locally. You can use it from a terminal or through the AgentX extension for Visual Studio Code.

## Before you begin

AgentX requires:

- Go 1.26 or newer to compile and install the binary.
- Access to the configured Azure OpenAI deployment.
- On Unix-like systems, a private `.env.production` file in the workspace or one complete Azure configuration supplied through the process environment. On Windows, supply the complete configuration through the process environment because this portable build rejects dotenv credential files until it can verify native ownership and DACL safety.
- VS Code 1.95 or newer for the editor extension.

The default `.env.production` file contains:

```dotenv
AZURE_OPENAI_ENDPOINT=https://your-resource.openai.azure.com
AZURE_OPENAI_MODEL_NAME=gpt-5.6-sol
AZURE_OPENAI_DEPLOYMENT=gpt-5.6-sol
AZURE_OPENAI_SUBSCRIPTION_KEY=your-secret-key
AZURE_OPENAI_API_VERSION=preview
```

Do not commit this file. On Unix-like systems, make it readable and writable only by its owner:

```sh
chmod 600 .env.production
```

AgentX rejects partial mixtures of file-based and process-based Azure configuration. Supply the complete configuration through one source.

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

### Control reasoning and turn limits

The default reasoning effort is `high`. Supported values are `none`, `low`, `medium`, `high`, `xhigh`, and `max`:

```sh
agentx --effort medium
agentx --print --effort xhigh --max-turns 20 "investigate this failure"
```

The configured model is deployment-backed. A `--model` value must match `AZURE_OPENAI_MODEL_NAME`; AgentX will not silently route a different logical model name to the deployment.

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

Denials and mandatory safety checks take precedence over broad allow rules. Bash remains approval-sensitive, and protected paths such as `.env.production`, `.git`, `.agentx`, and `.codex` do not become readable or writable merely because a broad rule was allowed.

### Use bare mode

Bare mode suppresses implicit repository instructions, skills, plugins, MCP configuration, memory, and output styles:

```sh
agentx --bare
```

Use this for a minimal session when you do not want workspace customization loaded.

### Resume, continue, and fork sessions

AgentX persists sessions by default.

```sh
agentx --continue
agentx --resume SESSION_ID
agentx --resume SESSION_ID --fork-session
```

- `--continue` opens the latest eligible session.
- `--resume` opens a specific session.
- `--fork-session` creates a new session from the selected durable history.
- `--no-session-persistence` disables transcript writes for a headless request.

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
| `agentx.envFile` | Credential file, relative to the workspace by default. |
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
5. Confirm that `agentx.envFile` resolves to a valid protected file.
6. Run `agentx --version` in the same local or remote environment where the extension host runs.

For Remote SSH, Dev Containers, or WSL, the extension and AgentX binary run in the remote workspace extension host. Install or configure the binary and credentials in that environment, not only on the desktop host.

## Security guidance

- Never paste credentials into prompts, tool inputs, chat context, or diagnostics.
- Never commit `.env.production` or other secret-bearing files.
- Review permission requests carefully, especially shell commands and writes.
- Prefer `plan` or `dontAsk` when inspecting an unfamiliar repository.
- Review `AGENTS.md`, `.codex/skills`, and `.agentx` before enabling trusted workspace features.
- Use `--bare` or `agentx.bare` when repository customization is not required.
- Remember that the extension is a presentation adapter: the AgentX binary owns permissions, tools, transcripts, and recovery.

## Current limitations

- This profile accepts text input; image, audio, and arbitrary attachment blocks are not supported.
- Provider OAuth, delegated agents and teams, cloud handoff, and automatic binary updates are unavailable.
- The VS Code extension does not provide a complete session-history browser.
- Reasoning effort, permission mode, output style, allow/deny rules, bare mode, environment file, and trust loading are restart-bound in VS Code.
- MCP support is stdio-only in the current runtime profile.

For implementation boundaries and exact compatibility status, see the repo-local [runtime architecture](.codex/skills/coding-directives/references/runtime-architecture.md), [runtime conformance profile](.codex/skills/coding-directives/references/runtime-conformance.md), and the standalone extension's [VS Code host protocol](https://github.com/greenpau/agentx-vscode-extension/blob/main/docs/PROTOCOL.md).
