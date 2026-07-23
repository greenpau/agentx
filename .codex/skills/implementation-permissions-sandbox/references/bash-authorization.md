# Bash authorization analyzer

This contract implements the Bash-specific authorization boundary: parsing, command segmentation, command-rule matching, environment and wrapper normalization, semantic injection checks, path extraction, read-only recognition, edit-mode recognition, sandbox selection, and the final allow/ask/deny result. It is intentionally independent of the implementation language. Requirement identifiers `BASH-AUTH-*` are normative and stable.

The companion [Bash authorization flow](../assets/bash-authorization.drawio) shows the normal and degraded paths. The general permission result protocol remains defined by [permission decision protocol](permission-decision.md); this document owns every Bash-specific refinement.

## Contents

- Boundary, inputs, and outcomes
- Primary AST analysis
- Supported and unsupported Bash grammar
- Legacy analysis
- Rule grammar and matching
- Environment assignments and wrapper commands
- Whole-request decision order
- Operator and compound-command handling
- Path and redirection analysis
- Read-only recognition
- `sed` recognition and simulated edits
- Permission-mode behavior
- Destructive-operation warnings
- Sandbox selection and execution handoff
- Cancellation, faults, and cross-platform behavior
- Normal scenarios
- Fault and adversarial scenarios
- Implementation checklist

## Boundary, inputs, and outcomes

`BASH-AUTH-001` — The analyzer receives a validated shell request containing at least the command text and optionally timeout, description, background intent, and an explicit request to disable sandboxing. It also receives a live permission context containing mode, scoped command/read/edit rules, working directory, sandbox policy, classifier configuration, noninteractive status, and a cancellation signal. Re-read mutable permission state after asynchronous classifiers or user interaction; do not assume the initial snapshot remains current.

`BASH-AUTH-002` — The analyzer returns one of four internal behaviors:

| Behavior | Meaning at this boundary |
| --- | --- |
| `allow` | Authorization is complete; carry the returned input into execution. |
| `deny` | Execution is forbidden; no approval surface may override this result. |
| `ask` | A safety or configured ask condition requires approval. |
| `passthrough` | No Bash-specific authorization was found; the common permission service must request approval or apply its mode policy. |

An outcome may include a source-attributed reason, exact or prefix rule suggestions, a pending classifier check, or an approved input replacement. An ordinary unresolved command is `passthrough`; a mandatory Bash safety review is `ask`.

`BASH-AUTH-003` — All accepted command segments need an explicit terminal authorization result. Across a compound request, precedence is `deny` over `ask` over `allow` over `passthrough`. A rule- or path-derived ask must not hide a deny on another segment.

`BASH-AUTH-004` — Input schema validation and general pre-tool hooks run before this analyzer. The analyzer itself performs no shell side effect. A later executor must receive the same authorized command, except for an explicitly approved internal replacement such as a simulated `sed` edit.

`BASH-AUTH-005` — Authorization suggestions are conveniences, never evidence that the current request is safe. Do not suggest persisting malformed, structurally unverified, injection-sensitive, or otherwise non-round-trippable text.

## Primary AST analysis

`BASH-AUTH-010` — Prefer a real Bash concrete-syntax parser and an allowlisted AST projection. Parsing returns exactly one of:

- `simple`: a bounded ordered list of simple commands, source spans, resolved argument vectors, static leading assignments, and redirections;
- `too-complex`: parsing succeeded or timed out, but some structure cannot be proven safe;
- `parse-unavailable`: the parser is disabled, gated off, shadow-only, or could not be loaded, so the legacy analyzer remains authoritative.

A parser resource limit, timeout, or cancellation internal to structural analysis is `too-complex`, not permission to fall back to a more permissive tokenizer.

`BASH-AUTH-011` — Before trusting the AST, reject to `too-complex` on lexical differentials that can desynchronize parser and shell behavior: carriage returns and other control characters, Unicode and zero-width whitespace, backslash followed by space or tab, dangerous mid-word line continuation, zsh `~[...]`, a word-initial zsh `=command`, and quoted braces embedded in an otherwise unquoted brace context.

