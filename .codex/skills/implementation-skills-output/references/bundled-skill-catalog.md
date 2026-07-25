# Dormant bundled-skill catalog

This catalog preserves a broader profile's product-shipped instruction
capabilities. The standalone AgentX profile registers no bundled skills:
`SKILL-003` admits only the active repository root, and `SKILL-004` excludes
bundles. Every descriptor, scenario, extraction rule, and registry rule below
is conditional on a future profile first changing that source contract and
marking the feature available in runtime conformance.

`BSKILL-000` — In the standalone profile, expose zero bundled descriptors,
perform no packaged-skill extraction, and do not merge this catalog into the
project-skill registry. Source presence in this reference is not runtime
eligibility.

## Registry and profile

`BSKILL-001` — If a future conformance profile enables bundles, register bundled descriptors in deterministic product order before a session registry is exposed. Each descriptor retains canonical name, description, caller visibility, model-invocation policy, optional argument hint, live enablement, temporary allowed tools, and prompt builder. Return registry copies rather than mutable storage. Build-eliminated descriptors are supported absence; a live enablement predicate is checked on registry reads.

`BSKILL-002` — A bundled skill with reference files extracts them lazily once per process generation under its protected skill root. Concurrent first invocations share one extraction attempt. Validate every relative path, create owner-only directories/files exclusively without following the final symlink, and never unlink-and-retry a collision. If extraction fails, return the core prompt without a false base-directory reference; the skill remains usable unless its contract requires those files.

## Shipped descriptors

`BSKILL-010` — `update-config` is user-invocable and supplies read-only settings/hook guidance. Ordinary invocation includes the current full settings schema and the user's request. A `[hooks-only]` prefix selects hook documentation and verification instead. It guides deliberate edits through normal file tools; the skill itself grants only `Read` and cannot mutate configuration during expansion.

`BSKILL-011` — `keybindings-help` is model-invocable but hidden from direct user invocation, and exists only when keybinding customization is enabled. Generate its contexts, actions, reserved shortcuts, platform notes, validation distinctions, and configuration format from the active keybinding contract, then append the request. It grants only `Read` and never writes a binding during expansion.

`BSKILL-012` — Internal `verify` loads a packaged verification instruction plus its packaged reference files, prepends the protected extracted base directory, and appends an optional user request. External profiles omit it. A missing/unsafe reference extraction follows `BSKILL-002` and never substitutes another filesystem tree.

`BSKILL-013` — `debug` is explicitly user-invoked and excluded from model listings. It enables debug capture when the profile does not already capture it, reports the active log path/state, includes a bounded recent tail or a clear missing-log condition, and instructs analysis through `Read`, `Grep`, and `Glob`. An optional issue description is appended without becoming command authority.

`BSKILL-014` — Internal `lorem-ipsum` is a context-stress fixture. Accept an optional positive integer token target, default 10,000; reject a supplied nonpositive or nonnumeric value. Produce deterministic bounded filler approximating the requested target and omit the descriptor outside its internal profile.

`BSKILL-015` — Internal, explicitly user-invoked `skillify` turns the current session's repeatable process into a proposed skill. It includes available session memory, requires the model to show the complete proposed skill before asking confirmation, and permits only the declared read/write/edit/search/question plus narrowly scoped directory-creation capabilities. It never writes before confirmation and is omitted outside its internal profile.

`BSKILL-016` — `remember` exists only while automatic memory is enabled. It asks the model to compare automatic memory, project instructions, local instructions, and shared/team memory; identify obsolete, duplicate, conflicting, or promotable entries; ask about ambiguity; and modify an existing destination rather than create one unless absent. Optional user context is appended as context, not policy.

`BSKILL-017` — `simplify` reviews the current changes for reuse, clarity, correctness, and efficiency, applies justified fixes through the ordinary capability boundary, and summarizes changes or confirms no issue. Optional arguments narrow focus but do not skip verification or broaden permissions.

