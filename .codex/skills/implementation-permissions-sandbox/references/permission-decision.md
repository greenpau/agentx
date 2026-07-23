# Permission decision protocol

This document defines modes, rule grammar, decision ordering, approval handoff, automatic-mode behavior, and persisted updates. `PERM-*` identifiers are normative and stable.

## Contents

- Permission context
- Modes
- Rule grammar
- Decision ordering
- Approval protocol
- Automatic mode
- Plan and edit semantics
- Failure and recovery
- Acceptance scenarios

## Permission context

`PERM-001` — Maintain one immutable permission context per published generation containing:

- active mode;
- additional working directories with source attribution;
- allow, ask, and deny rules grouped by source;
- whether bypass mode is available;
- dangerous rules temporarily stripped by automatic mode;
- prompt suppression state;
- pre-plan mode to restore;
- automatic-mode availability, classifier state, and denial counters.

`PERM-002` — Rule sources are ordered settings sources followed by runtime sources: command-line argument, command/session update, and ephemeral session contribution. Preserve each rule's source for diagnostics and persistence.

`PERM-003` — The permission result is a tagged value, not a boolean:

```text
allow {
  reason, matchedRule?, normalizedInput?, sandboxRecommendation?,
  updatedPermissions?
}
ask {
  reason, message?, matchedRule?, suggestions[],
  normalizedInput?, mandatoryInteraction: boolean
}
deny {
  reason, message?, matchedRule?, isInterrupt?: boolean
}
cancel { reason }
```

Reasons distinguish at least rule, mode, subcommand, prompt-required tool, hook, asynchronous agent, sandbox override, classifier, working directory, safety, and other tool-specific checks.

`PERM-004` — Bound a complete permission generation and every already
structurally validated request before cloning or retaining callback-owned data.
Reject excessive rule/edit-cycle counts; oversized identifiers, input,
commands, individual strings, or aggregate text; excessive projection items,
shell segments, redirections, or path entries; and unsupported path-operation
tags. Size rejection is a fixed permission-boundary error and performs no
approval callback or side effect. The hard public ceilings are:

| Projection | Maximum |
| --- | --- |
| rules in one generation | 4,096 |
| edited-approval cycles | 16 |
| items in one permission or shell projection | 4,096 |
| tool input JSON | 16 MiB |
| aggregate retained request text | 16 MiB |
| one content, path, reason, or command-working-directory string | 1 MiB |
| tool or tool-use identifier | 4 KiB |
| one serialized rule/pattern | 64 KiB |
| one rule source | 4 KiB |
| shell command | 1 MiB |
| analyzable shell segments | 50 |
| words in one analyzed shell segment | 4,096 |
| redirections in one analyzed shell segment | 64 |

`PERM-005` — Incidental formatting of configuration, evaluators, paths,
requests, decisions, approvals, rules, and shell analysis reports only a fixed
type/shape marker. Input JSON, paths, rule text, reasons, suggestions, and
selected values remain available only through deliberate permission protocol
fields; `%v`, `%+v`, `%#v`, `%s`, and `%q` are not alternate decision or
diagnostic surfaces.

## Modes

`PERM-010` — Public modes are:

| Mode | Core behavior |
| --- | --- |
| `default` | ask unless rules/tool safety allow or deny |
| `acceptEdits` | automatically allow eligible in-scope file edits; other requests use normal rules |
| `plan` | restrict mutating work while allowing planning/read behavior |
| `dontAsk` | convert any unresolved ask into deny |
| `bypassPermissions` | allow after explicit deny/ask-independent safety checks; available only when startup policy permits |

Internal gated modes include `auto`; `bubble` exists only as an internal/type-level transport state. External serialization of `auto` projects as `default` unless the consumer is explicitly allowed to understand it.

`PERM-011` — Startup mode precedence is:

1. explicit bypass command-line switch;
2. explicit command-line permission mode;
3. settings default mode;
4. `default`.

