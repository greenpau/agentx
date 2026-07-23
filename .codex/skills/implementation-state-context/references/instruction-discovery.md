# Project and User Instruction Discovery Contract

This document is normative for discovering, parsing, ordering, injecting, reloading, and observing filesystem instructions. It describes behavior without requiring the implementation or its programming language. The generic Markdown-asset loader is included only where its search and cache behavior shares this boundary; command, agent, skill, and output-style semantics remain owned by their dedicated skills.

## Contents

- [Responsibility and result model](#responsibility-and-result-model)
- [Eager instruction discovery and precedence](#eager-instruction-discovery-and-precedence)
- [Lazy downward discovery and conditional rules](#lazy-downward-discovery-and-conditional-rules)
- [Parsing and include expansion](#parsing-and-include-expansion)
- [Trust, approval, exclusions, and symlinks](#trust-approval-exclusions-and-symlinks)
- [Generic Markdown configuration discovery](#generic-markdown-configuration-discovery)
- [Disabled and filtered modes](#disabled-and-filtered-modes)
- [Caching, invalidation, and hooks](#caching-invalidation-and-hooks)
- [Limits and failures](#limits-and-failures)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Responsibility and result model

**SC-INSTR-001 — Boundary.** Own the filesystem-to-model instruction pipeline: choose sources, walk directories, parse instruction files, expand includes, classify conditional rules, apply trust and source gates, preserve ordering, construct model-visible entries, maintain discovery caches, and emit audit hooks. Do not make the instruction loader execute commands, grant tool permission, or interpret command/skill/output-style semantics.

**SC-INSTR-002 — Instruction entry.** Represent every accepted file as an entry containing an absolute or otherwise unambiguous path, source type (`Managed`, `User`, `Project`, `Local`, and optionally memory-only types), transformed text, optional including-parent path, and optional path-glob list. When transformation makes the injected text differ from disk, retain the raw disk text and mark the entry as a partial view so later write safety can demand an explicit read.

**SC-INSTR-003 — Model projection.** Concatenate nonempty accepted entries in discovery order. Prefix the collection with a strong statement that these are user/codebase instructions and override default behavior. Label each entry with its path and source meaning. Trim leading and trailing whitespace from each entry at projection time; do not merge entries or erase provenance.

**SC-INSTR-004 — Priority meaning.** Later entries have greater prompt recency and therefore the intended higher priority. Preserve the exact order below; do not replace it with alphabetical sorting, a map keyed by filename, or a generic configuration-precedence algorithm.

**SC-INSTR-005 — Projection labels.** Describe `Project` as checked-in project instructions and `Local` as the user's private non-checked-in project instructions. Describe auto-memory as persistent user memory and feature-gated team memory as shared organizational memory, wrapping team content in a source-attributed team-memory delimiter. For compatibility, both `User` and `Managed` currently fall through to the same private-global-instructions description even though managed discovery has policy authority; do not infer policy precedence from that display label.

## Eager instruction discovery and precedence

**SC-INSTR-010 — Eager source roots.** On an ordinary eager load, search these source groups in order:

1. the platform-managed instruction file, then the managed recursive rules directory;
2. when the user setting source is enabled, the user configuration-home instruction file, then its recursive `rules` directory;
3. project and local candidates at every ancestor of the original working directory;
4. explicitly added directories, when their independent instruction-discovery gate is enabled;
5. optional auto-memory and team-memory entrypoints, when their independent features are enabled.

The managed base is the operating-system policy directory (system application-support directory on macOS, program-files policy directory on Windows, and `/etc/agentx-code`-equivalent on other systems). The user base is the configurable application home, defaulting to a `.agentx` directory under the user's home.

**SC-INSTR-011 — Managed and user layout.** The managed top-level file is `AGENTX.md`; managed rules live recursively below `.agentx/rules`. The user top-level file is `AGENTX.md` in the user configuration home; user rules live recursively below `rules` in that same home. Managed sources are always eligible. User sources require user-setting-source eligibility.

**SC-INSTR-012 — Ancestor walk.** Starting at the original working directory, collect every directory up to but excluding the filesystem root, reverse the collection, and process it from the outermost ancestor down to the original working directory. This eager instruction walk does not stop at the home directory, Git root, stable project root, or trust boundary. A top-level candidate in an ancestor outside the original working directory is still a normal discovered file, not an external include.

**SC-INSTR-013 — Per-directory order.** At each ancestor, when the project setting source is enabled, process `AGENTX.md`, then `.agentx/AGENTX.md`, then every Markdown file recursively found below `.agentx/rules` that has no effective path condition. When the local setting source is enabled, process `AGENTX.local.md` after those project candidates. This makes closer directories later than outer directories and local instructions later than project instructions at the same level.

**SC-INSTR-014 — Rule enumeration.** Recurse into rule directories in the filesystem enumeration order supplied by the active filesystem adapter. Accept only regular files whose names end in lowercase `.md`. Do not promise alphabetical precedence inside a rule directory unless the implementation deliberately introduces and documents a compatibility break.

**SC-INSTR-015 — Additional-directory order.** When the additional-directory instruction gate is true, process explicit directories in command-line/state order after the ordinary ancestor walk. At each directory process `AGENTX.md`, `.agentx/AGENTX.md`, and unconditional `.agentx/rules/**/*.md`; do not search `AGENTX.local.md`. This explicit source is not suppressed merely because the project setting source is disabled. The directory itself is accepted as an explicit top-level root, but includes originating there still use the external-include rule relative to the original working directory.

**SC-INSTR-016 — First-discovery deduplication.** Share one processed-path set across the eager source groups. A normalized path accepted earlier prevents a later attempt from producing another entry. Consequently managed, then user, then outer-to-inner project/local order also determines which path alias wins. Deduplication is a discovery rule, not a claim that same-named files have the same identity.

**SC-INSTR-017 — Nested worktree eager behavior.** When the original working directory is a worktree physically nested inside its canonical main repository, the ancestor walk would pass through both trees. Load project files from the worktree checkout, but suppress checked-in `Project` candidates in directories inside the canonical main tree and outside the worktree root. Continue to load `AGENTX.local.md` from those main-tree directories because local files are normally untracked. Do not apply this suppression to a sibling worktree whose path is outside the main tree.

**SC-INSTR-018 — Optional memory tail.** Optional auto-memory and team-memory entrypoints are appended after filesystem instructions and deduplicated by normalized path. They are memory context, not `InstructionsLoaded` events. Their discovery, selection, and synchronization remain owned by the memory/compaction skill.

## Lazy downward discovery and conditional rules

**SC-INSTR-020 — Trigger.** Lazy instruction discovery begins after a successful file-touch/read path is placed in a per-query trigger set. Drain each distinct trigger after ordinary at-mention/file processing has populated the set. If no trigger exists, do not acquire application state or walk directories.

**SC-INSTR-021 — Permission boundary.** Before lazy discovery, require the trigger path to lie in an allowed working path under the current tool-permission context. An out-of-scope trigger yields no instruction attachments. This gate does not retroactively govern eager top-level instruction discovery.

**SC-INSTR-022 — Lazy phase order.** For an allowed target, use a fresh processed-path set and preserve this phase order:

1. matching managed conditional rules;
2. matching user conditional rules, when user settings are enabled;
3. directories strictly below the original working directory on the path to the target, parent to child; for each, load project top-level files, local top-level file, unconditional project rules, then matching conditional project rules;
4. directories from the filesystem outer ancestor down through the original working directory; load only matching conditional project rules because their unconditional rules were eager-loaded.

Convert new entries to model attachments in this same order.

**SC-INSTR-023 — Target containment.** Downward directory collection only admits directories whose resolved textual path is beneath the original working-directory prefix and stops at the filesystem root. A target in an additional allowed directory outside that prefix can still receive managed/user conditional rules, but it does not cause a downward project-instruction walk outside the original working directory.

**SC-INSTR-024 — Conditional base.** Match project rule globs against a path relative to the directory containing that rule's `.agentx` directory. Match managed and user rule globs relative to the original working directory. Reject an empty relative path, a path escaping the base with `..`, or a cross-volume result that remains absolute.

**SC-INSTR-025 — Conditional matching.** Interpret the normalized patterns with ignore-file glob semantics. A matching rule is attached only once per session/query owner: maintain a non-evicting loaded-instruction-path set in addition to the bounded file-state cache, so eviction from the latter cannot cause repeated instruction injection.

**SC-INSTR-026 — Conditional/unconditional split.** Recursive rule scanning parses every candidate and its includes, then retains entries according to each entry's own effective `paths` field. Path conditions do not automatically inherit from an including parent. During nested-directory processing, use a copy of the processed set for the unconditional pass, then the original set for the conditional pass, and finally merge the unconditional identities back; this prevents the first pass from hiding a conditional parent in the second.

**SC-INSTR-027 — Lazy attachment state.** When injecting a new lazy entry, add a `nested_memory`-equivalent attachment with path, transformed entry, and cwd-relative display path; record it in the non-evicting loaded set; and seed file-state with raw bytes plus a partial-view flag when transformed text differs from disk. Compaction or conversation clear may intentionally empty the non-evicting set so necessary context can be reintroduced.

## Parsing and include expansion

**SC-INSTR-030 — Text decoding.** Read the whole candidate as UTF-8 text. Missing files and directories do not produce entries. After frontmatter/comment/memory-entrypoint transformation, omit an entry whose remaining text is empty or whitespace-only.

**SC-INSTR-031 — Frontmatter envelope.** Recognize YAML frontmatter only at byte/text position zero, beginning with a `---` line and ending with the next `---` line. Remove the envelope from injected content. For instruction rules, only `paths` affects discovery; accept either a string or string list. Unknown keys are retained by the generic parser but have no instruction-discovery effect.

**SC-INSTR-032 — Path-pattern grammar.** Split a string at commas that are outside braces, trim nonempty pieces, recursively expand brace alternatives, and flatten string lists. Remove a trailing `/**` from each result. Empty results or a collection containing only `**` mean unconditional applicability. Invalid non-string `paths` values behave as no patterns.

**SC-INSTR-033 — Frontmatter recovery.** First attempt ordinary YAML parsing. If it fails, retry after quoting simple unindented scalar values containing YAML-special characters. If both attempts fail, log a diagnostic, use empty frontmatter, and still inject the body after the frontmatter envelope. For a rule file this means malformed `paths` frontmatter degrades to an unconditional rule; preserve this compatibility behavior or explicitly classify a fail-closed redesign as a migration.

**SC-INSTR-034 — Comment transformation.** Strip only complete block-level HTML comment spans recognized as Markdown HTML blocks. Preserve comments in fenced code and inline code, preserve inline comments embedded in ordinary paragraph tokens, preserve residual text on a line after a closed block comment, and leave an unclosed comment in the injected body. A transformation must set partial-view/raw-content metadata.

**SC-INSTR-035 — Include recognition.** Scan parsed Markdown text nodes recursively, including list children. Ignore fenced code and inline code. Ignore non-comment HTML tokens. For a closed block-comment token, remove comment spans and scan only residual text. Recognize an include only when `@` begins the text or follows whitespace; consume a non-whitespace token while allowing backslash-escaped spaces; strip a `#fragment`; unescape spaces; and reject an empty remainder.

**SC-INSTR-036 — Include path forms.** Accept `@./relative`, `@bare-relative`, `@~/home-relative`, and non-root `@/absolute` forms. A bare form must begin with an ASCII letter, digit, dot, underscore, or hyphen and must not begin with another `@` or the reserved punctuation group `# % ^ & * ( )`. Resolve relative forms against the realpath-resolved directory of the including file, not necessarily the lexical symlink location. Deduplicate multiple identical include tokens within one file before recursion.

**SC-INSTR-037 — Include file types.** Permit extensionless files and a text allowlist covering Markdown/plain text; JSON, YAML, TOML, XML, CSV; HTML/CSS and preprocessors; common programming and shell languages; configuration, database, protocol, template, build, documentation, lock, log, diff, and patch formats. Reject a file with a nonempty extension outside the allowlist, including images, PDFs, archives, and executables. Decode every permitted type as UTF-8 text.

The exact case-insensitive extension allowlist is:

| Family | Extensions |
| --- | --- |
| text and data | `.md`, `.txt`, `.text`, `.json`, `.yaml`, `.yml`, `.toml`, `.xml`, `.csv` |
| web and style | `.html`, `.htm`, `.css`, `.scss`, `.sass`, `.less` |
| JavaScript-family source | `.js`, `.ts`, `.tsx`, `.jsx`, `.mjs`, `.cjs`, `.mts`, `.cts` |
| Python and Ruby | `.py`, `.pyi`, `.pyw`, `.rb`, `.erb`, `.rake` |
| Go, Rust, JVM, and .NET | `.go`, `.rs`, `.java`, `.kt`, `.kts`, `.scala`, `.cs` |
| C-family and Swift | `.c`, `.cpp`, `.cc`, `.cxx`, `.h`, `.hpp`, `.hxx`, `.swift` |
| shell and command | `.sh`, `.bash`, `.zsh`, `.fish`, `.ps1`, `.bat`, `.cmd` |
| configuration and database | `.env`, `.ini`, `.cfg`, `.conf`, `.config`, `.properties`, `.sql`, `.graphql`, `.gql` |
| protocol, framework, and templates | `.proto`, `.vue`, `.svelte`, `.astro`, `.ejs`, `.hbs`, `.pug`, `.jade` |
| other language source | `.php`, `.pl`, `.pm`, `.lua`, `.r`, `.dart`, `.ex`, `.exs`, `.erl`, `.hrl`, `.clj`, `.cljs`, `.cljc`, `.edn`, `.hs`, `.lhs`, `.elm`, `.ml`, `.mli`, `.f`, `.f90`, `.f95`, `.for` |
| build and documentation | `.cmake`, `.make`, `.makefile`, `.gradle`, `.sbt`, `.rst`, `.adoc`, `.asciidoc`, `.org`, `.tex`, `.latex` |
| artifacts that remain textual | `.lock`, `.log`, `.diff`, `.patch` |

**SC-INSTR-038 — Expansion order.** Emit the including file first, then recursively emit each resolved include in token-discovery order. Each child records its immediate parent path. This parent-before-child order is the compatibility behavior even if older prose describes includes-first; children therefore have later prompt recency than their parent.

**SC-INSTR-039 — Depth and cycles.** Begin the top-level file at depth zero and reject an attempt whose depth is five or greater. Thus a root plus four recursive include levels can be emitted. Track normalized logical paths; when a path resolves through a symlink, also record its normalized canonical target. Skip a path already present. This bounds direct cycles and most alias cycles without treating a missing file as fatal.

**SC-INSTR-040 — Shared rule recursion.** For recursive rules directories, maintain a visited-directory set containing both lexical and resolved directory paths. Follow symlinked directories and files, determine a symlink target's type before recursion, and stop a directory cycle. A rule file is processed through the same include, exclusion, transformation, depth, and deduplication pipeline as a top-level instruction file.

## Trust, approval, exclusions, and symlinks

**SC-INSTR-050 — External definition.** Only a recursively included child is classified as external. It is external when its resolved path is outside the original working directory. Top-level managed/user/ancestor/additional files and top-level symlinks are never subjected to this include check merely because their path or target lies elsewhere.

**SC-INSTR-051 — Include policy.** User instructions may always include external paths. Managed, project, and local instructions include an external child only when the current project has persisted approval or the caller explicitly requests a force-inclusion probe. Internal children need no external approval.

**SC-INSTR-052 — Approval lifecycle.** Persist approval and warning-shown state per canonical project identity. Before the first interactive warning, perform a separate force-inclusion discovery pass to enumerate external children, without injecting that pass or firing load hooks. Accepting sets approved and warning-shown; declining or cancelling sets not-approved and warning-shown. Once warning-shown, startup does not prompt again unless configuration is reset; unapproved external children remain silently omitted.

**SC-INSTR-053 — Trust timing.** The interactive surface asks about external includes only after workspace trust has been established. Noninteractive mode treats workspace trust as implicit but has no approval dialogue; absent persisted approval, managed/project/local external children remain omitted. The loader itself has no trust latch and relies on its caller's startup ordering. Generic Markdown configuration discovery can be prefetched before the interactive trust dialog and must remain read-only until downstream invocation.

**SC-INSTR-054 — Exclusion setting.** Apply configured exclusion globs only to `User`, `Project`, and `Local` entries, including their recursive includes. Never exclude `Managed`, auto-memory, or team-memory entries through this setting. Match normalized slash-separated absolute candidate paths with dotfile matching enabled. For an absolute exclusion pattern, also attempt a variant whose longest existing static directory prefix is realpath-resolved, so aliases such as platform temporary-directory symlinks can match either spelling.

**SC-INSTR-055 — Symlink resolution.** Before reading a file, attempt a nonblocking-safe canonical resolution that refuses network-style UNC resolution and avoids canonicalizing sockets, devices, and FIFOs. On missing, broken, permission-denied, or cyclic links, retain the lexical path and continue best-effort. Resolve include bases from the canonical target when available. Record both lexical and canonical identities for later deduplication, but preserve first-discovery directionality: a canonical path discovered after its symlink is skipped; a symlink first encountered after the canonical path may still be read because duplicate checking occurs before adding its newly resolved target.

**SC-INSTR-056 — Eager versus tool authorization.** Filesystem tool allow/deny rules do not authorize eager instruction reads. Lazy trigger discovery does require an allowed working path, and ordinary file-read validation still governs the tool action that produced the trigger. Keep workspace trust, instruction include approval, setting-source eligibility, and tool filesystem permission as separate decisions.

## Generic Markdown configuration discovery

**SC-INSTR-060 — Generic loader scope.** Reuse a separate Markdown discovery primitive for configuration subdirectories such as commands, agents, output styles, skills, workflows, and feature-gated templates. It returns path, containing base directory, parsed frontmatter, body, and setting-source attribution; consumers own semantic validation and name conflict resolution.

**SC-INSTR-061 — Generic roots and return order.** Search the managed `.agentx/<kind>` directory, the user configuration-home `<kind>` directory, and existing project `.agentx/<kind>` directories. Return managed files first, then user files, then project directory groups ordered from the starting cwd outward toward the stop boundary. User/project groups require their setting source, and user/project agents are additionally suppressible by plugin-only policy. Managed files remain eligible.

**SC-INSTR-062 — Generic upward boundary.** Start at the supplied cwd, omit the home directory because it is loaded separately as user scope, and normally stop after checking the nearest Git root. If cwd is inside a nested independent repository that itself lies inside the session's stable project repository, widen the stop boundary to the session project root so the parent project's configuration remains visible. Do not widen for a sibling or unrelated repository. Unexpected filesystem errors during existence checks propagate; inaccessible/missing candidates are skipped.

**SC-INSTR-063 — Generic worktree fallback.** A worktree walk stops at the worktree's nearest Git marker. If that worktree lacks the requested `.agentx/<kind>` directory, append the canonical main repository's corresponding directory as a fallback. If the worktree has the directory, do not also load the main copy. This applies independently per configuration kind and supports sparse checkouts without duplicating ordinary worktrees.

**SC-INSTR-064 — Generic recursive search.** Recursively return lowercase `*.md`, include hidden paths, follow symlinks, and ignore ignore-files. Prefer the configured fast search provider; support a native traversal fallback. Bound a search attempt to three seconds. Native traversal stops promptly on abort, tracks visited target directories by device/inode with canonical-path fallback, logs and skips inaccessible entries, and does not guarantee lexical order.

**SC-INSTR-065 — Generic parse failure.** Read discovered files concurrently as UTF-8. A single read or parse exception logs and drops that file without dropping siblings. Frontmatter uses the same envelope and recovery parser as SC-INSTR-031/033. An inaccessible root returns an empty group; an unexpected search-provider error propagates to the semantic consumer, which may degrade its whole feature to empty.

**SC-INSTR-066 — Generic identity deduplication.** After concatenating source groups, inspect each path with non-following file metadata and use device-plus-inode as an identity when reliable. First identity wins in return order. If metadata is unavailable or reports the unreliable all-zero identity, include the file (fail open). Hard links deduplicate; a symlink and its target are not guaranteed to deduplicate because non-following metadata identifies the link itself.

**SC-INSTR-067 — Generic policy versus precedence.** The generic loader's return order and first-identity-wins rule do not by themselves define same-name override behavior. Commands, agents, skills, and output styles must apply their own documented registry precedence after discovery.

## Disabled and filtered modes

**SC-INSTR-070 — Hard disable.** A hard disable switch makes rendered user-context assembly omit every eager instruction group, including managed, user, project, local, auto/team memory, and explicit additional-directory instructions; the current date and unrelated user context remain present. This is not a loader-level kill switch. Direct callers such as interactive startup file-state seeding, external-approval probing, status/doctor views, or the memory editor may still discover/read files, and the lazy attachment pipeline can still inject nested or conditional instructions as described by SC-INSTR-076.

**SC-INSTR-071 — Setting-source gates.** Disabling user settings suppresses the user top-level file and user rules. Disabling project settings suppresses eager ancestor project top-level files and project rules, plus lazily discovered nested `AGENTX.md` and `.agentx/AGENTX.md`; it does not suppress ancestor local files, explicitly added directories, or the current lazy project-rule scans. Disabling local settings suppresses `AGENTX.local.md`. Managed sources remain enabled. The lazy project-rule exception is an observed compatibility leak, not permission to generalize source-gate bypasses.

**SC-INSTR-072 — Bare compatibility behavior.** In bare mode with no additional directories, skip instruction discovery entirely. With at least one additional directory, enter the ordinary eager loader. The additional directory is actually searched only when the separate additional-directory instruction environment gate is true; meanwhile any otherwise enabled managed/user/project/local sources are also searched. This is the observed compatibility behavior even though the user-facing intent describes add-dir-only loading. An implementation that narrows this to explicit directories must mark the change as intentional.

**SC-INSTR-073 — Project-level suppression experiment.** A runtime suppression gate may omit `Project` and `Local` entries from eager model projection and lazy attachments while still allowing their discovery and eager audit-hook emission. Do not mistake discovery, hook reporting, and model injection for one boolean state.

**SC-INSTR-074 — Memory-index suppression.** A separate runtime gate may omit auto-memory and team-memory index entrypoints from model injection because another memory retrieval path supplies selected attachments. It does not suppress ordinary managed/user/project/local instructions.

**SC-INSTR-075 — Generic simple mode.** Bare/simple mode suppresses custom agent directory discovery and changes skill discovery to explicit additional directories only, subject to project-setting and plugin-only policy. Other generic Markdown consumers must state their own simple-mode behavior; do not infer it solely from the shared loader.

**SC-INSTR-076 — Lazy disabled-mode compatibility.** Neither the hard eager-disable switch nor bare mode is consulted when draining file-triggered nested-instruction attachments. Managed/user conditional rules retain their ordinary setting-source gates; nested project top-level files retain the project gate; local top-level files retain the local gate; but nested and cwd-level project rule scans run even when the project setting source is disabled. The project-level suppression experiment can still filter `Project`/`Local` results before attachment. Preserve these axes separately or document a fail-closed redesign.

## Caching, invalidation, and hooks

**SC-INSTR-080 — Discovery cache keys.** Memoize eager instruction discovery by the raw first argument controlling force-inclusion. A force-inclusion probe is separate from ordinary policy; for strict compatibility, an omitted argument and an explicit false can also occupy distinct cache entries even though their discovery semantics match. Memoize rendered user context above discovery as a distinct layer. Memoize generic Markdown discovery by configuration kind plus supplied cwd; semantic consumers may add another cache above it.

**SC-INSTR-081 — Instruction invalidation operations.** Provide two inner-cache operations:

- a correctness-only clear that removes eager discovery results without arming audit hooks;
- a reset that records a one-shot top-level load reason, arms eager hooks, and clears discovery.

The initial state is armed with `session_start`. Compaction resets with `compact`; conversation clear/resume resets with `session_start`.

**SC-INSTR-082 — Invalidation matrix.** Preserve these observed invalidations:

| Event | Discovery cache | Rendered user-context cache | Eager hook reason |
| --- | --- | --- | --- |
| first session load | miss | miss | `session_start` once |
| compaction cleanup on main session | reset | clear | `compact` once |
| conversation clear/resume cache sweep | reset | clear | `session_start` once |
| worktree enter/exit or restored-worktree switch | correctness clear | not directly cleared | none |
| settings-sync write of user/local instruction | correctness clear | not directly cleared | none |
| memory editor open/refresh | correctness clear and immediately prime | not directly cleared | none |
| team-memory sync update | correctness clear | not directly cleared | none |
| debug prompt injection change | unchanged | clear | none |

System-prompt-section clearing is separate from rendered user-context clearing.

**SC-INSTR-083 — Two-layer staleness.** Clearing only the discovery cache does not change an already memoized rendered user context. Therefore mid-session worktree, sync, or memory-dialog correctness clears can leave the current model projection unchanged until an event also clears rendered user context. Preserve this compatibility boundary in tests; an implementation may invalidate both layers as a documented correctness improvement, but must not assume the reference already does so.

**SC-INSTR-084 — File-state change observation.** Interactive startup seeds file-state for discovered entries. Later turns compare modification times and can attach a textual diff for changed files, subject to read permission and full-read state. This changed-file attachment does not itself rebuild the rendered instruction context. Delete evicts file-state only on definite not-found; transient stat/read failures retain it for a later retry.

**SC-INSTR-085 — Generic cache invalidation.** Clearing skill caches or output-style caches also clears the shared generic Markdown cache. Agent-definition cache clearing does not inherently clear that shared cache. Plugin refresh and file-change detectors invoke domain-specific clears. Cache invalidation must account for both the shared discovery layer and each semantic consumer's memoization.

**SC-INSTR-086 — Eager hook emission.** On the first ordinary eager cache miss after hooks are armed, consume the one-shot reason even if no hook is configured. If a hook exists, emit one fire-and-forget event for every accepted `Managed`, `User`, `Project`, or `Local` entry. A top-level entry uses the armed reason; an included entry uses `include`. Never fire eager instruction hooks for auto/team memory or for a force-inclusion approval probe.

**SC-INSTR-087 — Lazy hook emission.** When a previously unseen lazy attachment is injected and a hook exists, emit `path_glob_match` when the entry has effective globs, otherwise `include` when it has a parent, otherwise `nested_traversal`. Include file path, memory type, globs, trigger file path, and parent file path as applicable. The glob reason takes precedence over include when both metadata fields exist.

**SC-INSTR-088 — Hook authority.** `InstructionsLoaded` is audit/observability only. Dispatch outside the REPL, do not await it in the instruction path, do not let its output alter or block context, and bound execution with the ordinary tool-hook timeout. Hook absence must avoid constructing per-file hook inputs.

**SC-INSTR-089 — Worktree hook configuration.** A startup-created worktree becomes current cwd, original cwd, and stable project root before first query; clear instruction discovery and recapture settings-file hooks from that worktree. A mid-session enter or resume-restored worktree changes current/original cwd and clears instruction discovery but intentionally leaves stable project root and the existing hook snapshot unchanged. Exit refreshes the hook snapshot only when startup worktree mode had changed the project root. Therefore newly discovered worktree instructions and the hooks observing them can resolve from different anchors in a mid-session transition.

## Limits and failures

**SC-INSTR-090 — Instruction size.** Ordinary managed/user/project/local instruction files and their permitted includes are read in full and are not truncated by the instruction loader. A transformed content length greater than 40,000 characters is advisory: status and doctor surfaces warn about performance but injection continues. Enforce context-window limits later in the query/context boundary, not by silently cutting one instruction file.

**SC-INSTR-091 — Memory entrypoint truncation.** Optional auto/team `MEMORY.md` entrypoints are different: trim and cap at 200 lines and 25,000 text units, line-first and then at the last newline before the size boundary where possible, with a visible truncation warning. Preserve raw text plus partial-view metadata. Do not apply this memory-specific cap to AGENTX instruction files.

**SC-INSTR-092 — File read failures.** Treat not-found and is-a-directory as ordinary absence. Treat access denied as omission plus privacy-preserving telemetry that does not reveal the full path. Other top-level read errors also omit the entry. One failed file never cancels sibling discovery.

**SC-INSTR-093 — Rule directory failures.** Missing, inaccessible, or non-directory rule roots yield an empty rule group. A recursive directory error is contained at that group; access failures may emit privacy-preserving telemetry. A broken or inaccessible symlink target is skipped. Directory cycles terminate through the visited set.

**SC-INSTR-094 — Search timeout and ordering.** The generic Markdown search has a three-second abort budget. Native traversal returns what it collected before observing abort; the fast provider may report an abort/error to its caller. Neither strategy supplies a cross-platform within-directory ordering guarantee. Tests that depend on name conflict precedence must impose it in the owning semantic registry, not infer it from filesystem order.

**SC-INSTR-095 — No hidden recovery.** A cache clear, source change, or read failure does not mutate the durable transcript and does not retroactively rewrite model messages from prior turns. Reloaded instructions affect only context/attachments assembled after the relevant cache and dedup state has been invalidated.

## Acceptance scenarios

**SC-INSTR-A01 — Normal eager stack.** Given managed, user, two ancestor project levels, local files, and one gated additional directory, the result is managed file/rules, user file/rules, outer project/dot-rules/local, inner project/dot-rules/local, then additional project/dot-rules. Prompt labels retain every source and path.

**SC-INSTR-A02 — Include parsing.** Given prose containing `@./a.md`, `@dir/file\ name.txt#part`, a fenced `@secret.md`, an inline-code `@secret2.md`, and a closed block comment followed by `@./b.md`, load `a`, the spaced file, and `b`; do not load either code example or the comment body; remove the fragment before resolution.

**SC-INSTR-A03 — Parent and cycle order.** Given `root` includes `child`, `child` includes `root`, emit `root` then `child` once each, attach `parent=root` to `child`, and terminate. Given a sixth-depth candidate, omit that candidate without failing earlier levels.

**SC-INSTR-A04 — Unsupported and large files.** Given includes of a PDF and an extensionless UTF-8 text file, omit the PDF and include the extensionless file. Given a 50,000-character project instruction, inject all characters and emit a performance warning rather than truncating it.

**SC-INSTR-A05 — External approval.** Given a project instruction including a path outside original cwd and no prior decision, a force probe lists it without hooks or injection. Decline records warning-shown and later ordinary loads omit it without another prompt. After approval, an invalidated ordinary load includes it and reports `include` when hooks are armed.

**SC-INSTR-A06 — User external include.** Given the same external child from a user instruction, include it without project approval and exclude it from the external-warning list.

**SC-INSTR-A07 — Top-level trust distinction.** Given an ancestor `AGENTX.md`, an explicit additional directory, and a top-level symlink whose target lies outside cwd, load all three as top-level candidates without external-include approval. Given an `@` child from any non-user candidate to that same target, gate the child.

**SC-INSTR-A08 — Conditional traversal.** Given a touched file below cwd, emit matching managed/user rules first, then newly discovered directories from cwd toward the target, then matching conditional rules from outer ancestor toward cwd. A nonmatching rule is absent; a target outside every allowed working directory causes no lazy scan.

**SC-INSTR-A09 — Conditional malformed frontmatter.** Given a rule whose `paths` YAML cannot parse even after recovery, log the parse failure, strip the envelope, and treat the body as unconditional. A stricter implementation must identify this as a deliberate security migration.

**SC-INSTR-A10 — Reload after compaction.** Given an initial hook-enabled load followed by a file edit and compaction, clear both rendered and discovery caches, emit the new content on the next turn, and fire one eager event per instruction using `compact` for top-level entries and `include` for children.

**SC-INSTR-A11 — Correctness-only clear.** Given a populated rendered user-context cache followed by a settings-sync instruction write, clear discovery only. A direct discovery call sees new bytes, but the already memoized rendered context remains old and no eager hook fires until an outer-context invalidation occurs.

**SC-INSTR-A12 — Lazy dedup under LRU pressure.** After a conditional instruction is injected, evict its file-state entry through unrelated reads and touch another matching file. The non-evicting loaded-path set prevents reinjection and duplicate hook emission.

**SC-INSTR-A13 — Nested worktree.** In a worktree located below the main repository, load checked-in project instructions from the worktree, suppress duplicate checked-in main-tree ancestors above it, and still load main-tree local instructions. For generic agent/command discovery, use the main repository fallback only when the worktree lacks that configuration kind.

**SC-INSTR-A14 — Sparse worktree fallback.** Given a worktree with no `.agentx/agents` but a main repository that has it, generic discovery appends the main directory once. After the worktree gains its own directory and both relevant caches are cleared, load only the worktree copy.

**SC-INSTR-A15 — Bare modes.** Bare with no add-dir produces no eager instruction projection. Bare with an add-dir but the separate additional-directory gate off enters ordinary eager discovery, loads otherwise eligible standard sources, and does not scan the add-dir. With the gate on it also scans the add-dir. Hard disable suppresses every rendered eager variant, while a direct diagnostic/startup loader call can still return files. In either disabled mode, a subsequent allowed file-touch trigger still exercises the lazy compatibility path in SC-INSTR-076.

**SC-INSTR-A16 — Generic failures.** Given one unreadable Markdown file, one hard-link duplicate, one all-zero identity, and a recursive symlink cycle, drop the unreadable file, keep the first hard-link identity, retain the all-zero file fail-open, and terminate the directory cycle while returning unaffected siblings.

**SC-INSTR-A17 — Worktree cache transition.** Entering a worktree clears instruction discovery and cwd-sensitive prompt sections without arming load hooks. If rendered user context was already memoized, verify the compatibility-stale projection described by SC-INSTR-083; after a full conversation-cache clear, verify the worktree instructions replace it with `session_start` hook reasons. In startup worktree mode use hooks recaptured from the worktree; in mid-session mode retain the original project hook snapshot.

**SC-INSTR-A18 — Project-source lazy leak.** Disable the project setting source, leave project-level suppression off, and touch an allowed file beneath cwd. Eager project files/rules and nested project top-level files are absent, but matching nested or cwd-level `.agentx/rules` entries can still arrive as lazy attachments. Enabling project-level suppression removes those `Project` attachments.

## Non-normative provenance

Behavior was specified primarily from `utils/agentxmd.ts`, `utils/markdownConfigLoader.ts`, `utils/frontmatterParser.ts`, `utils/attachments.ts`, `context.ts`, `utils/hooks.ts`, `interactiveHelpers.tsx`, `components/AgentXMdExternalIncludesDialog.tsx`, `setup.ts`, `utils/sessionRestore.ts`, the enter/exit worktree tools, settings synchronization, compaction cleanup, cache-clear commands, and the direct command/agent/skill/output-style consumers of the generic Markdown loader. These paths are evidence only; the contracts above are sufficient for a standalone implementation.
