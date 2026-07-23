# IDE adapter contract

This document is normative for discovering a supported editor, connecting it as a session-scoped MCP client, translating editor notifications into conversation context, and handing file diffs back to the editor. `IDE-*` identifiers are stable implementation anchors. The adapter is optional: failure must not prevent a normal terminal session. Use the [IDE adapter diagram](../assets/ide-adapter.drawio) to trace the identity, validation, generation, and bidirectional path boundaries while reading.

## Contents

1. [Ownership and identity](#ownership-and-identity)
2. [Lockfile discovery and cleanup](#lockfile-discovery-and-cleanup)
3. [Workspace and process validation](#workspace-and-process-validation)
4. [Path conversion](#path-conversion)
5. [Connection configuration and transports](#connection-configuration-and-transports)
6. [Connection lifecycle and registry effects](#connection-lifecycle-and-registry-effects)
7. [Editor RPC contracts](#editor-rpc-contracts)
8. [Notifications and model context](#notifications-and-model-context)
9. [Command and installation behavior](#command-and-installation-behavior)
10. [Failures, security, and compatibility](#failures-security-and-compatibility)
11. [Acceptance scenarios](#acceptance-scenarios)

## Ownership and identity

`IDE-001` — Treat the editor as a dynamically discovered, session-scoped MCP peer, not as an LSP server and not as durable settings. Keep these identities distinct:

- lockfile identity: path, modification time, port, process identifier, workspace folders, editor name, transport, Windows-host flag, and optional authorization token;
- connection identity: one client instance and transport generation;
- registry identity: dynamic server name `ide` and any allowed model-callable IDE tools;
- presentation identity: human-readable editor name and connected/disconnected status.

`IDE-002` — Supported terminal/editor families are:

| Family | Recognized editor identifiers |
| --- | --- |
| VS Code protocol | `cursor`, `windsurf`, `vscode` |
| JetBrains protocol | `pycharm`, `intellij`, `webstorm`, `phpstorm`, `rubymine`, `clion`, `goland`, `rider`, `datagrip`, `appcode`, `dataspell`, `aqua`, `gateway`, `fleet`, `androidstudio` |

Process-name discovery is platform-specific. Aqua, Gateway, and Fleet are not automatically inferred on macOS or Linux from their generic process names. A forced-terminal development switch may declare the current terminal supported even when ordinary detection says otherwise.

`IDE-003` — Editor auto-connection is enabled when any of these is true: global `autoConnectIde`; explicit command-line IDE request; a supported embedded terminal; an explicit IDE port environment value; an explicit install target; or a truthy auto-connect environment value. An explicitly false auto-connect environment value overrides all enabling signals. Never replace an already configured dynamic `ide` connection.

## Lockfile discovery and cleanup

`IDE-010` — Search the configuration home's `ide` directory. Under Windows Subsystem for Linux, also search the Windows user's `.agentx/ide` directory, resolving the Windows profile from environment first and a host command second, plus every non-system `/mnt/c/Users/<user>/.agentx/ide` directory. Missing or unreadable directories contribute no candidates.

`IDE-011` — Candidate filenames end in `.lock`. Remove that suffix and parse the remainder as the candidate port. Sort files by modification time newest first before interpreting them. The compatibility contract does not add a stricter port range check at this stage; connection probing is the validity boundary.

`IDE-012` — A current lockfile is a JSON object with this grammar:

| Field | Type | Meaning |
| --- | --- | --- |
| `workspaceFolders` | optional string array | editor workspace roots |
| `pid` | optional process identifier | editor/extension process evidence |
| `ideName` | optional string | normalized or display editor name |
| `transport` | optional `ws` or `sse` | defaults to SSE-compatible behavior |
| `runningInWindows` | optional boolean | editor is on Windows while client is in WSL |
| `authToken` | optional string | WebSocket authorization token |

A legacy non-JSON lockfile remains accepted as newline-separated workspace paths. Retain this read compatibility even though new writers should emit JSON.

`IDE-013` — Cleanup is conservative and best effort:

1. Delete a lockfile that cannot be read or parsed.
2. If it has a process identifier and the process is definitively dead, delete it; under WSL, probe the endpoint as a second authority because the host process may be invisible to the guest.
3. If no useful process evidence exists, probe the port.
4. Use a 500 ms probe bound. Failure makes the candidate stale; cleanup errors are diagnostics, not startup failures.

`IDE-014` — Automatic discovery runs asynchronously and must not block initial session rendering. A request to wait for one available editor first cancels the prior wait, cleans stale candidates, then polls once per second for at most 30 seconds. Suspend polling while terminal scroll-drain work owns the presentation loop. Return an editor only when exactly one eligible candidate remains; zero or ambiguity returns no automatic choice.

## Workspace and process validation

`IDE-020` — A candidate is workspace-valid if any of these conditions succeeds, in order:

1. the explicit skip-validity environment switch is set;
2. its port equals the numeric explicit IDE port environment value;
3. the current working directory is equal to or a descendant of one advertised workspace folder after normalization.

Normalize paths to Unicode NFC. On Windows, compare drive letters case-insensitively by canonicalizing the drive prefix. Do not use substring matching for ancestry.

`IDE-021` — When running under WSL against a Windows editor, first verify that a UNC workspace names the current distribution. A UNC path for another distribution is not valid. Test the original path and then a Windows-to-WSL conversion so both native and translated lockfile forms work.

`IDE-022` — On a supported embedded terminal outside WSL, use process ancestry to disambiguate multiple editor instances: the lockfile process must be the terminal's direct parent or appear within at most ten ancestors. The explicit matching port bypasses ancestry filtering. Process inspection failure degrades to the remaining workspace/port evidence; it does not authorize an otherwise mismatched workspace.

`IDE-023` — Normal discovery excludes workspace-invalid candidates. If an explicit IDE port is present and exactly one valid candidate matches it, return only that candidate. An interactive selection request may ask to include invalid candidates so the user can see and choose them; process-ancestry filtering still applies.

## Path conversion

`IDE-030` — Convert paths only at the WSL/Windows boundary. Prefer the platform's canonical path converter for guest-to-host and host-to-guest translation. A host-to-guest fallback replaces separators and maps a drive `X:` to `/mnt/x`; a failed guest-to-host conversion returns the original path.

`IDE-031` — A UNC path targeting another WSL distribution is never rewritten into the current distribution. Return the original path and let workspace validation reject it. The distribution-name test is case-insensitive; an ordinary non-UNC path has no distribution mismatch.

`IDE-032` — Authorize file access using the local canonical path, but send the editor the form its host expects. Never treat a successful path-string conversion as permission to read a file.

## Connection configuration and transports

`IDE-040` — Host selection follows this order:

1. explicit host-override environment value;
2. under WSL for a Windows-hosted editor, the guest's default route gateway, but only if the candidate port responds there;
3. `localhost`.

`IDE-041` — Derive the endpoint exactly:

- WebSocket lockfile: `ws://<host>:<port>`;
- all other lockfiles: `http://<host>:<port>/sse`.

The dynamic configuration is `{type:"ws-ide", url, ideName, ideRunningInWindows?, authToken?}` for WebSocket and `{type:"sse-ide", url, ideName, ideRunningInWindows?}` for SSE.

`IDE-042` — WebSocket connects with subprotocol `mcp`, the ordinary product user-agent, and `X-AgentX-Code-Ide-Authorization: <token>` when a token exists. It uses configured proxy and TLS behavior. SSE uses the proxy-aware event-stream transport but the specified contract does not send the lockfile authorization token. Preserve that limitation explicitly; do not imply SSE authentication that the peer never receives.

`IDE-043` — Treat every lockfile and every message from the endpoint as untrusted. Validate notification and tool-result shapes before use. Never expose the authorization token in model context, logs, diagnostics, or serialized dynamic settings.

## Connection lifecycle and registry effects

`IDE-050` — A successful transport joins the ordinary MCP client lifecycle, then sends best-effort notification `ide_connected` with `{pid:<client-process-id>}`. Connection status is observable to the UI. Reconnect or replacement uses a new generation so late callbacks from the old client cannot alter the current selection or registry.

`IDE-051` — Although the editor may publish multiple internal RPC tools, expose only `mcp__ide__executeCode` and `mcp__ide__getDiagnostics` to the model. Other IDE methods remain adapter-internal. Tool discovery never turns arbitrary editor methods into model capabilities.

`IDE-052` — Disconnect is idempotent and ordered: detach the close callback, close transport best effort, clear client caches, remove the dynamic IDE client, remove every `mcp__ide__*` tool/command contribution, clear dynamic configuration, and publish disconnected state. A stale close callback cannot disconnect a newer generation.

`IDE-053` — Direct internal RPC is encoded as an MCP tool call with a fresh cancellation signal. Normalize a plain string, protocol content blocks, or no content. Malformed content is an explicit adapter error; cancellation does not reuse an already-aborted signal.

## Editor RPC contracts

`IDE-060` — `openDiff` sends:

```json
{
  "old_file_path": "editor path",
  "new_file_path": "editor path",
  "new_file_contents": "complete proposed text",
  "tab_name": "stable per-diff tab label"
}
```

Interpret the first result block as a status token:

| Status | Required result |
| --- | --- |
| `FILE_SAVED` | accept the second text block as the editor-saved authoritative content |
| `TAB_CLOSED` | accept the originally proposed content |
| `DIFF_REJECTED` | retain the old content and report rejection |
| missing/other/malformed | fail the handoff explicitly |

On cancellation or process-before-exit, call `close_tab` with `{tab_name}` best effort. At the start of a new prompt, `closeAllDiffTabs {}` is best effort and does not block the turn.

`IDE-061` — `openFile` sends `{filePath, preview:false, startText:"", endText:"", selectToEndOfLine:false, makeFrontmost:false}`. It is a presentation side effect: failure is logged/observed but does not fail the semantic file operation that requested it.

`IDE-062` — `getDiagnostics` sends `{uri:"file://<editor-path>"}` for one file and `{}` for the editor-wide snapshot. Diagnostics are optional context. Timeouts, unsupported methods, or malformed results return no diagnostics rather than preventing core execution.

`IDE-063` — Notebook-to-context conversion is a bounded developer-content adapter. Parse the notebook document, preserve cell order, use a declared cell identifier or deterministic `cell-<index>` fallback, retain code language and execution count, normalize stream/result/display/error outputs, and accept only PNG/JPEG image payloads. For a whole notebook, replace aggregate cell output above 10,000 characters/encoded-image characters with an explicit per-cell retrieval instruction; a request for one named cell may include its complete normalized output. Merge adjacent text result blocks without reordering images, reject malformed documents or unknown requested cells explicitly, and never treat notebook content as trusted policy.

## Notifications and model context

`IDE-070` — Accept these editor notifications only from the current client generation:

| Notification | Payload grammar | Effect |
| --- | --- | --- |
| `selection_changed` | optional `text`, optional `filePath`, optional/nullable `selection:{start:{line,character},end:{line,character}}` | update transient editor selection |
| `at_mentioned` | required `filePath`, optional `lineStart`, optional `lineEnd` | queue an explicit file/range mention |
| `log_event` | `eventName` plus metadata object | emit namespaced integration telemetry |

`IDE-071` — Selection coordinates arrive zero-based. When a range exists, keep `lineStart = start.line`; compute `lineCount = end.line - start.line + 1`, subtracting one when `end.character == 0`. Reset the selection to zero/empty when the connected client changes. For compatibility, a notification with text but a null selection does not itself clear the prior selection; reproduce this quirk or deliberately version the protocol before correcting it.

`IDE-072` — Add selection context only on the main conversation thread and only when the current editor is connected, a file path exists, the selection has nonempty text or represents an opened file, and read permission does not deny the path. For selected text, use `lineEnd = lineStart + lineCount - 1`; this adapter does not add one to the zero-based selection lines. For an opened file with no selected text, load applicable nested instruction memory first, then add the opened-file marker. Editor context never bypasses file permission.

`IDE-073` — `at_mentioned` is deliberately different: add one to provided start/end line numbers before creating the normal one-based file attachment. Missing bounds mean a whole-file mention. Ignore a mention from a superseded client.

`IDE-074` — Namespace `log_event` as `tengu_ide_<eventName>` and accept only scalar boolean/number metadata values; an absent metadata value is omitted. Do not forward arbitrary editor objects or source text.

## Command and installation behavior

`IDE-080` — The IDE command lists detected candidates, including workspace-invalid candidates for explicit user choice. Choosing one constructs the dynamic configuration and observes connected, failed, or a 35-second timeout; it must continue observing beyond an initially stale state. Warn that the VS Code-family command-line integration supports only one simultaneous CLI connection.

`IDE-081` — If no extension is found from an external terminal, detect running supported editors and offer installation where supported. VS Code, Cursor, and Windsurf may install or upgrade automatically when absent or older. Native JetBrains installation is not automated; show manual guidance. Linux launches the VS Code-family CLI with graphical-display inheritance removed, and Windows invokes the command wrapper form expected by that platform. Temporary installer artifacts are cleaned best effort.

`IDE-082` — Automatic extension installation defaults on unless global settings disable it or the explicit skip-auto-install environment switch is set. A successful installation sets the global diff tool to `auto` only when no diff tool preference exists; never overwrite an explicit preference.

`IDE-083` — `ide open` selects the current worktree root when present, otherwise the current working directory. If an eligible instance already exists, use it. Otherwise, the specified VS Code-family branch invokes the generic `code <path>` launcher even for Cursor or Windsurf; preserve this observable compatibility behavior unless a future version explicitly changes it. Other editor families receive manual-open instructions.

## Failures, security, and compatibility

`IDE-090` — Discovery, installation, notifications, diagnostics, file opening, and diff-tab cleanup are optional integration failures. An explicit user connection request reports its failure; automatic startup continues without an IDE.

`IDE-091` — Endpoint reachability proves only transport availability. Workspace validation, process ancestry, tool filtering, path permission, and message validation remain separate gates.

`IDE-092` — Keep the [general provider lifecycle](../assets/architecture.drawio) and the [specialized IDE identity/reconnect flow](../assets/ide-adapter.drawio) consistent. The specialized diagram is normative for generation checks and path-boundary ordering that the broad provider topology intentionally omits.

## Acceptance scenarios

1. **IDE-A01 — Workspace choice.** Place two live lockfiles for different workspaces. Automatic discovery in one workspace chooses its sole valid instance; explicit selection shows both.
2. **IDE-A02 — Foreign WSL distribution.** Under WSL, advertise a UNC workspace for another distribution. Verify it remains invalid even if the textual suffix resembles the current path.
3. **IDE-A03 — Host process visibility.** Make a lockfile process invisible to WSL but its Windows endpoint reachable. Cleanup retains it; an unreachable endpoint is removed.
4. **IDE-A04 — Explicit port.** Provide one explicit IDE port among several valid candidates. Verify only the matching candidate is returned.
5. **IDE-A05 — Transport authentication difference.** Connect by WebSocket with a token and inspect the handshake; then connect by SSE and verify the documented absence of that token rather than silently claiming authenticated SSE.
6. **IDE-A06 — Stale generation.** Replace a connected client and deliver a late selection and close callback from the old generation. Neither changes current state.
7. **IDE-A07 — Diff result mapping.** Return each `openDiff` status and verify authoritative content/rejection mapping, including malformed-result failure and best-effort tab close on cancellation.
8. **IDE-A08 — Selection coordinates.** Send a zero-width selection ending at character zero. Verify line-count adjustment and unchanged line-base behavior.
9. **IDE-A09 — Permission containment.** Deny read access to the selected file. Selection and opened-file notifications add no model context.
10. **IDE-A10 — Installation preference.** Install a VS Code-family extension with no diff preference and then with an explicit preference. Only the first case writes `auto`.
11. **IDE-A11 — Notebook projection.** Load a notebook containing text, two adjacent textual outputs, PNG output, one unsupported media type, and more than 10,000 output characters. Whole-notebook projection keeps cells ordered, merges adjacent text blocks, preserves only the supported image, and substitutes the explicit large-output retrieval notice; selecting one valid cell returns its full normalized output, while an unknown ID fails without partial context.

## Non-normative provenance

Evidence was specified from editor detection and path-conversion utilities, IDE command and lifecycle hooks, MCP IDE transports, attachment and diagnostic adapters, and structured-diff handoff callers. Names of source files and implementation libraries are not part of this contract.