Remote settings accept only `default`, `acceptEdits`, and `plan` as default modes. Unsupported values are diagnosed and ignored.

`PERM-012` — Entering plan mode stashes the prior mode and restores it on exit. A session that entered plan from bypass does not enter automatic mode as an indirect restoration path.

`PERM-013` — Bypass availability and bypass activation are separate. Managed policy, platform/surface constraints, dangerous-mode consent, or startup flags may make it unavailable even if a settings string requests it.

## Rule grammar

`PERM-020` — Canonical rule wire form is either `Tool` for all inputs or `Tool(content)` for content-specific matching. Empty content and `*` normalize to tool-wide form. Escape literal slash and parentheses in serialized content.

`PERM-021` — Built-in tool names begin with an uppercase letter. MCP names follow `mcp__server`, `mcp__server__*`, or an exact `mcp__server__tool` identity. Parenthesized content is invalid for MCP rules because specificity is expressed in the canonical name.

`PERM-022` — Normalize compatibility aliases before matching:

| Legacy identity | Canonical identity |
| --- | --- |
| `Task` | `Agent` |
| `KillShell` | `TaskStop` |
| `AgentOutputTool` or `BashOutputTool` | `TaskOutput` |

Build-gated aliases such as a brief/review tool normalize only when that feature exists. Unknown names remain invalid; they must not become wildcards.

`PERM-023` — Parse every rule independently. A malformed rule is removed with a source-aware warning. Do not discard valid siblings, and do not make malformed deny syntax behave like absence without prominently reporting it.

`PERM-024` — Updates support add, replace, and remove for rule sets, mode, and additional directories. Persistence destinations are only user, project, or local settings. Runtime/session rules remain in memory.

## Decision ordering

`PERM-030` — Given a structurally validated input and its rebuilt permission projection, evaluate in this exact order. The tool boundary owns schema errors and never degrades them to passthrough. Resource-dependent semantic checks run only after a final allow so denied paths cannot become existence or content oracles:

1. matching whole-tool deny rule;
2. matching whole-tool ask rule, except the documented sandboxed-shell auto-allow may continue;
3. tool-specific `checkPermissions`, including path and subcommand analysis;
4. tool-specific denial;
5. tool declares mandatory user interaction;
6. explicit content rule requiring ask;
7. safety check requiring ask;
8. bypass mode, or plan state explicitly inherited from bypass, allows;
9. matching whole-tool allow rule;
10. passthrough/unknown safe status becomes ask;
11. mode post-processing and approval handoff.

`PERM-031` — Deny always terminates the request without an approval prompt. In the initial pass, mandatory interaction, an explicit content-rule ask, and a safety ask are bypass-immune terminal **ask outcomes**: they return immediately to the current approval handler instead of falling through to bypass or whole-tool allow. A whole-tool ask is likewise terminal except for `PERM-032`. Here “terminal ask” means terminal for that evaluation pass, not a promise that an edited approval will be evaluated again. Bypass never authorizes an input rejected by the outer tool pipeline or an unavailable tool.

`PERM-032` — A whole-tool ask is normally terminal even if a later allow exists. For a shell command that will definitely run in the active sandbox and when `autoAllowBashIfSandboxed` is enabled, a non-mandatory ask may continue to tool/sandbox checks; explicit deny still terminates.

`PERM-033` — A successful allow resets the consecutive automatic-denial count. Any final ask under `dontAsk` becomes deny with mode reason.

`PERM-034` — If prompts are suppressed, an ask is offered to `PermissionRequest` hooks. Validate the hook result's own discriminator and declared fields, and apply managed policy to any requested settings/rule mutation. A hook-selected replacement follows the same structural rebuild and bounded reauthorization protocol as any other edited approval. If no hook resolves the ask, deny; never silently allow.

## Approval protocol

