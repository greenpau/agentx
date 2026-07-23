# Path, shell, and sandbox contract

This document defines path authorization, command-pattern semantics, shell analysis, sandbox policy, and isolation lifecycle. `PATH-*`, `SHELL-*`, and `SBOX-*` identifiers are normative.

## Contents

- Path identity and canonicalization
- File-rule pattern roots
- Protected resources
- Read and write decisions
- Destructive path analysis
- Shell rule matching
- Sandbox configuration, filesystem, and network isolation
- Sandbox selection per command
- Failure matrix
- Acceptance scenarios

## Path identity and canonicalization

`PATH-001` — Evaluate both lexical and canonical path identity. Preserve the original spelling for display, but authorization uses an absolute normalized path plus every symlink-resolution step.

`PATH-002` — Resolve relative paths against the active working directory unless a rule's grammar selects another root. A later `cwd` change updates active path context but does not change the original project root used for project settings.

`PATH-003` — Reject or require explicit review for ambiguous platform spellings before filesystem access:

- UNC and device-prefix paths not explicitly supported by the active platform adapter;
- Windows alternate data streams;
- 8.3 short-name components containing `~` plus a digit;
- trailing dot/space normalization hazards;
- reserved DOS device names;
- unhandled tilde expansion;
- leading `=` drive aliases;
- unresolved `$...` or `%...%` variables;
- traversal and abnormal multi-dot components.

Security comparison is case-insensitive on all platforms to avoid cross-filesystem bypasses.

`PATH-004` — For every symlink component, authorize the supplied path, the link object where relevant, and each resolved target. All must fit the requested read/write policy. Detect cycles and excessive depth with an explicit error.

## File-rule pattern roots

`PATH-010` — Interpret permission file patterns as gitignore-like matchers with these roots:

| Prefix | Root |
| --- | --- |
| `//` | filesystem root |
| `~/` | user home |
| `/` | directory containing the settings source that declared the rule |
| relative | active working directory |

A trailing `/**` matches the named directory itself and descendants.

`PATH-011` — Shell/sandbox configuration has a separate root convention where absolute `/...` is filesystem absolute, relative paths are source-rooted, and `//` is retained only as legacy compatibility. Do not reuse one parser silently for both grammars.

`PATH-012` — `:*` is invalid in file permission patterns. Treat malformed patterns as invalid rules, not broad matches.

## Protected resources

`PATH-020` — At minimum, treat these filenames as dangerous wherever security-relevant:

```text
.gitconfig .gitmodules .bashrc .bash_profile .zshrc .zprofile .profile
.ripgreprc .mcp.json .agentx.json
```

`PATH-021` — At minimum, treat these directories and their control content as dangerous:

```text
.git .vscode .idea .agentx
```

Project `.agentx/worktrees` follows its specialized worktree contract rather than a blanket allow.

`PATH-022` — Writes to application settings, extension commands, agents, skills, hooks, or equivalent executable configuration require a safety ask even if a broad edit rule matches. Narrow session-owned paths under project `.agentx/**` or user configuration home may be internally writable only as listed in `PATH-033`.

`PATH-023` — A session suggestion to create a skill or configuration entry validates each suggested child path: no traversal, glob syntax, protected target, or canonical escape.

## Read decision

`PATH-030` — For read operations, evaluate in order:

1. suspicious/ambiguous path -> ask or deny;
2. matching read deny -> deny;
3. matching read ask -> ask;
4. a matching edit allow may imply read access;
5. path within working directory -> allow unless protected by earlier checks;
6. internal-readable location -> allow;
7. matching read allow -> allow;
8. otherwise ask and suggest a directory-scoped rule when safe.

`PATH-031` — Read globbing authorizes the nonglob base before expansion. Each expanded canonical path is still checked. A pattern that has no safe base is rejected.

## Write decision

`PATH-032` — For write/create/edit operations, evaluate in order:

1. matching edit deny -> deny;
2. explicit internal carveout -> allow;
3. narrow session-owned `.agentx/**` carveout -> allow;
4. protected-resource and dangerous-path safety -> ask/deny;
5. matching edit ask -> ask;
6. `acceptEdits` plus in-scope working-directory path -> allow;
7. matching edit allow -> allow;
8. otherwise ask.

Writes and creates reject glob syntax because the target must be singular.

