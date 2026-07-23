# Native search and colored-diff compatibility contracts

This document specifies two performance-sensitive primitives used by the capability/UI surface: the incremental fuzzy file index behind file mentions, and the syntax-aware terminal diff renderer. The implementation may use native libraries or alternate algorithms, but it must preserve the observable ordering, scores, widths, escape lifecycle, disabled behavior, and refresh races. `FIDX-*` and `CDIFF-*` identifiers are stable anchors. Follow the [native search and diff diagram](../assets/native-search-diff.drawio) for progressive publication, generation fencing, and every colored-render fallback branch.

## Contents

1. [File-index state and loading](#file-index-state-and-loading)
2. [Fuzzy matching and scores](#fuzzy-matching-and-scores)
3. [File-suggestion discovery and refresh](#file-suggestion-discovery-and-refresh)
4. [Colored-diff availability and themes](#colored-diff-availability-and-themes)
5. [Language and syntax highlighting](#language-and-syntax-highlighting)
6. [Word ranges and hunk rendering](#word-ranges-and-hunk-rendering)
7. [Whole-file rendering and caller cache](#whole-file-rendering-and-caller-cache)
8. [Acceptance scenarios](#acceptance-scenarios)

## File-index state and loading

`FIDX-001` — The index owns an ordered unique path list, lowercase copy, 26-bit lowercase-letter bitmap per path, unsigned 16-bit stored path length, ready-prefix count, and top-level-entry cache. Exact path equality is case-sensitive. Loading discards empty strings and preserves the first occurrence of every other string.

`FIDX-002` — Synchronous load replaces the arrays, indexes every path, then publishes the full ready count. Asynchronous load has two phases:

1. deduplicate/filter input while the prior index remains readable;
2. replace arrays and reset ready count to zero, then fill metadata in source order.

During phase two, search reads only `[0, readyCount)`, so callers may observe empty then progressively larger partial results. Rebuild also replaces the top-level cache immediately from the complete deduplicated path list.

`FIDX-003` — In both asynchronous phases, inspect elapsed work after each 256th item and yield to the host scheduler when more than 4 ms has elapsed. `queryable` resolves after the first yielded indexing chunk, or at completion if no chunk yielded. `done` resolves only after every path is ready. The contract is bounded cooperative work, not a particular event-loop API.

`FIDX-004` — Lowercase each path using ordinary Unicode lowercase behavior. Set bitmap bit `0..25` only for ASCII `a..z`. Store path string length in unsigned 16-bit form; extremely long path length therefore wraps modulo 65,536 for the length bonus while matching still uses the complete string. This is a compatibility edge, not a recommendation for filesystem limits.

`FIDX-005` — Empty query does not fuzzy-match. Return cached unique first segments, where a segment ends at the first `/` or `\`. Stop scanning as soon as 100 unique nonempty segments have been encountered, sort those by length ascending then lexical code-unit order ascending, and give every result score `0`. Apply the caller's smaller limit. A never-loaded index has no cache and returns empty.

## Fuzzy matching and scores

`FIDX-010` — A nonpositive limit returns empty. A query containing any uppercase character is case-sensitive; an all-lowercase query compares against lowercase paths. Silently use only the first 64 query characters. Build the query bitmap from lowercase ASCII characters present in that effective query; reject any path whose bitmap lacks a required bit.

`FIDX-011` — Match the query as a greedy subsequence: find the earliest occurrence of character zero, then the earliest occurrence of each following character after the prior position. There is no backtracking to seek a globally better alignment. A missing character rejects the path.

`FIDX-012` — Compute the internal higher-is-better fuzzy score exactly:

```text
score = 16 * queryLength
      + 4 for each immediately consecutive pair
      - (3 + gapLength) for each nonzero gap
      + per-position bonuses
      + max(0, 32 - (uint16PathLength >> 2))
```

Per-position bonus is `+8` when the first query character matches at path position zero; otherwise `+8` when the preceding original path character is `/`, `\`, `-`, `_`, `.`, or space; otherwise `+6` when that preceding character is lowercase ASCII and the matched original character is uppercase ASCII. A later matched character at position zero receives no first-character or boundary bonus.

`FIDX-013` — Maintain only the best `limit` internal scores. Until full, collect candidates; once full, a new candidate must be strictly greater than the current lowest score to displace it. An exactly-threshold tie never displaces the retained path. Do not add a lexical tie-breaker. The specified lower-bound insertion means equal high scores admitted after the set is full can appear before earlier equal scores; preserve fixture-level ordering rather than assuming stable source order.

`FIDX-014` — Sort retained candidates by internal score descending. The public lower-is-better score is rank `i / matchCount`, not a normalized fuzzy score. Then, without reordering, if the path contains lowercase substring `test` with that exact case, multiply its rank by `1.05` and cap at `1`. Consequently the first result is always `0`, and public scores need not be monotonic after the test penalty.

`FIDX-015` — A best-case ceiling may skip expensive boundary scoring only when it proves a candidate cannot strictly beat the full top-k threshold. Such an optimization must be observationally equivalent to `FIDX-011` through `FIDX-014`.

## File-suggestion discovery and refresh

`FIDX-020` — File mentions request at most 15 suggestions. A configured custom `fileSuggestion` command bypasses the built-in index and configuration-file merge entirely; send it the base hook input plus `query`, trust its order only after validating its result schema, and take the first 15.

`FIDX-021` — For empty input, `.` or the platform's `./` equivalent, read the current directory directly, retain filesystem enumeration order, append the platform separator to directories, take the first 15, and start index refresh in the background. If empty input was not explicitly requested by the caller, return no suggestions without reading.

`FIDX-022` — For ordinary input, remove one leading current-directory prefix, expand a leading `~`, start/continue background refresh, and search the currently ready index prefix. If no index exists yet, return empty immediately; completion notification causes the typeahead owner to re-run the query and upgrade partial results.

`FIDX-023` — Prefer repository discovery:

1. locate repository root;
2. run `git -c core.quotepath=false ls-files --recurse-submodules` at that root with a 5-second bound;
3. normalize paths relative to current working directory;
4. apply `.ignore` and `.rgignore` patterns from repository root and current directory even to tracked files;
5. return tracked paths immediately;
6. in the background, run `git ... ls-files --others --exclude-standard` when gitignore is respected, otherwise omit `--exclude-standard`, with a 10-second bound;
7. normalize and apply `.ignore`/`.rgignore` again, then generation-safely merge untracked results.

Git tracks symlinks as links; this path does not follow them.

`FIDX-024` — If repository discovery fails, enumerate with the configured search executable using files, follow symlinks, hidden files, and explicit exclusions for `.git`, `.svn`, `.hg`, `.bzr`, `.jj`, and `.sl`. Add the no-VCS-ignore switch when `respectGitignore` is false. The overall acquisition has a 10-second cancellation bound.

`FIDX-025` — Include applicable configuration Markdown files and every nonroot parent directory of all files. Directory entries carry one trailing platform separator. The indexed ordering is directories first, then project/config files for the tracked build; a later merge is tracked files, config files, cached tracked directories, untracked files, then untracked directories. Exact deduplication in `FIDX-001` resolves overlaps.

`FIDX-026` — Only one refresh and one untracked fetch run at a time. With an existing cache, refresh immediately when the ordinary `.git/index` modification time differs; otherwise refresh at most once per 5 seconds so untracked changes are eventually seen. Worktrees whose `.git` is a file and repositories without an index fall back to the time floor.

`FIDX-027` — Avoid rebuild when a sampled path-list signature is unchanged. The signature contains list length and an FNV-like 32-bit hash of roughly 500 evenly spaced paths plus the final path. It deliberately can miss a same-length middle edit between samples; the next 5-second acquisition is allowed to catch it. Keep separate tracked-only and merged signatures so the two array shapes do not invalidate one another forever.

`FIDX-028` — Clearing caches increments a generation, discards index, active refresh ownership, tracked/config/directory lists, ignore cache, modification evidence, signatures, and completion subscribers. Every asynchronous merge/publication checks its captured generation. A stale operation may finish privately but cannot replace or signal the new cache. Discovery failure logs diagnostics and returns the current index, allowing retry.

## Colored-diff availability and themes

`CDIFF-001` — The colored renderer is available in every supported build unless `AGENTX_SYNTAX_HIGHLIGHT` is explicitly defined as false by the common environment-boolean grammar. Per-call skip-highlighting or `syntaxHighlightingDisabled` settings also select the ordinary fallback renderer. Absence is graceful; no partial ANSI output is emitted before fallback.

`CDIFF-002` — Choose color mode in this order: if the theme name contains case-sensitive substring `ansi`, use the ANSI palette; otherwise if `COLORTERM` equals exactly `truecolor` or `24bit`, use 24-bit RGB; otherwise use 256-color mode. Each rendered physical line begins with reset, or reset plus dim for a dim render, and ends with reset so styles cannot leak.

`CDIFF-003` — Convert a palette-index color directly: indices 0–7 use ordinary 30/40 codes, 8–15 bright 90/100 codes, and larger indices `38|48;5;n`. A terminal-default sentinel emits 39/49. In truecolor emit `38|48;2;r;g;b`.

`CDIFF-004` — For ordinary RGB in 256-color mode, quantize each channel at thresholds 48, 115, 155, 195, and 235 into levels `[0,95,135,175,215,255]`, producing cube index `16 + 36r + 6g + b`. Also consider the 24-step gray ramp (levels 8 through 238 by 10), choose the smaller squared RGB distance, use cube black below gray average 5, and keep an equal-channel cube corner above 244. This makes palette output deterministic across implementations.

`CDIFF-005` — Theme names use case-sensitive substring tests for `dark`, `ansi`, and `daltonized`. Base diff palette:

| Theme | Foreground | Addition line / word / decoration | Deletion line / word / decoration |
| --- | --- | --- | --- |
| ANSI | palette 7 | default / default / palette 10 | default / default / palette 9 |
| dark truecolor | `248,248,242` | `2,40,0` / `4,71,0` / `80,200,80` | `61,1,0` / `92,2,0` / `220,90,90` |
| dark 256 | same RGB foreground | palette 22 / palette 28 / `80,200,80` | same deletion RGB values, quantized |
| dark daltonized truecolor | same dark foreground | `0,27,41` / `0,48,71` / `81,160,200` | same dark deletion palette |
| dark daltonized 256 | same | palette 17 / palette 24 / `81,160,200` | same dark deletion palette |
| light | `51,51,51` | `220,255,220` / `178,255,178` / `36,138,61` | `255,220,220` / `255,199,199` / `207,34,46` |
| light daltonized | same light foreground | `219,237,255` / `179,217,255` / `36,87,138` | same light deletion palette |

## Language and syntax highlighting

`CDIFF-010` — Detect language in this order:

1. exact basename or stem: Dockerfile, Makefile, Rakefile, Gemfile, CMakeLists;
2. extension, only if the syntax engine recognizes it;
3. first line after stripping UTF-8 BOM: shebang containing bash or `/sh` -> bash, python -> python, node -> JavaScript, ruby -> Ruby, perl -> Perl; then `<?php` or `<?xml` prefixes;
4. otherwise plain text.

Exact filename mapping is Dockerfile→dockerfile, Makefile→makefile, Rakefile/Gemfile→ruby, and CMakeLists→cmake. Stem matching intentionally recognizes suffixed forms such as `Dockerfile.dev`.

`CDIFF-011` — Load the syntax engine lazily at first highlighted render. Highlight each logical line independently after appending a newline, then remove newline fragments before wrapping. Prefix content and stored parser stack are currently no-ops; multiline syntax state does not carry between lines. A highlighter exception or unknown language renders plain foreground.

`CDIFF-012` — If the syntax engine's token-tree shape is incompatible, log that mismatch only once per process and render plain text thereafter for affected lines. Do not crash or silently emit missing content. Deletion lines are deliberately plain text; additions and context lines receive syntax colors.

`CDIFF-013` — Default syntax theme report is `{theme:"ansi",source:null}` for ANSI theme names, `{theme:"Monokai Extended",source:null}` for dark names, and `{theme:"GitHub",source:null}` otherwise. Environment theme names are observed only for diagnostics and do not change the returned theme or source.

`CDIFF-014` — Preserve these scope-color groups:

- Monokai: keyword/operator `249,38,114`; storage `102,217,239`; built-in/type/title/attr `166,226,46`; literal/number/symbol `190,132,255`; string/regexp `230,219,116`; params `253,151,31`; comment/meta `117,113,94`; variable/property `255,255,255`; punctuation/substitution/default `248,248,242`.
- GitHub light: keyword/storage/operator `167,29,93`; built-in/type/literal/number/params/attr/variable/property/symbol `0,134,179`; string/regexp `24,54,145`; title/function `121,93,163`; class title `0,0,0`; comment/meta `150,152,150`; punctuation/substitution/default `51,51,51`.
- ANSI: keyword 13; storage/built-in/type 14; literal/number 12; string 10; title/function/class 11; comment/meta 8; unmapped default foreground 7.

Split storage-like words `const`, `let`, `var`, `function`, `class`, `type`, `interface`, `enum`, `namespace`, `module`, `def`, `fn`, `func`, `struct`, `trait`, and `impl` out of a generic keyword scope so they receive the storage color.

## Word ranges and hunk rendering

`CDIFF-020` — A hunk is `{oldStart,oldLines,newStart,newLines,lines[]}`. Interpret first character `+` as addition, `-` as deletion, and every other character as context; code is the remainder. Addition uses/increments new line, deletion old line, context displays new line then increments both. Compute gutter digits from `max(0, oldStart+oldLines-1, newStart+newLines-1)`.

`CDIFF-021` — Pair word diffs only in an adjacent run of one-or-more deletions immediately followed by one-or-more additions. Pair deletion `k` with addition `k` up to the smaller run length. Unpaired surplus lines receive only line background.

`CDIFF-022` — Tokenize each paired code string into runs of Unicode letters/digits/underscore, runs of whitespace, and one Unicode code point per punctuation token. Diff token arrays. Record changed ranges in underlying string offsets. If `(deletedLength + addedLength) / (oldLength + newLength) > 0.4`, discard both word-range sets; exactly 0.4 remains highlighted. A dim render skips all word diffing.

`CDIFF-023` — Hunk content width is `max(1, width - maxDigits - 3)` for marker, two gutter spaces, and digits. Transform each line in this order:

1. syntax-highlight addition/context or create plain deletion blocks;
2. remove newline fragments;
3. apply whole-line and optional word backgrounds;
4. wrap to content display width by Unicode code point and terminal cell width;
5. pad every physical addition/deletion segment to content width so background reaches the edge;
6. in ANSI theme only, dim deletion content;
7. prepend the marker to every wrapped segment;
8. prepend line-number gutter, using a blank number on continuation segments;
9. serialize escapes with full-render dim behavior.

If the first code point cannot fit an empty physical segment, emit that one code point even though it overflows, guaranteeing progress. Wrapping does not split escape sequences because it occurs on styled text blocks before serialization.

`CDIFF-024` — Context/ordinary line numbers are dimmed independently when the whole render is not dim. Addition/deletion gutter foreground uses its decoration color and line background. Unknown marker characters are not preserved as markers; they become context marker space.

## Whole-file rendering and caller cache

`CDIFF-030` — Whole-file coloring splits on newline and drops exactly one trailing empty element caused by a terminal newline. An empty string therefore produces zero rendered lines. Use one-based line numbers, content width `max(1, width - digitCount - 2)`, no marker, and no backgrounds. Syntax detection uses the first remaining line. Dim and reset lifecycle remains `CDIFF-002`.

`CDIFF-031` — The structured-diff caller floors width and clamps it to at least one. It falls back when the module is disabled, per-call/settings highlighting is disabled, or rendering returns unavailable. It never lets a negative/fractional width reach the colored renderer.

`CDIFF-032` — Cache rendered output by hunk object identity and key `theme|width|dim|gutterWidth|firstLine|filePath`. The compatibility key omits `fileContent`; this is currently safe only because prefix content is a no-op under `CDIFF-011`. If multiline state is later implemented, add content identity/version to the key before enabling it. At four cached variants, clear the hunk's entire inner cache before inserting the new variant.

`CDIFF-033` — In fullscreen, compute gutter width as `max(oldEnd,newEnd,1).digitCount + 3`. Split only when `0 < gutterWidth < totalWidth`; otherwise retain one raw ANSI column. ANSI-aware slicing preserves active styles across the cut. The gutter column is nonselectable from the left edge, content remains selectable, and both are single raw-render leaves rather than one layout row per diff line.

`CDIFF-034` — Keep the [catalog topology](../assets/architecture.drawio) and [specialized search/diff state and fallback flow](../assets/native-search-diff.drawio) consistent. The specialized diagram is normative for partial-index publication, stale-generation suppression, and fallback-before-ANSI-output ordering.

## Acceptance scenarios

1. **FIDX-A01 — Deduplication and partial publication.** Load `['A','a','A','']` and verify exact case-sensitive deduplication yields `A,a`; search during an asynchronous rebuild sees only the published ready prefix.
2. **FIDX-A02 — Empty-query segment cap.** Load more than 100 unique top-level segments in adversarial order. Empty search includes only the first 100 encountered, then sorts that subset by length and lexical order.
3. **FIDX-A03 — Query truncation and smart case.** Search a 65-character query and compare it with its first 64 characters. Results are identical. Add uppercase and verify smart-case changes matching.
4. **FIDX-A04 — Ranking compatibility.** Construct paths that exercise start, boundary, camel, consecutive, gap, and uint16 length bonuses. Verify internal ranking and rank-derived public scores, including the lowercase `test` penalty without reorder.
5. **FIDX-A05 — Tracked and untracked refresh.** Change one tracked file and one untracked file. Git-index modification triggers immediate tracked refresh; the untracked-only change appears through the 5-second floor and generation-safe merge.
6. **FIDX-A06 — Stale generation.** Clear caches while untracked discovery is running. Its late completion neither repopulates nor signals the cleared generation.
7. **CDIFF-A01 — Fallback before output.** Toggle explicit-false syntax environment, per-call skip, and settings disable. Each uses fallback with no leaked ANSI prefix.
8. **CDIFF-A02 — Color modes.** Render the same RGB theme in truecolor and 256 modes. Verify deterministic escape families and cube-versus-gray selection.
9. **CDIFF-A03 — Word-range threshold.** Render adjacent two-delete/one-add lines at change ratios exactly 0.4 and above 0.4, in dim and nondim modes. Verify pairing, threshold, and dim suppression.
10. **CDIFF-A04 — Width and ANSI safety.** Render a wide glyph into one remaining column. The renderer emits at least one code point, terminates, pads changed continuations, and never splits an ANSI escape.
11. **CDIFF-A05 — Whole-file trailing newline.** Render an empty whole file and a file ending in one newline. Verify zero lines for the former and no artificial trailing numbered line for the latter.
12. **CDIFF-A06 — Cache and fullscreen split.** Resize one hunk through five distinct cached variants. At the fifth insertion the prior inner variants are cleared; fullscreen gutter splitting occurs only when it leaves positive content width.

## Non-normative provenance

Evidence was specified from the synchronous/incremental file index, file-suggestion hook and repository discovery, colored-diff compatibility module, syntax wrapper, and structured-diff caller. Native-module names, syntax-library choice, source-language typed arrays, and UI framework cache types are not normative.
