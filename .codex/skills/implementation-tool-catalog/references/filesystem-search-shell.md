# Filesystem, search, shell, and editor contracts

## Contents

1. [Shared local-resource rules](#shared-local-resource-rules)
2. [Read](#read)
3. [Glob](#glob)
4. [Grep](#grep)
5. [Bash and PowerShell](#bash-and-powershell)
6. [Edit](#edit)
7. [Write](#write)
8. [NotebookEdit](#notebookedit)
9. [LSP](#lsp)
10. [Local-family acceptance cases](#local-family-acceptance-cases)

## Shared local-resource rules

- **FS-001:** Normalize paths against the session working directory, retain the user spelling for display, and authorize the resolved target. Never authorize a lexical path and execute a different canonical path.
- **FS-002:** Apply protected-resource, repository-boundary, managed-policy, symlink, and sandbox rules at the shared capability boundary. Tool validation may add narrower checks.
- **FS-003:** Capture enough file identity and modification state to reject a stale edit. Create the pre-edit backup and update in-memory file-history state before changing the target. Snapshot transcript persistence and IDE/LSP notification are later asynchronous evidence; neither is an atomic commit marker for the write.
- **FS-004:** Classify shell invocations from their complete parsed command, operators, redirections, environment assignments, paths, and subcommands. Unknown syntax is not read-only or concurrency-safe.
- **FS-005:** Stream progress for presentation, but return one normalized terminal result. Background execution returns a stable task identity instead of waiting for process completion; the task-runtime contract defines what survives a crash.
- **FS-006:** A file mutation is not one transaction with permission, backup metadata, native write, integration notification, post hooks, terminal result, or transcript append. Recovery cannot infer which of those phases completed and never automatically replays an unresolved mutation.

## `Read`

Input identifies one absolute file path plus optional line `offset` and `limit`. Resolve relative user references before constructing the tool input; the model-facing schema expects an absolute path.

1. Require read permission for the resolved target.
2. For ordinary text, return numbered lines bounded by offset, limit, and implementation safety ceilings. Make truncation explicit and preserve the information needed for a follow-up read.
3. For images, PDFs, notebooks, or other recognized media, return the supported content-block representation rather than corrupting bytes into text.
4. Reject directories with a message directing the caller to search/list behavior. Reject missing, unreadable, or unsupported files explicitly.
5. Treat this tool as concurrency-safe, read-only, non-destructive, closed-world, and exempt from result persistence by the declared `Infinity` cap. This exemption does not remove media or model-context safety limits.

## `Glob`

Input is a glob pattern and optional search root. Return matching paths sorted by modification time, newest first. The specified default result ceiling is 100, with an explicit context/profile override allowed. State when more matches exist. Do not follow inaccessible roots or turn a malformed pattern into a shell expression.

`Glob` is concurrency-safe, read-only, and a search-class result. Suppress it when the build provides embedded equivalent search primitives; callers must observe equivalent ordering and boundedness.

## `Grep`

Support a regex pattern, optional root, glob or file-type filter, output mode, case flag, line numbers, before/after/context lines, multiline search, offset, and head limit.

- Default output mode is file names with matches.
- Default head limit is 250; explicit zero means unlimited subject to safety/output caps.
- Apply offset after forming the mode-specific result stream.
- Preserve regex errors as validation/execution errors; never silently downgrade to literal search.
- Return search metadata sufficient to distinguish no match from failure and to request the next page.
- Declare a 20,000-character result cap. The tool is concurrency-safe and read-only.

## `Bash` and `PowerShell`

The two tools share lifecycle semantics but use shell-specific parsers, quoting rules, policy analyzers, and sandbox adapters.

Input contains command text, optional user-facing description, optional timeout, and—when background tasks are enabled—`run_in_background`. Use these exact specified defaults and ceilings:

- default timeout: 120,000 ms;
- maximum timeout: 600,000 ms;
- a positive `BASH_DEFAULT_TIMEOUT_MS` may replace the default;
- a positive `BASH_MAX_TIMEOUT_MS` may replace the ceiling, but the effective maximum cannot be below the effective default;
- default declared result cap: 30,000 characters;
- a positive output override may raise the shell cap no higher than 150,000 characters, still subject to the common result policy.

Process each call as follows:

1. Parse the whole command using the selected shell grammar; resolve compound commands and paths for permission analysis without executing them.
2. Compute read-only and concurrency-safe classifications conservatively. Only a synchronous command proven read-only is concurrency-safe. Background commands are never treated as ordinary synchronous reads.
3. Compose scoped shell permission rules, managed policy, tool checks, hooks, and sandbox choice. If native Windows requires a sandbox that is unavailable, deny rather than run unsandboxed.
4. Spawn in a registered process group with cancellable timeout. Maintain stdout/stderr ordering as faithfully as the presentation contract permits.
5. For synchronous completion, return exit status plus bounded output. For backgrounding, immediately return task ID, command summary, and running state; the task runtime owns later output and cancellation.
6. Treat timeout, signal termination, spawn failure, output overflow, and user cancellation as distinct terminal states.

`AGENTX_DISABLE_BACKGROUND_TASKS` removes the background field and behavior. `PowerShell` is absent unless the platform/shell gate passes.

## `Edit`

Input contains absolute `file_path`, `old_string`, `new_string`, and optional `replace_all`.

- Require `old_string != new_string`.
- Reject files larger than 1 GiB, non-text targets, and notebooks (use `NotebookEdit`).
- Require evidence that the current session previously read the existing file, and reject if its current modification identity differs from the observed version.
- With `replace_all=false`, require exactly one occurrence. Zero matches and multiple matches are actionable validation errors. With `replace_all=true`, require at least one occurrence and replace all exact matches.
- Preserve original line-ending style and file permissions. Write atomically where the platform permits.
- Enforce settings-file, team-memory, secret, and protected-path rules before writing.
- Capture the backup and in-memory history state, perform the target write, update current read state, notify LSP/IDE integrations asynchronously, map the deterministic diff/original-change result, run post hooks, and only then release the terminal bundle to the query layer. Snapshot persistence may still be queued at any of these later phases.

The descriptor is sequential, write-capable, non-destructive by annotation, closed-world, and blocking on interruption. “Non-destructive” does not mean permission-free.

## `Write`

Input contains absolute `file_path` and complete `content`.

- Permit creation after parent/path authorization.
- For an existing file, require a current prior-read observation; reject stale or unread overwrites.
- Enforce the same sensitive settings, team-memory, secret, protected-path, atomic-write, history, integration, and nontransactional phase-ordering rules as `Edit`.
- Return a normalized creation/replacement diff. An empty string is a valid complete file value, not “missing input.”

The descriptor is sequential and write-capable. A future implementation may classify particular writes as destructive only if this changes the permission and UI contracts consistently.

## `NotebookEdit`

Input identifies an absolute `.ipynb` file, cell selector, edit mode `replace|insert|delete`, optional new source, and for insertion a required cell type `code|markdown`.

1. Validate notebook JSON and locate the requested cell deterministically.
2. `replace` changes source while retaining compatible metadata; `insert` creates a new cell at the requested position; `delete` removes exactly the selected cell.
3. Clear code-cell outputs and execution count when code content changes.
4. Serialize a valid notebook without exposing implementation-specific object ordering as semantic behavior.
5. Return a cell-oriented diff and update file history/integration state.

Reject generic `Edit` attempts on notebook structure so the cell contract cannot be bypassed.

## `LSP`

Input selects one of `goToDefinition`, `findReferences`, `hover`, `documentSymbol`, `workspaceSymbol`, `goToImplementation`, `prepareCallHierarchy`, `incomingCalls`, or `outgoingCalls`, plus the fields required for that operation. File positions are 1-based in the tool contract even if the external protocol is 0-based.

- Require read permission for referenced files.
- Reject files above 10 MiB, unsupported languages, missing servers, disconnected servers, invalid positions, and operations not supported by the selected server.
- Translate protocol locations and ranges back to stable workspace paths and 1-based positions.
- Bound and normalize external results; distinguish empty successful results from transport failure.
- Expose only when `ENABLE_LSP_TOOL` is truthy and an applicable server is connected. It is concurrency-safe and read-only.

## Local-family acceptance cases

- **FS-A01:** A symlink escaping an allowed root is denied before `Read`, `Edit`, or shell execution.
- **FS-A02:** Two simultaneous proven-read-only shell calls may overlap; an unclassified call serializes behind writes.
- **FS-A03:** Editing after an external modification yields a stale-file error and leaves the file unchanged.
- **FS-A04:** `replace_all=false` with two matches yields a multiple-match error; true replaces both and records one atomic file-history event.
- **FS-A05:** A background shell call returns a running task ID, survives the originating model call, and later stops through `TaskStop`.
- **FS-A06:** An unsupported PowerShell sandbox configuration exposes no runnable `PowerShell` tool or explicitly denies a stale invocation.
- **FS-A07:** `Grep` with explicit limit zero does not apply the 250 default, but common output controls still prevent unbounded model context.
- **FS-A08:** Through the public core registry, executor, and real permission evaluator, read an existing file; reject an unread overwrite; create and replace another file; reject an externally stale overwrite; re-read, replace, and edit successfully; then observe the exact durable bytes through `Glob` and `Grep`. Preserve every tool-use correlation ID and file mode. An explicitly allowed shell command executes, while an unruled shell mutation is denied and creates no file.
