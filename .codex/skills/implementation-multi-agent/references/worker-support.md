# Multi-Agent Worker Support Contracts

This reference specifies the support plane around delegated work: persistent per-agent memory, project snapshots, built-in roles and prompt listing, process-local queues, isolated tmux ownership, source-operation extraction, team-memory classification, and presentation projection. These helpers must not quietly become transcript, task, permission, or completion authorities.

Use the [worker support diagram](../assets/worker-support.drawio) to distinguish durable evidence, process-local coordination, resource ownership, and display-only derivation.

## Contents

- [Contract map](#contract-map)
- [Persistent agent memory](#persistent-agent-memory)
- [Project memory snapshots](#project-memory-snapshots)
- [Built-in roles and listing prompt](#built-in-roles-and-listing-prompt)
- [Process-local mailbox](#process-local-mailbox)
- [Isolated tmux socket](#isolated-tmux-socket)
- [Source-operation extraction](#source-operation-extraction)
- [Team-memory classification](#team-memory-classification)
- [Presentation projection](#presentation-projection)
- [Conformance scenarios](#conformance-scenarios)

## Contract map

- **MA-SUP-001 — Memory scope and path.** Persistent memory is namespaced by effective agent type and explicit user, project, or local scope; path recognition normalizes traversal and remote-local memory retains canonical-project namespacing.
- **MA-SUP-002 — Snapshot reconciliation.** A project snapshot initializes empty memory, prompts before replacing newer/conflicting local memory, and records a timestamped sync marker separately from memory content.
- **MA-SUP-003 — Built-in role composition.** The built-in roster and each role's model, tool, permission, context, background, and one-shot traits are explicit products of build, surface, and runtime gates.
- **MA-SUP-004 — Agent-list prompt projection.** Tool descriptions derive from the frozen effective definitions and may move their dynamic listing into conversation attachments without changing selection or authorization semantics.
- **MA-SUP-005 — Process-local mailbox.** In-process delivery is a volatile filtered queue with FIFO waiter selection and revision notifications; it is not the durable cross-process team mailbox.
- **MA-SUP-006 — Tmux socket ownership.** Pane-backed work uses one lazily initialized process-owned tmux server, routes direct and shell-issued tmux commands through its socket, and cleans it up without touching the user's server.
- **MA-SUP-007 — Source-operation observation.** Git and pull-request summaries are derived only from recognized command plus output evidence; telemetry and session-to-PR linkage are observational and never completion evidence.
- **MA-SUP-008 — Team-memory classification.** Team-memory read/search/write counters derive from bounded path and tool-name classification and affect summary wording only.
- **MA-SUP-009 — Presentation is non-authoritative.** Color, grouping, truncation, progress collapse, source ordering, labels, and tool result suppression never alter task state, transcript content, routing, authorization, or usage accounting.

## Persistent agent memory

### Scope layout

Under `MA-SUP-001`, an agent definition may choose one of three scopes:

```text
user    -> {memory base}/agent-memory/{agent type}/
project -> {effective cwd}/.agentx/agent-memory/{agent type}/
local   -> {effective cwd}/.agentx/agent-memory-local/{agent type}/
```

When a remote-memory mount is configured, local scope instead becomes:

```text
{remote memory mount}/projects/{sanitized canonical Git root or project root}/
  agent-memory-local/{agent type}/
```

Replace colon characters in a namespaced agent type with hyphens before using it as a memory directory component. Preserve a trailing path separator in directory-returning interfaces where callers rely on prefix checks. The memory entrypoint is always `MEMORY.md` beneath the chosen directory.

Path classification first normalizes the absolute candidate, then accepts it only beneath:

- the configured user memory base's `agent-memory` subtree;
- the current working directory's project-memory subtree; or
- the local-memory subtree, with remote mount and `projects` prefix checks when redirected.

Normalization is required so `..` segments cannot bypass prefix checks. This predicate identifies a memory-family path; file tools still apply their normal path and permission policy.

### Prompt composition

Loading enabled memory is synchronous from the definition/prompt caller's perspective. It starts directory creation best-effort without awaiting it because spawn-time prompt construction may occur in a synchronous render path; later file writes must independently create parents. Build the standard memory prompt with display name `Persistent Agent Memory`, the exact directory, and one scope note:

- user memory must remain general across projects;
- project memory may be tailored to the project and is shared through version control;
- local memory may be tailored to project and machine and is not checked into version control.

Append a nonempty product-specific extra guideline after the scope note. Memory is selected context with an explicit path, not a copy of the entire parent transcript.

## Project memory snapshots

Under `MA-SUP-002`, project snapshots live at:

```text
{cwd}/.agentx/agent-memory-snapshots/{agent type}/
  snapshot.json       # { updatedAt: nonempty timestamp string }
  other regular files # snapshot payload
```

The target memory directory contains `.snapshot-synced.json` with `{syncedFrom: timestamp}`. Invalid, missing, or unreadable JSON/schema is treated as absent.

The reconciliation decision is:

```text
no valid snapshot metadata                         -> none
snapshot exists and target has no *.md files       -> initialize
target has memory and no valid sync marker          -> prompt-update
snapshot updatedAt is later than syncedFrom          -> prompt-update
otherwise                                            -> none
```

Do not compare individual memory-file times. The snapshot metadata timestamp is the version indicator.

Initialization creates the target directory, copies every regular snapshot file except `snapshot.json`, and then writes the sync marker. Replacement first removes existing regular files ending in `.md`, then copies the snapshot payload and writes the marker. Non-Markdown local files remain. Mark-as-synced writes only metadata and leaves local memory unchanged. Individual copy or metadata-write failures are diagnostic and do not crash the session; callers must not claim a successful content replacement solely because the marker write was attempted.

Snapshot synchronization is a user-visible reconciliation choice when local memory already exists. Never overwrite that memory merely because a newer project snapshot is available.

## Built-in roles and listing prompt

### Roster selection

Under `MA-SUP-003`, built-in selection follows this order:

1. On a noninteractive SDK/API surface, a dedicated environment control may disable the entire built-in set and return none.
2. When coordinator support is built and coordinator mode is explicitly enabled, replace the ordinary roster with coordinator worker definitions; do not merge the two sets.
3. Otherwise include `general-purpose` and `statusline-setup`.
4. Include `Explore` and `Plan` only when their build feature and runtime experiment enable them. In builds that include the feature, the external-provider default is enabled unless the experiment disables it.
5. Include `agentx-code-guide` only outside the TypeScript SDK, Python SDK, and SDK CLI entrypoints.
6. Include `verification` only when its build feature and independent runtime experiment enable it.

The ordinary built-ins have these immutable definition traits:

| Role | Capability and restriction contract |
| --- | --- |
| `general-purpose` | Candidate tool wildcard, default child model, broad research/search/multi-step role. |
| `Explore` | Read/search specialist; forbids delegation, plan exit, file editing/writing, and notebook editing; omits automatically injected project instructions; fast model for external users and inherited model for the internal build. |
| `Plan` | Architecture/plan specialist; inherits the Explore candidate tools and the same mutating/delegation denials; inherited model; omits automatic project instructions while retaining explicit file-read ability. |
| `statusline-setup` | Only file read and edit, fixed higher-capability model, orange display color; edits the status-line configuration contract. |
| `agentx-code-guide` | Official-product documentation specialist; local read/search plus web documentation tools, fast model, nonprompting permission mode; augments its system prompt with currently configured skills, agents, MCP servers, plugin skills, and settings. Embedded-search builds substitute shell-provided find/grep for dedicated glob/grep tools. |
| `verification` | Inherited model, red display color, background-by-default; forbids delegation, plan exit, project write/edit, and notebook edit; receives a critical read-only verification reminder and must return a classified evidence verdict. |

`Explore` and `Plan` are one-shot for result rendering: omit the continuation trailer that advertises ID, continuation command, and usage. That display choice does not make their transcript nonexistent.

### Dynamic listing placement

Under `MA-SUP-004`, describe each effective agent as:

```text
- {agent type}: {when-to-use} (Tools: {effective authored restriction})
```

For the description only, an authored allowlist plus denylist displays `allow - deny`; an empty result displays `None`; allowlist alone lists entries; denylist alone displays `All tools except ...`; neither displays `All tools`. Runtime tool construction remains governed by `MA-TOOL-001` and may remove more capabilities.

Filter the listing by any parent Agent rule's allowed types. A feature or explicit environment control may move the dynamic list from the tool description to a system-reminder attachment. In attachment mode the tool description stays static to preserve prompt-cache stability; registry changes produce new listing messages. This projection must use the same effective definitions and must not itself authorize a type.

When fork support is enabled, the prompt distinguishes a context-inheriting fork from a fresh typed child, tells the model not to poll an output file, and requires results to arrive as later external completion messages. Coordinator mode receives the slim shared portion because its system prompt already owns orchestration guidance. Background guidance is omitted when background work is disabled, inside an in-process teammate, or where fork-specific rules replace it.

## Process-local mailbox

Under `MA-SUP-005`, the in-process mailbox message schema is:

```text
id: string
source: user | teammate | system | tick | task
content: string
from?: string
color?: string
timestamp: string
```

It owns an in-memory queue, an ordered waiter list, a change signal, and a monotonically increasing revision starting at zero.

- `send` increments revision first. It resolves and removes the first waiter whose predicate accepts the message; if none accepts, it appends to the queue. Both paths emit the change signal.
- `poll(predicate)` removes and returns the first matching queued message or returns absent. The reference poll path does not emit the change signal.
- `receive(predicate)` immediately removes the first matching queued message, emits change, and returns an already-resolved future; otherwise it appends a waiter and returns its future.
- One message satisfies at most one waiter. Waiter and queue ordering are FIFO among matching entries.

There is no persistence, cross-process locking, acknowledgement, or built-in waiter cancellation. Process shutdown loses queued messages and unresolved waiters. Use the durable teammate mailbox contracts for pane/process peers; never cite this queue as proof that a remote teammate received a message.

## Isolated tmux socket

Under `MA-SUP-006`, pane-backed execution owns a tmux socket named `agentx-{process ID}`. Direct pane operations pass that name via tmux's explicit socket selector. After initialization, every child shell receives a replacement tmux environment value:

```text
{actual socket path},{tmux server PID},0
```

This prevents shell-issued tmux commands from operating on a user's preexisting server. Before initialization, return no override and leave the shell environment unchanged.

### Availability and lazy initialization

Probe availability once per process and cache it. On Windows, execute tmux through WSL without an intervening login shell and force UTF-8; elsewhere locate the executable normally. Unavailable tmux disables pane-backed tools and leaves shell commands without isolation, with an explicit diagnostic.

Initialization is lazy after a pane tool is used or a shell command requires tmux. Multiple callers share one in-flight initialization future. Later callers await it but do not propagate its error; the original initialization logs failure and degrades without an initialized socket.

Create a detached base session on the named socket and set `AGENTX_SKIP_PROMPT_HISTORY=true` both for the initial session and the tmux server's global environment so nested product instances do not enter ordinary prompt history. Treat an already-existing base session on the same socket as recoverable. Register process cleanup that kills only this named server; an already-dead server is success-equivalent.

On Windows/WSL, pin `WSL_INTEROP=/run/WSL/1_interop` both on the base session and global server environment so detached and later-created sessions retain a stable interop endpoint.

Discover socket path and server PID from one tmux formatted query. If parsing fails, construct the conventional POSIX socket path from temporary directory, effective numeric user ID (zero fallback), and socket name, then query PID separately. Initialize state only when both path and numeric PID are known; otherwise remain uninitialized and report the combined failure. Test reset clears names, path, PID, in-flight state, availability cache, and used latch.

## Source-operation extraction

Under `MA-SUP-007`, recognize source operations from raw shell command text independently of shell family. Git command recognition tolerates global `-c value`, `-C path`, and `--name=value` options between `git` and the subcommand.

Derived summaries require both command intent and matching output evidence:

- commit or cherry-pick requires the standard bracketed commit line; expose the first six hexadecimal characters and classify committed, amended, or cherry-picked;
- push requires a ref-update line on combined stdout/stderr and extracts the destination branch;
- merge requires `Fast-forward` or `Merge made by`; rebase requires `Successfully rebased`; extract the first nonflag ref before a shell control token;
- supported GitHub CLI PR actions are create, edit, merge, comment, close, and ready. Prefer a full GitHub pull URL, otherwise parse the conventional pull-request number text.

Operational tracking runs only for exit code zero. It emits commit, amend, push, and supported PR action events; increments commit/PR counters where applicable; recognizes GitLab merge-request creation; and recognizes curl PR creation only when a POST method (explicit or implied by data) and an HTTP(S) pull/merge-request collection endpoint both occur. A successful GitHub PR creation with a parseable URL may asynchronously link the current session to repository, number, and URL. Dynamic-import or link failure must not change tool success.

These summaries are convenient evidence pointers, not proof that the repository now has a desired semantic state. Parent synthesis still uses worker status and transcript/result contracts.

## Team-memory classification

Under `MA-SUP-008`, classify a search/read as team memory only when its typed input has a `path` recognized by the shared team-memory path predicate. Do not infer from search pattern or glob text. Classify a write only for canonical file-write or file-edit tool names and a recognized `file_path`, falling back to `path`.

Summary wording appends read, search, and write parts in that order. It selects present versus past tense from active state and capitalization from whether the part is first. Read and write include singular/plural counts; search reports only that team memories were searched. These counters are display derivations and do not grant memory access or prove persistence.

## Presentation projection

Under `MA-SUP-009`:

- Store optional colors by agent type in process bootstrap state. The supported palette is red, blue, green, yellow, purple, orange, pink, and cyan, each mapped to its dedicated subagent theme slot. `general-purpose` deliberately has no type color. Invalid or absent color does not assign one; clearing removes the mapping.
- For discovery display, group sources as user, project, local, managed, plugin, command-line, and built-in. This order is explanatory and is not definition precedence. Deduplicate duplicate worktree discovery by `(agent type, source)`, annotate a losing candidate with the active winner's source, and sort names case-insensitively within a group.
- Render only progress values that actually carry a model message. Build tool-use/result correlation by tool-use ID. Consecutive search/read/REPL operations may collapse into a count summary; count completed result messages, not both request and result.
- A normal live view retains only the last three processed progress entries and summarizes hidden tool work. Transcript mode may show all. The compact fallback can show tool count and aggregate tokens from the latest assistant usage; it never replaces stored progress.
- Remote/background launch outputs, initialization text, errors, rejection, final assistant content, duration, and usage are visual projections of typed results. Hiding a routing-only or request-only SendMessage result avoids UI noise but does not delete the protocol event.
- Grouped agent display derives labels, colors, last-tool summaries, background markers, and completion/error badges. It must read terminal state from result/task evidence rather than infer completion from the lack of animated progress.

UI components do not append their labels, truncation, colors, or collapsed summaries into the child or parent transcript unless another explicit adapter creates a model-visible message.

## Conformance scenarios

### `MA-U01` — Memory scopes remain disjoint

Resolve the same plugin-namespaced agent type in user, project, local, and remotely mounted local scopes. Verify colon sanitization, canonical-project namespacing, normalized traversal rejection, and distinct `MEMORY.md` entrypoints. **Contracts:** MA-SUP-001, MA-SEC-001.

### `MA-U02` — Snapshot conflict requires a choice

Exercise absent metadata, empty memory, existing Markdown without sync metadata, newer snapshot, equal timestamp, malformed metadata, initialize, replace, and mark-only. Existing local Markdown is never overwritten by the check itself. **Contracts:** MA-SUP-002, MA-CAN-001.

### `MA-U03` — Snapshot replacement has bounded deletion

Place Markdown, non-Markdown, directories, and snapshot metadata in both source and target. Replacement deletes only target regular Markdown files, never copies source `snapshot.json`, and records sync after the copy attempt. **Contracts:** MA-SUP-002, MA-SEC-001.

### `MA-U04` — Roster gates do not leak roles

Test noninteractive disable, coordinator replacement, Explore/Plan gate off, SDK guide omission, and verification gate off. No disabled built-in survives through a stale display listing or fallback selection. **Contracts:** MA-SUP-003, MA-BLT-001, MA-OFF-001.

### `MA-U05` — Role restrictions survive backend changes

Run each built-in through every supported backend. Verify mutation/delegation restrictions, model choice, background default, one-shot trailer behavior, and critical verification reminder are properties of the frozen invocation plan rather than the UI. **Contracts:** MA-SUP-003, MA-PLAN-001, MA-AUTH-001.

### `MA-U06` — Listing attachment preserves semantics

Toggle dynamic-list attachment mode during plugin/MCP refresh. The static tool schema remains cacheable, the newest attachment names the effective definitions, and selection/tool authorization matches inline-list mode. **Contracts:** MA-SUP-004, MA-DEF-001, MA-TOOL-001.

### `MA-U07` — Process mailbox filtering is FIFO and volatile

Queue interleaved message sources and two filtered waiters. Verify first matching waiter wins, each message is consumed once, revision increments on send, immediate receive emits change, poll follows the reference notification behavior, and restart loses all state. **Contracts:** MA-SUP-005, MA-CAN-001.

### `MA-U08` — Durable and local mailboxes are not substituted

Send the same logical message through an in-process teammate and a pane teammate. Only the durable path may claim append/lock evidence; the process-local path may claim only direct delivery to a live waiter or queue admission. **Contracts:** MA-SUP-005, MA-MBX-001, MA-MBX-002.

### `MA-U09` — Tmux commands cannot reach the user's server

Start the product inside an existing user tmux session, initialize the process-owned socket concurrently from two callers, and invoke tmux both directly and through a child shell. Every post-initialization command reaches only `agentx-{pid}`; cleanup kills only that server. **Contracts:** MA-SUP-006, MA-ISO-001.

### `MA-U10` — Socket failure degrades explicitly

Test unavailable executable, initialization failure, malformed combined path/PID response, successful fallback PID query, and failed fallback. No incomplete socket state is exported and pane backends report unavailability without touching the user's environment. **Contracts:** MA-SUP-006, MA-OFF-001, MA-CLN-001.

### `MA-U11` — Operation summaries require paired evidence

Mix incidental SHAs/URLs, failed commands, global Git options, root commits, forced pushes, merge/rebase failure text, GitHub actions, GitLab creation, and curl bodies containing endpoint-like text. Emit only supported command-plus-output matches and never treat telemetry as worker completion. **Contracts:** MA-SUP-007, MA-SYN-001.

### `MA-U12` — Display collapse cannot alter evidence

Feed more than three mixed progress entries, unmatched results, routing-only SendMessage output, background launch, error, and terminal result. Compare stored transcript/task evidence before and after every display mode; only rendering changes. **Contracts:** MA-SUP-009, MA-OUT-001, MA-TRN-001.

### `MA-U13` — Team-memory summary is path-derived

Test a team-memory-looking search pattern with a normal path, a recognized path with arbitrary pattern, write/edit aliases, counts zero/one/many, and active/inactive wording. Only canonical path/tool matches affect the display summary. **Contracts:** MA-SUP-008, MA-SUP-009.
