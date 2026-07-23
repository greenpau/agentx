---
name: skill-authoring
description: Create or revise repo-local skills connected by actionable routing statements from AGENTS.md through broad and increasingly specialized skills. Use when documenting AgentX engineering knowledge, adding or routing a skill, reorganizing the skill hierarchy, or auditing that every skill is reachable and useful to contributors.
---

# Skill Authoring

## Inherit the default authoring workflow

Use the installed `$skill-creator` skill to apply the default authoring workflow. Read it completely and apply its initialization, naming, frontmatter, progressive-disclosure, metadata, validation, and testing guidance.

Apply this skill after `$skill-creator`. Where the two differ, retain the default requirements and add the repository hierarchy and engineering-contract requirements below.

## Build a routed skill hierarchy

Store every discoverable skill as a direct child of `.codex/skills/<skill-name>/`; do not nest discoverable skill directories inside one another.

Express hierarchy as actionable routing at any depth:

```text
AGENTS.md --Use A to do X--> skill A --Use B to do Y--> skill B --> ...
```

Write each repo-local route in this form:

```markdown
Use [skill-name](relative/path/to/SKILL.md) to perform a specific task.
```

Make the action specific enough that an agent can decide whether to load the linked skill. Put routes to broad skills in `AGENTS.md`. Put routes to narrower skills in the broad skill that delegates the work. A leaf skill needs no hierarchy boilerplate.

Do not add structural ancestry sections or require a downstream skill to link back to its router. The forward `Use ... to ...` statements define the hierarchy.

Read [the skill routing contract](references/hierarchy-contract.md) when creating, moving, routing, or auditing a skill.

Treat cross-skill references that do not tell the agent to use a skill for a task as supporting links, not hierarchy routes.

## Author project engineering contracts

Capture durable behavior and workflows needed to develop, review, operate, and verify AgentX. Treat the Go source, tests, and skills as cooperating project authorities; update the owning skill when implementation work changes its contract.

Prefer observable, language-neutral contracts:

- responsibility and boundaries
- inputs, outputs, and data shapes
- state and lifecycle transitions
- ordering, concurrency, and timing behavior
- invariants and decision rules
- error, cancellation, retry, and recovery behavior
- user-visible behavior and integration boundaries
- edge cases and acceptance scenarios

Keep contracts focused on behavior and ownership instead of duplicating source listings or private-symbol inventories. Link relevant source or tests when that helps contributors navigate the current repository, but state durable rules in the skill itself.

Put shared concepts in the broadest skill that needs them. Put specialized behavior in the narrowest routed skill that owns it. Do not duplicate the same rule across routing and routed skills.

## Author architecture diagrams

For every diagram-bearing implementation skill, apply [the shared architecture diagram contract](../implementation-architecture/references/diagram-contract.md). Describe the concrete question answered by each asset, identify the authoritative prose, and ensure every page self-identifies its product route, lifecycle position, owned boundary, neighboring owners, edge grammar, contract anchors, and normative limits without relying on its filename.

Update a diagram whenever ownership, topology, lifecycle, ordering, concurrency, durability, trust, or recovery changes. Route fan-out through distinct ports, route feedback around the forward flow, label every behavioral edge, and never use stage numbers for a partial order. Render every changed page at 100% and inspect it; valid XML is not evidence of readable geometry.

For implementation-architecture skills, regenerate shared overviews with `generate_drawio.rb`, normalize hand-authored pages with `enhance_custom_drawio.rb`, and run each tool's `--check` mode. Render every changed page at exact scale with `ruby .codex/skills/implementation-conformance-audit/scripts/render_drawio_preview.rb INPUT.drawio --output-dir OUTPUT_DIRECTORY --page PAGE`, visually inspect it, and finish with the implementation audit. The enhancer supplies standardized orientation and connector safeguards; the author still owns semantic labels, meaningful lanes, decision branches, and fidelity to prose contracts.

## Workflow

1. Read `AGENTS.md`, inventory `.codex/skills/*/SKILL.md`, and follow existing `Use ... to ...` routes relevant to the task.
2. Choose the narrowest existing file that should route to the new or moved skill. Route directly from `AGENTS.md` only when no existing skill owns the concern.
3. Define the new skill's responsibility so it is cohesive, non-overlapping, and small enough to load independently.
4. Initialize the skill with the default `$skill-creator` workflow under `.codex/skills/<skill-name>/`.
5. Write the project engineering contract. Use references only for detailed material that would otherwise bloat `SKILL.md`.
6. Add a precise `Use [skill](path) to ...` statement to the selected routing file. Add no backlink solely to represent routing.
7. Add further routes inside the new skill only when it delegates narrower work.
8. Generate or refresh `agents/openai.yaml` and run the default skill validator.
9. Starting at `AGENTS.md`, audit all repo-local `Use ... to ...` routes and repair broken targets, cycles, ambiguous actions, and unreachable skills.
10. Report the new or changed routing chain using repository-relative paths.

## Route skills by task

When a task matches a routing statement, read and apply the linked skill. Continue following narrower routing statements only while they match the requested work.

Interpret routed skills as focused workflows:

- Routing skills define shared vocabulary, invariants, and dispatch decisions.
- Routed skills add narrower behavior without repeating unrelated routing context.
- A specialized skill must not silently weaken requirements already applied earlier in the route.

## Completion criteria

Finish only when:

- the default skill validator passes for every changed skill
- every repo-local skill is reachable through an actionable route from `AGENTS.md`
- every `Use ... to ...` target exists and its action is unambiguous
- no routing chain is cyclic
- no structural ancestry metadata or routing-only backlinks remain
- the authored knowledge gives contributors actionable ownership, behavior, and verification guidance
- examples and acceptance scenarios cover normal behavior and material edge cases
- every diagram is understandable without its filename or surrounding Markdown
- every behavioral edge is labeled and routed without ambiguous overlap
- every page identifies its broader product position, owned/deferred scope, prose anchors, and authority
