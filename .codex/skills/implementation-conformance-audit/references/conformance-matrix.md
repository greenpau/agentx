# Conformance Matrix

Use this matrix against an implementation. Attach traces to stable contract IDs rather than reference symbols.

Each row is a parameterized scenario suite. For every source artifact mapped to
the suite, instantiate its named contract anchors in both the normal and fault
trace; merely running one representative feature does not cover every row.

| Scenario suite | Domain | Normal trace | Required fault trace | Required invariant |
| --- | --- | --- | --- | --- |
| `CONF-000` | Cross-domain architecture and routers | one complete turn through each applicable surface/domain route | fault at every ownership handoff named by the contract under test | adapters preserve canonical identity, authority, ordering, persistence class, and terminal evidence |
| `CONF-001` | Startup/settings | fresh trusted interactive launch | invalid higher-priority setting plus recoverable lower source | no trust-dependent execution before trust |
| `CONF-002` | State/context | two turns with a mode/model change | stale async update races a newer snapshot | process, session, turn, and durable state remain distinct |
| `CONF-003` | Query/model | streamed text, ordered native PNG/JPEG/conservative-PDF content, tool, continuation, final; separately scoped installed-runtime qualification | partial stream, unsupported media/profile, missing or tampered blob, media rejection, overload retry, fallback, abort | accepted typed input is durable, media is neither duplicated nor silently dropped, and every iterator/resource closes |
| `CONF-004` | Tool protocol | validated read and write | malformed input, denial, thrown hook, sibling failure | one live-turn terminal result per accepted tool ID and explicit crash recovery |
| `CONF-005` | Tool catalog | one normal call from each exposed capability family | schema edge, unavailable dependency, cancellation, and disabled profile | descriptor metadata agrees with execution and result behavior |
| `CONF-006` | Permissions/sandbox | scoped allow and approved ask | symlink escape, dangerous command, unavailable required sandbox | deny wins and no side effect precedes approval |
| `CONF-007` | Tasks | start, progress, output delta, completion | kill racing process exit and crash around notification enqueue | terminal status never regresses; same-process notification gate and crash window are explicit |
| `CONF-008` | Terminal engine | layout, input, diff, focus, and frame write | resize, split escape sequence, width ambiguity, output failure | byte output and cell geometry remain deterministic for the declared terminal profile |
| `CONF-009` | Interactive surface | edit, submit, stream, scroll, dialog | resize/input split/focus loss/cancel | UI-only state never enters model context |
| `CONF-010` | Headless/SDK | initialize with scoped-source attachment capability, correlated begin/chunk/commit, typed prompt, control request, result, idle; provider-free native session list and one revision-bound deletion | capability absence, stale bare-string source negotiation, malformed/duplicate/reordered/oversized import, concurrent/terminal-ledger/session-manifest ceilings, late duplicate response, EOF, invalid `now`, rejected conversation-option mix, stale page/revision, lock contention, and cleanup-pending status | single FIFO output, stable prompt UUID, atomic typed admission, independent durable-manifest and terminal-upload bounds, and exactly one upload terminal acknowledgement for SDK mode; management emits only its one versioned object and never starts a semantic session |
| `CONF-011` | Optional experiences | one eligible flow for each included optional family | absent build, failed dependency, revoked eligibility | optional failure does not corrupt the shared session |
| `CONF-012` | Commands/input | discovery, expansion, repeatable selected-file import, attachment-only prompt, local and prompt command | path replacement/growth/truncation, link/directory/unreadable/unsupported/oversized input, slash with attachments, missing committed reference, escaped PDF names, comment-spoofed PDF structure, invalid classic xref/page graph, object/xref stream, incremental PDF update, cancellation | command provenance and effect type remain explicit; attachment-bearing input cannot become a local command and the set is admitted atomically |
| `CONF-013` | Skills/output | discover, select, invoke, and restore | malformed skill, policy exclusion, missing resource | runtime skill context changes only at its declared boundary |
| `CONF-014` | Plugins/hooks | deterministic discovery and lifecycle invocation | corrupt plugin, blocked marketplace, timeout, failed hook | one bad entry does not corrupt other registries |
| `CONF-015` | MCP/LSP/IDE | connect, discover, call or language/IDE request | auth required, session invalid, crash/restart | remote input is validated, generation-fenced, and bounded |
| `CONF-016` | Transcript/recovery | append typed manifests, flush, attachment resume/fork copy, workspace inventory, and identity-bound delete | truncated tail, missing/tampered blob, orphan upload, orphan tool pair, DAG sibling, incomplete fork, unsafe/link/replacement identity, concurrent selection/delete, and crash at each deletion commit boundary | recovery preserves evidence and does not invent success; no bytes or paths enter the transcript, referenced blobs remain retained, inventory and all selectors share one authority, and retained session data is never called deleted |
| `CONF-017` | Memory/compaction | discover, extract, synchronize, compact typed media, restore | secret rejection, lock loss, summary overflow, media-limit pressure, quarantined/damaged blob, stale generation | authoritative transcript and attachment manifests remain intact while provider media context stays bounded |
| `CONF-018` | Remote/bridge | connect, deliver, acknowledge, reconnect | replay, epoch mismatch, lost permission response | identities and known loss windows remain explicit |
| `CONF-019` | Multi-agent | spawn, message, task completion, cleanup | orphan child, leader loss, worktree conflict | parent/child authority and transcript ownership stay explicit |
| `CONF-020` | Auth/network | select credentials/provider and exact loopback text/PNG/JPEG/conservative-PDF request; run `MOD-A14B` separately for every claimed artifact/deployment profile | refresh race, proxy failure, revoked token, unsupported media profile, every media/request-size boundary, provider media rejection | secrets and attachment bytes/paths remain behind the port and are redacted; known-invalid media makes zero calls and rejected media does not retry |
| `CONF-021` | Platform/lifecycle and portable data primitives | process/file/terminal resource use plus one normal parse, serialization, cache, collection, sequence, hash, bounded-text, error projection, read-only/exclusive owned-child acquisition, existing-only lock, no-replace detach, sync, and strict descriptor-rooted cleanup per mapped contract | shutdown mid-write, signal storm, OS feature absent, malformed/oversized data, stat/read and clear/refresh races, unit-boundary input, dependency/profile absence, raced destination, parent/child/lock replacement, link or mount boundary, unsupported durability, and every documented unsupported-value edge | cleanup is bounded and idempotent only where promised; strict cleanup never treats a missing identity as success; data units, collision/recovery policy, ordering, identity, durability, bounds, and intentional divergences exactly match the instantiated contract |
| `CONF-022` | Observability | usage, attachment import, and lifecycle events | malformed hostile media plus every sink unavailable and unknown schema field | semantic output and exit status remain unchanged; bytes, base64, source/storage paths, data URLs, and provider bodies never enter observation |
| `CONF-023` | Implementation evidence | instantiate one named contract from its routed owner using only skills | stale source hash, missing route/diagram/scenario, or contradictory contract | no green audit from an unreviewed artifact or uninstantiated normative contract |

## Trace format

Record:

1. profile axes and surface;
2. input and relevant initial state;
3. ordered canonical events with correlation IDs;
4. permission and policy provenance;
5. durable records and filesystem/process/network side effects;
6. terminal state and cleanup evidence;
7. applicable contract IDs;
8. intentional divergence, if any.

Normalize timestamps, random IDs, costs, environment-specific paths, and nondeterministic progress timing before comparison. Do not normalize away semantic ordering, discriminator values, error classes, decision sources, or terminal outcomes.

The generated [contract-to-scenario manifest](contract-scenario-coverage.tsv) binds every stable normative contract to one of these parameterized suites. A suite row is a template, not one representative test: instantiate it separately for every mapped contract ID and record that ID in the trace. Detailed `*-A*` scenarios add stronger fixtures where present; they do not excuse skipping the parameterized instance for another contract in the same domain.
