# PowerShell authorization analyzer

This contract implements the PowerShell-specific authorization boundary: native AST acquisition, statement and command projection, case-insensitive rule matching, alias and module normalization, parse-failure deny preservation, security validation, path binding, read-only recognition, edit-mode recognition, destructive-path denial, sandbox handoff, and final allow/ask/deny reduction. It is language-neutral. Requirement identifiers `PS-AUTH-*` are normative and stable.

The companion [PowerShell authorization flow](../assets/powershell-authorization.drawio) shows parser, normal, degraded, and platform branches. The common outcome and approval protocol remains defined by [permission decision protocol](permission-decision.md).

## Contents

- Boundary, inputs, and outcomes
- Native parser process and cache
- AST projection and structural coverage
- Command identity, aliases, and parameter syntax
- Rule grammar and matching
- Parse-failure fallback
- Whole-request decision order
- Security validators
- Path extraction and authorization
- Git and path-resolution transition guards
- Read-only recognition
- Read-only catalog and safe flags
- `acceptEdits` mode
- Destructive warnings and hard denials
- Sandbox, execution, cancellation, and faults
- Normal scenarios
- Fault and adversarial scenarios
- Unsupported grammar and extension rules
- Implementation checklist

## Boundary, inputs, and outcomes

`PS-AUTH-001` — The analyzer receives a validated request containing command text and optional timeout/background/sandbox-override fields plus a live permission context. It returns `allow`, `deny`, `ask`, or `passthrough` with the meanings in `BASH-AUTH-002`. Empty or whitespace-only command text returns allow with an explicit “empty command” reason.

`PS-AUTH-002` — PowerShell authorization is case-insensitive for cmdlet, alias, rule, and flag comparisons, but preserves original text for display and exact execution. Filesystem case behavior remains platform-dependent under the common path contract.

`PS-AUTH-003` — After a successful parse, collect all applicable decisions and reduce them as `deny` over `ask` over `allow` over `passthrough`. Preserve the first result of each behavior for its user-facing message. This collect-then-reduce invariant prevents an early UNC/security/path ask from hiding a later subcommand or path deny.

`PS-AUTH-004` — Parser output is untrusted protocol data even though it comes from a local helper process. Normalize singleton-vs-array JSON shapes, reject malformed JSON, map unknown node kinds to conservative categories, and do not execute any submitted command during parsing.

## Native parser process and cache

`PS-AUTH-010` — Parse with the installed PowerShell runtime's native language parser, not a POSIX tokenizer and not an independently approximated grammar. Invoke a generated parser program with no profile, no interactive mode, and no logo. The user command is first UTF-8 Base64 encoded into a data variable; the full parser program is then UTF-16LE Base64 encoded for the runtime's encoded-command switch. The command is never interpolated as executable source into the parser program.

`PS-AUTH-011` — Enforce a UTF-8 byte limit before spawn. On native Windows, derive the limit from the 32,767-character process command-line cap, fixed argument overhead, current parser-program length, two Base64 expansions, and a 100-byte safety margin; recalculate whenever the embedded program changes. On Unix-like systems retain a 4,500-byte command limit, safely below per-argument limits. A length violation returns invalid parse error `CommandTooLong` and never spawns.

`PS-AUTH-012` — Parser timeout defaults to five seconds and may be overridden only by a positive configured value. Retry exactly once after a timeout. Two timeouts return invalid parse error `PwshTimeout`. A spawn exception, nonzero helper exit, empty stdout, or invalid JSON returns `PwshSpawnError`, `PwshError`, `EmptyOutput`, or `InvalidJson` respectively.

`PS-AUTH-013` — Memoize parsing by exact command text with an LRU capacity of 256. Concurrent callers share the same in-flight parse. Evict transient failures (`PwshSpawnError`, `PwshError`, `PwshTimeout`, `EmptyOutput`, `InvalidJson`) after delivery so a later call may retry. Cache deterministic syntax and length failures.

`PS-AUTH-014` — If no PowerShell executable is available, return invalid parse error `NoPowerShell`. Authorization still applies raw deny/ask rules and the degraded deny scan in `PS-AUTH-040`; execution later reports runtime absence without pretending the command ran.

## AST projection and structural coverage

`PS-AUTH-020` — The parser protocol reports:

- parse validity and native error message/identifier;
- all top-level statements and their exact source extents;
- pipeline elements, command elements, and redirections;
- recursively nested commands for every statement;
- all variable references and whether each is splatted;
- stop-parsing token presence;
- one-level children of colon-bound command parameters;
- type literals;
- security-pattern summary flags;
- `using` statements and script requirements.