`BASH-AUTH-012` — The structural allowlist may represent only a top-level program/list, simple pipelines, and redirected statements composed of simple commands. Recognized separators are newline, `;`, `&`, `&&`, `||`, `|`, and `|&`. Each executable command retains its original source span so downstream rule display can reproduce the submitted spelling.

`BASH-AUTH-013` — Reject unknown AST node types by construction. Control-flow constructs, function definitions, subshells and grouped commands, process substitution, arbitrary expansion, parser error nodes, and new grammar forms not explicitly modeled are `too-complex`. Never infer safety from the absence of a known-dangerous node.

`BASH-AUTH-014` — Static leading assignments may be tracked only when their names and literal values are unambiguous. Reject assignments involving dangerous special variables such as `IFS` or `PS4`, tilde expansion, executable substitution, unsupported arrays, or dynamically evaluated subscripts. A known literal variable may flow into a later argument only when its expansion is provably one inert argument; reject empty, word-splitting, or glob-producing expansions.

`BASH-AUTH-015` — Command substitution is supported only through a narrow extraction path that proves the inner command list independently and places a non-executable placeholder in the outer argument. Any substitution that cannot satisfy that proof is `too-complex`. Process substitution is never accepted by the primary analyzer.

`BASH-AUTH-016` — A quoted or backslash-escaped `cat` heredoc may be recognized when the delimiter is literal, the closing delimiter is an exact line, the construct occurs in the expected argument position, the body/remaining text uses the safe character subset, and recursively analyzed surrounding text is safe. Nested or ambiguous matches are rejected.

`BASH-AUTH-017` — After structural parsing, run semantic validation on normalized argument vectors. Reject shell evaluators and resolution-changing builtins including `eval`, `source`, `.`, `exec`, `command`, `builtin`, `fc`, `coproc`, `trap`, `enable`, `mapfile`, `readarray`, `hash`, `bind`, `complete`, `compgen`, `alias`, and arithmetic/evaluation-oriented builtins; also reject zsh module builtins, process-environment reads, dangerous subscript evaluation, and newline/hash differentials.

`BASH-AUTH-018` — Semantic wrapper stripping must use the argument vector, not string slicing. It understands the same safe wrapper family described in `BASH-AUTH-042`, including the fuller `stdbuf` option forms. An unknown option, missing option value, or wrapper that leaves no executable causes semantic failure.

`BASH-AUTH-019` — On `too-complex`, evaluate exact whole-command deny, ask, and allow rules, then prefix/wildcard deny rules. If none decides, return `ask` with no suggestions and the structural reason. On semantic failure, do the same whole-command checks and additionally check each parser-extracted simple-command span for prefix/wildcard deny; otherwise ask with no suggestions. Exact allow is therefore a conscious escape hatch, but a broad allow is not.

## Supported and unsupported Bash grammar

`BASH-AUTH-020` — An implementation may broaden the primary grammar only when it preserves an executable-by-executable proof. At minimum, the supported set is simple literal commands, static safe assignments, ordinary single/double quoting without executable expansion, supported redirections, bounded pipelines/lists, and the narrow heredoc/substitution forms above.

`BASH-AUTH-021` — The following remain unsupported for automatic authorization unless an exact rule explicitly decides the whole request: loops, conditionals, case statements, functions, arithmetic commands, compound assignments with evaluation, aliases defined in the same input, dynamically selected command names, sourced files, nested shells, subshell/group scopes, process substitution, uncontrolled command substitution, dynamic globbing that affects executable identity, and shell-specific extensions not represented by the parser.

`BASH-AUTH-022` — Unsupported grammar produces a user-review request, not an execution error. Syntax that the shell itself cannot parse also asks, without a persistence suggestion. A syntactically valid but unsupported construct reports the unsupported structural reason.

## Legacy analysis

`BASH-AUTH-030` — Use the legacy path only when the primary parser is genuinely unavailable or deliberately disabled. First run the legacy shell-word parser over the full command. A parse failure returns `ask` with no rule suggestion.