`PERM-040` — An interactive or structured approval request includes canonical tool identity, validated input, explanation, reason, permission suggestions, and whether the ask is mandatory. The presentation layer may redact secrets but may not change the decision semantics.

`PERM-041` — User outcomes are:

- allow once;
- allow with updated input;
- allow and apply one or more scoped permission updates;
- deny;
- deny and interrupt the broader turn;
- cancel/close, which is distinct from an explicit deny in UI behavior but terminates this request.

`PERM-042` — **Hardened edited-approval scope.** An approval is scoped to the current tool-use identifier and the exact input shown to the winning responder. If that responder supplies a different object, clone it and invoke the tool boundary's rebuild operation. Rebuild must rerun structural validation and recompute tool checks, classification, normalized command/path evidence, rules input, and sandbox recommendation before ordinary permission ordering runs again. Invalid input or a rebuild failure denies. A newly denied replacement denies; an ask may return to the active approval surface only within a finite edit-cycle bound, which defaults to two. Exceeding the bound denies.

The original model input remains immutable transcript/audit evidence while the final selected input becomes decision/execution evidence. Any completed edit cycle sets `userModified=true`. Resource-dependent semantic validation runs after the final allow and before execution. Deny, close, interrupt, or abort settles the request. The recovered one-shot compatibility profile skipped revalidation and could authorize a newly protected edit; AgentX intentionally does not preserve that unsafe gap.

`PERM-043` — Permission suggestions name an editable destination and exact or prefix rule. Applying one performs a source-local settings update. It cannot remove managed denies, convert a managed ask to allow, or persist to a noneditable source.

`PERM-044` — Hook-supplied updated permissions use the same validation and authority rules as user updates. A hook is not an administrator merely because it runs before execution.

`PERM-045` — Permission evidence has separate lifetimes. The winning semantic result drives the tool call; an in-memory decision map supports tracing and is deleted in finalization; settings mutations persist through the settings subsystem; optional hook attachments follow hook visibility/persistence rules; and the eventual tool result is conversation evidence. The common external profile has no standalone durable pending-permission or complete decision-audit record, so none of these artifacts substitutes for another.

`PERM-046` — Approval race settlement uses an atomic first-claim guard. The first decisive local, host, bridge, channel, hook, recheck, or classifier responder determines the permission result. A later responder cannot replace that returned result. Responders ready in the same scheduling turn have no fixed source priority beyond whichever claims first.

## Automatic mode

`PERM-050` — Automatic mode availability requires all of:

- runtime/build feature present;
- selected model supports the classifier contract;
- no circuit breaker or managed disablement;
- trusted settings configuration permits it;
- opt-in state satisfies `tengu_auto_mode_config.enabled` semantics (`enabled`, `disabled`, or opt-in defaulting to disabled).

`PERM-051` — On entry, remove and stash dangerous allow rules. On exit, restore the exact stashed rules at their original source and order unless the user deliberately changed them while in auto mode.

`PERM-052` — Dangerous rules include:

- tool-wide shell rules for Bash or PowerShell;
- any Agent rule;
- interpreter, shell, remote-session, package-runner, or arbitrary-evaluation patterns;
- explicit command families including Python, Node, Deno, TS runners, Ruby, Perl, PHP, Lua, `npx`, `bunx`, package-manager run commands, Bash/sh, SSH, zsh/fish, eval/exec/env/xargs/sudo, PowerShell/pwsh/cmd/WSL and equivalent expression/process/job/session escape APIs.

The list is a lower bound. New code-execution aliases default dangerous until reviewed.

`PERM-053` — Auto-mode post-processing:

1. retain deny and mandatory asks;
2. prompt for intrinsically interactive tools;
3. on PowerShell, require explicit rules unless the relevant reviewed gate is active;
4. simulate `acceptEdits` for eligible edits, excluding Agent/REPL-style delegation;
5. allow the fixed safe-tool set;
6. send remaining candidates to the safety classifier.

