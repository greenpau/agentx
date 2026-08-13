# Exact command-line grammar

This document is normative for token recognition, option arity, aliases, early rewrites, and the externally shipped command tree. It complements [the CLI behavior contract](cli-contract.md), which owns initialization and execution semantics. `CLIG-*` identifiers are stable implementation anchors.

## Contents

1. [Notation and parser rules](#notation-and-parser-rules)
2. [Bootstrap dispatch and position sensitivity](#bootstrap-dispatch-and-position-sensitivity)
3. [Root command and output options](#root-command-and-output-options)
4. [Session, model, context, and capability options](#session-model-context-and-capability-options)
5. [Conditional and internal root options](#conditional-and-internal-root-options)
6. [MCP command tree](#mcp-command-tree)
7. [Authentication, plugin, and utility commands](#authentication-plugin-and-utility-commands)
8. [Conditional command families](#conditional-command-families)
9. [Parsing failures and compatibility](#parsing-failures-and-compatibility)
10. [Acceptance scenarios](#acceptance-scenarios)

## Notation and parser rules

`CLIG-001` — The ordinary root grammar is:

```text
agentx [root-options] [prompt]
```

`<value>` is required, `[value]` is optional, and `<value...>` is one-or-more variadic input. A present optional-value option becomes boolean true when no value follows; otherwise it consumes the next non-option token. A variadic option consumes tokens through the next recognized option or end of arguments. `--` ends option parsing and leaves subsequent tokens to the active command's positional grammar.

`CLIG-002` — Enable positional options at the root and under `mcp`: options before a subcommand token belong to the parent; options after it resolve against the selected subcommand. Do not implement global option scanning that steals subcommand arguments. Unknown options, missing required values, excess positionals, and invalid choices fail through one consistent usage/error path.

`CLIG-003` — `--plugin-dir <path>` is the deliberate exception to bulk lists: it consumes exactly one path and may be repeated. Do not change it to a variadic argument; that would consume subcommand words. In contrast, `--tools`, allowed/denied tools, MCP configs, beta headers, extra directories, files, and channel server lists are variadic.

`CLIG-004` — If raw arguments contain `-p` or `--print`, do not register ordinary subcommands, except when an application connection URL is also present. Thus `agentx -p mcp list` routes the trailing words as default-command input rather than invoking MCP management. This is both a startup optimization and observable grammar.

## Bootstrap dispatch and position sensitivity

`CLIG-010` — Before constructing the ordinary parser, apply the following ordered recognizers:

The standalone Go profile first performs the `GCFG-PATH-006` application-home
bootstrap and then the `AUTH-045` existence gate before constructing the full
parser. Thus malformed and recognized forms both stop for a missing file;
informational handlers do not parse the credential document after the gate.

1. Sole `--version`, `-v`, or compatibility `-V`: after the shared bootstrap
   and existence gate, print the resolved build-identity banner beginning with
   `agentx <version>` and append only available Git and build facts, with no
   ordinary imports. The full root parser advertises only `-v, --version`;
   `-V` is accepted only in this sole-argument fast path.
2. Feature-gated `--dump-system-prompt` in argument position zero; an optional `--model <model>` is found in the remaining tokens.
3. Position-zero standalone hosts `--agentx-in-chrome-mcp`, `--chrome-native-host`, and gated `--computer-use-mcp`.
4. Gated `--daemon-worker <kind>` in position zero.
5. Gated position-zero bridge aliases `remote-control`, `rc`, `remote`, `sync`, and `bridge`.
6. Gated position-zero `daemon`.
7. Gated background selectors `ps`, `logs`, `attach`, `kill`, or either `--bg`/`--background` anywhere.
8. Gated position-zero template selectors `new`, `list`, `reply`.
9. Gated position-zero `environment-runner` or `self-hosted-runner`.
10. A worktree option together with `--tmux` or `--tmux=classic`.
11. Sole common mistakes `--update` and `--upgrade`, rewritten to the `update` command.
12. Presence of `--bare`, which sets the minimal-mode latch before loading modules.

Each recognized fast path owns all remaining tokens and does not fall through to the ordinary parser unless its documented handler declines the request.

`CLIG-011` — A token beginning `cc://` or `cc+unix://` is recognized before ordinary dispatch. Interactive use removes the URL and dangerous-permission marker and enters pending direct-connect state; headless use rewrites to `open <url> ...`. An arbitrary prompt containing such text must not accidentally execute a second ordinary command.

`CLIG-012` — `assistant` is early-recognized only as raw argument zero. With no next argument, it discovers sessions; with a next non-option token, that token is the session identifier. `assistant --help`, or a root flag before `assistant`, reaches the gated ordinary stub and prints assistant usage with failure rather than attaching.

`CLIG-013` — `ssh` is early-recognized only as raw argument zero. Extract these options from either side of the host: `--local`, `--dangerously-skip-permissions`, `--permission-mode <mode>` or `--permission-mode=<mode>`, `-c`/`--continue`, `--resume <value>` or `--resume=<value>`, and `--model <value>` or `--model=<value>`. The first remaining non-option is host and the second is optional directory. Print mode is rejected. Missing host, help/unknown forms, or a root flag before `ssh` falls through to the registered usage stub.

## Root command and output options

`CLIG-020` — General and diagnostic root options:

| Spelling | Arity / parse |
| --- | --- |
| `-h, --help` | flag |
| `-v, --version` | flag; see sole `-V` compatibility in `CLIG-010` |
| `-d, --debug` | boolean flag selecting the DEBUG session-logger threshold |
| `--verbose` | flag |
| `--bare` | flag |
| `--init` | hidden flag |
| `--init-only` | hidden flag |
| `--maintenance` | hidden flag |
| `--mcp-server` | flag selecting the standalone provider-neutral core-capability host |
| `--list-providers` | flag selecting strict provider-registry discovery |
| `--mcp-debug` | deprecated flag alias behavior, not a new transport option |
| `--hard-fail` | hidden, feature-gated flag |

The standalone Go profile does not register debug filters, `-d2e`,
`--debug-to-stderr`, `--debug-file`, or the deprecated `--mcp-debug`; those
forms are ordinary unknown options. In particular, `--debug=<filter>` is not
accepted as a boolean spelling.

`CLIG-021` — Noninteractive and structured-output options:

| Spelling | Arity / domain |
| --- | --- |
| `-p, --print` | flag |
| `--output-format <format>` | `text`, `json`, or `stream-json` |
| `--input-format <format>` | `text` or `stream-json` |
| `--json-schema <schema>` | required JSON-schema text |
| `--include-hook-events` | flag |
| `--include-partial-messages` | flag |
| `--replay-user-messages` | flag |
| `--attachment <path>` | one nonempty path; repeatable; initial headless user input |
| `--enable-auth-status` | hidden flag, default false |
| `--max-thinking-tokens <tokens>` | hidden/deprecated number |
| `--max-turns <turns>` | hidden number |
| `--max-budget-usd <amount>` | positive number |
| `--task-budget <tokens>` | hidden positive integer |
| `--workload <tag>` | hidden string |
| `--sdk-url <url>` | hidden string; forces SDK stream modes under `CLI-011` |

## Session, model, context, and capability options

`CLIG-030` — Session and placement grammar:

| Spelling | Arity / domain |
| --- | --- |
| `-c, --continue` | flag |
| `-r, --resume [value]` | optional session identifier/search; boolean true when omitted |
| `--fork-session` | flag |
| `--from-pr [value]` | optional PR number/URL/search; boolean true when omitted |
| `--no-session-persistence` | flag selecting a temporary, nonresumable, memory-free session |
| `--resume-session-at <message id>` | hidden required string |
| `--rewind-files <user-message-id>` | hidden required string |
| `--session-id <uuid>` | required UUID text, validated semantically |
| `-n, --name <name>` | required display name |
| `-w, --worktree [name]` | optional worktree name; boolean true when omitted |
| `--tmux` | flag; bootstrap also recognizes literal `--tmux=classic` |
| `--teleport [session]` | hidden optional session |
| `--remote [description]` | hidden optional description; distinct from position-zero bridge alias `remote` without leading dashes |

`CLIG-031` — Model and prompt-context grammar:

| Spelling | Arity / domain |
| --- | --- |
| `--provider <id>` | one required nonempty exact provider-registry ID |
| `--model <model>` | selected provider's logical-model assertion; never a provider selector or override |
| `--effort <level>` | `none`, `low`, `medium`, `high`, `xhigh`, or `max`; must belong to the selected provider's declared subset |
| `--agent <agent>` | string |
| `--betas <betas...>` | variadic strings |
| `--fallback-model <model>` | string |
| `--thinking <mode>` | hidden `enabled`, `adaptive`, or `disabled` |
| `--system-prompt <prompt>` | string |
| `--system-prompt-file <file>` | hidden file path |
| `--append-system-prompt <prompt>` | string |
| `--append-system-prompt-file <file>` | hidden file path |
| `--prefill <text>` | hidden string |
| `--deep-link-origin` | hidden flag |
| `--deep-link-repo <slug>` | hidden string |
| `--deep-link-last-fetch <ms>` | hidden finite number; invalid text becomes absent |

`CLIG-032` — Capability and configuration grammar:

| Spelling | Arity / domain |
| --- | --- |
| `--allowedTools, --allowed-tools <tools...>` | equivalent aliases, variadic strings |
| `--tools <tools...>` | variadic strings; empty string disables all, `default` selects normal built-ins |
| `--disallowedTools, --disallowed-tools <tools...>` | equivalent aliases, variadic strings |
| `--mcp-config <configs...>` | variadic inline-JSON-or-path values |
| `--permission-prompt-tool <tool>` | hidden string |
| `--permission-mode <mode>` | current permission-mode enum |
| `--dangerously-skip-permissions` | flag |
| `--allow-dangerously-skip-permissions` | flag |
| `--settings <file-or-json>` | one path or inline JSON value |
| `--setting-sources <sources>` | comma-separated `user`, `project`, `local` selection |
| `--add-dir <directories...>` | variadic paths |
| `--ide` | flag |
| `--strict-mcp-config` | flag |
| `--agents <json>` | JSON object text |
| `--plugin-dir <path>` | exactly one path per occurrence, repeatable |
| `--disable-slash-commands` | flag |
| `--chrome` / `--no-chrome` | positive/negative pair |
| `--file <specs...>` | variadic `file_id:relative_path` values |

`--attachment` is distinct from the legacy/download-oriented `--file` family.
It preserves one explicit path per occurrence, implies headless execution even
without prompt text, and delegates all path/media validation to the native
session attachment importer. It is not accepted by native session management
or the standalone MCP tool host.

The semantic conflict and validation matrix remains `CLI-010` through `CLI-020`; grammar recognition alone never authorizes dangerous permission mode, extra paths, plugins, files, or MCP servers.

`CLIG-033` — The standalone Go profile exposes native session management as
additive root flags because v1.0.x treats positional words as prompt text:

```text
agentx --list-sessions --cwd <workspace>
       [--output-format text|json]
       [--session-page-size <1..500>]
       [--session-page-token <opaque-token>]

agentx --delete-session <session-id>
       --session-revision <opaque-revision>
       --cwd <workspace>
       [--output-format text|json]
```

`--list-sessions` is a boolean flag and rejects an inline value.
`--delete-session`, `--session-revision`, `--session-page-size`,
`--session-page-token`, and `--cwd` each require one value. List and delete are
mutually exclusive. Both require an explicitly supplied nonempty `--cwd`;
absence of `--output-format` means text. Revision is delete-only and required
there. Page size and page token are list-only. Every management option may
occur at most once; repeats fail before final-value validation so a later
valid-looking value cannot erase an earlier empty or forbidden value.
Before the first standalone `--`, a `--list-sessions` or `--delete-session`
token is never consumed as the missing scalar value of an earlier option:
reject that invocation before surface inference or runtime construction.
After standalone `--`, the same spelling remains literal prompt text when no
management selector was already recognized.
Management rejects
`stream-json`, every prompt/positional token (including one after `--`), and
every explicitly supplied ordinary conversation, model, permission, tool,
extension, MCP, persistence, structured-input, or SDK option. This check uses
option occurrence, not only the normalized value, so explicit defaults such as
`--input-format text` and `--max-turns 100`, empty-valued conversation options,
explicit `--provider`, and explicit `--print` still conflict. A revision or
pagination option without its selector is also a usage error.

`CLIG-034` — `--provider` consumes exactly one value in either
`--provider <id>` or `--provider=<id>` form. A missing or empty value is a usage
error, and the selector may occur only once; repeated selectors are rejected
before provider selection even when their values are identical. Preserve the
bytes supplied by the caller for strict configuration
validation: do not trim whitespace, fold case, interpret the value as a model,
or perform a prefix/fuzzy match. Provider IDs themselves are stable,
case-sensitive `auth.json` identities. Provider resolution follows `CLI-029`:
an explicit exact match wins over the declared default; otherwise a singleton
is the effective default or a multi-provider registry requires exactly one
declared default. `--model` is evaluated only after this resolution and can
assert only the selected provider's logical model.

`CLIG-035` — The standalone `--mcp-server` host and both `CLIG-033` native
session-management forms are provider-neutral exclusion modes. They reject an
explicit `--provider` occurrence, including one whose normalized value would
otherwise be empty, and return before provider selection or construction. A
model-backed interactive, print, aggregate JSON, or stream-JSON invocation may
use the selector.

`CLIG-036` — The standalone provider-registry discovery grammar is:

```text
agentx --list-providers [--output-format text|json]
```

`--list-providers` is a boolean selector, rejects an inline value, and may
occur only once. It accepts no positional token and no explicit root option
other than one optional, nonrepeated `--output-format`; absence of that option
means text. It is mutually exclusive with native session management,
standalone MCP, and every conversation/provider/session/workspace/permission/
tool/extension/SDK option. Before the first standalone `--`, its token is never
consumed as the missing value of an earlier scalar option. After `--`, the same
spelling is literal prompt text when discovery was not already selected.
Discovery does not infer `--print`, acquire stdin, or select a provider merely
because stdout is redirected. Its versioned JSON projection is `WIRE-026`.

## Conditional and internal root options

`CLIG-040` — Register a gated option only when its build/runtime capability exists. A missing gated option is an ordinary unknown option, not a startup failure:

| Gate/profile | Root options |
| --- | --- |
| advisor eligibility | hidden `--advisor <model>` |
| transcript classifier available | hidden `--enable-auto-mode` |
| proactive or assistant experience | `--proactive` |
| assistant messaging | `--messaging-socket-path <path>` |
| brief experience | `--brief` |
| assistant experience | hidden `--assistant` |
| channels | hidden `--channels <servers...>`, `--dangerously-load-development-channels <servers...>` |
| bridge | hidden `--remote-control [name]`, `--rc [name]` |
| hard-fail build | hidden `--hard-fail` |

`CLIG-041` — Teammate process options are hidden but registered in supported builds: `--agent-id <id>`, `--agent-name <name>`, `--team-name <name>`, `--agent-color <color>`, `--plan-mode-required`, `--parent-session-id <id>`, `--teammate-mode <mode>` with `auto|tmux|in-process`, and `--agent-type <type>`. Identity triplet validation is semantic and fail-fast.

`CLIG-042` — Internal distribution options are supported absence in external builds: `--delegate-permissions`, hidden `--dangerously-skip-permissions-with-classifiers`, hidden `--afk` (all imply auto permission mode), hidden `--tasks [id]`, and `--agent-teams`. Do not expose dead strings from an excluded build as callable behavior.

## MCP command tree

`CLIG-050` — The MCP tree is:

```text
agentx mcp serve [-d|--debug] [--verbose]
agentx mcp add [options] <name> <commandOrUrl> [args...]
agentx mcp remove [-s|--scope <scope>] <name>
agentx mcp list
agentx mcp get <name>
agentx mcp add-json [-s|--scope <scope>] [--client-secret] <name> <json>
agentx mcp add-from-agentx-desktop [-s|--scope <scope>]
agentx mcp reset-project-choices
```

Scope values are `local`, `user`, or `project`; `add`, `add-json`, and desktop import default to `local`, while `remove` without scope searches the scopes under its handler contract.

`CLIG-051` — `mcp add` options are:

| Spelling | Arity / behavior |
| --- | --- |
| `-s, --scope <scope>` | required value, default `local` |
| `-t, --transport <transport>` | `stdio`, `sse`, or `http`; absence means `stdio` |
| `-e, --env <env...>` | variadic `KEY=value` values |
| `-H, --header <header...>` | variadic `Name: value` values |
| `--client-id <clientId>` | string for HTTP/SSE OAuth |
| `--client-secret` | flag: read/prompt for secret under credential contract |
| `--callback-port <port>` | string parsed as base-10 port by handler |
| `--xaa` | hidden unless XAA enabled; requires client ID, client-secret request, and configured IdP |

`--` may separate the server name from a stdio executable and its flags. For HTTP/SSE, `commandOrUrl` is the URL. For stdio, URL-like text without an explicit transport remains a command but emits a corrective warning. OAuth/XAA options on stdio emit a warning and are ignored.

`CLIG-052` — With XAA enabled, add:

```text
agentx mcp xaa setup --issuer <url> --client-id <id>
                     [--client-secret] [--callback-port <port>]
agentx mcp xaa login [--force] [--id-token <jwt>]
agentx mcp xaa show
agentx mcp xaa clear
```

Setup validates a URL, requires HTTPS except loopback HTTP, and requires a positive integer callback port. `--client-secret` means read `MCP_XAA_IDP_CLIENT_SECRET`; absence of that variable is an error. The injected ID token is intentionally an argv compatibility path and therefore security-sensitive.

## Authentication, plugin, and utility commands

`CLIG-060` — Authentication grammar:

```text
agentx auth login [--email <email>] [--sso] [--console] [--agentx]
agentx auth status [--json] [--text]
agentx auth logout
```

Login-method conflicts and status-format conflicts are handler validation, not parser aliases.

`CLIG-061` — Plugin is aliased as `plugins`. Every plugin/marketplace subcommand also accepts hidden `--cowork` to select its alternate storage area:

```text
agentx plugin validate <path>
agentx plugin list [--json] [--available]
agentx plugin marketplace add <source> [--sparse <paths...>] [--scope <scope>]
agentx plugin marketplace list [--json]
agentx plugin marketplace remove|rm <name>
agentx plugin marketplace update [name]
agentx plugin install|i <plugin> [-s|--scope <scope>]
agentx plugin uninstall|remove|rm <plugin> [-s|--scope <scope>] [--keep-data]
agentx plugin enable <plugin> [-s|--scope <scope>]
agentx plugin disable [plugin] [-a|--all] [-s|--scope <scope>]
agentx plugin update <plugin> [-s|--scope <scope>]
```

Install and uninstall default scope to `user`; enable/disable auto-detect when omitted; update applies its documented update-scope default. `--available` requires JSON output semantically.

`CLIG-062` — General utility grammar:

```text
agentx setup-token
agentx agents [--setting-sources <sources>]
agentx doctor
agentx update|upgrade
agentx install [target] [--force]
```

The update/upgrade aliases compare full build-version strings, including build metadata, under the updater contract.

## Conditional command families

`CLIG-070` — Direct-connect commands exist only with that feature:

```text
agentx server [--port <number=0>] [--host <string=0.0.0.0>]
              [--auth-token <token>] [--unix <path>] [--workspace <dir>]
              [--idle-timeout <ms=600000>] [--max-sessions <n=32>]
agentx open <cc-url> [-p|--print [prompt]] [--output-format <format=text>]
```

SSH's help stub is `agentx ssh <host> [dir] [--permission-mode <mode>] [--dangerously-skip-permissions] [--local]`; actual execution follows the early grammar in `CLIG-013`.

`CLIG-071` — Classifier builds whose cached circuit breaker is not disabled expose `auto-mode defaults`, `auto-mode config`, and `auto-mode critique [--model <model>]`. Bridge builds register hidden `remote-control|rc` help stubs but the position-zero bootstrap path owns execution. Assistant builds register `assistant [sessionId]` as the usage stub described in `CLIG-012`.

`CLIG-072` — Feature-gated bootstrap-owned families are `daemon ...`, background `ps|logs|attach|kill` and `--bg|--background`, templates `new|list|reply`, `environment-runner ...`, and `self-hosted-runner ...`. Their remaining token grammars belong to their isolated entrypoint contracts and were not present in the specified ordinary command parser. An implementation must either implement those entrypoint-specific grammars from its corresponding domain skill or omit the entire gated family; it must not guess and advertise partial flags.

`CLIG-073` — Internal-distribution commands are supported absence externally: `up`; `rollback [target] [-l|--list] [--dry-run] [--safe]`; `log [number|sessionId]`; `error [number]`; `export <source> <outputFile>`; task `create`, `list`, `get`, `update`, and `dir`; and hidden `completion <shell> [--output <file>]`.

## Parsing failures and compatibility

`CLIG-080` — Parser failures write usage/diagnostic output to the non-protocol error channel and exit nonzero before session creation. Structured stdout remains empty. Sensitive argv values such as injected tokens and inline settings are never echoed by generic diagnostics.

`CLIG-081` — Preserve aliases, optional-value booleans, and the distinction between a token's spelling and semantic normalization. Do not silently convert legacy camel-case tool flags into new canonical output; accept both spellings and record one normalized value.

`CLIG-082` — Help output lists only options/subcommands enabled in the current build and hides explicitly hidden entries. Parsing support may intentionally exceed help visibility. A compiled-out family is not mentioned; a runtime-policy-disabled recognized command reports policy denial after parse.

`CLIG-083` — The existing headless architecture diagram already shows early CLI routing into interactive, print, and SDK execution. This exact grammar specializes the parser node and adds no independent topology, so a duplicate diagram is unnecessary.

## Acceptance scenarios

1. Invoke sole `-V` for a source-default build and a linker-stamped build, then invoke `-V prompt`. The first two print their corresponding build-identity banners; the last follows ordinary unknown-option behavior.
2. Invoke `-p mcp list`. Verify no MCP subcommand registration or MCP health check occurs and the tokens route through the default print action.
3. Invoke `--plugin-dir A --plugin-dir B mcp list`. Verify two paths are retained and `mcp list` remains a subcommand.
4. Put a root option after `mcp add` where no same-named child option exists. Verify it is rejected by child grammar instead of globally consumed.
5. Invoke `assistant --help` and `--debug assistant`. Verify neither attaches; both reach the documented usage/failure route.
6. Invoke SSH with model and permission flags before and after host. Verify both orders normalize identically, while `-p` is rejected.
7. Pass `--resume` without a value and with a UUID. Verify the parser yields true in the first case and the string in the second.
8. Use both tool-flag spellings and repeatable plugin directories. Verify normalized values are identical and no variadic option swallows the next recognized flag.
9. Invoke XAA grammar with the gate off. Verify `xaa`/`--xaa` are absent or unknown; with the gate on, validate the setup/login forms exactly.
10. Build without direct connect, bridge, classifier, and internal distribution support. Help and parser omit all corresponding commands; the default session remains usable.
11. Invoke `-d` and `--debug` and verify both enable the same boolean diagnostic mode. In the standalone Go profile, reject `--debug=<filter>`, `-d2e`, `--debug-to-stderr`, `--debug-file`, and `--mcp-debug` before session construction.
12. Invoke `--attachment A --attachment=B` with prompt text and with no text.
Preserve `A,B` in occurrence order and infer headless mode for both. Reject a
missing/empty path, session-management and standalone-MCP combinations, and a
slash-prefixed prompt carrying attachments.
13. Invoke `--provider terra-west` and `--provider=terra-west`; verify both
select the same exact registry ID. Repeat with an empty value, changed case,
surrounding whitespace, an unknown ID, and a model name; verify none is
silently normalized or matched as a provider. Repeat the selector in separate,
inline, and mixed forms and verify a usage error before runtime construction.
14. Omit `--provider` for a singleton registry, a multi-provider registry with
one declared default, and a multi-provider registry with no default. Verify
the first two resolve deterministically and the third fails with the
instruction to add `"default": true` to exactly one provider or pass an exact
provider ID.
15. Select a nondefault provider and assert its model with `--model`; repeat
with another provider's model and verify assertion failure without reselection.
Combine `--provider` with `--mcp-server`, session list, and session deletion;
verify every provider-neutral form rejects it before provider construction.
16. Invoke `--list-providers` with terminal and redirected stdout, with explicit
text and JSON output, and with a multi-provider/no-default registry. Verify the
selector never infers a model turn and all valid forms preserve the complete
registry. Repeat the selector, pass an inline boolean value, place it where a
scalar value is missing, and combine it with a prompt, `--provider`, `--cwd`,
session management, MCP, or stream JSON. Verify each fails through the stable
usage channel before discovery output or runtime construction.

## Non-normative provenance

Evidence was specified from the bootstrap dispatcher, ordinary root parser, MCP command modules, subcommand handlers, feature gates, and validation branches. A particular parser library and source-language callback types are not required; token-level behavior is.