`PS-AUTH-021` — Walk Begin, Process, End, Clean, and DynamicParam blocks. Walk traps separately. A top-level Param block is a sibling of those blocks, so recursively surface commands, redirections, and security patterns from default values and validation attributes. Surface `using module`, `using assembly`, and `#Requires` as independent booleans because they are not ordinary block statements.

`PS-AUTH-022` — Classify statements into pipeline, pipeline-chain, assignment, conditional, loop, switch, try, trap, function, data, or unknown. For a normal pipeline, retain every direct pipeline element. For all statement kinds, recursively collect nested command ASTs and every file redirection, including those beneath parameter values, parenthesized expressions, hashtables, and control flow.

`PS-AUTH-023` — Map argument elements into `StringConstant`, `Parameter`, `Variable`, `ScriptBlock`, `SubExpression`, `ExpandableString`, `MemberInvocation`, or `Other`. Numeric constants are inert `StringConstant`. Array and parenthesized expressions are `SubExpression`. Unknown forms are `Other`. For a colon-bound parameter, retain its argument child's mapped type so `-InputObject:$env:SECRET` cannot masquerade as a harmless parameter token.

`PS-AUTH-024` — A simple-statement proof succeeds only for a nonempty `PipelineAst` whose every pipeline element is `CommandAst`. Assignment, expression-source pipeline, pipeline chain, control flow, and any future statement kind fail closed. Nested commands disqualify read-only classification even if direct commands appear safe.

`PS-AUTH-025` — File redirections distinguish create vs append and output/error/all streams. Merging redirects such as `2>&1` are not file writes. `$null` and `${null}` are immutable null sinks; other spellings are ordinary targets. Dedupe direct and recursively rediscovered redirections by operator and target where practical, but correctness must not depend on the count.

`PS-AUTH-026` — Derive global flags for script blocks, sub/array/parenthesized expressions, expandable strings, member invocation, assignments, splatting, and stop-parsing. Combine element-type traversal with recursively reported security patterns so constructs hidden below non-pipeline statements remain visible.

## Command identity, aliases, and parameter syntax