`PERM-054` — Safe-tool set includes read/search/LSP/tool-search and MCP resource enumeration/read, task metadata and output inspection, user-question and plan/team/workflow metadata operations, bounded sleep, and equivalent nonmutating control operations. The list is explicit, versioned, and fail-closed for new tools.

`PERM-055` — Classifier input includes canonical tool, normalized input, permission context needed for safety, and bounded relevant transcript. Classifier outcomes are allow, deny, or require-manual-review with reason.

`PERM-056` — Classifier failure defaults fail-closed when the iron-gate policy is enabled (the specified default). An overlong transcript requests manual review interactively and aborts headless execution rather than truncating away security context invisibly.

`PERM-057` — Track consecutive and total auto denials. Limits are three consecutive or twenty total. Reaching either limit exits or suspends automatic execution: interactive sessions ask for review; headless sessions abort. Any successful allow resets the consecutive count, not total count.

## Plan and edit semantics

`PERM-060` — `acceptEdits` is scoped automatic authorization for eligible workspace file mutations. It does not allow protected configuration, out-of-scope paths, commands, agents, or mandatory-interaction tools.

`PERM-061` — Plan mode permits reads and plan-owned internal writes. Other mutation requests ask or deny according to the plan tool contract. Do not implement plan mode as a user-interface convention.

`PERM-062` — A mode may influence only the documented stages. It cannot override managed deny rules, path safety, schema validation, or sandbox-required failure.

## Failure and recovery

`PERM-070` — If permission context cannot be constructed, expose no mutating tools or make every request deny. Never reuse a context from a different project/session.

`PERM-071` — If an approval transport disconnects, return cancel/deny for the live pending request. Common external profiles do not persist a standalone pending-approval waiter. Cross-process orphan-response recovery, where explicitly supported, is a separate compatibility path requiring an unresolved transcript tool-use and duplicate suppression; ordinary reconnect never resurrects a process-local waiter merely from request identity.

`PERM-072` — Every accepted tool-use identifier receives a terminal result describing denial, cancellation, or permission-system failure; permission errors cannot strand the model loop.

## Acceptance scenarios

**PERM-A01 — Bounded public projection.** Construct a permission generation
over each rule/edit-cycle ceiling and requests over each identifier, input,
item-count, command, string, and aggregate-byte ceiling. Each fails before an
approver or rebuild callback runs and produces no partial decision or side
effect. Values exactly at the supported limits continue through ordinary
deny-first evaluation.

**PERM-A02 — Opaque permission formatting.** Put a unique secret in every
public configuration, path, request, decision, approval, rule, and shell field,
then render each value with `%v`, `%+v`, `%#v`, `%s`, and `%q`. Every rendering
contains only its fixed opaque shape and no secret; deliberate typed field
access remains unchanged.

1. Rules contain `Bash` in allow, `Bash(git *)` in ask, and `Bash(git push *)` in deny. `git push origin main` denies, ordinary `git status` asks, and unrelated commands follow tool/mode checks.
2. Bypass mode receives a safety ask for writing a protected settings file. It asks; it does not allow.
3. `dontAsk` receives an unresolved ask and no hook. It denies with mode provenance and returns a terminal tool result.
4. An approval changes a destination path. The selected object is structurally rebuilt and reauthorized for the same tool-use ID. A newly protected path denies, an unresolved ask may reprompt only within the edit-cycle bound, and the untouched model input remains audit evidence.
5. Auto mode starts with `Bash(python *)` allowed. The rule is absent during auto classification and restored byte-for-byte on exit.
6. A classifier service fails after two consecutive denials. The current request fails closed; at three consecutive denials interactive mode requests manual review and headless mode terminates.

## Non-normative provenance

Reference behavior was specified from permission context types, rule parsers, mode transitions, tool permission orchestration, safety classifiers, approval UI/SDK adapters, and settings update utilities under `utils/permissions/`, `services/tools/`, permission components, and tool definitions. Paths and symbols are provenance only.
