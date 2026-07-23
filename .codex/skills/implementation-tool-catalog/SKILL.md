---
name: implementation-tool-catalog
description: Implement the complete model-callable capability catalog, its deterministic registry assembly, per-tool semantic contracts, feature and profile gates, and output limits. Use when adding, porting, filtering, classifying, or testing a built-in, MCP, shell, file, web, task, coordination, planning, worktree, or optional tool.
---

# Implementation Tool Catalog

## Purpose

Implement observable capability surface without copying an implementation-language module layout. Treat a tool as a typed, policy-aware capability descriptor whose availability and classification can depend on its input and session profile. Pair this catalog with the common execution protocol; this skill specifies *what* tools exist and mean, while the protocol specifies *how* every accepted call is validated, authorized, scheduled, recorded, and completed.

Use [the architecture diagram](assets/architecture.drawio) to review registry assembly, profile filtering, execution delegation, and normalized results. Use the [native search and diff diagram](assets/native-search-diff.drawio) to trace foreground file queries against progressive index publication and the colored-diff availability, fallback, cache, and fullscreen-gutter branches.

## Implementation workflow

1. Load [descriptor and registry contracts](references/descriptor-registry.md) before designing a tool interface or registry.
2. Resolve the intended canonical tool and profile in [the registry matrix](references/registry-matrix.md). Preserve aliases only at lookup and permission-matching boundaries; emit canonical names in new records.
3. Implement the applicable family contract:
   - Use [filesystem, search, shell, and editor contracts](references/filesystem-search-shell.md) for local workstation capabilities.
   - Use [native search and colored-diff contracts](references/native-search-diff.md) for the incremental file index, fuzzy ranking, suggestion refresh, syntax-aware diff coloring, word ranges, and terminal-width rendering.
   - Use [web, MCP, LSP, and open-world contracts](references/web-mcp-lsp.md) for external protocols and network data.
   - Use [agent, task, team, scheduling, and planning contracts](references/orchestration-task.md) for stateful or asynchronous coordination.
   - Use [interaction, skill, output, worktree, and optional contracts](references/interaction-extension.md) for user interaction and profile-specific surfaces.
4. Apply [result limits, persistence, failures, and acceptance scenarios](references/results-errors-acceptance.md). Never let display truncation silently become transcript truncation or vice versa.
5. Test the enabled and disabled paths. A compiled-but-disabled tool must be absent from the model registry; a known but unavailable external capability must fail explicitly if invoked through a stale transcript.

## Catalog invariants

- **TCAT-001 — Fail-closed defaults.** An omitted classification means sequential, write-capable, non-destructive, closed-world, non-interactive, and blocking-on-interruption. Omission never grants concurrency or read-only status.
- **TCAT-002 — Per-input classification.** Compute concurrency, read-only, destructive, open-world, and permission scope from validated input when the descriptor allows it. Do not cache one invocation's classification for another.
- **TCAT-003 — Deterministic exposure.** Apply build inclusion, runtime gates, platform support, session mode, agent profile, deny rules, and current availability before model exposure. Sort built-ins and external tools in stable partitions; built-ins win canonical-name collisions.
- **TCAT-004 — Capability versus task.** A tool call may create durable work, but the tool result must return the created task identity and lifecycle state. The task registry, not the tool call object, owns later progress, cancellation, and recovery.
- **TCAT-005 — Permission delegation.** Tool-specific checks may refine or preapprove a request, but they do not bypass the common permission, hook, policy, sandbox, and cancellation pipeline.
- **TCAT-006 — Complete result pairing.** Every accepted tool-use identifier receives exactly one terminal result, including denial, cancellation, sibling failure, malformed external output, and unavailable-after-resume cases.
- **TCAT-007 — Untrusted extension data.** Validate MCP schemas, annotations, names, progress, content blocks, and metadata as untrusted input. Missing safety annotations default to unsafe classifications.
- **TCAT-008 — Stable transcript identity.** Persist overflow decisions and exact replacement text by tool-use identifier so resume and repeated projection preserve prompt-cache and transcript identity.
- **TCAT-009 — Disabled behavior is contractual.** Feature-gated, internal, test, platform, and provider tools are supported absence states, not initialization errors.
- **TCAT-010 — Technology neutrality.** Preserve schemas, ordering, limits, states, and user-visible behavior; choose native libraries and operating-system adapters appropriate to the implementation language.

## Boundary with adjacent skills

- Use the common tool-protocol skill for schema validation order, hooks, permission composition, scheduling, cancellation, progress, result pairing, and persistence queues.
- Use the permissions-and-sandbox skill for rule parsing, shell/path analysis, protected resources, approval updates, and isolation selection.
- Use the task-runtime skill for durable process and agent lifecycles after a tool launches background work.
- Use the query-model skill for deciding when normalized results continue the model loop.

## Definition of done

Consider a catalog implementation complete only when every registry row has an explicit implemented-or-unsupported decision, aliases resolve deterministically, all profile matrices are tested, input-dependent classifications are covered, limits survive resume, and every acceptance scenario in the references passes without consulting source code.
