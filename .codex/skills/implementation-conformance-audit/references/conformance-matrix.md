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
| `CONF-003` | Query/model | streamed text, tool, continuation, final | partial stream, overload retry, fallback, abort | accepted input is durable and every iterator/resource closes |
| `CONF-004` | Tool protocol | validated read and write | malformed input, denial, thrown hook, sibling failure | one live-turn terminal result per accepted tool ID and explicit crash recovery |
| `CONF-005` | Tool catalog | one normal call from each exposed capability family | schema edge, unavailable dependency, cancellation, and disabled profile | descriptor metadata agrees with execution and result behavior |
| `CONF-006` | Permissions/sandbox | scoped allow and approved ask | symlink escape, dangerous command, unavailable required sandbox | deny wins and no side effect precedes approval |
| `CONF-007` | Tasks | start, progress, output delta, completion | kill racing process exit and crash around notification enqueue | terminal status never regresses; same-process notification gate and crash window are explicit |
| `CONF-008` | Terminal engine | layout, input, diff, focus, and frame write | resize, split escape sequence, width ambiguity, output failure | byte output and cell geometry remain deterministic for the declared terminal profile |
| `CONF-009` | Interactive surface | edit, submit, stream, scroll, dialog | resize/input split/focus loss/cancel | UI-only state never enters model context |
| `CONF-010` | Headless/SDK | initialize, prompt, control request, result, idle | malformed NDJSON, late duplicate response, EOF | single FIFO output and correlated requests |
| `CONF-011` | Optional experiences | one eligible flow for each included optional family | absent build, failed dependency, revoked eligibility | optional failure does not corrupt the shared session |
| `CONF-012` | Commands/input | discovery, expansion, attachment, local and prompt command | collision, malformed frontmatter, missing attachment, cancellation | command provenance and effect type remain explicit |
| `CONF-013` | Skills/output | discover, select, invoke, and restore | malformed skill, policy exclusion, missing resource | runtime skill context changes only at its declared boundary |
| `CONF-014` | Plugins/hooks | deterministic discovery and lifecycle invocation | corrupt plugin, blocked marketplace, timeout, failed hook | one bad entry does not corrupt other registries |
| `CONF-015` | MCP/LSP/IDE | connect, discover, call or language/IDE request | auth required, session invalid, crash/restart | remote input is validated, generation-fenced, and bounded |
| `CONF-016` | Transcript/recovery | append, flush, resume, fork | truncated tail, orphan tool pair, DAG sibling | recovery preserves evidence and does not invent success |
| `CONF-017` | Memory/compaction | discover, extract, synchronize, compact, restore | secret rejection, lock loss, summary overflow, stale generation | authoritative transcript remains intact |
| `CONF-018` | Remote/bridge | connect, deliver, acknowledge, reconnect | replay, epoch mismatch, lost permission response | identities and known loss windows remain explicit |
| `CONF-019` | Multi-agent | spawn, message, task completion, cleanup | orphan child, leader loss, worktree conflict | parent/child authority and transcript ownership stay explicit |
| `CONF-020` | Auth/network | select credentials/provider and request | refresh race, proxy failure, revoked token | secrets remain behind the port and are redacted |
| `CONF-021` | Platform/lifecycle and portable data primitives | process/file/terminal resource use plus one normal parse, serialization, cache, collection, sequence, hash, bounded-text, and error projection per mapped contract | shutdown mid-write, signal storm, OS feature absent, malformed/oversized data, stat/read and clear/refresh races, unit-boundary input, dependency/profile absence, and every documented unsupported-value edge | cleanup is bounded and idempotent where promised; data units, collision/recovery policy, ordering, identity, bounds, and intentional divergences exactly match the instantiated contract |
| `CONF-022` | Observability | usage and lifecycle events | every sink unavailable and unknown schema field | semantic output and exit status remain unchanged |
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