`PS-AUTH-030` — Compute `nameType` from the raw command token before stripping a module prefix. A `Verb-Noun` ASCII token is a cmdlet; any token containing `.`, `/`, or `\` is an application; others are unknown. Any non-ASCII character in command position is conservatively an application. This gate prevents `scripts\Get-Content` from inheriting cmdlet authorization after the display name becomes `Get-Content`.

`PS-AUTH-031` — Strip a module qualifier only when the token is not a drive path, UNC path, `./`-style path, or `../`-style path. Strip surrounding quotes from an invocation-operator command name. Preserve the raw token separately for application gating and safe-executable exceptions.

`PS-AUTH-032` — Canonical alias resolution is a closed, prototype-free map. It covers navigation, content/item operations, process/service/output/help, invoke/session, formatting, write/export/property, and search aliases. Important mappings include `ls/dir/gci -> Get-ChildItem`, `cat/type/gc -> Get-Content`, `cd/sl/chdir -> Set-Location`, `rm/del/ri/rmdir -> Remove-Item`, `mv/mi -> Move-Item`, `cp/ci/cpi -> Copy-Item`, `iex -> Invoke-Expression`, `iwr -> Invoke-WebRequest`, and `irm -> Invoke-RestMethod`. Omit ambiguous aliases that collide with native executables in modern PowerShell, notably `sc`, `sort`, `curl`, and `wget`.

`PS-AUTH-033` — For a path-free name only, remove Windows executable suffix `.exe`, `.cmd`, `.bat`, or `.com` before canonical external-command checks. Do not strip `.ps1`, and do not strip a suffix from a name containing a path separator. Thus `git.exe` receives git safety checks while `scripts\git.exe` remains an application path.

`PS-AUTH-034` — Treat ASCII hyphen, en dash, em dash, and horizontal bar as PowerShell parameter prefixes whenever AST element type is unavailable; AST `Parameter` is authoritative when available. PowerShell 5.1 command-line switches may additionally use `/` where the specific validator states so. Strip backtick escapes and colon-bound values before parameter-name comparisons.

`PS-AUTH-035` — Accept a parameter abbreviation only when it lies between the documented minimum unambiguous prefix and full parameter name. Apply this to encoded command, file execution, item type, and other safety-sensitive parameters; a plain `startsWith` rule without a minimum is not sufficient.

## Rule grammar and matching

`PS-AUTH-036` — Use the shared exact, legacy `prefix:*`, and unescaped-wildcard grammar from `BASH-AUTH-040` and `BASH-AUTH-041`, with case-insensitive matching. Exact suggestions are omitted when command text contains a newline or literal `*`, because such rules cannot round-trip safely.

`PS-AUTH-037` — Match a rule against the original trimmed command, a command whose first token is canonicalized through alias resolution, and a form where the rule's first token is canonicalized to the input. Normalize the separator between command name and arguments to one space, including tab-separated input. For exact canonical comparison, normalize the remainder on both sides.

`PS-AUTH-038` — Strip module prefixes from input names. For deny and ask rules, also strip a module prefix from the rule name, accepting safe overmatch. For allow rules, do not strip the rule's module prefix: an allow for `ModuleA\Get-Thing` must not authorize `ModuleB\Get-Thing`.

`PS-AUTH-039` — Prefix matches require equality or a literal space boundary after the prefix. Wildcards are considered only in prefix mode. Whole-command exact matching checks deny, then ask, then allow. Prefix matching checks deny, then ask, then a prior exact allow, then prefix/wildcard allow.

## Parse-failure fallback

`PS-AUTH-040` — Raw exact and prefix/wildcard deny rules run before checking parser validity. Defer a prefix ask or vulnerable UNC ask so a successful parse can still discover a later deny. If parsing fails, a deferred ask retains its rule-attributed message after the degraded deny scan.

`PS-AUTH-041` — On invalid parse, an exact allow may return only when no deferred ask exists and the raw first token is not classified as an application. This preserves explicit cmdlet authorization during parser outage without letting a local script/executable borrow cmdlet identity.

`PS-AUTH-042` — The degraded deny scanner:

1. removes backtick-newline continuation and remaining backticks;
2. splits conservatively on `;`, pipe, newline, carriage return, braces, parentheses, and `&`;
3. strips repeated assignment prefixes such as `$x = $y +=`;
4. strips invocation or dot-source prefixes and surrounding command-name quotes;
5. canonicalizes aliases through the normal matcher;
6. applies prefix/wildcard deny to every fragment;
7. independently hard-denies a dangerous positional `Remove-Item` target.

It may over-deny text inside strings/comments because the parser is already degraded; this is an explicit fail-safe limitation.

`PS-AUTH-043` — If no fallback deny or deferred ask fires, invalid syntax/parser failure returns `ask` with the native or helper error and no persistence suggestions. Parser failure never becomes read-only or `acceptEdits` allow.

## Whole-request decision order

`PS-AUTH-050` — Implement the successful-parse flow as follows:

1. retain any deferred raw ask or UNC ask;
2. run the ordered security validators;
3. ask for `using` statements and script requirements;
4. inspect resolved argument text for non-filesystem providers and escaped UNC forms;
5. evaluate deny/ask rules for every direct and nested command in both raw and canonical form;
6. add directory-change-plus-git, bare-repository, git-internal write/extraction, and `.git/` write asks;
7. run path constraints, including stale-working-directory state;
8. admit an exact whole-command allow only when every subcommand is non-application and no argument can leak an evaluated value;
9. add read-only allow, file-redirection ask, and mode allow;
10. reduce deny, ask, allow, passthrough;
11. if unresolved, independently process each subcommand, then run the statement fail-closed gate and build exact suggestions.

`PS-AUTH-051` — Direct and nested commands both participate in deny/ask checks. Build a canonical subcommand from AST command name plus resolved arguments so invocation operators, quote-stripped names, tabs, aliases, and module prefixes cannot bypass a rule.

`PS-AUTH-052` — Safe output commands are still included in the early deny/ask pass. Only the later approval-collection loop may filter a zero-argument, non-application safe output sink. This preserves deny precedence.

`PS-AUTH-053` — In the final subcommand loop, explicit allow does not auto-allow an application, an argument-leaking command, or any compound containing link creation. Built-in read-only requires a provably safe parent statement, no compound directory-change/link hazard, and an allowlisted command. Per-subcommand `acceptEdits` uses a one-statement synthetic parse but remains disabled when compound context changes path resolution.

`PS-AUTH-054` — After the loop, add the complete statement text for every non-provably-safe statement not already represented by a pushed subcommand. This catches bare variable/expression statements and non-command pipeline sources. Track a statement as represented only when a subcommand is actually pushed for approval, not merely visited or auto-allowed.

`PS-AUTH-055` — If no subcommands need approval but any script block exists, ask because filtered formatting commands must not hide executable block content. Otherwise allow only when every surfaced operation was independently allowed. Suggestions are exact per unresolved subcommand; do not create a wildcard suggestion implicitly.

## Security validators

`PS-AUTH-060` — Run the following validators in this exact first-match order; each returns ask or passthrough:

1. `Invoke-Expression`/`iex`;
2. dynamic command-name AST element;
3. encoded-command parameter abbreviations, including Unicode dash and applicable slash forms;
4. nested `pwsh`/`powershell` command or file execution;
5. download cradles;
6. downloader utilities;
7. `Add-Type`;
8. COM or unsafe constrained-language `New-Object` type;
9. dangerous `-FilePath` execution;
10. `Invoke-Item`;
11. scheduled-task creation;
12. `ForEach-Object -MemberName` invocation;
13. `Start-Process`, including `RunAs` and nested PowerShell;
14. script-block injection;
15. subexpressions;
16. expandable strings;
17. splatting;
18. stop-parsing `--%`;
19. member invocation;
20. non-allowlisted type literals;
21. environment-variable mutation;
22. module loading/install/download;
23. alias/variable runtime-state mutation;
24. WMI/CIM process spawning.

`PS-AUTH-061` — Downloader detection covers web request/rest aliases, downloader-oriented `New-Object`, `Start-BitsTransfer`, `certutil` URL cache, and `bitsadmin` transfer. A download combined with expression evaluation is not required for utilities whose operation itself performs a download.

`PS-AUTH-062` — `Start-Process` validation understands colon-bound values, quoting, backtick escapes, alternate parameter prefixes, `-Verb RunAs`, and nested PowerShell names. Dangerous script-block consumers include every cmdlet capable of executing a block or file; narrow filtering/formatting consumers do not become safe until the statement and argument gates also pass.

`PS-AUTH-063` — Type-literal validation uses a constrained-language allowlist of inert primitives and safe accelerators. Exclude network/provider-capable types such as ADSI, WMI, and CIM session objects. An unknown literal asks. Static or instance member invocation asks independently even when the type itself is allowed.

`PS-AUTH-064` — Environment writes include assignments to `env:` variables and environment-aware write cmdlets. Module loading includes import/install/save/update/script installation surfaces. Runtime alias or variable definition asks because it changes future command resolution or default parameters. WMI/CIM method invocation asks because it can spawn a process.

## Path extraction and authorization

`PS-AUTH-070` — Maintain a closed per-cmdlet path-binding catalog. The operation families are:

| Operation | Cmdlets |
| --- | --- |
| write/create | `Set/Add/Remove/Clear-Content`, `Out-File`, `Tee-Object`, `Export-Csv`, `Export-Clixml`, `New/Copy/Move/Rename/Set-Item`, `Invoke-WebRequest`, `Invoke-RestMethod`, `Expand/Compress-Archive`, `Set/New/Remove-ItemProperty`, `Clear-Item`, `Export-Alias` |
| read/navigation | `Get-Content`, `Get-ChildItem`, `Get-Item`, `Get-ItemProperty`, `Get-ItemPropertyValue`, `Get-FileHash`, `Get-Acl`, `Format-Hex`, `Test/Resolve/Convert-Path`, `Select-String`, `Set/Push/Pop-Location`, `Select-Xml`, `Get-WinEvent` |

Every entry declares path parameters, harmless switches, non-path value parameters, positional skip, optional-write behavior, and any leaf-only path parameter. An unknown parameter makes the invocation unvalidatable and asks; it must never consume the following path silently.

`PS-AUTH-071` — Recognized path parameter aliases include `-Path`, `-LiteralPath`, `-PSPath`, and `-LP` where the cmdlet supports them, plus command-specific names such as `-OutFile`, `-InFile`, `-Destination`, `-TargetPath`, `-ArchivePath`, `-FilePath`, and event-log path options. Merge common switches `Verbose`, `Debug`, `WhatIf`, `Confirm` and common value parameters `ErrorAction`, `WarningAction`, `InformationAction`, `ProgressAction`, `ErrorVariable`, `WarningVariable`, `InformationVariable`, `OutVariable`, `OutBuffer`, and `PipelineVariable` into every cmdlet entry.

`PS-AUTH-072` — Parameter binding accepts full names and only valid PowerShell abbreviations. Parse colon-bound and space-separated values. For `Invoke-WebRequest`/`Invoke-RestMethod`, skip positional URI, validate `-OutFile` and local `-InFile`, and treat the write as optional when neither disk target exists. `New-Item -Name` is leaf-only: accept no slash/backslash, dot, or `..`; otherwise ask because its base depends on another parameter.

`PS-AUTH-073` — Only `StringConstant` and `Parameter` elements are statically safe path input. A bare comma-list without evaluation metacharacters may be treated as inert array text. Variables, parenthesized values, hashtables, type coercion, script blocks, expression pipeline sources, and colon-bound non-string children ask. Before asking, perform any safe best-effort deny check on a recoverable literal path.

`PS-AUTH-074` — Normalize surrounding quotes, tilde, slash direction, drive spelling, and traversal. Backticks, `provider::` syntax, UNC/DavWWWRoot/`@SSL`, unresolved `$` or `%`, non-filesystem providers, and custom PSDrive prefixes ask. On native Windows, distinguish a real drive letter from a two-or-more-character provider prefix; on POSIX, any alphanumeric prefix before `:` is provider-like.

`PS-AUTH-075` — Writes reject wildcard syntax. Read traversal resolves the complete path. Read globs authorize the non-glob base but still ask because symlink expansion of every eventual match is not statically validated. PowerShell glob metacharacters are `*`, `?`, `[`, and `]`; braces are literal.

`PS-AUTH-076` — Path authorization order is: matching deny; internal editable carveout; automatic-edit safety; in-working-directory read or accepted edit; internal readable carveout; sandbox external-write allowlist; explicit allow rule; otherwise unresolved. A path deny returns deny. Unsafe/unresolved returns ask with read-directory or edit suggestions as applicable.

`PS-AUTH-077` — Check every statement and nested command in two logical passes or collect all results so a later deny beats an earlier ask. Any write-capable cmdlet with no extracted target asks unless its catalog entry marks the write optional. File redirects are checked as writes except null/merge redirects.

`PS-AUTH-078` — If a compound contains `Set-Location`, `Push-Location`, `Pop-Location`, or `New-PSDrive` (plus Windows-only `ndr`/`mount` aliases), every path-bearing statement asks because the validator's working directory or provider namespace is stale. Continue collecting denials despite that ask.

## Git and path-resolution transition guards

`PS-AUTH-080` — Canonicalize git-sensitive paths by stripping parameter prefixes and quotes/backticks/provider qualifiers, normalizing slash/case/platform spelling, removing NTFS trailing spaces/dots, resolving traversal, and recognizing drive-relative re-entry. Treat `HEAD`, `objects`, `refs`, `hooks`, `.git/`, and NTFS `git~N` short names as git-internal.

`PS-AUTH-081` — A compound directory change plus git asks. A current directory with `HEAD`, `objects`, and `refs` but no valid `.git/HEAD` asks as a possible bare repository. Creating git-internal content then running git asks. Any archive extractor plus git asks because archive contents are opaque. Any `.git/` write asks even without a current git command.

`PS-AUTH-082` — Detect link creation through `New-Item -ItemType/-Type` abbreviations, Unicode/slash parameter prefixes, colon values, quotes, and backtick escapes. `SymbolicLink`, `Junction`, and `HardLink` all poison later resolution; a compound containing one disables explicit-allow, read-only, and edit-mode shortcuts for its other commands.

## Read-only recognition

`PS-AUTH-090` — Read-only requires a valid parse, no script block/subexpression/expandable string/splat/member invocation/assignment/stop-parsing flag, at least one segment, no compound path-namespace change, no file redirection, a safe first command, safe remaining pipeline commands, and no nested commands.

`PS-AUTH-091` — An allowlisted command must be non-application except bare `where.exe`, whose raw token is checked exactly. It must have a prototype-free catalog entry, satisfy any regex/callback, and expose element types. Every argument must be string/parameter or an inert bare comma-list. Every colon-bound parameter child must be a string constant.

`PS-AUTH-092` — If a catalog entry sets `allowAllFlags`, argument-type/callback checks still run. Otherwise missing/empty safe flags means positional arguments only and rejects all flags. Cmdlet parameters use AST type and Unicode-dash normalization; native Windows programs may use `/`. Common PowerShell parameters are accepted for cmdlets only.

`PS-AUTH-093` — Safe output name-only filtering contains only zero-argument `Out-Null`. Formatting, selecting, sorting, grouping, measuring, `Where-Object`, `Out-String`, and `Out-Host` pass through the full allowlist plus argument-leak callback. A pipeline tail may be skipped only after that check. `Write-Output` is never name-only safe.

`PS-AUTH-094` — `argLeaksValue` rejects `Variable`, evaluated `Other`, script block, subexpression, expandable string, and any colon-bound non-string child. A bare comma-list of identifiers is allowed only when its text contains none of `$`, `(`, `@`, `{`, or `[`. This applies even to an explicit command allow: allowing a cmdlet does not authorize leaking secret-expanded values through output or coercion errors.

## Read-only catalog and safe flags

`PS-AUTH-095` — Implement the read-only cmdlet catalog with these safe flag surfaces. Comparisons are case-insensitive; common parameters from `PS-AUTH-071` are additionally safe:

| Cmdlet family | Safe flags or rule |
| --- | --- |
| `Get-ChildItem` | Path, LiteralPath, Filter, Include, Exclude, Recurse, Depth, Name, Force, Attributes, Directory, File, Hidden, ReadOnly, System |
| `Get-Content` | Path, LiteralPath, TotalCount/Head, Tail, Raw, Encoding, Delimiter, ReadCount |
| `Get-Item` / `Get-ItemProperty` | Path, LiteralPath; Force/Stream for item; Name for property |
| `Test-Path` | Path, LiteralPath, PathType, Filter, Include, Exclude, IsValid, NewerThan, OlderThan |
| `Resolve-Path` | Path, LiteralPath, Relative |
| `Get-FileHash` / `Get-Acl` | Path/LiteralPath plus Algorithm/InputStream; Audit/Filter/Include/Exclude respectively |
| `Set/Push/Pop-Location` | Path/LiteralPath, PassThru, StackName as applicable |
| `Select-String` | Path, LiteralPath, Pattern, InputObject, SimpleMatch, CaseSensitive, Quiet, List, NotMatch, AllMatches, Encoding, Context, Raw, NoEmphasis |
| JSON/CSV/XML/HTML conversion | InputObject and pure formatting options: Depth, Compress, EnumsAsStrings, AsArray/Hashtable, NoEnumerate, Delimiter, Header, NoTypeInformation, NoHeader, UseQuotes/Culture, Property, Head/Title/Body/Pre/Post/As/Fragment |
| `Format-Hex` | Path, LiteralPath, InputObject, Encoding, Count, Offset |
| object comparison/join/random | documented inert InputObject/property/separator/culture/count/seed/shuffle options |
| `Convert-Path` | Path, LiteralPath |
| `Join-Path` | Path, ChildPath, AdditionalChildPath; never Resolve |
| `Split-Path` | Path/LiteralPath, Qualifier/NoQualifier, Parent, Leaf, LeafBase, Extension, IsAbsolute; never Resolve |
| system inspection | `Get-HotFix`, ItemPropertyValue, PSProvider, Process, Service, ComputerInfo, Host, Date, Location, PSDrive, Module, Alias, History, Culture/UICulture, TimeZone, Uptime with their read/display flags only |
| output/coercion | Write-Output, Write-Host, Start-Sleep use explicit flags plus argument-leak callback |
| format/select tails | Format-Table/List/Wide/Custom, Measure/Select/Sort/Group/Where-Object, Out-String/Out-Host allow flags only after argument-leak callback |
| network inspection | Get-NetAdapter/IPAddress/IPConfiguration/Route/DnsClientCache/DnsClient with local display/filter flags; no remote session flags |
| event logs | Get-EventLog display/filter flags; Get-WinEvent LogName/ListLog/ListProvider/ProviderName/Path/MaxEvents/FilterXPath/Force/Oldest |
| CIM metadata | Get-CimClass with ClassName, Namespace, MethodName, PropertyName, QualifierName |

`PS-AUTH-096` — Deliberately exclude `Select-Xml` and `Test-Json` from read-only because external entity/schema references can perform network access; exclude `Get-Command` and `Get-Help` because name lookup can autoload module code; exclude clipboard access; exclude WMI/CIM instance queries; exclude `netsh`; exclude any remote-session/network parameter omitted above.

`PS-AUTH-097` — Native read-only catalog:

- Git, GitHub CLI, Docker, and .NET CLI use dedicated validators.
- `.NET` accepts only `--version`, `--info`, `--list-runtimes`, and `--list-sdks`.
- Windows display tools include vetted forms of `ipconfig`, `netstat`, `systeminfo`, `tasklist`, `where.exe`, `hostname`, `whoami`, `ver`, `arp`, `route print`, and `getmac`.
- Cross-platform tools include vetted flags for `file`, `tree`, and `findstr`.
- `hostname` rejects positionals; `route` requires its first nonflag verb to be `print`; `ipconfig` rejects positionals; `file` excludes magic-database compilation.

`PS-AUTH-098` — Git rejects any `$` argument, dangerous global flags (`-c`, `-C`, exec/config/git-dir/work-tree/attr-source forms), attached `-c...`/`-C...`, and parser differentials in value-taking global options. Skip every known value-taking global option before locating the subcommand. Use the shared read-only subcommand/flag catalog. `ls-remote` rejects URL, `@`, colon, and variable positionals.

`PS-AUTH-099` — GitHub CLI read-only is internal-user gated and rejects every `$` argument. Docker rejects every `$` argument before any fast path; only shared read-only commands and validated flag configurations pass. These checks defend against PowerShell expanding a variable after the validator saw only its literal extent.

## `acceptEdits` mode

`PS-AUTH-100` — `acceptEdits` auto-allows only canonical `Set-Content`, `Add-Content`, `Remove-Item`, and `Clear-Content` plus their unambiguous aliases. It deliberately excludes `New-Item`, copy, move, rename, archive, and other complex-binding writes.

`PS-AUTH-101` — Mode allow requires a valid parse, no security flags, nonempty segments, no compound directory-change-plus-write, no compound link creation, only `CommandAst` pipeline elements, no application command, safe argument types, and every element being either the approved writer or a validated harmless output tail. Paths remain subject to path authorization and dangerous-removal denial.

`PS-AUTH-102` — Bypass and `dontAsk` are handled by the common permission flow, so mode validation returns passthrough for them. They do not bypass explicit deny, parse-independent dangerous removal, or mandatory analyzer asks.

## Destructive warnings and hard denials

`PS-AUTH-110` — Emit an informational first-match warning for recursive/forced `Remove-Item`, wildcard `Clear-Content`, `Format-Volume`, `Clear-Disk`, destructive git reset/push/clean/stash forms, broad SQL drop, stop/restart computer, and recycle-bin clearing. Warning metadata does not authorize execution.

`PS-AUTH-111` — Unlike Bash, a dangerous PowerShell removal target is a hard deny. Deny root, home, system roots, drive roots, broad globs, and other common-contract dangerous targets both in successful AST path extraction and the parse-failure positional fallback. No approval can convert this one request to allow.

## Sandbox, execution, cancellation, and faults

`PS-AUTH-120` — PowerShell authorization has no sandbox auto-allow shortcut. A command may still be wrapped by the shared sandbox executor on Linux, macOS, or WSL when the same selector used for Bash returns true, but isolation does not replace its PowerShell permission analysis.

`PS-AUTH-121` — Native Windows has no supported sandbox wrapper. If settings require sandboxing and policy forbids unsandboxed commands, reject both during ordinary input validation and again immediately inside the tool call for direct callers that bypass validation. The second check is authoritative. Never silently run unsandboxed.

`PS-AUTH-122` — On supported POSIX hosts, pass the authorized PowerShell process through the shared sandbox selector, honoring a permitted explicit override and non-authoritative exclusions. On native Windows force process sandbox selection false after the policy gate.

`PS-AUTH-123` — Parser timeout and process faults become invalid parse and the degraded authorization branch. The parser never consumes the tool cancellation signal directly, so common turn cancellation must still prevent execution after permission resolution. No parser failure may downgrade an explicit deny.

`PS-AUTH-124` — If PowerShell becomes unavailable at execution time, return an explicit “not available” result without a nonzero-command error because no command ran. A pre-spawn failure similarly returns an explicit failed-execution message. Spawn failure must not trigger an unsandboxed retry.

`PS-AUTH-125` — Backgrounding, progress, timeout, interruption, persisted output, and cwd restoration happen after authorization. They preserve the command and sandbox decision. An interrupt yields an explicit interrupted tool result and registered processes/tasks are cleaned up on every exit path.

## Normal scenarios

`PS-AUTH-N001` — `Get-Content ./README.md` parses as one command, its path is authorized, its flags/argument types are safe, and read-only classification returns allow.

`PS-AUTH-N002` — `gci -Recurse | Select-Object Name` canonicalizes `gci`, validates the first command and its path/flags, validates the selection tail's inert argument, and allows. `Select-Object @{N='x';E={...}}` does not.

`PS-AUTH-N003` — A deny rule for `Remove-Item:*` also denies `rm ./x`, a module-qualified removal, a tab-separated alias invocation, and an invocation-operator form surfaced canonically by the AST.

`PS-AUTH-N004` — In `acceptEdits`, `Set-Content ./out.txt 'x'` can allow after path validation. `Copy-Item`, `New-Item`, or the same write following `Set-Location` asks.

`PS-AUTH-N005` — On POSIX with sandbox enabled, an already authorized `pwsh` command is wrapped at spawn. Its authorization reason remains the PowerShell rule/read-only/mode reason, not sandbox auto-allow.

## Fault and adversarial scenarios

`PS-AUTH-F001` — If the native parser is absent or times out, `Get-Process; iex evil` is split by the fallback and a deny rule for `Invoke-Expression:*` still returns deny. Without such a deny it asks with no suggestion.

`PS-AUTH-F002` — `scripts\Get-Content secret` retains application `nameType` even though its display name is stripped. It cannot use a cmdlet allow or read-only entry.

`PS-AUTH-F003` — `Write-Output $env:SECRET`, `Write-Output -InputObject:$env:SECRET`, and calculated-property hashtables all fail the argument-leak gate even when `Write-Output:*` is explicitly allowed.

`PS-AUTH-F004` — `Set-Location ~; Get-Content ./.ssh/id_rsa` asks because relative-path resolution changed after validation. `New-PSDrive` and link creation trigger the same class of guard.

`PS-AUTH-F005` — `Remove-Item /` or a drive/home equivalent is denied even when parsing fails. This differs intentionally from the Bash mandatory ask.

`PS-AUTH-F006` — A command longer than the platform parser byte limit is not spawned for parsing. Raw/fallback deny checks still run; no AST-based allow applies.

`PS-AUTH-F007` — `Get-Process | Out-String -InputObject:(Remove-Item ./x)` surfaces subexpression/nested command/redirection information and asks or denies; output-tail filtering cannot erase it.

`PS-AUTH-F008` — On native Windows where managed policy requires sandboxing and forbids unsandboxed commands, every PowerShell request is rejected before process creation, including direct internal callers.

## Unsupported grammar and extension rules

`PS-AUTH-130` — Valid PowerShell grammar may be parsed but remains unsupported for auto-authorization unless every construct is covered: assignments, pipeline chains, control flow, functions, traps, script/data blocks, dynamic invocation, expression pipeline sources, script blocks, subexpressions, expandable strings, splatting, member calls, stop-parsing, using/requirements, custom providers, runtime alias/variable changes, and nested commands all force review.

`PS-AUTH-131` — Adding a cmdlet to an allowlist requires all of: exact runtime command identity, complete parameter-binding audit for supported PowerShell versions, explicit path parameters, known switches and value parameters, safe element-type policy, network/module-autoload review, alias/native-executable collision review, and adversarial tests for abbreviated, Unicode-dash, slash, colon-bound, quoted, backtick, pipeline-bound, and positional forms.

`PS-AUTH-132` — Adding a native application requires a raw-token identity gate, complete subcommand/global-option consumption grammar, variable-expansion rejection where data may leave the machine or alter behavior, platform-specific option rules, and a proof that no accepted flag writes or executes code.

`PS-AUTH-133` — New AST node, statement, redirect, type-literal, provider, or token kinds fail closed until mapped. Parser evolution must be differential-tested against both Windows PowerShell 5.1 behavior where supported and current PowerShell Core behavior.

## Implementation checklist

- Reproduce parser byte-limit derivation and test multibyte Windows commands near the process cap.
- Test every named block, top-level Param block, trap, using statement, requirement, nested command, and deeply nested redirection.
- Differential-test Unicode dash, slash parameters, abbreviations, colon binding, backticks, invocation operators, module qualification, aliases, and native-executable collisions.
- Test raw, canonical-input, and canonical-rule matching for deny, ask, and allow; prove module stripping remains asymmetric.
- Exercise parse absence, one/double timeout, helper failure, empty output, invalid JSON, deterministic cache, transient eviction, and concurrent parse sharing.
- Test deny precedence from every later segment against each earlier ask class.
- Test provider, UNC, traversal, glob, PSDrive, cwd transition, link creation, git internal path, archive, and dangerous removal cases.
- Treat the read-only flag catalog and deliberate omissions as conformance fixtures, not examples.
- Test application and argument-leak gates at exact allow, per-command allow, read-only, output-tail, and mode paths.
- Test POSIX sandbox wrapping and native-Windows required-sandbox refusal independently.
- Keep all normal and fault scenarios executable in the target language.

## Reference provenance

This contract was implemented from the PowerShell tool permission, native parser, security, path, read-only, mode, destructive-warning, git-safety, common-parameter, constrained-language, shell-rule-matching, sandbox-selection, and direct tool-execution responsibilities. Source file names are provenance only; the requirements above are the project contract.
