---
name: implementation-query-model
description: Implement the shared conversation turn engine, API-bound message projection, model streaming, retry and fallback, stop processing, derived progress/prompt assistance, speculative execution, and continuation decisions. Use when implementing or reviewing prompt submission, recursive model/tool turns, streamed content assembly, usage accounting, model recovery, structured output, token/turn limits, prompt suggestions, speculation, or API-safe conversation normalization.
---

# Implementation Query and Model Runtime

Implement semantic query engine shared by interactive, headless, SDK, agent, and remote adapters. Keep presentation, transcript persistence, tool execution, and compaction behind explicit collaborators; this skill owns the ordering and decisions that connect them.

## Implementation workflow

1. Read the complete [query and model contract](references/query-model-contract.md) before changing turn execution, API payload construction, model streaming, retry logic, or message repair.
2. Read the complete [model and provider catalog](references/model-catalog.md) before changing model aliases, provider mappings, defaults, allowlists, options, capability predicates, context/output limits, validation, deprecation, delegated-model inheritance, or overload fallback.
3. Use the [architecture diagram](assets/architecture.drawio) to trace the normal loop and recovery edges, the [model resolution and availability diagram](assets/model-resolution-availability.drawio) for selection and provider/capability boundaries, and the [derived assistance and speculation diagram](assets/derived-assistance-speculation.drawio) for stop-phase scheduling, stale suggestion generations, copy-on-write boundaries, and non-atomic acceptance. Treat the written `QM-*` and `MOD-*` requirements as authoritative when a diagram omits detail.
4. Model durable history, API-bound history, partial stream state, and presentation events as separate data products. Never mutate durable history merely to satisfy one provider's wire rules.
5. Implement one iteration as an explicit state transition. Preserve the ordering of projection, context-pressure handling, streaming, recovery, tool continuation, hooks, queued input, and limit checks.
6. Make every accepted tool-use identifier terminal before leaving or retrying an attempt. Repair malformed historical pairing only in the API projection unless strict validation is enabled.
7. Snapshot turn-level gates and limits at the documented boundaries. Do not let an asynchronous configuration refresh change policy midway through one query invocation.
8. Keep derived summaries, prompt suggestions, and speculative work outside authoritative history until their explicit projection/acceptance point.
9. Exercise every applicable acceptance scenario in the references, including model-policy/provider faults, cancellation, partial-stream, derived-assistance, and speculative-commit cases.

## Ownership boundaries

- Own user-turn admission through terminal query outcome, model-request construction, stream interpretation, model retry/fallback, usage accumulation, and API-safe message normalization.
- Receive tool execution as an ordered stream of normalized results: concurrency-safe groups may arrive in completion order, while accepted identity, pairing, source parentage, and unsafe barriers remain authoritative. Do not implement permission or sandbox policy here.
- Receive compaction and context-collapse decisions through bounded interfaces. Do not make summaries authoritative transcript replacements.
- Publish semantic events; leave terminal layout, SDK serialization, and remote transport framing to presentation adapters.
- Request transcript append and flush operations through persistence interfaces. Do not make transport retries rewrite durable conversation history.
- Own the scheduling and projection points for derived assistance; leave its UI rendering to adapters and never treat an auxiliary model call as the user's authoritative answer.

## Completion check

Confirm that normal completion, local-only input, tool continuation, stop-hook blocking, tool/agent progress summaries, prompt-suggestion suppression, speculative boundary/acceptance, structured-output failure, budget exhaustion, model fallback, prompt overflow, maximum output, cancellation, incomplete stream, and non-retryable API failure each end with an explicit outcome and a coherent message chain.
