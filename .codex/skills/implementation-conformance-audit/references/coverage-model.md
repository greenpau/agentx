# Coverage Model

## Contents

1. Meaning of exhaustive coverage
2. Evidence layers
3. Primary ownership
4. Unknown behavior
5. Review procedure

## Meaning of exhaustive coverage

“Every line” is treated as an evidence obligation, not an instruction to transliterate source. Each application source artifact is fingerprinted, counted, and assigned to one leaf skill. Every behavior carried by that artifact must be restated in the owner's language-neutral contracts. A standalone implementation consumes the contracts and diagrams, never the ledger or source.

For this Go repository, the live application-source inventory is every regular
`*.go` file at the repository root and below `pkg/`, excluding only
`*_test.go`. The exclusion is about evidence type, not importance: test files
remain executable scenario evidence and must run in CI, while the artifact
ledger tracks production code that can enter a shipped binary. Platform-tagged,
runtime-gated, test-profile, and generated production Go files remain in scope.
The inventory must be nonempty, and an unclassified new production file fails
ledger generation and audit.

This prevents two opposite failures:

- line-by-line paraphrase that remains coupled to the original language and architecture;
- high-level prose that cannot demonstrate whether an implementation area was examined.

## Evidence layers

| Layer | Purpose | Proof |
| --- | --- | --- |
| Artifact ledger | Show that no application source artifact or changed byte escaped review | path, line count, byte count, SHA-256, primary owner |
| Reviewed trace | Prevent a mechanical hash refresh from masquerading as semantic review | exact reviewed hash, owner, contract anchors, scenario suites, review generation, boundary class/reason |
| Contract | State implementation behavior without implementation-language dependencies | stable ID, inputs/outputs, state, ordering, errors, invariants |
| Diagram | Make lifecycle, dependencies, or concurrency visually testable | Draw.io graph linked by the skill |
| Acceptance scenario | Define observable parity | preconditions, event/side-effect outcome, failure outcome |
| Conformance trace | Compare a rebuild with the contract | canonical event sequence and durable/side-effect assertions |

Coverage is complete only when all layers agree. The generated ledger and independently reviewed trace intentionally have different update paths: changing source and refreshing fingerprints leaves the old reviewed hash behind, so the audit remains red until a reviewer updates the semantic binding. The one-time trace initializer emits only `unreviewed` scaffold rows with empty anchors; it can never manufacture a passing review from owner defaults. Binding an artifact only to its leaf's broad entry contract proves classification, not review, and is rejected even when the row also names a narrow contract from another domain; each row needs a narrower semantic anchor from its primary owner. A ledger row with no relevant contract is “examined but undocumented.” A contract without a ledger owner may be a deliberate product invariant, but it must still have acceptance evidence.

Large observable data declarations—command identities, model catalogs, configuration schemas, generated event fields, or readable identifier lexicons—need exact standalone tables or wire schemas rather than a phrase such as “include the built-ins.” Where practical, the audit regenerates or compares these manifests against the evidence snapshot so omitted order, duplicate weighting, discriminator, or field optionality cannot hide behind a passing artifact hash.

## Primary ownership

Assign shared behavior to the narrowest leaf that enforces the authoritative rule. Examples:

- the permission skill owns whether an edit is allowed;
- the interactive skill owns how the approval is displayed;
- the headless skill owns the structured permission request;
- the remote skill owns relay and replay;
- the tool protocol owns how the final decision becomes a paired tool result.

Do not copy the decision algorithm into all four skills. Collaborating skills reference the authoritative contract ID and specify only their adaptation.

Ownership classification is explicit. There is no “miscellaneous utility” or platform fallback: an unmatched new source artifact makes ledger generation fail until its enforcing domain is chosen. Mixed presentation/authority artifacts keep one primary owner in the ledger and may name additional collaborating contract anchors in the reviewed trace; directory location alone never moves permission, transcript, or policy authority into a UI adapter.

Generated or wire-schema source still needs an owner because field names, optionality, discriminator values, and compatibility behavior are observable. Decorative assets and archive files are excluded from the application-source ledger but may have separate provenance.

Historical source traces for an implementation that is absent from this
repository are non-normative provenance and must not be mixed into the active
trace. The legacy TypeScript review is retained under `references/provenance/`;
only rows that exactly match the current production-Go ledger are live
conformance evidence.

## Unknown behavior

Use one of these labels:

- **Normative:** directly supported and required for parity.
- **Client-observed:** server implementation is absent, but client messages and reactions define an observable protocol assumption.
- **Inferred:** multiple call sites imply a contract; mark the inference and provide a falsifiable scenario.
- **Opaque optional boundary:** implementation is build-eliminated or absent; specify availability checks, interface, disabled behavior, and containment only.
- **Intentional divergence:** the implementation deliberately strengthens or changes an existing compatibility behavior; name the affected contract, reference compatibility impact, and precise safety or migration rationale.

Never convert comments, telemetry labels, or dead imports into normative behavior without corroborating runtime evidence.

## Review procedure

For every changed ledger row:

1. Inspect the complete artifact and its callers/callees at the behavioral boundary.
2. Identify state mutations, inputs, outputs, ordering, side effects, constants, retries, cancellation, errors, and cleanup.
3. Update the primary owner's contract and any adapter-specific collaboration rule. Name those narrower contracts in the reviewed trace; never leave the row bound only to its broad domain entry anchor.
4. Add a normal and material edge/failure scenario.
5. Update diagrams when topology, lifecycle, or ordering changes.
6. Refresh the ledger and run both mechanical and independent standalone tests.

Refreshing the generated ledger never updates `source-contract-trace.tsv`.
After source changes, a reviewer must inspect the changed artifact and
deliberately update its reviewed hash and semantic anchors. This separation is
what makes a stale review fail closed instead of turning a mechanical hash
refresh into a passing semantic audit.

The sole guarded automation is
`scripts/review_release_evidence.rb`, which attests an exact patch-release
change to the two `main.go` source-controlled version fallbacks. It requires the
committed source hash to have an existing reviewed build-identity binding,
requires the new fallbacks and `VERSION` to agree on the next patch, requires
the regenerated ledger to match the new source, preserves all semantic fields,
and rejects every unrelated working-tree or source change. This is a bounded
reuse of an existing review, not a general hash-refresh path.