`BASH-AUTH-031` — Before trusting legacy splitting, run the security validators in deterministic order. Early special cases cover empty/incomplete input, the narrow safe heredoc form, and safe static git-commit text. The remaining checks cover, in order, risky `jq` construction, obfuscated flags, metacharacter differentials, variable expansion, quote/comment desynchronization, quoted newlines, carriage return, newline, `IFS`, process environment access, command/process/parameter substitutions, redirection, backslash whitespace/operators, Unicode whitespace, mid-word `#`, brace expansion, zsh forms, and malformed tokens.

`BASH-AUTH-032` — Mark parser-desynchronization findings separately from ordinary safety asks. A desynchronization ask blocks legacy segment authorization and offers no suggestion, unless an exact whole-command allow exists. Ordinary newline and output-redirection findings are not parser-desynchronization findings and may be handled by later segmentation/path checks.

`BASH-AUTH-033` — Preserve the explicit detector for the legacy word parser's single-quote/backslash differential. Never assume a generic shell-word library implements Bash quoting precisely.

`BASH-AUTH-034` — Split legacy compound input into executable spans using recognized operators, while respecting quotes to the extent the legacy parser can prove. Cap the resulting fan-out at 50 segments. More than 50 returns `ask` because exhaustive rule, path, and safety analysis is intentionally bounded.

## Rule grammar and matching

`BASH-AUTH-040` — Parse shell rule contents in this order:

1. a string matching `^(.+):\*$` is the legacy prefix form;
2. otherwise any unescaped `*` makes a wildcard form;
3. otherwise the rule is exact text.

An asterisk is unescaped when preceded by an even number of consecutive backslashes. A trailing `:*` is never interpreted as a wildcard form.

`BASH-AUTH-041` — Wildcard matching trims the pattern, treats `\*` as a literal asterisk and `\\` as a literal backslash, quotes every other regular-expression metacharacter, replaces unescaped `*` with any-character repetition, and anchors the entire command. Matching is case-sensitive and dot-all. When `git *` contains the sole unescaped wildcard, the trailing space-plus-arguments is optional, so it matches both `git` and `git status`; this exception does not apply to multi-wildcard patterns.

`BASH-AUTH-042` — Exact matching compares the trimmed submitted text and a version with safe output redirections removed. Prefix matching accepts only exact prefix equality or prefix followed by a literal space, and operates on redirection-stripped text. Wildcard matching uses the full candidate. Prefix/wildcard rules never authorize an unsplit compound command; each executable segment must be checked independently. Exact rules may deliberately cover the whole compound text.

`BASH-AUTH-043` — For every match, consider the original command plus fixed-point normalized candidates. Deny and ask candidates repeatedly strip all leading static environment assignments and safe wrappers. Allow candidates strip wrappers and only the safe environment variables in `BASH-AUTH-045`. This asymmetry is required: potentially executable-changing assignments may widen a deny but must never widen an allow. Mandatory path, mutation, and dangerous-removal analysis independently strips every static leading assignment to reveal the actual executable, then forces review when any stripped assignment was not allow-safe. Wrapper normalization consumes only each wrapper's validated option grammar and never guesses how many words precede the child command.

`BASH-AUTH-044` — A prefix that identifies an executable also matches `xargs <prefix>` only when `xargs` itself is bare or uses the recognized safe option surface. This supports commands such as `xargs git ...` without treating arbitrary `xargs` invocation as equivalent.

`BASH-AUTH-045` — The ordinary safe environment-name set is closed and includes:

- Go selection: `GOEXPERIMENT`, `GOOS`, `GOARCH`, `CGO_ENABLED`, `GO111MODULE`;
- Rust diagnostics: `RUST_BACKTRACE`, `RUST_LOG`;
- Node/Python/test mode: `NODE_ENV`, `PYTHONUNBUFFERED`, `PYTHONDONTWRITEBYTECODE`, `PYTEST_DISABLE_PLUGIN_AUTOLOAD`, `PYTEST_DEBUG`;
- model credential forwarding: `AGENTX_API_KEY`;
- locale/time/terminal/color/display controls: `LANG`, `LANGUAGE`, `LC_ALL`, `LC_CTYPE`, `LC_TIME`, `CHARSET`, `TERM`, `COLORTERM`, `NO_COLOR`, `FORCE_COLOR`, `TZ`, `LS_COLORS`, `LSCOLORS`, `GREP_COLOR`, `GREP_COLORS`, `GCC_COLORS`, `TIME_STYLE`, `BLOCK_SIZE`, `BLOCKSIZE`.

An internal build may add explicitly gated cloud/cluster selector names, but must keep them source-attributed and unavailable to ordinary users.

`BASH-AUTH-046` — Never treat executable-resolution or interpreter-injection variables as safe allow normalizers. At minimum exclude `PATH`, `LD_*`, `DYLD_*`, `PYTHONPATH`, `NODE_PATH`, `CLASSPATH`, `RUBYLIB`, `GOFLAGS`, `RUSTFLAGS`, `NODE_OPTIONS`, `HOME`, `TMPDIR`, `SHELL`, and `BASH_ENV`. Sandbox exclusion matching may strip arbitrary assignments only after blocklisting this binary-hijack family; exclusion remains non-authoritative.

`BASH-AUTH-047` — A static leading assignment parser accepts ordinary/append/array variable names and quoted or conservatively safe unquoted values. It rejects dollar expansion, backticks, shell operators, malformed quoting, and other executable syntax. The all-assignment normalizer and safe-assignment normalizer share this lexer but use different name policies.

`BASH-AUTH-048` — String-level wrapper normalization recognizes only:

- `timeout` with a valid duration and known GNU option/value forms;
- `time` with its supported harmless options;
- `nice`, bare, `-n value`, or legacy `-N`;
- `stdbuf` with safe fused `-i`, `-o`, or `-e` buffering values;
- bare `nohup`.

It removes leading safe assignments/comments first, then wrappers/comments. It does not resume assignment stripping after a wrapper; this conservative limitation prevents treating a wrapper's next assignment-looking token as an environment prefix. Argument-vector semantic normalization may support separate/long `stdbuf` forms because it is not string-ambiguous.

`BASH-AUTH-049` — Full-line comments and blank lines may be discarded only after lexical safety checks prove they are not embedded in a quoted multiline argument. If every line is a comment, retain the original text so the analyzer does not invent an empty command.

## Whole-request decision order

`BASH-AUTH-050` — Implement this ordering exactly; later broad allows must not bypass earlier structural, rule, or path checks:

1. Run the primary parse and semantic gates, or the legacy parse precheck.
2. If a sandbox is enabled, selected for this command, and configured for sandbox auto-allow, run the sandbox rule gate in `BASH-AUTH-090`.
3. Evaluate exact whole-command deny.
4. Run prompt-description deny and ask classifiers concurrently when enabled and not in auto mode; high-confidence deny wins, then high-confidence ask.
5. Analyze non-simple operators and recursively authorize each pipeline/list segment.
6. On the legacy path, enforce the parser-desynchronization gate.
7. Split into parser-derived or legacy command spans and remove an exact no-op `cd <current-working-directory>` prefix.
8. Enforce the segment bound, multiple-directory-change guard, and directory-change-plus-git guard.
9. Evaluate every segment's exact and prefix deny/ask, path constraints, exact/prefix allow, `sed`, permission mode, read-only, and passthrough result.
10. Reduce all segment results with deny precedence.
11. Revalidate original-command output redirections and paths because splitting may have removed them.
12. Combine path asks with segment asks without hiding independent operations.
13. Honor exact whole-command allow only after those checks.
14. Allow when every segment is allowed and injection safety is established.
15. Otherwise build bounded exact/prefix suggestions and attach an optional pending allow classifier.

`BASH-AUTH-051` — Prompt-description classifiers receive the original command, working directory, applicable natural-language descriptions, cancellation signal, and noninteractive flag. Run deny and ask checks concurrently; only a high-confidence match acts. If cancelled after either call, throw the common abort error instead of manufacturing a permission result.

