---
name: implementation-state-context
description: Implement process bootstrap facts, observable application state, state-change reactions, working-directory identity, and model context assembly. Use when implementing session state, selectors, permission context storage, prompt precedence, cached and volatile prompt sections, repository context, or model token limits.
---

# Implementation State and Context

## Workflow

1. Classify every value by lifetime: process bootstrap, live session/application, durable transcript, or background task.
2. Implement immutable snapshot updates and synchronous change reactions without hiding authoritative mutation in presentation components.
3. Preserve stable project identity separately from the operational working directory.
4. Assemble system and user context with explicit precedence, cacheability, invalidation, and model-limit rules.
5. Verify mode, settings, worktree, compact, and resume transitions against the acceptance scenarios.

Read [the complete state and context contract](references/state-context-contract.md) before implementing or auditing this domain. Read [the project and user instruction discovery contract](references/instruction-discovery.md) when implementing filesystem instructions, conditional rules, includes, worktrees, trust approval, instruction caches, or `InstructionsLoaded` audit events. Read [the readable-identifier lexicon](references/readable-identifier-lexicon.md) when implementing plan slugs, generated team names, or default bridge titles; its ordered tables and modulo mapping are normative. Use the [architecture diagram](assets/architecture.drawio) to inspect state ownership and prompt assembly, and the [instruction discovery graph](assets/instruction-discovery.drawio) to verify discovery order, include gates, lazy traversal, and invalidation.

## Boundaries

Own bootstrap state, the application store, selectors, state reactions, user/project/repository context, system-prompt composition, and context/output limits. Defer settings-source loading and managed policy to the startup/settings skill. Defer query iteration and API requests to the query/model skill. Treat UI contexts as transient adapters, never durable domain stores.

## Completion check

- Preserve all `SC-*` contracts in the reference.
- Preserve all `SC-INSTR-*` discovery, ordering, trust, reload, and fault contracts.
- Preserve the exact `SC-ID-*` entropy mapping, token order, duplicate weighting, and slug format.
- Test referential no-op updates, session switching, cwd/worktree changes, custom prompt precedence, cache invalidation, and subagent isolation.
- Confirm every model-visible hidden fact is deliberate, ordered, and implementible after resume.
