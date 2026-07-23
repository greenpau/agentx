# Descriptor and registry contracts

## Descriptor schema

Represent each tool with a descriptor independent of its renderer and implementation language. The fields below are the minimum implementation contract.

| ID | Field | Contract |
| --- | --- | --- |
| TD-001 | `name` | Stable canonical, case-sensitive model/API name. |
| TD-002 | `aliases` | Optional accepted lookup names. Never create duplicate registry entries. |
| TD-003 | input schema | Strict externally visible schema plus semantic validation after structural validation. Preserve compatibility fields explicitly. |
| TD-004 | output schema | Typed internal result where available, then a deterministic model-facing result-block mapping. |
| TD-005 | prompt and description | Model-visible purpose and usage contract; compute lazily when it depends on profile state. |
| TD-006 | enablement | Current availability predicate, evaluated after bootstrap and whenever the registry is rebuilt. |
| TD-007 | classifiers | Functions of validated input: concurrency-safe, read-only, destructive, open-world, search/read collapse class, and auto-classifier text. |
| TD-008 | interruption | `block` by default; `cancel` only when the operation explicitly supports interruption and a terminal cancellation result. |
| TD-009 | permission hooks | Path extraction, rule matching, tool-specific precheck, and optional amended input. The shared boundary remains authoritative. |
| TD-010 | execution | Cancellable call returning data, optional deliberate new messages, optional context modifier for serialized tools, and optional extension metadata. |
| TD-011 | equivalence | Optional observable-input backfill and equivalence comparison used for replay, permission updates, and UI continuity. |
| TD-012 | presentation | User-facing name, use/progress/queued/rejected/result/error projections. Presentation never changes semantic data. |
| TD-013 | discovery | Optional search hint, deferral hint, always-load marker, MCP/LSP identity, and strict-schema marker. |
| TD-014 | result cap | Optional declared character cap; absence uses the global cap, `Infinity` is an explicit hard opt-out. |

Builder defaults are normative: enabled; sequential; write-capable; non-destructive; closed-world; no required interaction; interruption blocks; tool-specific permission precheck returns allow with unchanged input. These defaults are conservative except the precheck, which delegates to—not replaces—the general permission decision.

## Registry assembly

Apply these stages in order:

1. **TREG-001 Build inclusion.** Instantiate only capabilities present in the build. Treat missing optional modules as supported absence.
2. **TREG-002 Base order.** Maintain the declared base enumeration because its membership affects stable system-prompt caching. Do not treat source-file order as the implementation package layout.
3. **TREG-003 Mode shaping.** In simple mode expose `Bash`, `Read`, and `Edit`. In simple plus REPL mode expose `REPL` instead of those primitives. Coordinator mode additionally needs `Agent`, `TaskStop`, and `SendMessage` before its stricter allowlist is applied.
4. **TREG-004 Special injection.** Keep `ListMcpResourcesTool`, `ReadMcpResourceTool`, and `StructuredOutput` out of the ordinary direct pool; inject them only when their protocol/session conditions require them.
5. **TREG-005 Blanket denial.** Remove a tool before model exposure when a scope-free deny rule matches its canonical name. A deny for an MCP server prefix removes every tool from that server.
6. **TREG-006 REPL hiding.** When `REPL` is active, hide direct `Read`, `Write`, `Edit`, `Glob`, `Grep`, `Bash`, `NotebookEdit`, and `Agent`; the VM adapter may expose equivalent primitives internally.
7. **TREG-007 Live enablement.** Evaluate `isEnabled` after state initialization. Re-evaluate registries when login, connection, feature, policy, or mode state changes.
8. **TREG-008 External merge.** Filter MCP tools by the same deny rules. Sort built-ins by canonical name, sort permitted MCP tools by canonical name, concatenate the built-in partition first, then deduplicate by canonical name. Thus a built-in wins a collision and the built-in cache prefix stays contiguous.

`getMergedTools`-equivalent counting views may concatenate without deduplication only for explicitly documented token/search calculations. They must never become the executable registry.

## Presets and lookup

- The only predefined preset is `default`; parse the preset case-insensitively and reject every unknown value.
- Expanding `default` returns currently enabled base canonical names, not aliases and not an aspirational superset.
- Canonical lookup precedes alias lookup. Preserve the canonical descriptor in transcript and telemetry records after an alias match.
- A deferred tool remains known but may be represented by `ToolSearch` until selected. `alwaysLoad` is an explicit exception.

## Execution profiles

| Profile | Contract |
| --- | --- |
| Main session | Use the complete filtered pool for the current mode. |
| Custom/async agent | Allow `Read`, `WebSearch`, `TodoWrite`, `Grep`, `WebFetch`, `Glob`, shell tools, `Edit`, `Write`, `NotebookEdit`, `Skill`, `StructuredOutput`, `ToolSearch`, `EnterWorktree`, and `ExitWorktree`; remove `TaskOutput`, plan-mode tools, `AskUserQuestion`, `TaskStop`, recursive `Agent` except the privileged internal profile, and workflow recursion. |
| In-process teammate | In addition to its worker pool, allow `TaskCreate`, `TaskGet`, `TaskList`, `TaskUpdate`, `SendMessage`, and trigger-gated cron tools. |
| Coordinator | Restrict to `Agent`, `TaskStop`, `SendMessage`, and `StructuredOutput`. |
| REPL | Expose `REPL`; hide the direct primitive list in `TREG-006`. |
| Headless structured | Inject `StructuredOutput` only when a caller supplied a response schema. |

Never infer authority from a profile allowlist: the permission pipeline still evaluates each invocation.

## Feature and platform gates

Keep these dimensions independent:

- Internal user profile: `Config`, `Tungsten`, `SuggestBackgroundPR`, and `REPL` require `USER_TYPE=ant` in the specified profile.
- `TodoWrite` and the `TaskCreate/Get/Update/List` family are mutually exclusive under the task-v2 gate.
- Embedded native search suppresses dedicated `Glob` and `Grep`.
- `ENABLE_LSP_TOOL` opts into `LSP`; connection state is a second availability check.
- Worktree tools require worktree mode; team tools require swarm mode.
- `PROACTIVE` or `KAIROS` includes `Sleep`; `AGENT_TRIGGERS` includes cron; `AGENT_TRIGGERS_REMOTE` includes `RemoteTrigger`.
- `MONITOR_TOOL`, `KAIROS`, `KAIROS_PUSH_NOTIFICATION`, `KAIROS_GITHUB_WEBHOOKS`, `OVERFLOW_TEST_TOOL`, `CONTEXT_COLLAPSE`, `TERMINAL_PANEL`, `WEB_BROWSER_TOOL`, `UDS_INBOX`, `WORKFLOW_SCRIPTS`, `HISTORY_SNIP`, and `COORDINATOR_MODE` independently include their named families.
- `AGENTX_VERIFY_PLAN=true` includes `VerifyPlanExecution`; `NODE_ENV=test` includes `TestingPermission`.
- `PowerShell` requires a supported platform/shell policy. If required sandbox support is unavailable, fail closed rather than silently running unsandboxed.
- Tool search uses an optimistic exposure check; the actual deferral decision is request-time and may retain always-load tools.

## Non-normative provenance

The specified evidence used descriptor definitions, the base-tool registry, tool-name constants, and tool-result storage utilities. Those paths and symbol names are discovery aids only; an implementation must implement the contracts above without reproducing that module structure.
