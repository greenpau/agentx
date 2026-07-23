# Repository Instructions

This repository develops AgentX as a standalone Go application. Repo-local skills are the project’s progressive-disclosure engineering handbook: they route contributors to the behavioral contracts, architecture, implementation guidance, and verification procedures relevant to a change.

## Project Summary

AgentX is a terminal-first agentic software-engineering client. It is not an implementation of the language model itself. It is the orchestration runtime between a user, a model API, the user's workstation, optional extension providers, and local or remote execution environments. Its job is to turn a request into a resumable conversation in which the model can inspect context, propose and invoke capabilities, receive results, continue reasoning, and report a final outcome without bypassing the user's safety controls.

The Go source, tests, and routed repo-local skills together define the product. Preserve protocols, state transitions, ordering, concurrency rules, permission decisions, persistence semantics, recovery behavior, and user-visible interactions. Keep durable engineering guidance in `AGENTS.md` or a reachable repo-local skill instead of a separate documentation tree.

### Product responsibilities

- Support several product surfaces over shared contracts. The interactive terminal UI, one-shot/headless execution, structured SDK streaming, and feature-gated remote or bridge connections reuse the semantic session engine; the MCP-server entrypoint reuses the capability contracts as a standalone tool host. A surface may adapt input, output, and permission prompts, but must not invent a separate meaning for shared operations.
- Build model context from the conversation, project and user instructions, working-directory and repository facts, selected model and mode, tools, agents, skills, plugins, MCP resources, attachments, memory, and applicable policy. Keep stable prompt material cacheable while allowing explicitly dynamic session material to change.
- Run the agent loop: normalize user input, request a streamed model response, publish incremental progress, detect tool requests, execute approved work, append tool results, and continue until the model stops or the turn reaches a terminal condition.
- Make model-requested side effects cross a capability boundary. Validate each request, apply policy and permission rules, run lifecycle hooks, obtain interactive approval when required, select sandboxing or another isolation mechanism, execute, normalize the result, and record enough information for display and recovery. Unknown, malformed, or unsafe requests fail closed.
- Manage work that outlives a single model call. Local processes, subagents, remote agents, teammates, scheduled work, monitors, and memory consolidation use explicit identities, lifecycle states, cancellation, durable output, and completion notification rather than becoming anonymous background activity.
- Preserve resumable session state. Record the conversation and significant metadata incrementally, restore coherent message chains after interruption, retain file-history and attribution information needed for rewind or review, support continuation and branching, and compact or project history when context limits require it.
- Provide an extension plane. Built-in and external commands, model-callable tools, agents, skills, hooks, output styles, plugins, and MCP servers have distinct discovery and invocation contracts and are combined into session-scoped registries before use.
- Integrate with the surrounding development environment: files, shells, source control, worktrees, language servers, notebooks, web services, IDEs, browsers, authentication providers, enterprise policy, telemetry, and update mechanisms. Each integration must degrade or fail without corrupting the conversation.
- Render a responsive terminal experience with streaming messages, rich tool status, diffs, prompts, dialogs, search, history, tasks, teammate views, notifications, keyboard customization, and optional modal editing, while keeping presentation state out of the model-visible transcript unless deliberately translated into a message.

### Canonical session lifecycle

The implementation may divide these stages differently, but preserve their dependency order and externally visible effects:

1. Determine the entrypoint and interaction mode early enough that all later initialization, logging, and error output use the correct contract.
2. Load and validate configuration according to scope and precedence; run compatible migrations; establish platform, network, authentication, trust, policy, and initial permission state. Reject unusable configuration clearly and avoid partially initialized execution.
3. Discover and filter commands, tools, agents, skills, plugins, hooks, output styles, MCP connections, and remote capabilities according to build features, runtime gates, platform, identity, policy, and user choices.
4. Create or restore the session's bootstrap state, application state, transcript, task registry, file history, attribution state, and model context. Recovery must reconcile interrupted or orphaned tool calls before a new turn proceeds.
5. Accept user input or an equivalent external event; expand supported references and attachments; distinguish local commands from model-bound prompts; run input hooks; queue, reject, redact, or persist the input as its contract requires.
6. Compose the effective system and user context, send the request, and expose streaming text, thinking, usage, retry, status, and tool-use events through the active presentation adapter.
7. For each tool-use block, resolve the tool by canonical name or supported alias, validate its schema and semantic preconditions, evaluate permissions and hooks, then schedule it. Concurrency-safe tools may overlap only when ordering and shared-state safety are preserved; unsafe tools serialize. Cancellation and sibling failure must produce explicit terminal results for every accepted tool-use identifier.
8. Append normalized tool results and any deliberate context changes, then continue the model loop. Stop on successful completion, user cancellation, non-recoverable error, configured turn or cost limit, token-budget exhaustion, or an equivalent terminal policy decision. Retry and compaction paths must remain bounded.
9. Fan the same semantic session events out to the terminal, structured output, SDK, bridge, or remote transport; flush durable state; notify about background completion when appropriate; and release processes, connections, locks, terminal modes, and other registered resources during shutdown.

### Implementation invariants

