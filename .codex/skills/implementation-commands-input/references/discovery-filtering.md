# Discovery, precedence, visibility, and surface filtering

## Source assembly

Discover sources concurrently where safe, then concatenate in this exact precedence order:

1. bundled skills;
2. enabled built-in plugin skills;
3. filesystem skills and legacy command files for the current working directory;
4. workflow-backed commands;
5. plugin commands;
6. plugin skills;
7. built-in commands.

Do not globally deduplicate this list. Invocation resolves the first available match; autocomplete keeps duplicates with source-specific stable IDs and annotations. A failed filesystem-skill or plugin-skill source logs a diagnostic, contributes an empty slice, and does not suppress other sources.

Dynamic skills discovered after file operations are filtered by live availability/enablement, deduplicated by canonical name against the current base list, and inserted after external/plugin slices but before the first built-in. Cache expensive discovery by working directory. Clearing command caches must also clear plugin, skill, dynamic search-index, and layered memoization caches as appropriate.

## Lookup and source labels

For an exact invocation, compare in list order:

1. canonical name;
2. displayed/user-facing name;
3. aliases.

Comparison is case-sensitive. Suggestions may match case-insensitively. Display source labels as follows: workflow `(workflow)`; plugin with known name `(plugin-name)`, otherwise `(plugin)`; bundled `(bundled)`; settings source label; built-in and MCP normally no suffix. Preserve loaded-from and version identity even when labels are hidden.

## Model-visible skill subsets

The model Skill index includes prompt commands that are not built-in, are not `disableModelInvocation`, and either:

- are loaded from bundled skills, skill directories, or legacy command directories; or
- are plugin/MCP commands with an explicit user description or `whenToUse`.

The slash skill browser narrows to prompt-based skills with source/description eligibility, while permitting explicitly user-only entries. MCP skill commands live outside the ordinary source list and are included only under the `MCP_SKILLS` gate when `type=prompt`, `loadedFrom=mcp`, and model invocation is enabled.

## Visibility and suggestions

- Hidden commands are excluded from ordinary typeahead/help; exact typed fallback may reveal/execute them.
- Suggest only when input is slash command text without entered arguments. Mid-input slash detection is presentation/autocomplete only and does not change submission routing.
- Fuzzy matching uses threshold 0.3 with relative weights: canonical name 3, name parts 2, aliases 2, description 0.5.
- Rank exact canonical, exact alias, canonical prefix, alias prefix, then fuzzy; usage recency breaks ties.
- At bare `/`, offer up to five recent prompt skills, then built-in local/UI commands alphabetically, then user, project, managed-policy, and other sources.
- Split searchable name parts at colon, underscore, and hyphen.
- Completing inserts `/<displayed-name> `; an explicit execute action may submit a non-prompt command or a prompt command with no named arguments.
- Mid-input candidates require start/whitespace then `/` plus a letter and subsequent `[A-Za-z0-9_:-]` characters.

## Remote mode

The specified remote-safe built-in set is exact:

`session`, `exit`, `clear`, `help`, `theme`, `color`, `vim`, `cost`, `usage`, `copy`, `btw`, `feedback`, `plan`, `keybindings`, `statusline`, `stickers`, and `mobile`.

Pre-filter to this set before the interactive surface becomes usable and preserve it after remote initialization. The set describes local terminal controls that remain meaningful while execution is remote; it is not a general permission allowlist.

## Remote Control bridge

- Prompt commands are safe because they expand to model text under normal policy.
- Local UI commands are always unsafe because they require the local terminal renderer.
- Local commands are unsafe by default. The exact specified safe set is `compact`, `clear`, `cost`, internal `summary`, `release-notes`, and internal `files`.
- A recognized but unsafe bridge command returns `/<name> isn't available over Remote Control.`
- Unknown remote slash text remains ordinary text, avoiding accidental rejection of `/shrug`-style messages.

## Noninteractive filtering

Reject local commands with `supportsNonInteractive=false`, prompt commands with `disableNonInteractive=true`, and all local UI commands unless the headless adapter has a specified non-UI alternative. Where the registry contains two descriptors with the same canonical name (for example interactive and noninteractive variants), source/list order and surface filtering select exactly one observable behavior.

## Discovery acceptance cases

- **DF-A01:** A bundled skill and built-in share a name; invocation selects bundled, while suggestions show both with source identities.
- **DF-A02:** Plugin skill discovery fails; built-ins, filesystem skills, and plugin commands remain available.
- **DF-A03:** Login state changes without process restart; availability-filtered commands appear/disappear on the next registry request.
- **DF-A04:** A bridge attempts `/model`; it receives the unavailable-over-Remote-Control result and no local picker opens.
- **DF-A05:** Remote inbound `/shrug` is ordinary text when no recognized safe command matches.