`BASH-AUTH-052` — A speculative prompt-description allow classifier may start before hooks finish. Cache it by exact command text so concurrent consumers share one request. It is unavailable in auto mode, bypass mode, when the feature is off, or when there are no allow descriptions. Only a high-confidence match auto-approves, and only if the user/leader has not already claimed the decision. Expected cancellation errors are swallowed; unexpected faults propagate.

## Operator and compound-command handling

`BASH-AUTH-060` — Recursively authorize each executable side of `|`, `|&`, `&&`, `||`, `;`, newline, and supported background separators. A subshell/group or operator form that cannot be represented as independently ordered simple commands asks. Do not let a safe left pipeline stage authorize the right stage.

`BASH-AUTH-061` — If recursive operator handling returns allow, re-run whole-original-command legacy safety when on the legacy path and always re-run original redirection/path analysis. Segment processing is allowed to remove redirections for command identity, so this second check is load-bearing.

`BASH-AUTH-062` — Detect normalized directory changes after safe assignment/wrapper stripping. Treat `cd`, `pushd`, and `popd` as directory-changing. More than one such segment asks. Any compound request containing a directory change and normalized `git`—including `xargs git`—asks before read-only or broad command rules can act.

`BASH-AUTH-063` — Thread a `compoundCommandHasCd` fact into every path and mode check. Paths following a directory change would otherwise be resolved against the validator's stale working directory. A directory change combined with a redirect or other path operation therefore asks even when the lexical path appears inside the current project.

`BASH-AUTH-064` — A duplicate segment string may be displayed once in a map-backed explanation, but authorization still evaluates every occurrence in order. Do not use display deduplication to skip execution-side checks.

`BASH-AUTH-065` — Compound suggestions contain at most five distinct allow rules, preserving left-to-right segment order. If a mandatory safety ask has no path suggestion, synthesize an exact command suggestion only for display completeness; do not synthesize around an explicit configured ask rule.

## Path and redirection analysis

`BASH-AUTH-070` — Recognize these path-bearing command families and operation classes:

| Operation | Commands |
| --- | --- |
| read | `cd`, `ls`, `find`, `cat`, `head`, `tail`, `sort`, `uniq`, `wc`, `cut`, `paste`, `column`, `tr`, `file`, `stat`, `diff`, `awk`, `strings`, `hexdump`, `od`, `base64`, `nl`, `grep`, `rg`, `git`, `jq`, common checksum tools |
| create | `mkdir`, `touch` |
| write | `rm`, `rmdir`, `mv`, `cp`, in-place `sed` |

Unknown commands receive no path-based auto-authorization.

`BASH-AUTH-071` — Generic positional extraction honors `--` and treats every later argument as positional. Command-specific extraction then applies:

- `cd` joins its target or uses home; `ls` uses nonflags or `.`;
- `find` extracts starting roots before its first non-global predicate, honors global `-H/-L/-P`, recognizes path-taking predicates such as `-newer*`, treats post-`--` values conservatively as paths, and defaults to `.`;
- `tr` skips its one or two character-set operands;
- `grep`/`rg` skip the pattern and known option values, use later values as paths, and default recursive searches to `.`;
- `sed` distinguishes program text, `-e` expressions, `-f` script files, and data files;
- `jq` distinguishes options, filter, and files;
- `git` extracts paths only for `diff --no-index`, taking the first two operands;
- any option on `mv` or `cp` forces review because target-directory options alter positional meaning;
- `-t`, attached `-tDIR`, `--target-directory DIR`, and `--target-directory=DIR` on `cp`, `mv`, `install`, or `ln` project the directory as a write and every source as a read before forcing review.

A `git` invocation outside the proven read-only subcommand surface always requires review. Before that review, conservatively project the static values of `-C`, `-f`/`--file`, `--git-dir`, `--work-tree`, and `--directory`, plus explicit pathspec operands, so a protected resource cannot disappear behind Git option grammar.