`PATH-033` — Internal writable locations are narrowly enumerated:

- current plan file;
- session scratchpad and job directory;
- current agent memory directory;
- default automatic-memory directory, but not a custom override outside the trusted root;
- project launch configuration at `.agentx/launch.json`.

`PATH-034` — Internal readable locations include session history and selected memories, plans, persisted tool output, scratch and temporary storage, task/team metadata, and the session's nonce-scoped bundled-skill extraction. Enumeration is explicit; parent directories are not automatically readable.

`PATH-035` — Runtime temporary storage is `/tmp/agentx-<uid>` on Unix-like systems and a platform temp directory `/agentx` on Windows. Create with owner-only access. Bundled skill extraction uses a version component plus a random 16-byte nonce and owner-only directory mode.

## Destructive path analysis

`PATH-040` — A recursive/remove target is dangerous when it is a wildcard, ends in `/*`, resolves to filesystem root, user home, a drive root, or a direct child of one of those broad roots. Require explicit review or reject according to tool policy.

`PATH-041` — Never resolve destructive targets from an unexpanded environment variable, unresolved glob, or broad implicit current directory. Resolve and display the exact canonical target set before authorization.

## Shell rule matching

`SHELL-001` — Supported command content patterns are:

| Form | Behavior |
| --- | --- |
| exact text | full command match |
| legacy `prefix:*` | prefix followed by end-of-string or word boundary |
| wildcard `*` | any characters, including line breaks, within a full-string match |
| escaped `\*` | literal asterisk |
| escaped `\\` | literal backslash |

PowerShell matching is case-insensitive. Other shells use the active platform's documented case policy.

`SHELL-002` — One trailing space-plus-wildcard is optional as a whole. Thus `git *` matches `git` as well as `git status`; it does not make an internal wildcard optional.

`SHELL-003` — Anchor generated regular expressions at both ends and use dot-all semantics. Escape every non-wildcard regular-expression metacharacter. Invalid escape sequences fail the rule.

`SHELL-004` — Split compound commands into execution segments using the actual shell grammar adapter, including pipes, logical operators, separators, and subshells. Normalize leading environment assignments and reviewed wrappers before identifying the effective executable.

`SHELL-005` — Analyze each segment independently for permission, path access, sandbox compatibility, and destructive behavior. Preserve execution ordering and short-circuit semantics in the execution layer; authorization must know the complete potential segment set.

`SHELL-006` — A command path analyzer rejects unsupported UNC/device forms, unresolved variables, leading `=`, and globs in create/write targets. Read globs use `PATH-031`.

## Sandbox configuration schema

`SBOX-001` — Sandbox configuration supports:

```text
enabled: boolean                         default false
failIfUnavailable: boolean              default false
autoAllowBashIfSandboxed: boolean       default true
allowUnsandboxedCommands: boolean       default true
ignoreViolations: string[]
enableWeakerNestedSandbox: boolean
enableWeakerNetworkIsolation: boolean
excludedCommands: string[]
enableRipgrep: boolean
enabledPlatforms: platform[]
network: {
  allowedDomains: string[],
  allowManagedDomainsOnly: boolean,
  allowUnixSockets: string[],
  allowAllUnixSockets: boolean,
  allowLocalBinding: boolean,
  httpProxyPort?: integer,
  socksProxyPort?: integer
}
filesystem: {
  allowWrite: string[], denyWrite: string[], denyRead: string[],
  allowRead: string[] managed-only
}
```

Reject unknown security-sensitive shapes rather than interpreting truthy values loosely.

`SBOX-002` — Active sandboxing requires all of: enabled setting, supported platform (macOS, Linux, or WSL2 as implemented), required dependencies, included platform, and successful initialization. `failIfUnavailable=true` converts missing support into a pre-execution failure; otherwise degrade explicitly to permission-governed unsandboxed behavior.

`SBOX-003` — Store the initialization promise synchronously before awaiting setup so concurrent callers share one attempt. On initialization error, clear it so a later settings change or dependency repair can retry. Publish active state atomically.

## Filesystem isolation

`SBOX-010` — Seed writable roots with the active working directory and runtime temp root, then add approved additional directories and edit-allow rule paths.

