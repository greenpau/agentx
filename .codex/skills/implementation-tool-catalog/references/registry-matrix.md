# Observable tool registry matrix

## Reading the matrix

Classification columns are invocation properties, not blanket permission grants: **C** concurrency-safe, **R** read-only, **D** potentially destructive, **O** open-world, and **U** requires synchronous user interaction. `Y/N` is fixed; `input` means compute from validated input; `default` means the conservative descriptor default. A declared result cap is still clamped by the global rules unless it is `Infinity`.

## Core and workstation tools

| ID | Canonical name (aliases) | Purpose and principal input | C/R/D/O/U | Availability | Declared cap |
| --- | --- | --- | --- | --- | --- |
| TM-001 | `Read` | Read a bounded absolute file path, including text, images, PDFs, and notebooks where supported; optional offset/limit. | Y/Y/N/N/N | Base; hidden by REPL | `Infinity` |
| TM-002 | `Glob` | Match a glob pattern below an optional path; return newest-first paths with a result-count ceiling. | Y/Y/N/N/N | Base unless embedded search; hidden by REPL | global |
| TM-003 | `Grep` | Regex search files with mode, path/glob/type, context, offset, and head limit. | Y/Y/N/N/N | Base unless embedded search; hidden by REPL | 20,000 |
| TM-004 | `Bash` | Execute shell text with optional timeout, description, and backgrounding. | input/input/N/N/N | Base; simple-mode primitive; hidden by REPL | 30,000 |
| TM-005 | `PowerShell` | Execute PowerShell with the shell contract analogous to `Bash`. | input/input/N/N/N | Platform/shell gate | 30,000 |
| TM-006 | `Edit` | Replace one unique text occurrence or all occurrences in a previously read file. | N/N/N/N/N | Base; simple-mode primitive; hidden by REPL | global |
| TM-007 | `Write` | Create or replace a file after path, state, and policy validation. | N/N/N/N/N | Base; hidden by REPL | global |
| TM-008 | `NotebookEdit` | Replace, insert, or delete a code/markdown notebook cell. | N/N/N/N/N | Base; hidden by REPL | global |
| TM-009 | `LSP` | Perform definition/reference/hover/symbol/implementation/call-hierarchy queries. | Y/Y/N/N/N | `ENABLE_LSP_TOOL` plus connected server | global |
| TM-010 | `REPL` | Execute internal VM primitives while direct workstation primitives are hidden. | implementation-defined | Internal profile plus REPL mode | implementation-defined |

## Web, protocol, and discovery tools

| ID | Canonical name (aliases) | Purpose and principal input | C/R/D/O/U | Availability | Declared cap |
| --- | --- | --- | --- | --- | --- |
| TM-011 | `WebFetch` | Fetch an HTTP(S) URL and transform public content according to a prompt. | Y/Y/N/N/N | Base; policy/network dependent | 100,000 |
| TM-012 | `WebSearch` | Search the web with a query and mutually exclusive allow/block domain filters. | Y/Y/N/N/N | Provider/runtime dependent; deferred | 100,000 |
| TM-013 | `ToolSearch` | Select deferred tools by keyword query or exact `select:` list. | Y/Y/N/N/N | Optimistic tool-search gate | global |
| TM-014 | `mcp__<server>__<tool>` | Invoke a discovered MCP schema; preserve raw server/tool identity separately. | annotation/annotation/annotation/annotation/N | Connected MCP server, policy, name dedupe | 100,000 |
| TM-015 | `mcp__<server>__authenticate` | Start or report server OAuth authentication. | N/N/N/Y/Y-or-external | Server supports authentication | 10,000 |
| TM-016 | `ListMcpResourcesTool` | List resources, optionally for one server, isolating per-server failures. | Y/Y/N/Y/N | Special conditional injection | global |
| TM-017 | `ReadMcpResourceTool` | Read one `server`/`uri` resource; persist binary content. | Y/Y/N/Y/N | Special conditional injection | global |
| TM-018 | `WebBrowser` | Feature-specific browser/computer interaction. | descriptor-defined | `WEB_BROWSER_TOOL` build gate | descriptor-defined |

