---
name: implementation-conformance-audit
description: Audit or refresh AgentX implementation coverage, route reachability, contract traceability, diagram validity, and conformance evidence. Use after changing application source, AGENTS.md, an implementation skill, a Draw.io diagram, or a runtime boundary that changes documented behavior.
---

# Implementation Conformance Audit

Prove that the engineering-guidance graph is navigable, every source artifact has a primary behavioral owner, and source, tests, contracts, diagrams, and conformance claims remain consistent.

Open [architecture.drawio](assets/architecture.drawio) for the evidence chain. Apply [the architecture diagram contract](../implementation-architecture/references/diagram-contract.md) when reviewing any Draw.io asset. Read [coverage-model.md](references/coverage-model.md) before interpreting the generated [artifact ledger](references/source-coverage.tsv) and independently reviewed [source-to-contract trace](references/source-contract-trace.tsv), and read [conformance-matrix.md](references/conformance-matrix.md) with the generated [contract-to-scenario manifest](references/contract-scenario-coverage.tsv) when evaluating project conformance.

For the current worktree's sanitized, environment-scoped live Azure attachment
run, read [native-attachment-production-qualification.md](references/native-attachment-production-qualification.md).
It is non-normative execution evidence for one tested profile, not a source
mapping, universal provider claim, release-artifact attestation, or replacement
for `MOD-A14B` on another deployment.

For the current worktree's sanitized multi-provider registry, discovery,
reasoning, tool-continuation, continuity, privacy, and cleanup run, read
[multi-provider-production-qualification.md](references/multi-provider-production-qualification.md).
It is likewise environment- and artifact-scoped evidence rather than a
provider capability probe, universal deployment claim, or release attestation.

## Audit workflow

1. When application source changes, run `ruby .codex/skills/implementation-conformance-audit/scripts/build_source_coverage.rb` to refresh [source-coverage.tsv](references/source-coverage.tsv).
2. Review each changed artifact's assigned leaf skill. Restate new behavior as a language-neutral contract with a stable ID, failure rules, and an acceptance scenario; a fresh hash alone is not coverage. After review, update only that artifact's row in `source-contract-trace.tsv` with the new reviewed hash, owner, contract anchors, scenario suites, review generation, and any boundary classification.
   For `make release`, `scripts/review_release_evidence.rb` may attest the `main.go` row only when the previously committed source is already reviewed and the complete source delta consists of the two version fallbacks advancing to the next patch named by `VERSION`. It preserves the existing semantic bindings, requires `PLAT-005` and `PLAT-A07`, and fails closed for any other source or evidence change.
3. Run `ruby .codex/skills/implementation-conformance-audit/scripts/audit_architecture.rb`. It must reject a refreshed ledger whose reviewed trace still names the old hash.
4. After changing any stable contract/scenario definition, regenerate `contract-scenario-coverage.tsv` with `ruby .codex/skills/implementation-conformance-audit/scripts/build_contract_scenario_coverage.rb`; each contract is a separate parameterized conformance-suite instance.
5. Repair missing routes, cycles, placeholders, stale metadata, absent/invalid Draw.io files, source drift, unowned artifacts, or contract-evidence gaps.
6. Run the default skill validator for every changed skill.
7. Regenerate generated overview diagrams and run their check-only mode.
8. After editing a custom Draw.io asset, run `ruby .codex/skills/implementation-architecture/scripts/enhance_custom_drawio.rb`, then run the same command with `--check`. The enhancer standardizes the visible standalone context band and edge metadata; it does not prove that the underlying topology is correct.
9. Render every changed page at 100% with `ruby .codex/skills/implementation-conformance-audit/scripts/render_drawio_preview.rb INPUT.drawio --output-dir OUTPUT_DIRECTORY --page PAGE`. Use `--list-pages` first for a multi-page asset and use a fresh `--output-name` when comparing revisions. Inspect labels, ports, waypoints, and context panels; reject edge-through-node, ambiguous shared segments, clipped context, or unreadable hierarchy.
10. Forward-test at least one normal, one failure, one cancellation, one recovery, and one disabled-profile scenario using only the public project contract relevant to that scenario.

## Coverage rules

