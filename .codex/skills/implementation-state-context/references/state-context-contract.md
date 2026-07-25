# State and Context Implementation Contract

This document is normative for the observable state and prompt behavior. Names in the provenance section are non-normative evidence only.

## Contents

- [Responsibility and vocabulary](#responsibility-and-vocabulary)
- [State ownership and data models](#state-ownership-and-data-models)
- [Bootstrap and session identity](#bootstrap-and-session-identity)
- [Session activity tracking](#session-activity-tracking)
- [Application store and reactions](#application-store-and-reactions)
- [Context and prompt assembly](#context-and-prompt-assembly)
- [Model context and output limits](#model-context-and-output-limits)
- [Failure, invalidation, and disabled behavior](#failure-invalidation-and-disabled-behavior)
- [Acceptance scenarios](#acceptance-scenarios)
- [Non-normative provenance](#non-normative-provenance)

## Responsibility and vocabulary

**SC-001 — State lifetimes.** Implement at least four conceptually separate lifetimes:

1. **Process bootstrap facts:** values required before the application store, including entrypoint, session identity, cwd roots, persistence controls, model overrides, prompt-section caches, and sticky feature latches.
2. **Live application state:** immutable observable snapshots used to coordinate the current UI, registries, permissions, tasks, and running operations.
3. **Durable session state:** transcript events and associated snapshots that survive process exit.
4. **Background-task state:** independently identified work whose lifetime can exceed a model call or foreground turn.

References between lifetimes are allowed, but one lifetime must not silently become storage for another.

**SC-002 — Presentation exclusion.** UI contexts, component-local values, overlays, progress indicators, selection, focus, and rendering caches are presentation state. They enter model context or durable history only through an explicit semantic translation.

**SC-003 — Single reaction boundary.** Put cross-domain reactions to application-state changes behind one deterministic state-change boundary. Components may request changes; they must not independently persist model selection, rewrite environment variables, or notify remote controllers.

## State ownership and data models

### Process bootstrap record

**SC-010 — Bootstrap fields.** The process record must be capable of holding:

- entrypoint and client/surface identity;
- normalized original cwd, stable project root, and current cwd;
- current session ID, optional parent session ID, and optional owning project directory;
- initial and overriding model choices;
- permission-bypass, trust, persistence, scheduled-work, interactive, and feature-mode latches;
- accumulated cost, token, duration, line, model-usage, and prompt identity metrics;
- per-turn output-budget snapshot and continuation count;
- system-prompt section cache, beta-header latches, and one-shot post-compaction state;
- registered lifecycle hooks, invoked-skill records, slow operations, session cron entries, and scroll-idle state.

**SC-011 — Initial bootstrap defaults.** Unless an entrypoint deliberately overrides them, initialize numeric usage/cost/duration counters to zero; interaction and session start times to the current time; optional models and prompt IDs to absent; interactivity, bypass, trust, persistence, scheduled tasks, fast mode, and special internal modes to false or unset; hook and registry collections to empty; and the session ID to a fresh random UUID.

**SC-012 — Setting-source eligibility.** Default eligible sources are user, project, local, command-line flag, and managed policy settings. Eligibility is not the same as precedence or writability; those rules belong to startup/settings.

### Live application snapshot

**SC-020 — Immutable snapshot.** Represent live state as one logical snapshot. Updates receive the prior snapshot and return either the identical object for a no-op or a new snapshot. Never mutate an already-published snapshot.

**SC-021 — Required domains.** The snapshot must represent, at minimum:

- effective settings, theme/output/view preferences, and effort/thinking controls;
- tool permission context and mode;
- current main-loop model and fast-mode state;
- tasks and task-name registry;
- tools, MCP clients/state, plugins, agents, and hooks exposed to the session;
- file history, attribution, inbox/queued messages, notifications, and elicitation requests;
- active overlays and other transient interaction state;
- bridge/remote connectivity and permission relays;
- speculation state and worker permission state.

**SC-022 — Permission-context shape.** Store mode, scoped working directories, allow/deny/ask rule collections separated by source, bypass availability, and optional automated-check and prior-mode metadata. Default mode is `default`, except a teammate explicitly requiring plan review starts in `plan`. Default rule collections and working-directory map are empty; bypass is unavailable.

**SC-023 — Speculation state.** Model speculation as either `idle` or `active`. Active state owns an abort operation, mutable references required by the pipeline, the last completion boundary, counts, and pipeline metadata. Valid completion boundaries include ordinary completion, shell completion, edit completion, and denied-tool completion.

**SC-024 — Collection identity.** Mutable service handles or function-bearing task objects may require identity preservation across snapshots. Document these exceptions explicitly; never deep-copy opaque live handles.

**SC-025 — Initial application defaults.** A fresh state snapshot uses empty task and notification maps, an empty task-name map, no expanded item, selection indices of `-1`, no main-loop model value until selection resolves, remote connection status `connecting`, empty file-history and attribution state, empty MCP/plugin/elicitation collections, an empty active-overlay set, fast mode false, and speculation `idle`. Thinking and prompt-suggestion defaults come from their independently gated configuration. Do not use an uninitialized or partially populated object as a hidden fifth state.

## Bootstrap and session identity

**SC-030 — Working-directory meanings.** Preserve three distinct paths:

- `original cwd`: the session's initial anchor for history and project-scoped discovery;
- `project root`: stable identity for history, sessions, plans, and skills; it does not change when a mid-session tool enters a worktree;
- `current cwd`: the path used for current file and process operations.

Resolve the initial cwd to a canonical path and normalize Unicode to NFC when possible. If canonicalization fails because the filesystem path is inaccessible, retain the raw cwd after NFC normalization rather than aborting startup.

**SC-031 — Atomic session switch.** Change the active session ID and its owning project directory atomically. A caller must not be able to observe a new ID with an old directory or vice versa. Invalidate the former session's plan-slug association and notify session-ID observers after the pair changes.

**SC-032 — Session regeneration.** Regeneration optionally promotes the prior session to parent, creates a new UUID, clears the owning project directory, invalidates the prior plan slug, and emits the same switch signal.

**SC-033 — Cost lifecycle.** Resetting session cost clears aggregate monetary cost, token/usage metrics, durations, line counts, per-model usage, prompt identity, and start time. Restoring cost implements start time as current time minus persisted duration so elapsed-time reporting continues coherently.

**SC-034 — Per-turn output budget.** At turn start, snapshot cumulative output usage and the configured budget and reset continuation count. Budget checks compare later usage against this stable turn baseline.

**SC-035 — Interaction batching.** Ordinary interaction-time updates may set a dirty flag and commit the real time at the next render flush. An explicit immediate update writes the current time synchronously for flows that wait inside effects.

**SC-036 — Scroll-idle contract.** Mark scrolling active on input and consider it idle 150 milliseconds after the last scroll signal. Expensive background operations may wait for this flag; they must not block input processing.

**SC-037 — One-shot post-compaction marker.** Successful compaction arms a marker consumed by the first subsequent successful API response. Repeated reads after consumption return false.

**SC-038 — Mode-transition attachments.** Entering plan mode clears any pending exit-plan attachment; leaving plan mode arms one. Entering automatic mode clears its exit attachment and leaving arms it, while direct automatic-to-plan or plan-to-automatic transitions do not pretend the user exited the broader controlled workflow.

**SC-039 — Hook and invoked-skill registries.** Registering hooks appends by event rather than overwriting. Removing plugin hooks retains callback hooks without plugin provenance. Key invoked skills by agent identity plus skill name, retain their content/path/time, and support clearing one agent without clearing others.

**SC-040 — Bounded ephemeral bootstrap data.** Keep at most 10 slow-operation records, expire them after 10 seconds, and omit prompt-editor execution from this list. Keep session cron entries process-local and non-durable. Beta header latches are sticky-on until session clear or compaction.

## Session activity tracking

**SC-041 — Process-global activity accounting.** Maintain one process-global activity callback, a nonnegative aggregate reference count, separate counts for the only specified reasons `api_call` and `tool_exec`, and the wall-clock start of the oldest still-aggregate activity interval. `start(reason)` increments both aggregate and reason counts. On only the aggregate `0 → 1` transition, record the current time and, when a callback is registered and no heartbeat timer exists, start the timer. Concurrent/nested reasons share this timer; they do not each create one.

**SC-042 — Thirty-second active and idle timers.** The active timer repeats every 30,000 ms. Every tick emits the non-PII `session_keepalive_heartbeat` diagnostic with current aggregate count, regardless of whether transport output is enabled. It invokes the current callback only when `AGENTX_REMOTE_SEND_KEEPALIVES` passes the product's truthy-environment predicate. `stop(reason)` decrements the aggregate only while it is positive, independently decrements/removes the supplied reason count, and on aggregate zero clears an existing heartbeat and starts one 30,000 ms one-shot idle timer when a callback still exists. The idle timer emits `session_idle_30s` and clears its own handle. A stop for a reason that was not active can therefore remove no reason while still consuming a positive aggregate count; preserve this compatibility behavior rather than inventing per-reason balancing.

**SC-043 — Registration, manual signal, and teardown.** Registering replaces the single callback. If work is already active and no heartbeat timer exists, registration immediately arms the repeating timer and clears any idle timer. A manual activity signal invokes the callback immediately only under the same environment gate and emits no heartbeat diagnostic itself. Unregistering nulls the callback and clears active and idle timers, but deliberately retains aggregate/ref-by-reason accounting and oldest-start evidence so a later registration during active work can rearm. `isTrackingActive` reports callback presence, not refcount or timer state.

**SC-044 — Shutdown evidence.** The first activity start registers exactly one process-cleanup observer for the lifetime of the module. Cleanup logs `session_activity_at_shutdown` with current aggregate count, the current per-reason count map, and oldest elapsed milliseconds only when the aggregate is positive and an oldest start exists; otherwise elapsed age is null. Cleanup is observational: it does not reset counts, callbacks, or timers. Stopping the final activity likewise leaves the oldest timestamp stale but semantically ignored while count is zero.

## Application store and reactions

**SC-050 — Store notification order.** For a non-no-op update: publish the new snapshot, synchronously execute the central change reaction, then synchronously notify subscribers. For an identity no-op, perform none of these actions.

**SC-051 — Subscription equality.** Selectors compare selected values by identity unless a stronger comparator is explicitly supplied. A selector that creates a new object on every call therefore causes updates; consumers needing stability must memoize or select primitives/stable references.

**SC-052 — Provider uniqueness.** The top-level application-state provider must not be nested. Construct the store once for the provider lifetime. A setter-only consumer does not subscribe.

**SC-053 — Initialization race.** If asynchronous remote settings later invalidate a permission mode chosen during initial-state creation, convert it through the same central state transition rather than mutating initial state out of band.

**SC-054 — Mode externalization.** Convert internal mode to its external representation before notifying remote metadata. Suppress an external notification when two internal modes map to the same external value, but still emit the raw SDK mode change. Feature-specific one-shot mode metadata must update atomically with the mode transition.

**SC-055 — Model reaction.** Changing the main-loop model persists or removes the corresponding user preference and updates the bootstrap model override. Do this once at the state-change boundary.

**SC-056 — Preference reactions.** Persist expanded-view, legacy todo/spinner compatibility, verbosity, and internal panel preferences when their state fields change. Failures in these secondary reactions are logged and do not roll back the authoritative state snapshot.

**SC-057 — Authentication cache invalidation.** A changed settings object invalidates model/API and supported cloud-provider credential caches. Treat a reference change as potentially relevant even if a shallow field comparison would miss it.

**SC-058 — Environment application.** Apply new environment entries additively. Do not delete process variables merely because a later settings snapshot omits them; process-wide consumers may already depend on them.

## Context and prompt assembly

**SC-060 — Context inputs.** Build model context from the selected model/mode, core prompt, user and project instructions, repository facts, current date/environment, tools, agents, skills, plugins, MCP instructions, memory, attachments, and policy. Each input must state whether it is stable, volatile, conditional, or externally supplied.

**SC-061 — Concurrent ordered sections.** Resolve system-prompt sections concurrently but emit them in descriptor order. A cacheable section computes once until invalidated. A volatile section recomputes each turn; retain its latest value only for diagnostics/cache-break comparison.

**SC-062 — Cache clear.** Clearing system-prompt sections also clears beta-header latches. Clear user-context caches when cwd-sensitive instructions, memory files, worktree state, or compaction semantics require rediscovery.

**SC-063 — Prompt precedence.** Select prompt source in this order:

1. explicit system override, which replaces every other prompt including append text;
2. coordinator prompt when eligible and no main agent overrides it;
3. selected agent prompt;
4. custom prompt;
5. default product prompt.

When no explicit override is active, append-system text is always last. In proactive/internal modes, a custom agent instruction may append to the default rather than replacing it; gate this behavior explicitly.

The standalone Go default product prompt begins with the exact generic identity
phrase `You are AI agent,` and must not identify the assistant as AgentX. This
wording applies only to the default source selected at step 5; an explicit
override retains its supplied identity text.

**SC-064 — Custom-prompt context.** A custom system prompt suppresses the ordinary generated system context but does not suppress user/project instruction context.

**SC-065 — Side-question projection.** A side question excludes an in-progress assistant message whose stop reason is absent and uses a noninteractive tool context. Thinking defaults to adaptive unless globally disabled.

**SC-066 — Repository snapshot.** Repository context is a memoized conversation snapshot. When enabled, collect branch, default branch, working status, five recent commits, and configured source-control user concurrently. Truncate status text to 2,000 characters. Any source-control failure yields no repository snapshot rather than failing the turn.

**SC-067 — User context.** Always include the local calendar date. A hard instruction-disable switch suppresses filesystem instruction discovery. Bare/hermetic mode skips discovery unless an explicit additional directory requires it. Memory selection is filtered before injection.

**SC-068 — Debug injection.** Injecting debug system content invalidates both user-context and system-context caches so the next request observes it consistently.

## Model context and output limits

**SC-070 — Base sizes.** Use a 200,000-token default input context and a 20,000-token compact-summary output allowance unless model capability, beta, or a valid explicit override changes them.

**SC-071 — Input-window decision.** Evaluate independent controls rather than conflating them: hard disable of extended context, explicit model suffix requesting one million tokens, model-advertised maximum input, beta eligibility, account experiment, internal model eligibility, and fallback default. A valid positive internal maximum-context override may replace the result.

**SC-072 — Output families.** Preserve model-family output defaults and upper bounds as configuration data. Required observed families include:

| Family | Default | Upper bound |
| --- | ---: | ---: |
| newest high-capability model | 64K | 128K |
| newest balanced model | 32K | 128K |
| recent general models | 32K | 64K |
| older high-capability models | 32K | 32K |
| legacy generation 3 | 4,096 or 8,192 | same |
| later legacy balanced model | 32K | 64K |

A provider-advertised output cap of at least 4,096 may override family data. Maximum thinking budget is output upper bound minus one token.

**SC-073 — Usage percentage.** Compute context percentage from input, cache creation, and cache read tokens; round and clamp to 0–100.

## Failure, invalidation, and disabled behavior

**SC-080 — Leaf bootstrap reliability.** Bootstrap state must remain importable without higher application services. Do not add dependencies that create initialization cycles or require UI/runtime initialization.

**SC-081 — Best-effort context.** Repository inspection, optional memory, and noncritical state reactions fail at their own boundary. They may log or omit context but must not corrupt state or abort an otherwise valid turn.

**SC-082 — Feature axes.** For every optional state/context field, document separately: build inclusion, runtime gate, account eligibility, managed policy, platform support, and current availability. Disabled behavior is an absent field/section or stable default, not a partially initialized object.

**SC-083 — Subagent shared globals.** In-process subagents can share module/process caches with the main thread. Any cleanup operation must know its query owner and avoid clearing main-thread prompt, memory, or collapse state on behalf of a subagent.

## Acceptance scenarios

**SC-A01 — Canonicalization fallback.** Given an inaccessible cwd whose canonical lookup fails, startup succeeds using NFC-normalized raw cwd and file operations remain anchored there.

**SC-A02 — Worktree identity.** Given a mid-session worktree entry, current cwd changes but project root and session-history identity remain anchored to the original project.

**SC-A03 — Atomic resume.** During a session switch, no observer can see the target session ID paired with the former session's project directory.

**SC-A04 — Referential no-op.** An update returning the prior snapshot emits no reaction and no subscriber notification.

**SC-A05 — Prompt override.** With default, agent, custom, append, and explicit override all configured, only the explicit override becomes the system prompt; user/project context remains governed by SC-064.

**SC-A06 — Stable and volatile sections.** Across two turns, a cacheable section computes once, a volatile date/status section computes twice, and section order remains identical despite different completion times.

**SC-A07 — Auth reaction failure.** If clearing one optional credential cache throws, the state update remains published and the failure is logged without reentrant state corruption.

**SC-A08 — Subagent compact.** A subagent cleanup resets its local compaction state but does not clear the main thread's user-context or context-collapse cache.

**SC-A09 — Nested activity and late transport registration.** Start `api_call`, start `tool_exec`, and register a callback after both starts. Verify one repeating 30,000 ms timer is armed, the diagnostic fires on every tick, callback output occurs only while the environment gate is truthy, stopping one reason leaves the timer active, and stopping the second clears it and arms the one-shot idle diagnostic.

**SC-A10 — Unregister/reconnect accounting.** During one active API call, unregister the callback, advance past 30 seconds, and verify neither timer output nor idle output occurs. Register a replacement callback without another start and verify the retained positive aggregate rearms the active timer. A manual signal obeys the environment gate, cleanup reports the one active reason and elapsed age once, and final stop reaches zero without resetting the historical oldest timestamp.

**SC-A11 — Generic default agent identity.** Build the standalone Go default
prompt without an explicit override. It begins with `You are AI agent,` and
does not contain `You are AgentX`. Repeat with an explicit override containing
its own identity and verify prompt precedence preserves the override unchanged.

## Non-normative provenance

Behavior was specified primarily from `bootstrap/state.ts`, `state/AppState.tsx`, `state/AppStateStore.ts`, `state/store.ts`, `state/onChangeAppState.ts`, `context.ts`, `utils/queryContext.ts`, `utils/systemPrompt.ts`, `utils/context.ts`, and `constants/systemPromptSections.ts`.