## Conversation, planning, and extension tools

| ID | Canonical name (aliases) | Purpose and principal input | C/R/D/O/U | Availability | Declared cap |
| --- | --- | --- | --- | --- | --- |
| TM-019 | `AskUserQuestion` | Ask 1–4 structured questions with choices and optional multi-select. | Y/Y/N/N/Y | Base; disabled when another channel owns interaction | 100,000 |
| TM-020 | `Skill` | Invoke a discovered prompt skill inline or in a forked context. | N/N/N/N/N | Base; discovered skill and rules required | 100,000 |
| TM-021 | `EnterPlanMode` | Change the main session to plan-only behavior. | Y/Y/N/N/N | Main session and eligible mode only | 100,000 |
| TM-022 | `ExitPlanMode` | Submit the current plan for approval or teammate handoff; optional allowed prompts. | Y/N/N/N/Y | Plan mode, main session; teammate path avoids prompt | 100,000 |
| TM-023 | `StructuredOutput` | Validate and emit exactly one final value against caller JSON schema. | Y/Y/N/N/N | Noninteractive schema injection only | global |
| TM-024 | `Config` | Read or update one supported primitive setting. | Y/input/N/N/N | Internal profile | 100,000 |
| TM-025 | `SendUserMessage` (`Brief`) | Emit user-visible markdown with optional attachments and normal/proactive status. | Y/Y/N/N/N | Entitlement plus opt-in; assistant mode can imply opt-in | 100,000 |
| TM-026 | `SendUserFile` | Feature-specific delivery of a file to the user. | descriptor-defined | `KAIROS` build/profile gate | descriptor-defined |
| TM-027 | `PushNotification` | Feature-specific user notification. | descriptor-defined | `KAIROS` or push-notification gate | descriptor-defined |
| TM-028 | `SubscribePR` | Feature-specific pull-request event subscription. | descriptor-defined | GitHub-webhook gate | descriptor-defined |
| TM-029 | `Workflow` | Execute a registered workflow without recursive workflow access in workers. | descriptor-defined | `WORKFLOW_SCRIPTS` | descriptor-defined |
| TM-030 | `Snip` | Feature-specific transcript-history snipping. | descriptor-defined | `HISTORY_SNIP` | descriptor-defined |

## Agent, task, team, and scheduling tools

