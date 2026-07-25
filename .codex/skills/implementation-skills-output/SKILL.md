---
name: implementation-skills-output
description: Implement or diagnose repo-local skill discovery from .codex/skills; skill invocation and isolation; prompt listing; and output-style discovery and prompt transformation. Use when skills are missing, unexpectedly unavailable, malformed, or rooted incorrectly, or when implementing reusable instruction packages or user-selected response styles.
---

# Implementation Skills and Output Styles

## Preserve instructions as attributed capabilities

A skill is an invocable instruction package with discovery, visibility, permission, substitution, execution-context, and compaction semantics. An output style is a selected prompt transformation with separate collision and precedence rules. Neither is merely a file concatenation operation.

Use the [architecture diagram](assets/architecture.drawio) to inspect discovery and prompt projection. Read [skill runtime](references/skill-runtime.md) for sources, metadata, conditional visibility, invocation, permission, forked execution, and security. Read the [dormant bundled-skill catalog](references/bundled-skill-catalog.md) only when evaluating a future profile that explicitly enables product-shipped descriptors; it does not make bundles available in the standalone profile. Read [output styles](references/output-styles.md) for built-in and custom style schemas, precedence, forced plugin styles, and system-prompt effects. Requirement identifiers `SKILL-*`, `BSKILL-*`, and `STYLE-*` are stable implementation anchors.

## Skill workflow

1. Resolve and freeze the active repository root independently from the selected working-directory depth, then admit only its `.codex/skills` directory when the workspace is trusted and the session is not bare.
2. Canonicalize the selected root and discover only the supported layout. Parse frontmatter independently from the Markdown body and derive documented fallbacks.
3. Retain a reason-coded discovery outcome so `/skills`, `/doctor`, and DEBUG diagnostics can distinguish gating, root selection, root access, empty input, and rejected definitions without exposing bodies or exact paths.
4. Resolve path-conditioned visibility, then fit the model-visible listing within its context budget without changing callable identity.
5. On invocation, resolve the canonical skill name, enforce user/model visibility and permission rules, substitute only supported variables, and attach base-directory provenance.
6. Either inject the instruction content into the current turn or run it in a forked agent context. Propagate progress and return one normalized result.
7. Preserve invoked skill content and its hooks through compaction so later turns retain the contract that shaped the work.

## Output-style workflow

1. Load built-ins, plugin styles, user styles, project styles, and managed styles with their domain-specific precedence.
2. Validate metadata, namespace plugin styles, and omit malformed individual styles without discarding the registry.
3. Resolve a forced plugin style before the configured setting; otherwise select the configured name or default behavior.
4. Add the selected style as a deliberate dynamic system-prompt section and decide independently whether standard coding instructions remain.
5. Freeze the selection for the active session unless the surface explicitly rebuilds session context.

## Invariants

- A frontmatter display name never changes the filesystem-derived command identity.
- `disable-model-invocation` and `user-invocable` govern different callers.
- Remote skill bodies cannot execute inline shell substitutions on the local workstation.
- Skill permission matching uses canonical identity and deny-first evaluation.
- A custom output style that omits `keep-coding-instructions` suppresses the normal coding-task section; default behavior retains it.
- Listing truncation may reduce descriptions but must not invent names, reorder precedence, or make an unavailable skill callable.

## Verification checks

- User, managed, plugin, bundled, MCP, explicit-directory, and nested-repository skills never enter the registry.
- A path-conditioned skill appears only after a touched path matches its gitignore-style patterns.
- A model call to a model-disabled skill fails, while explicit user invocation remains available if user-invocable.
- A trusted root `.codex/skills` definition is available; the same definition copied to user configuration or a plugin is absent.
- A trusted session launched from a repository descendant discovers the same root skills as a root launch, while an inner repository boundary prevents outer-skill inheritance.
- Every empty `/skills` result has one bounded actionable cause; DEBUG reports safe root relation, gates, counts, and reason codes without skill bodies, credentials, or exact workspace paths.
- Managed style content shadows project, user, plugin, and built-in content of the same name.
- An unknown configured style falls back to default behavior without injecting a phantom style section.
