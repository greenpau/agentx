---
name: implementation-commands-input
description: Implement user-invoked command discovery and execution together with prompt normalization, slash parsing, skill argument expansion, attachments, pasted content, hooks, and queued input priorities. Use when porting slash commands, input submission, command palettes, plugin or skill commands, remote-safe routing, or mid-turn user messages.
---

# Implementation Commands and Input

## Purpose

Implement boundary that turns terminal, headless, SDK, bridge, channel, and scheduled input into either local control, UI flow, prompt expansion, shell input, or normalized model-visible messages. Keep command routing distinct from model-callable tools: commands originate with a user or external event and may *expand into* a prompt, whereas tools cross the capability boundary at the model's request.

Use [the architecture diagram](assets/architecture.drawio) to review discovery, parsing, expansion, queueing, and transcript projection. Use [the command-effect workflow diagram](assets/command-workflow-effects.drawio) when implementing a command that owns a dialog, performs multiple mutations, hands authority to another runtime, or can finish partially applied. Use [the specialized workflow diagram](assets/command-state-machines.drawio) for plugin bootstrap, detached remote work, metered delegation, immediate coupled state, fault injection, and transcript metadata.

## Implementation workflow

1. Implement [the command descriptor and result contracts](references/command-contract.md).
2. Assemble and filter registries using [discovery, precedence, visibility, and surface rules](references/discovery-filtering.md).
3. Route leading-slash input and substitute arguments using [slash parsing and prompt expansion](references/slash-expansion.md).
4. Normalize ordinary input, references, images, paste placeholders, hooks, and priorities using [input, attachment, and queue contracts](references/input-attachments-queue.md).
5. Reconcile actual built-in/internal registry symbols through [the independently maintained source registry manifest](references/source-registry-manifest.md), then match observable built-ins and gated surfaces against [the command catalog](references/command-catalog.md).
6. Classify every catalog row and follow its stable workflow route in [the command workflow index](references/command-workflow-index.md). Treat its `CC-155` reconciliation rule as a required maintenance test.
7. For complex commands, implement the applicable state-machine contract:
   - Use [insights workflow contracts](references/workflow-insights.md) for local history collection, cached analysis, report generation, and optional internal upload.
   - Use [integration and extension workflows](references/workflow-integrations.md) for GitHub App installation, plugins and marketplaces, MCP control, and integration handoffs.
   - Use [session, account, and artifact workflows](references/workflow-session-account.md) for clear, compact, branch, rewind, resume, export, feedback, login, and logout.
   - Use [settings, diagnostics, and capability workflows](references/workflow-settings-diagnostics.md) for model/effort/fast selection, settings, usage/stats, agents, diagnostics, permissions, sandbox, hooks, memory, and working directories.
   - Use [specialized workflows](references/workflow-specialized.md) for thinkback installation/playback, ultraplan, rate-limit recovery, bridge fault injection, brief mode, ultrareview, and transcript tags.
8. Run [command-workflow acceptance scenarios](references/workflow-acceptance.md), then run [the shared failure, recovery, and input scenarios](references/acceptance.md) across interactive, headless, remote, and resumed sessions.

## Core invariants

- **CMDI-001 — Route before model context.** Decide local command, UI command, prompt command, shell mode, or ordinary prompt before creating API-bound messages. UI-only routing metadata never enters model context accidentally.
- **CMDI-002 — First-match precedence.** Preserve source ordering. Canonical name, displayed name, and aliases resolve to the first available descriptor; do not globally deduplicate sources because autocomplete must retain source attribution.
- **CMDI-003 — Live availability.** Cache expensive discovery by working directory, but re-evaluate auth/provider availability and `isEnabled` on every registry request.
- **CMDI-004 — Exact typed behavior.** Hidden commands remain invocable by exact name when enabled. Unknown valid command names fail locally; slash-prefixed file paths or invalid command syntax remain ordinary prompts.
- **CMDI-005 — Raw versus represented input.** Retain raw arguments for execution, a redacted form for sensitive transcript display, pre-expansion input for history, and normalized content blocks for the model as distinct values.
- **CMDI-006 — Attachment identity.** Preserve attachment provenance and paste/image identity through resize, storage, history, queueing, prompt expansion, and resume. A placeholder is never the authoritative content.
- **CMDI-007 — Queue ownership.** Each queued item retains UUID, priority, mode, origin, permission mode, agent identity, paste state, and slash-routing flags. Consume it at most once from one live process queue generation at the appropriate stop point. Queue state and its consumption claim do not survive restart.
- **CMDI-008 — Hook containment.** Input hooks may block, stop continuation, or add bounded context, but cannot silently lose the original user-visible event or bypass permission/policy.
- **CMDI-009 — Surface safety.** Remote, bridge, headless, and interactive surfaces share command semantics but filter UI and workstation-dependent operations explicitly.
- **CMDI-010 — Recovery.** Durable command effects, transcript-visible results, and deliberate model messages expose partial application and reconcile after dismissal, error, cancellation, compaction, or restart. Process-local queue state is an explicit exception: a crash may lose an enqueued-but-unconsumed item, and resume never treats queue diagnostics as a durable outbox.
- **CMDI-011 — Explicit effect boundary.** A complex command validates and snapshots before its first effect, identifies the authority that owns each effect, and reports whether failure occurred before mutation, after partial mutation, or after a committed handoff. Never describe a non-transactional sequence as rolled back.
- **CMDI-012 — One workflow owner.** A catalog row classified as `workflow` resolves to exactly one primary `CMD-WF-*` contract. Supporting workflows may be linked from that contract, but command dispatch, completion, and cancellation have a single owner.

## Boundary with adjacent skills

- Use the interactive-REPL skill for prompt-editor key handling, dialogs, and presentation state.
- Use the headless/SDK skill for stdin acquisition, structured control requests, and output projection.
- Use the query-model skill after input has become normalized model-visible messages.
- Use the extension-plane and skills-output skills for manifest/frontmatter discovery and skill content contracts.
- Use the task-runtime skill for queued task notifications and background completion events.

## Definition of done

The implementation is complete when every catalog entry has a type and supported-absence decision, all discovery sources retain precedence and attribution, slash and argument fixtures are exact, durable paste/attachment records survive history and resume, live queue priorities behave at model/tool boundaries with the documented restart-loss window, remote filters fail safely, and every acceptance scenario passes. In addition, `scripts/audit_command_workflows.rb` must compare the actual built-in/internal registry descriptor symbols with the independent manifest, derive and validate the contiguous catalog identity range, require `/tag`, prove every catalog identity appears exactly once in the workflow index, ensure every `workflow` row names one defined `CMD-WF-*` contract, ensure non-workflow rows name none, and retain the `CC-155` maintenance contract.