`BASH-AUTH-072` — Read paths may suggest a directory-scoped read rule. Create/write paths may suggest edit rules and may use `acceptEdits` only within its allowed scope. A path deny returns deny; every other unresolved, dynamic, ambiguous, or out-of-scope path returns ask.

`BASH-AUTH-073` — Parse output redirects from the AST when available. Treat `>`, `>|`, and `&>` as create/write; `>>` and `&>>` as append/write; treat `>&digits` as descriptor duplication and any other `>&target` as a file write. Input redirection is read-side and does not itself create an edit. `/dev/null` is a safe output sink. Dynamic targets ask.

`BASH-AUTH-074` — On the legacy path, reparse redirects conservatively and retain the known quote/backslash guard. Keep `>|` intact as one write-redirection operator rather than splitting its pipe as a pipeline. Resolve every relative static target against the authorized command working directory. If the redirect parser cannot prove a static target, ask rather than omit the path.

`BASH-AUTH-075` — `rm` and `rmdir` targeting root, home, a system root, a broad wildcard, or an immediate broad-root child produce a mandatory ask with no persistence suggestion. This Bash policy is intentionally an ask; the PowerShell dangerous-removal policy is a hard deny.

## Read-only recognition

`BASH-AUTH-080` — Read-only auto-allow requires a successful shell-word parse, a whole-command legacy security `passthrough`, no vulnerable UNC form, and every segment individually recognized as read-only. It is disabled for directory-change-plus-git, a current directory that resembles a bare repository, a compound that creates git internal files before running git, or a sandboxed git invocation outside the original working directory.

`BASH-AUTH-081` — Before command lookup, remove only a terminal `2>&1`. Reject unquoted globs, dollar variables, vulnerable UNC paths, and any unsupported token. Dollar text inside single quotes is literal; other variable expansion is not read-only proof.

`BASH-AUTH-082` — The read-only catalog is a closed set combining declarative command/flag specifications and tightly anchored regular expressions. It covers vetted forms of Git, ripgrep, Pyright, Docker, checksums, file/grep/search/display utilities, `man`/help, process/network inspection, `sed`, and a small internal-only network catalog. On Windows, remove `xargs` from auto-allow because file-content/UNC behavior differs.

`BASH-AUTH-083` — Declarative flag validation rejects unknown flags, missing or malformed flag values, shell-operator tokens, variables, brace expansion, and callback failures. `git ls-remote` rejects URL-like, SSH-like, colon-bearing, `@`-bearing, or variable-bearing positionals to prevent data exfiltration.

`BASH-AUTH-084` — `xargs` is read-only only when its executed command is one of `echo`, `printf`, `wc`, `grep`, `head`, or `tail`, and its own options are from the unambiguous safe set (`-I`, numeric `-n/-P/-L/-s`, `-E`, `-0`, `-t`, `-r`, `-x`, `-d`). Do not accept optional-value legacy forms whose argument consumption is ambiguous.

`BASH-AUTH-085` — Regex-recognized commands use a restricted simple-command alphabet excluding substitution, redirects, grouping, braces, operators, and line breaks. Preserve deliberate omissions: recursive/output-writing `tree`, `fd --exec`, file magic compilation, date/hostname mutation, dangerous terminal capabilities, and network commands with kill/dump/filter/netns behavior are not read-only.

## `sed` recognition and simulated edits

`BASH-AUTH-086` — A read-only `sed` form is either:

1. `-n`, `--quiet`, or `--silent`, optional safe regex/null/portable flags, and only semicolon-separated `p`, `Np`, or `N,Mp` expressions with file operands; or
2. exactly one slash-delimited substitution with exactly two unescaped delimiters and only `g`, `p`, `i/I`, `m/M`, plus at most one numeric occurrence flag, with no file operand in ordinary read-only mode.

`BASH-AUTH-087` — In `acceptEdits`, permit an in-place flag and file operands only for the single safe substitution form. Reject semicolons in substitutions and the shared dangerous-expression set: non-ASCII ambiguity, braces, newlines, comments, negation, tilde or offset ranges, alternate/backslash delimiter tricks, malformed substitution, write commands `w/W`, execute commands/flags `e/E`, and suspicious transliteration. A failure asks even in edit mode.

