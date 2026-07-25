# Skill discovery and invocation contract

This document is normative for skill sources, metadata, listing, permission, substitution, execution, and reload. `SKILL-*` identifiers are stable implementation anchors.

## Contents

1. [Skill identity and source model](#skill-identity-and-source-model)
2. [Filesystem discovery](#filesystem-discovery)
3. [Frontmatter schema](#frontmatter-schema)
4. [Model-visible listing](#model-visible-listing)
5. [Invocation input and substitution](#invocation-input-and-substitution)
6. [Permission and tool augmentation](#permission-and-tool-augmentation)
7. [Inline and forked execution](#inline-and-forked-execution)
8. [Bundled extraction security](#bundled-extraction-security)
9. [Failure and recovery](#failure-and-recovery)
10. [Acceptance scenarios](#acceptance-scenarios)
11. [Non-normative provenance](#non-normative-provenance)

## Skill identity and source model

`SKILL-001` — A skill descriptor contains canonical invocation name, display name, description, source type, canonical root, body, validated metadata, visibility state, optional path conditions, and generation.

`SKILL-002` — Filesystem command identity is derived from the skill directory or legacy command path. Frontmatter `name` changes only display metadata; it never renames the callable command.

`SKILL-003` — AgentX accepts one skill source:

| Source | Notes |
| --- | --- |
| project | the active repository's root `.codex/skills` directory |

`SKILL-004` — Bare or untrusted-workspace mode loads no skills. User configuration, managed roots, nested roots, explicit external directories, plugins, bundles, legacy commands, and remote providers never contribute skills in the standalone AgentX profile. Remote-descriptor and bundled-extraction rules retained below are dormant safety contracts for a future profile that explicitly adds such a source; they do not make those sources eligible now.

## Filesystem discovery

`SKILL-010` — A directory skill exists only when its direct child under the repository root `.codex/skills` contains a file exactly named `SKILL.md`. Arbitrary Markdown siblings are not separate skills.

`SKILL-011` — Reuse and identity-pin the active repository root established by startup/state context before skill discovery; the skill loader does not derive a second root independently. The selected working directory may be that root or any descendant; it must not replace the repository root merely because the session started below it. Root project instructions and root `.codex/skills` use the same frozen repository boundary, while an inner repository, submodule, or worktree identity becomes its own boundary rather than inheriting an outer repository. When no active repository identity exists, report that state instead of scanning arbitrary ancestors. Select only `<repository-root>/.codex/skills`, canonicalize it once, and scan its direct skill directories deterministically. No source-precedence merge exists because there is exactly one eligible root. `AGENTS.md` routing makes skills useful to contributors but is not a filesystem-discovery prerequisite for an otherwise valid direct child.

`SKILL-012` — Managed skill switches cannot add or reinterpret skills because managed skills are outside the AgentX product profile.

`SKILL-013` — Do not discover nested `.codex/skills` roots. Repository-local means the single root belonging to the active repository, so touched paths cannot change the discovery boundary.

`SKILL-014` — Conditional metadata `paths` uses gitignore-style patterns. A conditional skill is excluded until session path evidence matches at least one pattern. Matching may be triggered by nested traversal, an explicit attachment/reference, or another documented touched-file event; mere existence is insufficient.

`SKILL-015` — Watch skill roots with a 1,000 ms stability window, 500 ms file polling when needed, and 300 ms discovery debounce. A runtime without reliable event watching may poll every two seconds. Reload publishes a new generation; stale scans cannot replace it.

`SKILL-016` — Retain one explainable discovery outcome per generation. It records the trust and bare gates, whether an active repository root was resolved, the working-directory relationship to that root, eligible-root state (`not_selected`, `missing`, `unreadable`, `empty`, or `loaded`), scanned/accepted/rejected counts, and bounded omission reason codes. Canonical paths remain internal identity evidence. Ordinary DEBUG logs use only the constant relative root `.codex/skills`, a bounded opaque repository identity when needed, and relationship/reason classifications; they never expose exact workspace paths, skill bodies, arbitrary frontmatter, or credentials.

`SKILL-017` — Project `/skills`, `/doctor`, and DEBUG diagnostics from the same discovery outcome and caller-filtered invocation registry. Choose the primary empty cause in this order: trust/bare gate, active-repository selection, root access/state, definition validation, then caller visibility/policy; retain subordinate counts and reasons rather than discarding them. An empty result distinguishes at least untrusted workspace, bare mode, no active repository, missing or unreadable root, empty root, all definitions rejected, and accepted definitions all filtered for the caller. A normal successful discovery remains quiet at INFO. DEBUG includes safe gates, counts, generation, and bounded reason categories under the observability contract; one malformed definition cannot hide a valid sibling.

`SKILL-018` — `/skills` is an explicit-user projection. It lists path-active, policy-available definitions with `user-invocable=true`, including a definition that disables only model invocation. The model-visible prompt listing separately applies model eligibility. Discovery outcomes retain discovered, accepted, user-visible, and model-visible counts plus bounded omission categories such as malformed, path inactive, user disabled, model disabled, policy filtered, and unavailable. A root with accepted definitions but zero user-visible entries is reported as caller-filtered, never as no definitions discovered.

## Frontmatter schema

`SKILL-020` — Parse these fields. Unknown fields may be retained for forward diagnostics but cannot silently gain authority.

| Field | Type and behavior |
| --- | --- |
| `name` | display string only |
| `description` | model/user discovery description |
| `allowed-tools` | tool/rule list temporarily contributed while invoked |
| `argument-hint` | display hint |
| `arguments` | argument metadata/schema used by invocation surface |
| `when_to_use` | conditional guidance included in listing |
| `version` | opaque version metadata |
| `model` | model override; literal `inherit` means no override |
| `disable-model-invocation` | boolean; blocks model caller only |
| `user-invocable` | boolean, default `true`; governs explicit user path |
| `hooks` | skill-scoped hook configuration |
| `context` | `fork` selects isolated agent execution |
| `agent` | requested agent type for forked execution |
| `effort` | reasoning-effort override |
| `shell` | supported shell substitution/configuration metadata |
| `paths` | gitignore-style conditional visibility patterns |

`SKILL-021` — If description is absent, use the first nonempty Markdown body line, remove one Markdown heading marker, trim it, and cap at 100 characters. An empty body yields a conservative generated description or remains discoverable by name according to caller; never execute body content to derive metadata.

`SKILL-022` — A malformed individual skill is omitted with a bounded schema diagnostic and root-relative descriptor identity such as `<skill-name>/SKILL.md`; ordinary user and DEBUG output never includes an absolute workspace path. Other skills in the root remain available. Treat invalid boolean strings, unsupported context values, and malformed hooks as validation failures for the affected capability, not truthy values.

## Model-visible listing

`SKILL-030` — Build listing entries from the same filtered registry used by invocation. Include canonical name, bounded description/when-to-use guidance, and argument hint as appropriate. Exclude model-disabled, policy-filtered, path-inactive, or unavailable entries.

`SKILL-031` — Listing character budget is approximately one percent of the selected model context, using four characters per token; if model context is unavailable, use an 8,000-character fallback. Cap each project-skill description at 250 characters before global allocation.

`SKILL-032` — Preserve full project-skill descriptions where possible. When remaining budget would allocate fewer than 20 description characters per remaining skill, list names without descriptions rather than arbitrary unusable fragments. A future source profile may define source-specific budgeting only after making that source eligible under `SKILL-003`.

`SKILL-033` — Truncation is deterministic and presentation-only. It does not change skill precedence, canonical names, user discoverability, or the invocation map.

## Invocation input and substitution

`SKILL-040` — Skill invocation is serialized relative to other skill invocations. Normalize an optional leading slash, resolve canonical name, then verify availability and caller eligibility.

`SKILL-041` — Reject unknown, malformed, path-inactive, or non-prompt skill definitions. A model caller is rejected when `disable-model-invocation=true`; a user caller is hidden/rejected when `user-invocable=false`.

`SKILL-042` — Create invoked content in this order:

1. `Base directory for this skill: <canonical-root>` line;
2. skill Markdown body;
3. supported argument substitution using the parsed invocation arguments;
4. `${AGENTX_SKILL_DIR}` -> canonical skill root;
5. `${AGENTX_SESSION_ID}` -> active session identifier;
6. reviewed shell substitutions only for trusted local skills and only through the permission/tool boundary.

Escape or structured-template substitute values; do not let arguments alter parser structure accidentally.

`SKILL-043` — MCP/remote skill bodies never perform local inline shell expansion. Shell-looking expressions remain text. A remote descriptor may ask the model to use an ordinary tool later, which then crosses normal permission, but discovery/invocation itself cannot execute it.

`SKILL-044` — Attach body content as deliberate user/attachment/system-tagged messages correlated with the tool use. Filter synthetic command display/progress messages so they do not become accidental model-visible transcript content.

## Permission and tool augmentation

`SKILL-050` — Evaluate skill rules deny-first using canonical identity. Supported forms include exact `Skill(name)` and prefix/group form such as `name:*` according to serializer. A remote alias is canonicalized only after deny-capable identity information is preserved.

`SKILL-051` — Permission suggestions offer exact and safe prefix rules and persist only to editable local settings selected by the user. A skill cannot self-persist its own allow.

`SKILL-052` — Auto-allow only descriptors whose nonempty behavior-bearing properties are all in an explicit reviewed safe-property allowlist. Any new or unrecognized nonempty property defaults to ask. This future-proof rule prevents new metadata (hooks, shell, fork, model changes) from inheriting old automatic trust.

`SKILL-053` — `allowed-tools` contributes command/skill-source permission allows only for the dynamic extent of the invocation or fork. Normalize and validate each rule, preserve source attribution, and remove it on completion/cancel. Managed denies still dominate.

## Inline and forked execution

`SKILL-060` — Inline invocation adds the skill messages and any deliberate context changes to the current query loop, preserving the current model unless a valid model override is set. The `[1m]` model-context suffix, when present in the active model string, survives the override policy. Apply valid effort override independently.

`SKILL-061` — `context: fork` creates an isolated subagent context. Select the explicit `agent` or documented inherited/default agent, copy only authorized context, apply temporary allowed tools/model/effort/hooks, stream progress to the parent tool use, and return one normalized textual/structured result.

`SKILL-062` — Parent cancellation propagates to the fork. The fork owns its transcript/task identity and cannot write directly into the parent's message chain. Its result is appended through the accepted tool-use result pair.

`SKILL-063` — Skill-scoped hooks are active only for the invocation's dynamic extent and keep skill-root provenance. Hook semantics are defined by the plugin/hook skill.

`SKILL-064` — Invoked skill body, temporary tool rules needed to explain already-started work, and active hooks survive transcript compaction through explicit retained context. Discovery listings may be regenerated; authoritative invoked content cannot be silently dropped.

## Dormant bundled-extraction security

The standalone profile does not admit bundled skills. If a future profile adds
that source, all three conditional contracts in this section become mandatory
before its descriptors enter discovery.

`SKILL-070` — Extract any future-profile bundled skills under the protected runtime temp root using a product/version component and random 16-byte nonce. Directory access is owner-only.

`SKILL-071` — Create files without following symlinks, validate every relative archive path, reject traversal and special files, and prevent replacement races. The session authorizes only its nonce-scoped extracted root.

`SKILL-072` — Cleanup is idempotent. Extraction failure omits the bundled skill with a diagnostic; it never falls back to an attacker-controlled same-name directory.

## Failure and recovery

| Failure | Behavior |
| --- | --- |
| one malformed skill | omit descriptor; siblings remain |
| source gated by trust or bare mode | publish an empty registry with an explicit disabled reason; do not inspect the root |
| active repository unavailable | publish an empty registry with a no-repository reason; do not scan arbitrary ancestors |
| root absent or unreadable | publish an empty project-skill registry with its root-state diagnostic; no fallback root exists |
| duplicate canonical path | first eligible source wins |
| substitution failure | terminal skill error; no partial injected messages |
| fork startup failure | one normalized tool failure; no orphan task |
| watcher failure | polling fallback or frozen last coherent generation |
| compaction during invocation | retain invoked content and active hook contract |

## Acceptance scenarios

1. **SKILL-A01 — Filesystem versus display identity.** Directory `deploy/SKILL.md` declares `name: Release`. It is invoked as `deploy`, displayed as Release, and a rule for `Skill(Release)` does not accidentally authorize it.
2. **SKILL-A02 — Canonical-root deduplication.** A physical repository path and a symlink alias resolve to the same project root. The one eligible root is identity-pinned and scanned once; no duplicate appears.
3. **SKILL-A03 — Conditional generation.** A conditional database skill has `paths: db/**`. It is absent initially, appears after `db/schema.sql` is referenced, and publishes through a new generation.
4. **SKILL-A04 — Remote substitution containment.** A remote skill contains `${AGENTX_SKILL_DIR}` and `$(curl ...)`. The directory token is substituted only if the remote protocol defines a safe remote base; the shell expression remains inert and no local process starts.
5. **SKILL-A05 — Fork cancellation.** A forked skill is cancelled. The child is cancelled, emits one terminal result, and its temporary allowed tools/hooks disappear.
6. **SKILL-A06 — Unknown authority-bearing metadata.** A new unknown frontmatter property is nonempty. The skill is not auto-allowed even though an older version would have considered its known fields safe.
7. **SKILL-A07 — Nested working-directory coherence.** Start trusted, non-bare sessions from the repository root and from `pkg/subdir`. Root `AGENTS.md` context and root `.codex/skills/review/SKILL.md` use one frozen repository identity, and `/skills` lists the same callable skill in both sessions. Create an inner repository below `pkg/subdir`; a session rooted there does not inherit the outer repository's skills.
8. **SKILL-A08 — Explainable empty discovery.** Exercise untrusted, bare, no active repository, missing root, unreadable root, empty root, all-malformed definitions, and accepted definitions filtered to zero separately for user and model callers. `/skills`, `/doctor`, and DEBUG output agree on the primary state, bounded counts, and subordinate reasons; default successful discovery stays quiet. A malformed definition plus a valid sibling lists the sibling. A user-invocable definition with model invocation disabled appears in `/skills` but not the model listing. No projection contains a skill body, arbitrary frontmatter, credential, or exact workspace path.

## Non-normative provenance

Reference behavior was specified from filesystem skill loaders, legacy command adapters, plugin and bundled skill providers, prompt listing builders, skill tool permission/execution logic, conditional discovery watchers, and compaction context retention in `skills/`, `utils/skills/`, command loaders, and query orchestration. Paths and symbols are provenance only.
