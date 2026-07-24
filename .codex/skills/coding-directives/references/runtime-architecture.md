# AgentX architecture

## Runtime boundary

This implementation follows the language-neutral contracts reachable from the repository `AGENTS.md` and `.codex/skills/implementation-*`. Package dependencies point inward toward a single semantic runtime:

```text
main.go
  → build identity, signal ownership, and private application-home bootstrap
  → early CLI/mode selection and required auth.json existence gate
  → strict model credential parsing, provider, platform, and session construction
  → immutable extension and capability snapshots
  → prompt/context projection
  → shared engine
       → Azure Responses stream
       → capability validation/permission/scheduling
       → normalized events and append-only transcript
  → interactive, text, JSON, or NDJSON projection
```

The VS Code workspace extension is an external adapter over the last of those surfaces; it does not import or replace the semantic engine:

```text
VS Code command or webview input
  → Workspace Trust gate, folder selection, and binary resolution
  → workspace-scoped extension-host controller
  → agentx child in bidirectional NDJSON print mode
  → shared Go session engine and capability boundary
  → ordered SDK records and correlated control requests
  → bounded extension presentation state and CSP-isolated webview
```

Commands, tools, and tasks remain distinct. A slash command is user-side routing, a tool is a model-callable capability, and a task is identity-bearing work that can outlive one call.

The operational target in this tree is the local-agent core. It is not a declaration that every optional or enterprise subsystem has been built. Repository audits validate the skill hierarchy and contract coverage, while Go tests validate the implemented profile; neither substitutes for a per-contract conformance map. The precise partial and unavailable boundaries live in [runtime-conformance.md](runtime-conformance.md).

## Component ownership