`SBOX-011` — Regardless of broad writable roots, deny writes to user settings, managed settings/drop-ins, working-directory settings, and `.codex/skills` control content unless an owning internal operation uses a narrower trusted channel.

`SBOX-012` — For an existing bare repository, deny writes to control paths including `HEAD`, object store, references, hooks, and configuration. For a worktree, allow the main repository access needed by the worktree contract. If protected bare-repository paths do not exist before execution, track and remove any that a sandboxed command creates afterward.

`SBOX-013` — Build read policy analogously from working directory, internal readable roots, permission rules, and explicit denies. Managed-only `allowRead` cannot be expanded by user or project settings.

`SBOX-014` — Linux/WSL isolation may not support filesystem globs accepted elsewhere. Diagnose unsupported patterns and fail or conservatively broaden denial; never silently broaden allow.

## Network isolation

`SBOX-020` — Effective allowed domains derive from sandbox settings plus WebFetch permission allow/deny rules. Deny wins. Canonicalize hostnames and ports; an allowed suffix cannot match an unrelated lookalike domain.

`SBOX-021` — With managed-only domains enabled, only policy-origin allowed domains are effective. User/project/session additions are ignored and diagnosed.

`SBOX-022` — Unix socket paths, all-socket access, local binding, and HTTP/SOCKS proxy ports are separate capabilities. Enabling one never implies another.

`SBOX-023` — A managed-only network ask that cannot be satisfied interactively is a hard block. It cannot be converted to unsandboxed execution merely because `allowUnsandboxedCommands` is true.

## Sandbox selection per command

`SBOX-030` — A command runs sandboxed unless any of these applies:

- sandbox globally inactive with permitted degradation;
- validated input explicitly requests `dangerouslyDisableSandbox` and unsandboxed commands are allowed;
- the operation has no external command to isolate;
- an excluded-command rule matches the effective executable.

`SBOX-031` — Excluded commands are compatibility configuration, not permission. The specified selector evaluates every compound segment and, when any one segment matches an exclusion, skips isolation for the whole submitted shell process. Ordinary authorization must still approve every segment and path, so exclusion never becomes authority. Preserve this observable whole-process behavior for compatibility; an implementation that instead isolates segments independently must label the change as an intentional divergence and define its process/short-circuit migration semantics.

`SBOX-032` — An explicit unsandbox request is itself permission-relevant and appears in the approval explanation. Managed policy may prohibit it regardless of user settings.

`SBOX-033` — Cancellation propagates to sandbox setup, child process, output readers, and post-run cleanup. Cleanup is idempotent and bounded; a cleanup failure is reported separately from the command's exit result.

## Failure matrix

| Failure | Required result |
| --- | --- |
| canonicalization ambiguous | ask or deny before I/O |
| symlink escapes allowed root | ask/deny target; no operation |
| sandbox required but unavailable | terminal execution failure before spawn |
| sandbox optional but unavailable | explicit degraded status, normal permission still enforced |
| one compound segment unsafe | whole requested tool use does not execute without approval |
| protected repo artifact created | remove artifact during post-run cleanup and report violation |
| sandbox child killed | normalized interrupted result plus idempotent resource cleanup |

## Acceptance scenarios

1. An edit path inside the working directory is a symlink to user settings. Both lexical and resolved paths are checked, and protected-resource safety wins over `acceptEdits`.
2. A file rule `/generated/**` in project settings is rooted beside that settings file, while sandbox `/generated/**` means a filesystem-absolute path. Tests prove the distinction.
3. `git *` matches `git`; `git:*` is legacy prefix syntax only in the command grammar; `:*` in a file pattern is rejected.
4. `SAFE=1 env bash -c ... && rm -rf /x` exposes both effective segments. Authorization and destructive analysis see the removal even if the first segment could fail at runtime.
5. Sandbox is required on an unsupported platform. No process starts, a terminal result is paired with the tool use, and permission state remains unchanged.
6. A sandboxed command creates a previously absent bare-repository hook. Post-run cleanup removes it and reports a violation without deleting pre-existing repository data.

## Non-normative provenance

Reference behavior was specified from filesystem and shell permission analyzers, dangerous-path classifiers, sandbox settings schemas, platform adapters, command wrappers, repository protection, and tool execution orchestration under `utils/permissions/`, `utils/sandbox/`, shell tool implementations, and settings schemas. Paths and symbols are provenance only.
