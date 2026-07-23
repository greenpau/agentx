---
name: source-code-management
description: Create, review, or revise repository-compliant commit messages and commit-message files. Use when summarizing source changes for version control, choosing a change indicator, checking commit-message structure, or when the user asks to create a commit message for changes in this repository.
---

# Source Code Management

## Inspect the change first

Read the repository state before drafting a message. Inspect tracked, staged, and relevant untracked changes, plus recent subjects when history provides useful precedent. Base every claim on the actual diff or on verification reported by the user.

Do not stage files, create a commit, amend history, or push unless the user explicitly requests that separate action. Creating a commit message authorizes only the message artifact described below.

## Subject rules

Write the first line as the subject and conform to all of these rules:

- Use `<indicator>: <concise summary>`.
- Use exactly one indicator without a parenthesized scope.
- Keep the entire subject shorter than 87 characters.
- Do not end the subject with a period.
- Describe the outcome in imperative, present-tense language.
- Prefer a specific subsystem indicator over a generic maintenance indicator.

## Choose one change indicator

Use the narrowest subsystem that owns the primary behavior:

- `assistant`: proactive or persistent assistant behavior and session history
- `bootstrap`: startup initialization and bootstrap behavior
- `bridge`: process, transport, IDE, SDK, or external-runtime bridges
- `buddy`: companion state, generation, rendering, and interaction
- `cli`: command-line entrypoints, launchers, flags, and terminal startup
- `commands`: interactive command definitions, routing, and execution
- `components`: reusable application UI components outside the terminal renderer
- `context`: conversation context, compaction, memory, and context assembly
- `coordinator`: orchestration and coordination behavior
- `core`: shared top-level task, tool, query-engine, setup, or main-loop behavior
- `ink`: terminal rendering, layout, screen, focus, and input infrastructure
- `input`: keybindings, Vim behavior, text editing, and interaction mappings
- `query`: query execution, streaming, response handling, and query state
- `remote`: remote sessions, remote control, and remote service integration
- `server`: local server endpoints, protocols, and server lifecycle
- `services`: shared services not owned by a narrower subsystem
- `state`: application state containers, migrations, and persistence
- `tasks`: task lifecycle and concrete task implementations
- `tools`: tool definitions, authorization, dispatch, results, and tool UI
- `voice`: voice input, output, and voice-session behavior

Use a maintenance indicator when the primary change is cross-cutting or not owned by one product subsystem:

- `breakfix`: repair a reported regression, panic, or shipped user-visible failure
- `fix`: correct behavior without a known shipped breakage
- `feat`: add user-visible capability with no honest subsystem indicator
- `docs`: change documentation only
- `tests`: add or revise tests and fixtures primarily for coverage
- `refactor`: restructure implementation without changing behavior
- `skills`: change `AGENTS.md`, repo-local skills, or agent-facing instructions
- `ops`: update dependencies, toolchains, releases, versions, or maintenance config
- `build`: change build behavior, packaging, or generated build output
- `github`: change GitHub Actions, templates, or repository GitHub metadata
- `security`: perform hardening, vulnerability, audit, or disclosure-policy work
- `various`: intentionally combine unrelated changes that should not be split

If several indicators appear plausible, choose the one matching the user-visible or architectural center of the change. Prefer splitting unrelated changes over using `various` when the user controls commit scope.

## Body rules

Include these required sections in this exact order:

1. `Before this commit:`
2. `After this commit:`
3. `Tests:`
4. `More info:`

Apply these rules:

- Separate sections with one blank line.
- End every section label with a colon.
- Keep lines at or below 87 characters, except unavoidable links and detailed `More info` lines.
- Describe the prior behavior or repository state under `Before this commit`.
- Describe the resulting behavior or state under `After this commit`.
- Name the exact automated command or manual check under `Tests`.
- Write `Tests: not run — <reason>` when no verification ran.
- Summarize implementation decisions and material caveats under `More info`.
- Do not claim a test passed unless its result was observed or supplied by the user.

After the required sections, add only the applicable optional sections in this order:

1. `Resolves:` for issues completely resolved by the change
2. `Partial Resolution:` for issues only partly addressed
3. `See also:` for related references
4. `Links:` for a bulleted reference list

Use valid links in the first three reference sections. Separate multiple links with a comma and a space.

## Template

```text
indicator: concise subject under 87 characters

Before this commit: describe the previous behavior, limitation, or state.

After this commit: describe the new behavior, implementation, or state.

Tests: name the command or manual check, or say not run and why.

More info: summarize important implementation details and decisions.
```

## Commit-message file workflow

When the user asks to create a commit message for a change:

1. Inspect the complete intended commit scope.
2. Draft and validate the subject and body.
3. Create `tmp/commits` if it does not exist.
4. Write the message to `tmp/commits/YYYYMMDD_HHMM_<short-slug>.txt` using local repository time.
5. Add a numeric suffix rather than overwrite an existing file.
6. Return the path and a concise summary of the selected indicator.

Treat files in `tmp/commits` as untracked working artifacts. Do not stage or commit them unless the user explicitly asks.

## Acceptance scenarios

- A change limited to `AGENTS.md` and `.codex/skills` uses `skills`.
- A tool-handler behavior change with accompanying tests uses `tools`.
- A dependency-only update uses `ops`.
- A behavior-preserving reorganization across several subsystems uses `refactor`.
- A mixed change uses its dominant subsystem or is split; use `various` only when the mixture is intentional.