| ID | Canonical name (aliases) | Purpose and principal input | C/R/D/O/U | Availability | Declared cap |
| --- | --- | --- | --- | --- | --- |
| TM-031 | `Agent` (`Task`) | Launch/resume a subagent with prompt, type, model, background/team/isolation options. | Y/Y/N/N/N | Base; profile filtered; hidden by REPL | 100,000 |
| TM-032 | `TaskOutput` (`AgentOutputTool`, `BashOutputTool`) | Poll a background task with timeout and optional blocking. | Y/Y/N/N/N | Compatibility/deprecated; profile filtered | 100,000 |
| TM-033 | `TaskStop` (`KillShell`) | Cancel a running stoppable task; accepts compatibility `shell_id`. | Y/N/N/N/N | Base; main/coordinator state | 100,000 |
| TM-034 | `TodoWrite` | Replace the legacy visible todo list; empty storage after all complete. | N/N/N/N/N | Task-v2 disabled | 100,000 |
| TM-035 | `TaskCreate` | Create a durable pending task with subject, description, active form, metadata. | Y/N/N/N/N | Task-v2 enabled | 100,000 |
| TM-036 | `TaskGet` | Read one registered task and relationship state. | Y/Y/N/N/N | Task-v2 enabled | 100,000 |
| TM-037 | `TaskUpdate` | Update fields, ownership, relationships, metadata, status, or delete. | Y/N/Y/N/N | Task-v2 enabled | 100,000 |
| TM-038 | `TaskList` | List registered task summaries. | Y/Y/N/N/N | Task-v2 enabled | 100,000 |
| TM-039 | `SendMessage` | Send plain or structured team/mailbox messages to a resolved recipient. | Y/input/N/input/N | Registry base; useful only with coordination channel | 100,000 |
| TM-040 | `TeamCreate` | Create one named team for the current leader. | N/N/N/N/N | Swarm gate | 100,000 |
| TM-041 | `TeamDelete` | Delete a team only after active members stop. | N/N/Y/N/N | Swarm gate | 100,000 |
| TM-042 | `ListPeers` | Feature-specific peer discovery. | descriptor-defined | `UDS_INBOX` | descriptor-defined |
| TM-043 | `Sleep` | Interruptible wait used by persistent/assistant loops; queued input wakes it. | Y/Y/N/N/N; interrupt=`cancel` | `PROACTIVE` or `KAIROS` | 100,000 |
| TM-044 | `CronCreate` | Create local-time 5-field recurring or one-shot scheduled work. | N/N/N/N/N | Trigger gates and kill switch | 100,000 |
| TM-045 | `CronDelete` | Delete a caller-owned scheduled job. | N/N/Y/N/N | Trigger gates and kill switch | 100,000 |
| TM-046 | `CronList` | List caller-visible scheduled jobs. | Y/Y/N/N/N | Trigger gates and kill switch | 100,000 |
| TM-047 | `RemoteTrigger` | List/get/create/update/run authenticated remote triggers. | Y/input/N/Y/N | Remote-trigger gate, account, managed policy | 100,000 |
| TM-048 | `Monitor` | Feature-specific monitor lifecycle. | descriptor-defined | `MONITOR_TOOL` | descriptor-defined |

## Worktree, diagnostic, internal, and test tools

| ID | Canonical name | Purpose and principal input | C/R/D/O/U | Availability | Declared cap |
| --- | --- | --- | --- | --- | --- | --- |
| TM-049 | `EnterWorktree` | Create/select a session-owned isolated worktree and switch session paths. | N/N/N/N/N | Worktree mode | 100,000 |
| TM-050 | `ExitWorktree` | Keep or remove the current session-owned worktree; force protects dirty removal. | N/N/input/N/N | Worktree mode and active owned worktree | 100,000 |
| TM-051 | `Tungsten` | Internal virtual-terminal capability; singleton prevents async-agent use. | descriptor-defined | Internal profile | descriptor-defined |
| TM-052 | `SuggestBackgroundPR` | Internal background pull-request suggestion. | descriptor-defined | Internal profile | descriptor-defined |
| TM-053 | `VerifyPlanExecution` | Development-only plan verification. | descriptor-defined | `AGENTX_VERIFY_PLAN=true` | descriptor-defined |
| TM-054 | `OverflowTest` | Exercise result overflow behavior. | descriptor-defined | `OVERFLOW_TEST_TOOL` | descriptor-defined |
| TM-055 | `CtxInspect` | Inspect context-collapse state. | descriptor-defined | `CONTEXT_COLLAPSE` | descriptor-defined |
| TM-056 | `TerminalCapture` | Capture feature-specific terminal-panel state. | descriptor-defined | `TERMINAL_PANEL` | descriptor-defined |
| TM-057 | `TestingPermission` | Empty input; always asks, then returns a success string. | Y/Y/N/N/Y | Test environment only | 100,000 |
| TM-058 | `ToolResultRead` | Read one integrity-verified bounded byte range from an oversized result by its exact accepted tool-use ID. | Y/Y/N/N/N | Runtime-owned result store | 100,000 |

Rows marked `descriptor-defined` are compatibility boundaries evidenced by registry inclusion but not by a complete portable behavioral contract. An implementation may omit them when the corresponding build gate is absent. If it ships the gate, it must supply an explicit descriptor and acceptance suite rather than guessing from the name.

## Registry coverage rule

Assign every executable descriptor exactly one matrix ID. New aliases update the existing row. New canonical capabilities require a new stable row, profile analysis, family contract, disabled behavior, limit policy, and acceptance scenario before release.