- Keep the conversation engine independent of its presentation adapters. Interactive, headless, SDK, bridge, and remote modes should share message normalization, model invocation, tool orchestration, limits, and persistence wherever their contracts overlap. The standalone MCP tool host should reuse tool validation and result contracts without needing to emulate a conversational turn.
- Keep process-wide bootstrap facts, session/application state, durable transcript state, and background-task state conceptually separate. They have different lifetimes and recovery rules even when the implementation stores convenient cross-links between them.
- Treat the transcript as an event history, not a screen buffer. UI-only messages and ephemeral progress may be displayed or logged separately but must not silently enter the model context. Conversely, any model-visible hidden metadata must be deliberately represented and recoverable.
- Preserve the distinction between a command, a tool, and a task: a command is user-invoked routing or local control, a tool is a model-callable capability with validation and result mapping, and a task is durable asynchronous execution with lifecycle management. One feature may use all three without merging their contracts.
- Treat permissions as a composed decision, not a boolean. Mode, scoped allow/ask/deny rules, managed policy, tool-specific checks, hooks, path and command analysis, sandbox availability, and user choice may all participate. Denial, cancellation, and approval-with-updated-input are first-class outcomes.
- Treat compile-time inclusion, runtime feature gates, account eligibility, platform support, managed policy, and current availability as separate dimensions. Document both enabled and disabled behavior; source presence alone does not make a capability available to every user.
- Prefer bounded work, explicit timeouts, cancellation propagation, backpressure, retry ceilings, atomic or append-safe persistence, and idempotent cleanup. A crash, disconnect, or partial stream must leave enough evidence to resume safely or explain why recovery is impossible.

## Project Structure

The repository is organized by runtime ownership:

| Area | Responsibility |
| --- | --- |
| `main.go`, `pkg/cli/`, `pkg/app/` | Parse the command line, select a surface, assemble runtime services, execute turns, and coordinate shutdown. |
| `pkg/config/`, `pkg/prompt/`, `pkg/model/`, `pkg/engine/` | Load trusted configuration, assemble context, integrate with the model provider, and run the shared conversation loop. |
| `pkg/tool/`, `pkg/permission/`, `pkg/sandbox/`, `pkg/command/` | Define commands and capabilities; validate, authorize, isolate, schedule, execute, and normalize effects. |
| `pkg/transcript/`, `pkg/sessionlock/`, `pkg/task/`, `pkg/memory/`, `pkg/compact/` | Persist and recover sessions, coordinate durable work, retain bounded memory, and manage context pressure. |
| `pkg/extensions/`, `pkg/mcp/` | Discover trusted extensions and expose or consume MCP capabilities without bypassing core policy. |
| `pkg/protocol/`, `pkg/surface/` | Project shared session events into terminal, aggregate, structured, SDK, or MCP-facing forms. |
| `pkg/platform/`, `pkg/signals/`, `pkg/observability/`, `pkg/childenv/` | Own portable lifecycle, process signals, diagnostics, usage, and child-process environment boundaries. |
| `.codex/skills/` | Store the engineering handbook and behavioral contracts. All discoverable skills are sibling directories connected by actionable `Use ... to ...` routes. |
| `README.md`, `USER_GUIDE.md`, `pkg/README.md` | Provide user-facing and package-consumer entrypoints; detailed engineering instructions belong in repo-local skills. |
| `tmp/` | Hold disposable local work artifacts; it is not part of the runtime or engineering contract. |

### Dependency and ownership rules

Use this conceptual flow when locating behavior or decomposing it into skills:

```text
entrypoint or external event
  -> initialization, identity, trust, settings, and policy
  -> session state plus extension and capability registries
  -> input normalization and context assembly
  -> shared query engine and model stream
  -> tool validation, permission, hook, sandbox, and execution boundary
  -> normalized messages, task state, usage, and durable transcript
  -> terminal, structured, SDK, bridge, or remote presentation
```

- Dependencies should point inward toward shared contracts and the query/capability core. Presentation and transport layers adapt core events; the core must not require a particular terminal renderer or wire transport.
- Put authoritative rules with the domain that enforces them. For example, filesystem authorization belongs to the permission/tool boundary, while an approval dialog only renders and returns a decision.
- Keep registries compositional. Built-ins, user configuration, plugins, skills, agents, and MCP providers may contribute entries, but merging must handle naming, aliases, source attribution, precedence, disablement, and policy filtering deterministically.
- Preserve failure boundaries. Configuration, authentication, model transport, a single tool, an extension, telemetry, background work, and remote synchronization fail at different scopes; do not turn every failure into either a process crash or a swallowed warning.
- When authoring future implementation skills, route by these behavioral domains rather than by file count. A broad skill should define shared vocabulary and invariants, then route to narrow skills for individual protocols, lifecycles, or capability families.

## Repo-local skills

- Use [implementation-architecture](.codex/skills/implementation-architecture/SKILL.md) to develop, review, or audit AgentX across runtime domains and routed implementation subskills.
- Use [skill-authoring](.codex/skills/skill-authoring/SKILL.md) to create, revise, route, or audit repo-local skills.
- Use [source-code-management](.codex/skills/source-code-management/SKILL.md) to create or validate repository-compliant commit messages and commit-message files.

## Skill routing rules

- Use `skill-authoring` for every repo-local skill creation, revision, move, or hierarchy audit.
- Keep discoverable skills under `.codex/skills/<skill-name>/`.
- Express hierarchy through actionable routing statements: `Use [skill](path) to perform a specific task.`
- Route from this file to broad skills and from broad skills to narrower skills as needed.
- Do not add structural ancestry metadata or routing-only backlinks to skills.
- Ensure every repo-local skill is reachable by following `Use ... to ...` statements from this file.
- Keep durable implementation guidance in the owning skill and update it alongside the source and tests it governs.
