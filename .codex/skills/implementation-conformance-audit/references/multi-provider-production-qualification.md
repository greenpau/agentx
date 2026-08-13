# Multi-Provider Production Qualification

This note records sanitized, non-normative execution evidence for a final
current-worktree AgentX artifact, one explicitly identified precursor, and two
operator-configured Azure provider profiles. It supports `AUTH-045` through
`AUTH-048`, `AUTH-A12` through
`AUTH-A15`, `CLI-029` through `CLI-031`, `CLIG-034`, `CLIG-036`, `MOD-086`,
`WIRE-023` through `WIRE-026`, `TX-067A`, `CONF-003`, `CONF-004`, `CONF-010`,
`CONF-016`, and `CONF-020`. It changes none of those contracts and is not a
release attestation, universal deployment claim, or remote introspection of
provider capabilities.

## Contents

1. [Evidence scope](#evidence-scope)
2. [Live execution evidence](#live-execution-evidence)
3. [Passed local and deterministic cases](#passed-local-and-deterministic-cases)
4. [Privacy, integrity, and cleanup](#privacy-integrity-and-cleanup)
5. [Not qualified by this run](#not-qualified-by-this-run)

## Evidence scope

- Final executable: temporary current-worktree AgentX qualification build. It
  was not the unrelated repository `bin/agentx` artifact.
- Final executable SHA-256:
  `00df0a90d2bb49fc8c76ec50642609d791e4aab06d138a1618f269574114d236`.
- A broader precursor run used SHA-256
  `f6056ce7453365cabde0dbc75f4a279a7e741ee8f4272c85e6ab5e0e9e1a6d8d`.
  Source changed afterward to reject surrounding route whitespace, close a
  JSON-whitespace restore bypass, and reject option-looking missing provider
  values. Only evidence explicitly labelled precursor below is attributed to
  that artifact; it is not substituted for final-artifact qualification.
- Host: one supported POSIX development host.
- Registry: strict version 2 with two Azure/OpenAI Responses profiles. The
  default profile's logical model was `gpt-5.6-sol`; the explicit nondefault
  profile's logical model was `gpt-5.4`. Provider IDs matched those logical
  model strings in this environment, but the qualification did not rely on
  that convention.
- Operator-declared reasoning subsets: Sol declared
  `none|low|medium|high|xhigh|max`, default `high`; GPT-5.4 declared
  `none|low|medium|high|xhigh`, default `none`.
- Credentials came from the runtime's ordinary private application-home
  `auth.json`. No key, endpoint, deployment, API selector, account identity,
  absolute credential path, request, response, prompt, or model answer is
  retained here.
- Price was deliberately not used as a qualification constraint. The runtime
  has no authoritative deployment price table, so this note does not report a
  cost or call it zero.

Reasoning declarations are operator metadata. Live acceptance below proves
only these two configured endpoints accepted the exercised values at the time
of this run; it does not turn provider discovery into capability probing.

## Live execution evidence

The authoritative final-artifact run comprised 42 validated processes. Four
preliminary harness attempts were stopped because assertions confused public
provider type, an ambiguous short API selector, optional Read arguments, or an
expected opaque binding with private material. They are harness attribution
errors, not provider/model failures, and are excluded from the counts below.
The unified authoritative run had no provider or model transient.

- Provider-neutral discovery in exact text and one-object JSON forms preserved
  source order, reported discovery protocol version 1, marked every descriptor
  unselected, projected effective defaults and declared reasoning metadata,
  and exposed no credential or private route field. An invalid process effort
  did not affect discovery.
- A fieldless correlated `initialize` request was followed by a live user turn
  in one fresh process per provider. `system/init` identified only the selected
  provider/type/model/reasoning declaration. The correlated response contained
  the complete discovery-equivalent catalog with exactly that provider marked
  selected.
- An invocation without `--provider` selected the declared default. Four final-
  artifact processes ran concurrently across both providers with isolated
  identities, results, usage, and temporary state. In a precursor six-process
  `none|low|medium` repetition, all results were exact and credential-safe but
  one
  Sol/medium process emitted a safe stderr diagnostic, failing the stricter
  silent-success assertion. A complete immediate repetition passed without
  stderr. Raw diagnostics were not retained, so no cause is assigned.
- On the precursor, twenty-two independently computed arithmetic quality probes
  exercised two trials at every declared effort. All eighteen non-`none` trials returned the
  exact oracle result and positive reasoning-token usage. All four deliberately
  reasoning-heavy `none` trials reached the expected endpoint with zero
  reasoning tokens: three completed with a wrong
  answer and one Sol trial ended non-success. An earlier artifact run returned
  four wrong answers. Simpler exact-token, continuation, and media-path
  transport probes at `none` passed. These outcomes are retained as
  model-quality evidence rather than misclassified as routing failures or as a
  promise that `none` solves reasoning-heavy work.
- The final artifact completed one Read-tool continuation at all eleven
  provider/effort combinations. Every case paired accepted tool-use IDs with
  non-error tool results, finished in two model turns with the exact derived
  value, retained the 128,000-token context/output projections, and left the
  read-only fixture byte- and mode-identical. A broader pre-freeze run also
  passed one plan-mode Write denial per provider: each accepted call received
  an error result and permission denial, the model continued, and neither
  destination existed.
- One precursor GPT-5.4/high Read continuation failed after a successful paired
  Read result. A fresh retry and three independent stability probes passed.
  The precursor's frozen-artifact all-effort rerun then passed without a retry,
  and the later final-artifact run passed all eleven combinations without a
  provider/model transient. No raw diagnostics were retained, so the isolated
  earlier failure is reported as a
  transient observation rather than assigned an unsupported root cause.
- A generated non-sensitive PNG containing seven blue squares and five orange
  circles reached the qualified Sol media path and returned the exact counts.
  The GPT-5.4 profile advertised no attachment capability and rejected the same
  input locally with zero provider API duration and no assistant content.
- Each provider created and explicitly resumed a final-artifact durable session
  at its highest declared effort. All 20 durable events carried one complete,
  consistent provider ID/type/model/binding tuple. Cross-provider resume
  failed
  locally in both directions with the recorded provider ID as remediation and
  unchanged transcripts. Representative route drift failed before replay or
  provider I/O; restoring the route allowed a live resume. Revision-bound native
  deletion emptied both qualification workspaces. On the precursor, both
  sessions additionally continued, survived API-key-only rotation in an
  isolated registry, and rejected endpoint, deployment, and API-selector drift
  separately.
- A confirmed running max-effort print request received SIGINT, stopped without
  a false success, exited through the graceful status-0 path, and
  removed its temporary session.

## Passed local and deterministic cases

- Strict discovery grammar rejected prompts, stream JSON, workspace/provider
  flags, duplicate selectors, and duplicate output selectors before stdout.
  Exact, case-changed, whitespace-padded, empty, repeated, and unknown provider
  selectors; a cross-profile `--model`; a model used as a selector; unknown and
  profile-unsupported efforts; and an unsupported environment effort all
  failed locally with empty stdout and no temporary residue. An explicit
  `--effort none` overrode an unsupported process value and completed live.
  The final artifact separately rejected six option-swallowing and repeated-
  provider edge forms before configuration or network activity.
- A singleton without `default` was the effective default and completed live.
  A valid multi-provider registry without a default remained discoverable, its
  ordinary launch failed with the documented remediation, and exact explicit
  selection completed live. Multiple defaults, legacy auth version 1, duplicate
  IDs, an empty registry, an unknown top-level member, and an invalid unselected
  capability all failed before discovery output.
- Loopback integration selected each provider against a distinct TLS endpoint,
  deployment, key, model, and effort. A selected terminal failure with
  `x-should-retry:false` made exactly one selected request and zero requests to
  the healthy alternative, for both the default and explicit nondefault cases.
  A barrier-synchronized pair of explicit-provider requests preserved exact
  route/key/deployment/effort isolation concurrently.
- Existing loopback retry suites retained the selected client and byte-identical
  request, retried only before the first provider event within the bounded
  attempt/time policy, never replayed after an accepted event, honored
  cancellation, and never redirected credentials or attachment payloads.

## Privacy, integrity, and cleanup

- Final-artifact scanners inspected 91,817 public output bytes plus 6,008
  temporary bytes across 18 files. Every retained assertion scanner matched all
  configured keys and endpoint URLs exactly and matched distinguishable private
  route values and private JSON member names. A configured two-byte API selector cannot be attribution-scanned
  as a literal because it collides with ordinary protocol text; private-member
  exclusion, complete-key/endpoint scanning, structured-field checks, and
  deterministic loopback capture cover that ambiguity.
- No scanned live stdout, stderr, SDK catalog, aggregate result, transcript, or
  failure exposed credential material, endpoint URLs, distinguishable private
  route values, authentication headers, or private provider objects.
- The real 74-entry application-home tree was byte-for-byte unchanged across
  the final run and precursor adversarial no-persistence matrix. It remained
  mode `0700`; `auth.json` remained `0600`. Isolated auth copies were restored
  before cleanup.
- No no-persistence `agentx-session-*` tree remained. Tool workspaces and
  fixtures were unchanged, denied Write targets were absent, all durable test
  sessions were removed through native revision-bound deletion, and subsequent
  inventories were empty. Temporary harnesses and captured raw model/provider
  output were removed; ordinary deletion is not secure erasure.

## Not qualified by this run

- Any packaged, signed, installed-release, Windows, Linux, remote-host, proxy,
  enterprise-policy, companion-extension, or other-platform artifact.
- Provider profiles, accounts, regions, deployments, API selectors, model
  versions, reasoning efforts, context/output limits, credentials, or route
  changes other than the two exact environment-local profiles exercised here.
- Literal endpoint URL discovery. The public interface intentionally describes
  endpoint profiles while withholding their routes.
- Provider-side effort support beyond observed acceptance, deterministic model
  correctness at `none` for reasoning-heavy arithmetic, latency/throughput/SLA,
  authoritative cost, capacity behavior under real throttling, or live provider
  outage/no-fallback injection. Deterministic loopback tests remain authority
  for exact headers, route isolation, retry boundaries, and zero alternate calls.
- Native media beyond the separately qualified Sol profile and generated PNG.
  The prior attachment qualification remains authority for its JPEG/PDF,
  stream, fork, compaction, and media privacy scope; GPT-5.4 remains text-only.
- A released VS Code provider picker or setting. This run qualifies the binary
  discovery/initialization contract that an editor host can consume.

Repeat this qualification with the exact candidate artifact and every profile,
selector, platform, modality, and release claim before presenting the evidence
as broader support.
