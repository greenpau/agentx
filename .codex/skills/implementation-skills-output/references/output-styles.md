# Output-style contract

This document defines style discovery, precedence, selection, and prompt effects. `STYLE-*` identifiers are normative and stable.

## Contents

1. [Descriptor and built-ins](#descriptor-and-built-ins)
2. [Custom file schema](#custom-file-schema)
3. [Registry precedence](#registry-precedence)
4. [Selection](#selection)
5. [Prompt transformation](#prompt-transformation)
6. [Cache and failure behavior](#cache-and-failure-behavior)
7. [Acceptance scenarios](#acceptance-scenarios)
8. [Non-normative provenance](#non-normative-provenance)

## Descriptor and built-ins

`STYLE-001` — An output-style descriptor contains canonical name, description, prompt body, `keepCodingInstructions`, source and plugin identity, optional force flag, canonical file path, and generation.

`STYLE-002` — Built-in styles are:

| Name | Prompt behavior | Keep standard coding instructions |
| --- | --- | --- |
| `default` | no additional style prompt (`null` selection) | yes |
| `Explanatory` | bundled explanatory response guidance | yes |
| `Learning` | bundled learning-oriented response guidance | yes |

Built-in prompt wording may evolve, but names, null/default semantics, and retention behavior are public contracts.

## Custom file schema

`STYLE-010` — Discover custom Markdown recursively under `.agentx/output-styles` roots from managed, user, and project locations. Project traversal proceeds upward to the repository root. Follow eligible symlinks with canonical-path deduplication. A worktree may fall back to the main worktree's project styles only when the active worktree has no applicable definition.

`STYLE-011` — Parse frontmatter:

| Field | Behavior |
| --- | --- |
| `name` | style name; fallback is filename without extension |
| `description` | discovery description; fallback may be empty/bounded body summary |
| `keep-coding-instructions` | boolean or recognized boolean string |
| `force-for-plugin` | plugin-only force flag; ignored/rejected for ordinary filesystem styles |

The trimmed Markdown body is the prompt. Empty or malformed bodies are omitted with a file diagnostic.

`STYLE-012` — Plugin styles are namespaced `<plugin-name>:<style-name>`. Load only from enabled plugins. Search the standard `output-styles` directory plus manifest-declared file/directories, canonicalize paths, and deduplicate.

`STYLE-013` — A plugin force flag belongs to the validated plugin descriptor. A filesystem file cannot impersonate plugin authority by setting it.

## Registry precedence

`STYLE-020` — Build the final name map by assigning in this order:

1. built-ins;
2. enabled plugin styles;
3. user styles;
4. project styles;
5. managed styles.

Later assignment wins, so managed is highest precedence, then project, user, plugin, and built-in. This observed order is normative even if an implementation comment states otherwise.

`STYLE-021` — Within one source tier, use deterministic root and lexical file order. Record shadowed descriptors and their paths; asynchronous discovery completion cannot affect the winner.

`STYLE-022` — Plugin namespacing normally prevents collisions across plugins. An explicitly identical namespaced identity follows deterministic plugin registry order.

## Selection

`STYLE-030` — Before reading the configured `outputStyle`, inspect the deterministic final registry for plugin styles with validated force flag. The first forced plugin style wins. If several are forced, select the first and warn with all conflicting plugin identities.

`STYLE-031` — Without a forced style, read `outputStyle` from effective settings; absent means `default`. Resolve `default` to null style behavior. An unknown name also produces null/default behavior and a diagnostic; never inject an unknown label with empty prompt.

`STYLE-032` — `/output-style` is a deprecated configuration surface. A settings/configuration change generally affects the next session or explicit context rebuild, not the middle of an in-flight model request.

`STYLE-033` — Freeze the selected descriptor and generation for the session context. Plugin/background update changes disk/registry for a future session unless an explicit safe reload rebuilds model context.

## Prompt transformation

`STYLE-040` — A non-null selected style contributes a dynamic system-prompt section exactly equivalent to:

```text
# Output Style: <canonical-name>
<trimmed prompt body>
```

Keep this section deliberately separated from cached stable prompt material so a style change has explicit cache semantics.

`STYLE-041` — The selected style may also change the system-prompt introductory framing. Preserve the semantic distinction that coding-task instructions are not automatically synonymous with response style.

`STYLE-042` — Include the standard coding-task instruction section only when:

- selection is null/default; or
- selected descriptor has `keepCodingInstructions=true`.

For any non-null custom style, omitted or false `keep-coding-instructions` suppresses the standard coding section.

`STYLE-043` — Style prompt text is model-visible untrusted configuration and follows the source/trust policy. It does not grant tools, permissions, hooks, model selection, or execution authority.

## Cache and failure behavior

`STYLE-050` — Clear style discovery, selected-style, and prompt-section caches when relevant settings or enabled-plugin generations change. Publish one coherent prompt context; do not combine a new style body with an old retention flag.

`STYLE-051` — A malformed style file is isolated. Omit it, preserve all other styles, and retain the previous selected style only if the active session is intentionally frozen; a new session with an invalid configured style uses default behavior.

`STYLE-052` — If a selected plugin disappears on reload, the active in-flight request keeps its frozen prompt. The next rebuilt context resolves force/configuration again and falls back deterministically.

`STYLE-053` — Bound description and prompt file sizes according to general instruction-file limits. Diagnostics may include name/path/schema issue, never the entire potentially sensitive prompt by default.

## Acceptance scenarios

1. **STYLE-A01 — Precedence.** Built-in, plugin, user, project, and managed sources define `Explanatory`. The managed descriptor wins; diagnostics identify all shadowed definitions.
2. **STYLE-A02 — Forced-style collision.** Two enabled plugins force styles. Deterministic plugin registry order selects one and emits a multiple-force warning. The configured user style is ignored for that session.
3. **STYLE-A03 — Missing selection.** `outputStyle` names a missing style. No style section is injected and standard coding instructions remain.
4. **STYLE-A04 — Coding-instruction suppression.** A custom style omits `keep-coding-instructions`. Its section is injected and the standard coding-task section is absent.
5. **STYLE-A05 — Built-in retention.** `Learning` is selected. Its section is injected and standard coding instructions remain because the built-in explicitly opts in.
6. **STYLE-A06 — Frozen generation.** A plugin update replaces a style on disk mid-request. The request retains its frozen prompt; the next session uses the new version after cache invalidation.

## Non-normative provenance

Reference behavior was specified from built-in style constants, recursive Markdown configuration loaders, plugin output-style loading, selected-style resolution, settings application, and system-prompt composition under `outputStyles/`, `utils/plugins/`, and prompt builders. Paths and symbols are provenance only.