- `AUD-001` — Each application source artifact has exactly one primary leaf-skill owner in the ledger; collaborating owners are named in contract prose rather than duplicated in the ledger.
- `AUD-002` — Ledger line counts and hashes identify the evidence snapshot. They never substitute for behavior, ordering, schemas, error rules, or acceptance scenarios.
- `AUD-003` — Every repo-local skill is reachable from `AGENTS.md` through actionable, acyclic `Use [skill](path) to ...` routes.
- `AUD-004` — Every implementation skill contains and links at least one valid `.drawio` document.
- `AUD-005` — Every leaf contract uses stable IDs and language-neutral acceptance scenarios, with provenance isolated as non-normative.
- `AUD-006` — Unknown, build-eliminated, server-side, or missing-reference behavior is labeled as an inferred/opaque boundary with observable assumptions; it is not fabricated.
- `AUD-007` — Conformance compares canonical events, durable records, decisions, byte protocols where applicable, and side effects—not private names or module layout.
- `AUD-008` — A source fingerprint establishes evidence scope only. A source artifact is review-covered only when a separate trace row binds that exact hash to one or more owning contract IDs and executable scenario-suite IDs. Refreshing the artifact ledger never refreshes this reviewed binding; a changed hash, owner, missing contract, or missing scenario leaves the artifact uncovered until deliberate review updates the trace.
- `AUD-009` — Every stable normative contract is mapped explicitly to a parameterized conformance suite. The suite must be instantiated with that contract ID; running one representative scenario for an entire domain is not coverage.
- `AUD-010` — A trace row that names only its leaf's broad entry contract is classification, not semantic review. Every artifact must name at least one narrower contract that explains the behavior actually specified from that artifact; the audit rejects generic summary-only rows.
- `AUD-011` — Every Draw.io page is self-orienting: it names its context, question, start, end, owner, deferred owners, contract anchors, authority, and canonical-lifecycle position according to `ARCH-DGM-003..005`.
- `AUD-012` — Every behavioral edge is labeled and visually unambiguous. No edge crosses any node—including its own incident node after touching the selected port—or any unrelated label; no collinear segment overlap implies an unstated merge; and every fan-out, fan-in, skip, feedback, retry, or recovery relation uses distinct ports, explicit rails/waypoints, a junction, or a visible line jump. A topology-derived `handoff` label asserts adjacency only and cannot fabricate payload, policy, durability, or terminal semantics.
- `AUD-013` — Diagrams distinguish owned scope from external context and explicitly represent actor, authority, trust-zone, process, store, and lifetime changes where applicable.
- `AUD-014` — A diagram cannot introduce exact behavior absent from its prose anchors. Diagram/prose disagreement fails audit, with numbered prose governing until repaired.

## Interpreting failures

A routing or XML failure is mechanical and must be fixed before review. A source hash mismatch requires behavioral review even if the line count is unchanged. A passing audit means the evidence graph is internally consistent; it does not by itself prove that the prose captured the correct semantics. Use the conformance matrix and independent forward tests for that proof.

## Acceptance scenarios

### `AUD-A01` — New source file

Adding a source artifact makes the audit fail until the ledger is refreshed. Review then assigns its behavior to a leaf contract and adds or updates acceptance evidence. The final audit reports one owner and the new aggregate source totals.

### `AUD-A02` — Orphaned subskill

Creating a valid skill directory without an actionable route causes a reachability failure even if another Markdown link mentions it.

### `AUD-A03` — Diagram placeholder

A missing, malformed, or unlinked Draw.io document fails the audit. A valid XML file with no diagram page or graph model also fails.

### `AUD-A04` — Contract usability

Give a contributor `AGENTS.md`, the routed skills, and the source area being changed. If ownership, required state transitions, ordering, timeout, permission, recovery, and verification expectations remain ambiguous, the owning contract is incomplete even when the mechanical audit passes.

### `AUD-A05` — Hash refreshed without semantic review

Change one source artifact and refresh only the generated fingerprint ledger. The audit still fails because the reviewed source-to-contract trace retains the prior hash. Updating that trace requires naming the reviewed contract anchors, scenario suites, review generation, and any opaque or excluded boundary; copying the new hash alone is not a semantic review procedure.

For the narrowly guarded release case, advance only the two `main.go` source-controlled version fallbacks and `VERSION` to the next patch. The release evidence attestor accepts the already reviewed build-identity bindings and updates the reviewed hash. An additional source edit, stale prior review, mismatched version, missing `PLAT-005` or `PLAT-A07` binding, or unexpected working-tree change makes the attestation fail without updating the trace.

### `AUD-A06` — Generic trace rejected

Bind an artifact only to its owner's broad `*-001` entry contract and the owner's conformance suite. The audit rejects the row even though its owner, hash, and suite are valid. Bind it to one or more narrower contracts that state its specified semantics before claiming review coverage.

### `AUD-A07` — Contextless valid graph

A well-formed graph with enough nodes and edges but no product breadcrumb, owned boundary, lifecycle position, legend, contract anchors, or authority note fails.

### `AUD-A08` — Ambiguous routing

Two behavioral edges sharing a segment without a junction, crossing a vertex, obscuring a label, or terminating through one ambiguous port fail even though every endpoint ID exists.

### `AUD-A09` — Diagram/prose conflict

A diagram that changes ordering, ownership, timing, durability, or terminal behavior relative to its declared prose anchors fails. The numbered prose remains authoritative until the diagram is corrected.

### `AUD-A10` — Independent page orientation

Give a reviewer only one rendered page. They must identify its product location, question, owner, upstream input, downstream outcome, line meanings, deferred domains, and authoritative prose anchors.