`BASH-AUTH-088` — The preview/simulated-edit parser is narrower than execution recognition: one file, no glob, no unknown option, no multiple expression/file ambiguity, slash delimiter only, safe flags, and supported basic-regex conversion. If approved, inject a private simulated-edit structure only after permission resolution, then apply the displayed edit directly so preview and write are identical. Model-supplied private edit metadata must be rejected or stripped.

## Permission-mode behavior

`BASH-AUTH-089` — Mode evaluation occurs after explicit rules and path safety. `acceptEdits` may auto-allow the vetted write families and safe in-place `sed` only when their paths are authorized and no compound directory-change hazard exists. `dontAsk` and bypass behavior are applied by the common permission layer; neither may erase an explicit deny or mandatory Bash safety ask.

## Destructive-operation warnings

`BASH-AUTH-091` — Produce an informational warning for the first recognized destructive operation, independent of authorization: `git reset --hard`, force push, non-dry-run `git clean -f`, checkout/restore of `.`, stash drop/clear, forced branch deletion, `--no-verify`, amend, recursive/forced `rm`, broad SQL drop/truncate/delete, `kubectl delete`, or `terraform destroy`. A warning is presentation metadata, never an allow or deny.

## Sandbox selection and execution handoff

`BASH-AUTH-090` — Sandbox auto-allow is available only when sandboxing is enabled, the command will in fact use it, and `autoAllowBashIfSandboxed` is enabled. Evaluate full-command deny, every segment's deny, every segment's ask, then full-command ask. Deny wins globally. If no explicit deny/ask matches, return allow with a sandbox-specific reason. This shortcut does not consult broad command allow rules because the isolation policy itself supplies the allow.

`BASH-AUTH-092` — Sandbox selection returns false when isolation is disabled/unsupported, the request explicitly disables it and policy permits unsandboxed commands, the command is absent, or an excluded command matches. Explicit disablement is ignored when policy forbids unsandboxed execution.

`BASH-AUTH-093` — User sandbox exclusions use the same exact/prefix/wildcard grammar over every legacy-split segment and fixed-point assignment/wrapper candidates. Internal builds may also exclude configured command names or substrings. Parsing failure means “not excluded,” so other validation still runs.

`BASH-AUTH-094` — Sandbox exclusion is a convenience switch, not authorization. Preserve the specified observable behavior that if any segment matches an exclusion, the shared selector marks the whole submitted compound command unsandboxed; permissions must therefore still authorize every segment. Do not “repair” this compatibility behavior silently during a language port.

`BASH-AUTH-095` — The process executor recomputes sandbox selection from the authorized input immediately before spawn. The authorization reason and execution selection must agree; if an auto-allowed request can no longer be sandboxed because state changed, fail or reauthorize rather than running unsandboxed.

## Cancellation, faults, and cross-platform behavior

`BASH-AUTH-100` — Check cancellation after every awaited parser/classifier/prefix operation. Classifier cancellation throws the common abort result and starts no process. A structural parser timeout yields `too-complex`; parser module absence yields `parse-unavailable`; neither is silently considered safe.

`BASH-AUTH-101` — Cache speculative classifier work only by immutable command identity and remove/ignore failed work as defined by the classifier service. Never reuse a result after command modification, permission-context change that alters descriptions, or user ownership of the decision.

`BASH-AUTH-102` — Bash matching is case-sensitive on every platform. Working-directory no-op filtering additionally recognizes a Windows path converted to the POSIX spelling used by MSYS/MINGW shells. Filesystem path comparison follows the active path contract rather than command-name case rules.

`BASH-AUTH-103` — If the sandbox is required by policy but unavailable, the shared sandbox boundary must reject before spawn. If sandbox wrapping or shell spawn fails, return an explicit failed tool result; do not retry unsandboxed.