| Package | Authority |
| --- | --- |
| `pkg/app` | Process construction, application-home bootstrap, mode dispatch, session placement, and adapter wiring. |
| `pkg/cli` | Early flags and cross-option validation after the mandatory application-home bootstrap but before credential parsing or terminal initialization. |
| `pkg/signals` | Early continuous OS signal acquisition, conflict-checked surface-specific semantic SIGINT routing, globally first-request-wins shutdown state, and one joined exact-once force/failsafe gate. |
| `pkg/testing` | Test-profile-only capabilities, including the always-ask `TestingPermission` end-to-end permission probe; production registries omit them. |
| `pkg/config` | Strict versioned `auth.json` parsing, Azure normalization, model/effort validation, and redaction. |
| `pkg/childenv` | Credential-safe child-process environment projection and explicit per-provider environment scoping. |
| `pkg/identity` | Typed identifier generation, canonical parsing, validation, and wire-safe conversion. |
| `pkg/model` | Provider-neutral requests/events plus the Azure OpenAI Responses API adapter. |
| `pkg/engine` | Serialized submissions, provider streaming, recursive tool continuation, accounting, and terminal outcomes. |
| `pkg/protocol` | Versioned canonical events with explicit visibility and persistence classes. |
| `pkg/transcript` | Append-only JSONL ownership, defensive loading, deduplication, correlation, and crash recovery. |
| `pkg/permission` | Modes, deny-first rules, path/symlink protection, and conservative shell analysis. |
| `pkg/tool` | Descriptors, deterministic registry, lifecycle hooks, exact-once execution ledger, scheduler, output persistence, and core tools. |
| `pkg/task` | Durable local task/work-item state, output files, process cancellation, polling, and restart reconciliation. |
| `pkg/command` | Slash-command parsing, descriptors, aliases, sensitivity, surface eligibility, and local result variants. |
| `pkg/surface` | NDJSON framing, correlated control requests, and protocol-safe output. |
| `pkg/prompt` | Ordered stable/dynamic system context, trusted ancestor `AGENTS.md` snapshots, prompt precedence, and model-specific instructions. Broader managed/user/local/rules/include discovery and Git snapshots are outside the current profile. |
| `pkg/extensions` | Immutable skill/plugin/hook/output-style generations and precedence. |
| `pkg/mcp` | Untrusted MCP configuration, lifecycle, discovery, and stdio JSON-RPC transport. |
| `pkg/memory`, `pkg/compact` | Bounded derived memory and context-pressure projection; neither replaces transcript authority. |
| `pkg/sandbox`, `pkg/sessionlock` | Explicit OS-isolation availability and exclusive durable-session ownership. |
| `pkg/platform`, `pkg/observability` | Production platform detection, atomic diagnostic fallback, and ordered cleanup plus reusable process/filesystem contracts; OS signal acquisition belongs to `pkg/signals`, and not every portable helper has a production caller. |
| `pkg/distributed`, `pkg/features` | Contract-only remote identity/delivery primitives and explicit multi-axis feature availability; no remote transport is registered. |
| [VS Code extension repository](https://github.com/greenpau/agentx-vscode-extension) | External workspace-host adapter for process placement, NDJSON framing and correlation, Workspace Trust gating, editor-context projection, bounded presentation state, webview rendering, diagnostics, and target-specific VSIX packaging. It owns no session, transcript, permission-policy, or tool-execution truth. |

The `pkg/` tree is importable, but it is presently a trusted-host composition surface rather than a frozen SDK. Importers must retain the validation, authorization, credential-handling, locking, and lifecycle invariants described here and in [pkg/README.md](../../../../pkg/README.md).

## Application home and Azure `gpt-5.6-sol` mapping

The application home uses a nonblank `AGENTX_HOME`; otherwise it uses
`<user-home>/.agentx`. This is the sole supported override. A nonblank value
must be absolute and non-root, and an invalid override fails rather than
selecting the default. This standalone profile cleans platform path syntax but
does not yet normalize the selected spelling to Unicode NFC. Every invocation
acquires that directory and its `sessions/`
child before CLI parsing and freezes the selected home plus their acquired
bootstrap identities. Later session and memory paths derive from that frozen
selection and acquire their own subsystem identities; this profile does not
claim that every descendant operation remains rooted in one process-lifetime
home descriptor. Every invocation then requires the literal `auth.json` child
before full CLI parsing. Credential gates and reads open that child
descriptor-relative to the pinned home and reverify the textual home identity
afterward, so replacing the pathname cannot redirect credential selection.
Help, version, and standalone MCP stop if it is absent but do not parse it.
Model-backed surfaces strictly accept only the version-1 `azure_openai` schema
in `AUTH-045`, with no dotenv, `--env-file`, or process-credential fallback.
Supported POSIX platforms enforce effective-user ownership and private mode
bits. Windows model-backed startup is unavailable until native owner/DACL
inspection can authorize reading the credential file. The selected home and
all descendants are mandatory protected paths at the capability boundary even
when an override places the home within the active workspace. Before and after
ordinary permission evaluation, before execution, the runtime rechecks the
frozen home and `sessions` identities and denies tool use after a detected
rename, replacement, or supported-POSIX privacy change. The denial invalidates
a pending approval and is latched until process restart even if the original
inode returns to its pathname. Later descendant filesystem operations retain
their owning subsystem contracts rather than one process-lifetime home
descriptor.

The semantic model identity is `azure_openai.model`; Azure receives
`azure_openai.deployment` in the `model` field. Requests use `api-key`,
`stream:true`, and `store:false`. An empty string or symbolic `v1|preview`
`azure_openai.api_version` value uses `/openai/v1/responses` without a query,
matching Azure's documented v1 route. A dated Azure API version selects the
legacy `/openai/responses` route.

The provider boundary retains stable Go items across recursive calls:

- user, system, and assistant messages;
- function-call item ID, call ID, name, and raw arguments;
- function-call outputs correlated by the exact call ID;
- reasoning summaries and encrypted reasoning content needed for stateless replay;
- response/request IDs, terminal status, and token usage.

When reasoning is enabled, requests include `reasoning.encrypted_content`. Hidden reasoning is never projected into user-visible events or plaintext transcript messages. The encrypted provider item is durable internal continuation state. A response stream is successful only after an explicit terminal event; an SSE `type:error` inside HTTP 200 is still failure.

Retries are bounded, cancellation-aware, honor `Retry-After`, and apply only before any stream event makes execution uncertain. Credentials, headers, request bodies, and raw provider error bodies are excluded from diagnostics.

Provider-controlled structural metadata is validated before it can become a correlation key, transcript record, hook/permission payload, or capability identity. Response, item, and call identifiers; tool names; discriminators; phases; request identifiers; and opaque encrypted reasoning state fail closed on credential reflection, invalid UTF-8, terminal controls, or bidi/format controls. Model-visible text, summaries, arguments, and provider errors are sanitized across chunk, content-part, and structured-field boundaries, so splitting a credential between otherwise valid fields cannot bypass redaction. The provider-neutral engine repeats the gate before persistence and filters unsafe legacy metadata during restore.

## Turn and tool ordering

For every submission, the engine:

1. assigns a turn identity and persists accepted user input;
2. snapshots model, effort, prompt, tools, and limits;
3. streams canonical text, usage, function calls, errors, and completion;
4. stores replayable provider output without exposing hidden state;
5. persists every accepted function call before capability execution;
6. executes contiguous concurrency-safe groups while treating each unsafe call as a barrier;
7. normalizes exactly one terminal result per accepted call ID, retaining completion order within a concurrency-safe group and accepted order as the identity, pairing, and barrier sequence;
8. appends function outputs and recursively calls the model;
9. emits a terminal turn result, flushes durable state, then announces idle.

Malformed raw function arguments are preserved before structural parsing. Unknown tools, denial, cancellation, mapper failure, sibling cancellation, timeout, and missing executor results are explicit terminal outcomes. Large output is saved under a restrictive session result directory and replaced by a stable preview marker.

## Permissions and deliberate hardening

Permission is a composed decision rather than a boolean. Whole-tool and content rules are deny-first; path/shell safety and mandatory interaction dominate broad modes; `dontAsk` fails closed; bypass exists only through the explicit dangerous CLI flag and does not override mandatory safety checks.

Filesystem checks compare lexical and resolved existing-prefix targets. The
application bootstrap pins the selected home directory identity. Credential
loading rejects a symlink or multi-link `auth.json`; permission policy protects
both its exact selected path and the `auth.json` basename wherever encountered,
so a displaced still-named credential cannot shed mandatory review.
The application-home authorizer also verifies the frozen home and `sessions`
identities before and after ordinary permission evaluation. A sustained home
displacement therefore denies every tool, including a read of a displaced
session file whose basename is otherwise ordinary, and an approval pending
during detection cannot execute. This guard does not claim atomicity against an
external rename after authorization returns to execution. Once tripped, its
process-lifetime latch cannot be cleared by restoring the original inode.
Credential/configuration paths, `.git`, editor control directories, dotenv
files, home/root removal targets, ambiguous platform spellings, and symlink
traversal receive denial or mandatory review. Recursive search filters every
protected descendant after directory authorization, so approving a workspace
cannot expose the user-owned `~/.agentx/auth.json`. The user application home
and a workspace's `.agentx/` extension directory are distinct protected
identities; trusting the latter grants no authority over the former. Shell
analysis projects recognized static operands and input/output redirections from
its deliberately closed Bash grammar through path policy; unsupported or
ambiguous syntax requires review. Foreground and background Bash share the
selected sandbox command factory. Bash never becomes generically
auto-authorized merely because a command was classified read-only, and this
profile does not register a PowerShell command tool.

Validated file operations use rooted filesystem handles plus pre/post identity checks to resist symlink, hardlink, mount, and pathname-substitution races. On macOS, `sandbox-exec` is used only after a bounded capability probe succeeds; otherwise the unavailable state is explicit and normal Bash authorization remains mandatory. Unix foreground/background process cancellation owns a verified process group. A descendant that deliberately escapes that group remains an explicit limitation. Hosts that request `--owned-process-tree` add a process-wide containment boundary: Windows assigns AgentX to a kill-on-close Job Object before session setup, while non-Windows accepts the flag as a no-op because capability processes already use owned groups.

The Go port deliberately improves two specified compatibility gaps:

- an approval-supplied replacement is a complete object that is structurally validated, reclassified, path-analyzed, and reauthorized in a bounded loop;
- stream-attempt and executor ledgers never silently discard an accepted tool ID or convert an uncertain side effect into success.

## Transcript and recovery

Durable events carry schema version, event/session/turn identities, physical and logical parents, monotonic sequence, timestamp, origin, visibility, persistence, and one typed payload. UI progress is ephemeral and is rejected if found on disk.

The transcript store places each persistent session at
`<application-home>/sessions/<workspace-hash>/<session-id>/`, uses `0700`
directories and `0600` files, and preserves append ownership, fsync
boundaries, event/tool deduplication, and bounded record loading. The
pre-parser bootstrap creates the top-level `sessions/` child but does not
materialize a workspace or session child. A corrupt middle record is isolated;
a crash-truncated tail is ignored with a diagnostic. Resume selects the newest
eligible main leaf within the selected workspace, restores response-identity
siblings and correlated tool results, retains session-scoped
metadata/accounting, and rebuilds only the model projection. An uncertain
operation is never rerun. A response-identified assistant group whose calls
are all unresolved leaves the live projection; a missing member of a retained
mixed group receives a deterministic synthetic `interrupted` result in memory.
Legacy records without response identity use conservative per-call synthesis.

Fork selects the active durable projection, restamps identifiers, and appends the destination batch without copying ephemeral recovery evidence. A durable incomplete-publication marker is created before the copy and removed only after the copied transcript loads successfully; `--continue`, explicit resume, and destination reuse reject or ignore marked sessions. Source and destination are still independent stores rather than one cross-store transaction, but a process failure cannot publish a copied prefix as a resumable completed fork.

Provider-output metadata, semantic assistant messages, tool calls, and results are intentionally separate. Semantic data remains readable and presentation-neutral while opaque encrypted provider state can be replayed without pretending it is model reasoning text.

## Surfaces and control plane

Interactive, headless text, single-result JSON, and live NDJSON all use the same engine. Every surface classifies syntactically valid slash commands before model submission. Noninteractive registries advertise only descriptor-opted-in commands; recognized-but-unsupported and valid-unknown commands fail locally, while invalid slash grammar remains ordinary prompt text. Structured stdout contains JSON records only; warnings use stderr. The decoder handles arbitrary chunks, blank lines, a final unterminated record, malformed-input failure, and unknown-type warnings. U+2028/U+2029 are escaped.

The live reader continues while a turn runs so permission/control responses cannot deadlock behind model execution. Control waiters register before emission. User messages enter a bounded stable-priority queue; a `now` record cancels the current turn and then runs as the next serialized workload, while `next` and `later` wait. This is queued-turn preemption, not injection of new context into an in-flight recursive model/tool turn. Duplicate UUIDs are silent unless replay acknowledgement is enabled, in which case a schema-valid replay user record is emitted without execution. Interrupt returns a correlated control response and cancels the active turn; accepted tool IDs still settle before idle. Public records use the closed SDK discriminator union rather than wrapping internal protocol events. Initialize-time hook/MCP/prompt/agent/schema injection, historical assistant replay, and live environment/model/permission-mode mutation are explicit unsupported control outcomes.

### VS Code workspace adapter

The extension declares workspace-host placement and starts one AgentX process per selected workspace folder. In a local window that process runs locally; in a supported VS Code Remote window it runs where the workspace extension host runs. This placement does not implement the AgentX bridge, cloud, SSH, teleport, or distributed-session transports. Native qualification of each remote host remains separate from manifest placement.

Activation and view rendering are allowed in Restricted Mode so the user can see the disabled reason, but every route that could launch AgentX or consume workspace-defined launch configuration is guarded by VS Code Workspace Trust. After trust, the resolver selects an explicit machine-scoped binary path, a target-specific bundled binary, or the extension-host `PATH`, validates the supported `0.1.x` compatibility window, and spawns without a shell. Startup-only model effort, permission mode, extension-loading, allow/deny rules, and session-selection options become discrete argument entries; they are never presented as live mutations.

The host waits for `system/init`, sends a correlated empty `initialize` control, then projects the returned dynamic capability inventory. It decodes bounded UTF-8 NDJSON across arbitrary chunks and keeps structured stdout separate from bounded, credential-redacted stderr diagnostics. Known records are validated before state mutation; unknown well-formed record types become bounded diagnostics rather than guessed behavior. User UUIDs remain stable across an accepted write, and tool cards pair only by `tool_use_id`.

Permission and question requests remain AgentX controls. The extension correlates each response by `request_id`, supports allow once, complete-input replacement and allow, deny, and deny-and-stop, and deliberately offers no permanent approval because the current wire profile cannot persist a permission update. Editor context is explicit untrusted metadata containing workspace-relative paths, ranges, and bounded diagnostics; file content is not silently pasted, so inspection still crosses AgentX path and permission checks.

AgentX owns the durable transcript, session lock, resume/fork semantics, task truth, and terminal results. VS Code workspace state contains only a bounded, lossy presentation cache of sessions observed by that extension installation; it redacts prose, omits tool and local-command payloads, and is neither a complete session inventory nor model context. Shutdown first settles host-owned pending controls, requests interruption, closes protocol input, and uses bounded process-tree termination so an extension-host exit does not leave anonymous waiters. The webview has a closed CSP, a single local bundled script, no workspace-resource root or network route, and text-only rendering of model-controlled strings.

## Trust and extension plane

Extension discovery produces immutable session generations with deterministic
source precedence and diagnostics. User extensions are eligible by source
policy; project `AGENTS.md`, root `.codex/skills`, and workspace `.agentx/`
plugin manifests, hooks, output styles, and MCP definitions require
`--trust-workspace`. That workspace directory is not the user application home
selected by `AGENTX_HOME`; workspace trust never changes the application-home
identity or authorizes its credentials. The same repo-local `.codex/skills`
hierarchy documents implementation behavior and is the only runtime skill
source.

Plugin-contributed hooks, output styles, and stdio MCP servers are merged into the session registry with source attribution; plugin skill components are ignored. Skill invocation performs literal positional expansion and can install a turn-local `allowed-tools` scope. That scope is deny-only, honors exact content patterns and every shell segment, resets at the next user prompt, and cannot grant authority denied by base policy. Tool and session hooks execute with bounded input/output and failure containment.

MCP descriptors and results remain untrusted. Adapted MCP tools are open-world and use the ordinary composed permission rules: default policy asks, an exact allow rule may authorize, and denial still wins. Result text and metadata are scrubbed against credentials explicitly configured for that provider. Image and audio blocks are validated but represented as text/metadata placeholders because this profile has no binary attachment path from an MCP tool result to the model. Project configuration is gated by `--trust-workspace`, but that trust flag currently acts as approval; there is no separate per-server approval durably bound to the configuration fingerprint, so changing a trusted project descriptor does not force a new fingerprint-specific prompt. Reconnection cannot mutate the already-frozen tool registry until a new session.

Persistent project memory is resolved independently from the session path by
using the selected absolute workspace and lives at
`<application-home>/projects/<workspace-hash>/memory`, outside—not inside or
implicitly above—the `sessions/<workspace-hash>/<session-id>/` tree. It applies
the configured Azure credential redactor plus bounded secret heuristics and
enters context as attributed fallible notes. Unix mode bits are narrowed and
revalidated around memory operations. The store package retains stable
identity, no-symlink, single-link, and bounded-I/O defenses on Windows but
cannot establish or prove owner-only DACLs. That package-level limitation is
not a reachable standalone memory profile today: Windows model-backed startup
fails closed at credential verification before memory construction. Automatic and
`/compact` projections preserve authoritative history, tool call/result pairs,
and provider-response groups; each installed projection is durably recorded
before reuse and emits the standard SDK `compact_boundary` record. The current
summarizer is a deterministic, bounded, lossy excerpt of dropped context, not
the complete specified semantic compaction, summary, team-memory, or
consolidation subsystem. `/clear` is a separately durable context boundary.

## Feature profile

Availability is represented across independent axes: compiled inclusion, runtime gate, account eligibility, platform support, managed policy, configuration, and health. Presence in source is not treated as availability.

| Domain | Current profile |
| --- | --- |
| Application-home bootstrap and authentication | Operational: a nonblank `AGENTX_HOME`, otherwise `~/.agentx`; this is the sole supported override. One physical home plus `sessions/` is frozen before full CLI parsing; every invocation requires `auth.json`, including malformed input, while model-backed starts strictly parse version 1 with no legacy fallback. POSIX ownership/mode enforcement is operational; Windows credential loading is unavailable without native DACL verification. |
| Azure Responses + `gpt-5.6-sol` | Operational on supported POSIX platforms when the required application-home `auth.json` is private and schema-valid. |
| Headless text and aggregate JSON | Operational over the shared engine. |
| Bidirectional NDJSON | Partial: correlated controls and bounded priority queues are operational; in-flight prompt/context injection and several live initialization/mutation controls are unavailable. |
| VS Code workspace extension | Partial: Activity Bar chat, streaming, tool/result projection, permissions/questions, workspace-scoped sessions, editor references, Restricted Mode gating, diagnostics, and target VSIX packaging are operational; attachments, authoritative session inventory/history replay, live runtime mutation, remote AgentX transport, IDE MCP/LSP bridging, and native qualification of every packaged platform are unavailable. |
| Interactive terminal | Partial: a terminal-safe line REPL is operational; retained rendering, rich editor state, and the full terminal engine are unavailable. |
| Read/Write/Edit/Glob/Grep/Bash/question/task tools | Operational with permission enforcement; text-file profile. |
| Durable transcripts, resume, continue, fork | Partial: append/recovery, response-group uncertainty handling, active-branch projection, and completion-gated fork publication are operational; legacy graph compatibility, rewind, tombstone/snip replay, and general sidechain editing are unavailable. |
| Local background shell and task/work-item state | Operational; restart marks uncertain local processes failed and never replays. |
| Repository `.codex/skills`, plugin manifests, output styles, hooks | Operational as immutable, source-attributed session generations; project sources require explicit trust. |
| MCP stdio client | Partial: lifecycle, tool discovery/calls, generation fencing, and result normalization are operational; media forwarding and fingerprint-bound project approval are unavailable. |
| Standalone MCP stdio tool host | Operational for the local core catalog after the common application-home and `auth.json` existence gates; `--mcp-server` reuses capability/permission contracts without parsing credentials or constructing a model client. |
| MCP HTTP/SSE/WebSocket/OAuth, server-initiated elicitation/channels, and LSP | Configuration or protocol rejection state is represented where applicable; no executable adapter is registered. |
| Persistent project memory | Conditional for non-bare persistent sessions within the local bounded/heuristic profile: it uses an absolute-workspace hash outside the session tree, not canonical linked-worktree identity or configurable memory roots. Unix owner-only mode enforcement is operational; Windows model-backed startup is unavailable before memory construction. |
| Automatic/manual compaction | Partial: durable deterministic excerpt projections are operational; complete specified semantic compaction and distributed memory/consolidation are unavailable. |
| Remote bridge/cloud transport | Contract-only: identity, epoch, replay, gate, ACK, and lifecycle primitives exist, but no transport is registered or fabricated. |
| Delegated-agent/team backend, retained-mode TUI, HTTP/SSE MCP OAuth, LSP, remote bridge transport, voice, browser/computer use | Explicitly unavailable; state/transport contracts do not fabricate an executable backend. |

## Validation strategy

Go tests cover malformed wire input, SSE chunking and terminal failures, retry/cancellation, credential redaction, transcript corruption and unresolved calls, scheduler barriers, exact-once results, permission precedence, symlinks and protected paths, shell compounds/redirections, edited approvals, task races, extension precedence/cycles, MCP correlation, distributed replay gates, shutdown idempotence, and stdout purity. Loopback mock servers validate HTTP without using the production deployment.

Extension unit tests cover NDJSON chunking and bounds, protocol validation and correlation, child shutdown, presentation reduction, workspace cache bounds, editor-context bounds, diagnostic redaction, and webview injection defenses. The VS Code 1.95.3 Extension Host suite activates the built extension without a model turn, checks manifest-to-command registration, and verifies both trusted and Restricted Mode profiles; the Restricted Mode case uses a calibrated executable sentinel to prove that neither automatic nor explicit command paths spawn AgentX. A separate offline smoke test builds the actual Go binary and completes `system/init` plus correlated `initialize` using synthetic credentials and isolated state; it deliberately sends no user turn. Production bundling separates the CommonJS extension host from the browser-only webview bundle, while target packaging cross-builds one native binary, excludes source/tests/dependencies/maps/credential paths from the VSIX, checks archive integrity, and emits a SHA-256 sidecar. Cross-build success is not native runtime evidence.
