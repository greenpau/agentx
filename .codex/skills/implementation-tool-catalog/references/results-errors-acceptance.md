# Results, persistence, errors, and conformance

## Result normalization

A successful call returns internal `data`, then maps it to one model `tool_result` block carrying the original `tool_use_id`. It may deliberately add new messages. A context modifier is honored only for serialized tools so concurrent calls cannot race context state. MCP results may carry validated `_meta` and `structuredContent`; these never replace the paired result.

If a tool returns empty or whitespace-only textual output, normalize it to `(<CanonicalName> completed with no output)`. Preserve images and other supported content blocks. Do not stringify binary data or implementation objects accidentally.

## Per-result limits

Use these specified constants:

| ID | Value | Meaning |
| --- | --- | --- |
| RL-001 | 50,000 characters | Default/global maximum for a declared finite tool result. A runtime override may change the clamp; `Infinity` remains a hard opt-out. |
| RL-002 | 100,000 tokens | Absolute result token safety threshold. |
| RL-003 | 4 bytes/token | Conservative conversion used to derive a 400,000-byte safety threshold. |
| RL-004 | 200,000 characters | Default aggregate fresh-tool-result budget for one wire-equivalent message group. |
| RL-005 | 50 characters | Concise tool-summary target. |
| RL-006 | 2,000 characters | Stored-result preview included in the replacement message. |

A descriptor declaring 100,000 is therefore ordinarily clamped to 50,000. `Bash`/`PowerShell` declare 30,000, `Grep` 20,000, MCP authentication 10,000, and `Read` explicitly declares `Infinity`. Positive finite runtime overrides may alter the default/aggregate limits only when their feature gates are enabled. Invalid, zero, negative, NaN, or infinite overrides are ignored.

## Persisted overflow

Persist oversized textual output under the session's `tool-results` directory as `<tool-use-id>.txt` or `.json`.

1. Use exclusive creation so retries cannot overwrite prior evidence. On already-exists, reuse the existing artifact.
2. Store the complete eligible textual result and replace model-visible content with a stable `<persisted-output>` record containing saved path, size/summary, and the first 2,000 characters.
3. Persist arrays only when every block is textual. Image-bearing results bypass this mechanism and use media-specific bounds.
4. If persistence fails, retain the original result rather than losing it; record a diagnostic without making telemetry a correctness dependency.
5. Freeze the exact persistence decision and replacement text by tool-use ID. Reprojection and resume reuse it byte-for-byte.

For the aggregate budget, group consecutive user/tool-result messages that are equivalent on the wire. Consider only fresh eligible results. Replace the largest candidates until within budget. Do not replace `Read`, `Infinity` descriptors, prior results that were once retained, or frozen prior decisions. If frozen results alone exceed the budget, accept them rather than rewriting history.

## Error taxonomy

Every accepted call terminates as exactly one of:

| ID | Class | Required result behavior |
| --- | --- | --- |
| ER-001 | Unknown/disabled | Fail closed with canonical/alias and availability context; do not execute. |
| ER-002 | Structural invalidity | Report schema paths and expected shape without exposing secrets. |
| ER-003 | Semantic invalidity | Explain violated precondition and actionable correction. |
| ER-004 | Denied | Preserve denial source and safe reason; never invoke implementation. |
| ER-005 | User rejected/cancelled | Distinguish rejection, dismissal, and session cancellation. |
| ER-006 | Timeout/interrupted | State whether underlying work stopped or may still be running under a task identity. |
| ER-007 | Execution failure | Preserve bounded stdout/stderr or external status and attribution. |
| ER-008 | Malformed result | Fail the call; do not feed invalid extension output to the model. |
| ER-009 | Sibling/cascade cancellation | Emit an explicit terminal result for this tool-use ID even if it never started. |
| ER-010 | Recovery orphan | Reconcile an accepted call found without a result before accepting a new turn. |

Unknown aliases, stale feature state, disconnected MCP tools, and removed tasks are ordinary explicit errors, not process crashes. Rendering and telemetry failures do not change the semantic terminal state.

## Cross-catalog acceptance suite

- **TC-A01 Registry determinism:** Randomize external discovery order; executable registry order and built-in collision winners remain identical.
- **TC-A02 Profile isolation:** Snapshot main, simple, REPL, async-agent, teammate, coordinator, and structured-headless registries; each matches the profile contract and deny rules.
- **TC-A03 Dynamic classifier:** Run two inputs through one descriptor where one is read-only and one writes; permissions and scheduling differ per invocation.
- **TC-A04 Terminal pairing:** Inject denial, validation error, timeout, cancellation, sibling failure, and malformed MCP output; every accepted ID has one and only one result.
- **TC-A05 Result clamp:** A 60,000-character result from a 100,000-declared tool persists/truncates at the effective 50,000 clamp; a `Read` result follows its separate contract.
- **TC-A06 Aggregate freeze:** Reproject and resume an over-budget message group; exactly the same tool IDs are persisted with exactly the same replacement text.
- **TC-A07 Persistence race:** Two retries persist one tool ID concurrently; one artifact wins, neither overwrites it, and both projections reference the same content.
- **TC-A08 Extension safety:** Fuzz MCP names, schemas, annotations, progress, content blocks, and metadata; malformed values fail locally without registry corruption.
- **TC-A09 Disabled state:** Toggle every build/runtime/platform/account/policy gate; absent capabilities disappear cleanly and stale invocations fail explicitly.
- **TC-A10 Cancellation:** Cancel one running serialized tool and one interruptible sleep; dependent scheduling unblocks only after terminal results exist.
- **TC-A11 Transcript/UI separation:** Progress, previews, and dialogs render without silently entering model context; deliberate tool results and new messages do enter it.
- **TC-A12 Boundary consistency:** Run the same wire fixtures through alternate adapters; canonical schemas, results, ordering, paths, and state transitions remain equivalent.

## Coverage ledger

Maintain one row per registry-matrix ID with implementation status, schema fixtures, enabled/disabled tests, classifier tests, permission scenarios, output fixtures, failure fixtures, resume fixtures, and owner. A capability is not complete merely because its happy-path call executes.