`BASH-AUTH-104` — A command authorized for a background task retains its original authorization and sandbox decision for that spawn. Backgrounding, timeout, progress collection, interruption, and output persistence do not rerun or weaken rule analysis. Cancellation produces an explicit interrupted result.

## Normal scenarios

`BASH-AUTH-N001` — For `git status` in a normal repository, the AST yields one simple command, semantic checks pass, no deny/ask rule matches, path checks pass, and the read-only catalog returns allow.

`BASH-AUTH-N002` — For `NO_COLOR=1 timeout 30 git status`, safe assignment and wrapper normalization produce `git status` for rule/read-only matching while the executor retains the original command text.

`BASH-AUTH-N003` — For `cat input.txt | sort > output.txt`, authorize both pipeline commands, then check the original output redirection as a write. A read-only pipeline does not bypass the edit/path decision for `output.txt`.

`BASH-AUTH-N004` — With a deny rule `rm:*` and an ask rule matching the whole `echo ok && rm -rf build`, segment analysis returns deny even though the whole-command ask was discovered first.

`BASH-AUTH-N005` — When sandbox auto-allow is active for `make test`, no deny/ask matches, and sandbox selection is true, authorization returns allow with a sandbox reason; execution still wraps the actual spawn.

`BASH-AUTH-N006` — In `acceptEdits`, a provably safe `sed -i 's/old/new/g' file.txt` may become a simulated direct edit after path approval. The rule engine never trusts model-supplied simulated-edit metadata.

## Fault and adversarial scenarios

`BASH-AUTH-F001` — `echo ok | eval "$payload"` either becomes `too-complex`/semantic failure or has the `eval` span denied. It cannot be reduced to an allowed `echo` pipeline.

`BASH-AUTH-F002` — `PATH=/attacker git status` does not inherit an allow rule for `git:*`; deny/ask normalization may still see `git status`, but allow normalization retains the executable-changing assignment.

`BASH-AUTH-F003` — `cd /tmp/untrusted && git status` asks even when `git status` is read-only and individually allowed, preventing bare-repository and hook attacks.

`BASH-AUTH-F004` — A primary parser timeout returns `too-complex`, checks exact rules and broad deny, then asks without suggestions. It never falls through to the legacy tokenizer.

`BASH-AUTH-F005` — `echo x > $(compute-target)` is structurally unsupported or legacy-injection-sensitive. A dynamic redirect target cannot receive a file rule suggestion.

`BASH-AUTH-F006` — More than 50 legacy-split segments ask with a bounded-analysis reason. No prefix classifier or read-only loop is allowed to process an attacker-controlled unbounded fan-out.

`BASH-AUTH-F007` — If a user exclusion matches one segment of `trusted-tool && write-protected-file`, execution may select unsandboxed compatibility behavior, but the write still undergoes command and path authorization. Exclusion never becomes an allow.

`BASH-AUTH-F008` — If cancellation arrives while prompt classifiers are running, discard both results and return the common aborted tool outcome. Never present a stale approval prompt or start the shell.

## Implementation checklist

- Maintain fixtures for every supported separator, quote style, redirection, safe wrapper, and safe assignment.
- Differential-test the primary parser against the real Bash executable for argument vectors and command spans.
- Fuzz unknown AST node kinds and prove they select `too-complex`.
- Test deny/ask/allow precedence over every segment position in a compound request.
- Test allow-normalization asymmetry with `PATH`, loader variables, interpreter options, and harmless display variables.
- Test original-command redirection validation after pipeline splitting.
- Test cwd changes with read, write, redirect, git, `pushd`, and `popd` followers.
- Test rule wildcard escaping and the sole trailing-wildcard optional-argument rule.
- Test parser absence, timeout, malformed input, classifier failure, cancellation, and sandbox-state change before spawn.
- Keep the normal and fault scenarios above executable as conformance cases in the target language.

## Reference provenance

This contract was implemented from the Bash tool permission, security, path, read-only, mode, destructive-warning, `sed`, command-helper, sandbox-selection, shared shell-rule-matching, Bash AST/parser, and direct tool-execution responsibilities. Source file names are provenance only; the requirements above are the project contract.