`BSKILL-018` — `batch` is explicitly user-invoked and model-disabled. Require a nonempty transformation instruction and a source-control repository. It first researches and partitions a sweeping mechanical change, obtains required user confirmation, and then delegates independent units to isolated worktree agents in bounded parallel batches. Every worker prompt is self-contained, each worker validates its unit and opens its own change proposal, and the coordinator finally runs the declared simplify/review/end-to-end checks. A missing instruction or repository fails before agent/worktree effects.

`BSKILL-019` — Internal `stuck` diagnoses frozen, slow, or blocked local client sessions without killing unrelated processes by default. It distinguishes the current process from peers, gathers bounded process/log/resource evidence, searches known blocking patterns, and prepares an attributed diagnostic report for the configured internal feedback destination. Optional user context is appended; the descriptor is absent externally.

`BSKILL-020` — `loop` exists only in the scheduled-trigger build and while the live cron gate is enabled. Require nonempty input. Parse a leading `<integer><s|m|h|d>` interval, otherwise a trailing `every <duration>`, otherwise default to ten minutes; a phrase such as `every PR` is not a duration. Ask the model to create the recurring job through `CronCreate` and immediately execute the prompt once; a slash prompt is invoked through the Skill boundary. Empty post-parse work displays usage and creates no schedule.

`BSKILL-021` — `schedule` exists only in the remote-trigger build, with the live remote trigger flag, policy allowing remote sessions, and first-party authentication. It lists/gets/creates/updates/runs remote triggers through `RemoteTrigger`, uses structured questions to resolve missing schedule/environment/repository/connector choices, sanitizes connector names and bounds external metadata, and builds a self-contained remote prompt. Authentication or policy failure occurs before trigger mutation.

`BSKILL-022` — `agentx-api` exists only in the API-building profile. It detects the active project language from bounded repository evidence, substitutes the current supported model identifiers into packaged documentation, includes a reading guide and packaged reference index, and appends the task. It grants only `Read`, `Grep`, `Glob`, and `WebFetch`; requests targeting another provider do not trigger it. Companion content files are immutable packaged references, not a network-updated authority.

`BSKILL-023` — `agentx-in-chrome` exists only while the browser integration's live auto-enable contract passes. It contributes exactly the declared browser MCP tool allowlist, prepends the browser safety/operation guidance, requires current tab context before other browser actions, and appends the task. Site-level permission remains authoritative; invoking the skill cannot bypass extension or MCP policy.

## Acceptance scenarios

- **BSKILL-A01 — Registry profile.** Snapshot the base, internal, scheduled-trigger, remote-trigger, API-building, and browser-enabled profiles. The exact included descriptors, user/model visibility, live gates, and deterministic order match `BSKILL-001`; absent builds perform no prompt-builder or extraction work.
- **BSKILL-A02 — Protected extraction.** Invoke `verify` concurrently twice with a precreated final-component symlink collision. Both calls share one failed extraction, neither writes through the symlink, and both receive the core prompt without a base-directory claim.
- **BSKILL-A03 — Pre-effect rejection.** Invoke `batch` with no instruction and outside a repository, `loop` with empty input, and `schedule` without first-party authentication. Each returns its specific correction before creating worktrees, cron entries, triggers, or child agents.
- **BSKILL-A04 — Authority containment.** Invoke `update-config`, `keybindings-help`, `agentx-api`, and `agentx-in-chrome`. Prompt generation performs no configuration/file/browser mutation; subsequent work crosses only each descriptor's declared tools and ordinary permission policy.
- **BSKILL-A05 — Argument and packaged-content stability.** Reinvoke `verify`, `agentx-api`, and `remember` with and without arguments. Packaged/reference content stays byte-stable for one build, optional user text is appended in its declared section, and no argument changes callable identity or source attribution.
- **BSKILL-A06 — Scheduled parsing.** Exercise leading interval, trailing interval, default interval, `every PR`, and interval-only inputs for `loop`. Only valid nonempty work reaches one create call and one immediate execution, with the original slash prompt preserved.
