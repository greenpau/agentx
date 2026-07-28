# Contract Map

Use this map to load every owner involved in a cross-cutting feature. The first skill owns the named truth; collaborating skills adapt it.

| Concern | Authoritative owner | Required collaborators |
| --- | --- | --- |
| Entrypoint, trust, settings precedence | `implementation-startup-settings` | auth/network, platform lifecycle, all surfaces |
| Process/session/turn state lifetimes | `implementation-state-context` | transcript recovery, interactive REPL, distributed runtime |
| Prompt and model continuation | `implementation-query-model` | tool protocol, memory compaction, headless SDK |
| Generic tool lifecycle | `implementation-tool-protocol` | permissions/sandbox, plugins/hooks, concrete catalog |
| Concrete tool semantics | `implementation-tool-catalog` | permissions/sandbox, MCP/LSP, multi-agent |
| Permission truth and isolation | `implementation-permissions-sandbox` | tool protocol, startup settings, remote bridge |
| Edited-input approval one-shot selection | `implementation-permissions-sandbox` | tool protocol, headless SDK, remote bridge, multi-agent |
| Durable asynchronous work | `implementation-task-runtime` | multi-agent, remote bridge, headless SDK |
| Terminal byte rendering | `implementation-terminal-engine` | interactive REPL only |
| Interactive input and presentation | `implementation-interactive-repl` | commands/input, terminal engine, state/context |
| Structured/headless protocol | `implementation-headless-sdk` | query/model, transcript recovery, remote bridge |
| Exact SDK permission request/response wire | `implementation-headless-sdk` | permissions/sandbox, remote bridge, multi-agent |
| Optional product experiences | `implementation-optional-experiences` | platform lifecycle, tool catalog, feature/profile gates |
| User command and prompt routing | `implementation-commands-input` | interactive REPL, headless SDK, skills/output |
| User attachment import and typed-message admission | `implementation-commands-input` | headless SDK, query/model, transcript recovery, memory compaction, auth/network, platform lifecycle, observability |
| Runtime skills and output styles | `implementation-skills-output` | commands/input, state/context, plugins/hooks |
| Plugin and hook lifecycle | `implementation-plugins-hooks` | tool protocol, startup settings, MCP/LSP |
| MCP and LSP protocols | `implementation-mcp-lsp` | tool protocol, auth/network, headless SDK |
| Transcript and resume truth | `implementation-transcript-recovery` | state/context, memory compaction, remote bridge |
| Native session inventory, selection, and deletion | `implementation-transcript-recovery` | headless SDK, platform lifecycle, startup settings |
| Shared append-only JSONL primitives | `implementation-platform-lifecycle` | transcript recovery, state/context, task runtime |
| Context pressure and memory | `implementation-memory-compaction` | query/model, transcript recovery, plugins/hooks |
| Remote placement and bridge | `implementation-remote-bridge` | headless SDK, auth/network, permissions/sandbox |
| Exact CCR worker/client wire | `implementation-remote-bridge` | headless SDK, auth/network, state/context |
| Agents, teams, worktrees, mailboxes | `implementation-multi-agent` | task runtime, tool catalog, transcript recovery |
| Credentials, providers, proxies, TLS | `implementation-auth-network` | startup settings, query/model, MCP/LSP |
| OS/process/filesystem lifecycle | `implementation-platform-lifecycle` | terminal engine, auth/network, all executable capabilities |
| Installation and update policy/mechanics | `implementation-platform-lifecycle` | startup settings, interactive REPL, auth/network, observability |
| Metrics, usage, and diagnostics | `implementation-observability` | every event producer; never semantic authority |

## Cross-cutting change rule

For a change touching more than one row:

1. Implement the authoritative owner's contract first.
2. Add adapters without copying authority into collaborators.
3. Use the same correlation identifiers across all projections.
4. Test the authoritative state with every adapter disconnected.
5. Test each adapter with replay, cancellation, malformed data, and partial failure.
6. Add a conformance trace whose event sequence names the applicable contract IDs.
