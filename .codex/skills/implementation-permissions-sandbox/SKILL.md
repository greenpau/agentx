---
name: implementation-permissions-sandbox
description: Implement permission modes, scoped rules, path and shell analysis, approval decisions, auto-mode safety, protected resources, and operating-system sandbox selection. Use when implementing or reviewing whether a capability may run, what must prompt, and which isolation boundary applies.
---

# Implementation Permissions and Sandbox

## Preserve a composed safety decision

Permission is an ordered decision protocol, not a boolean and not a presentation concern. It combines managed policy, mode, source-attributed allow/ask/deny rules, tool-specific validation, path and command analysis, hooks, sandbox availability, and a user's response. The terminal or SDK renders an approval request; the permission service owns the decision.

Use the [architecture diagram](assets/architecture.drawio) to inspect rule evaluation and isolation selection. Read [permission decision protocol](references/permission-decision.md) for modes, rule grammar, ordering, auto-mode classifiers, approval updates, and denial limits. Read [path, shell, and sandbox contract](references/path-shell-sandbox.md) for canonicalization, protected resources, compound-command analysis, sandbox policy, and cleanup. Read [Bash authorization analyzer](references/bash-authorization.md) with its [authorization flow](assets/bash-authorization.drawio) for Bash parsing, wrapper and environment normalization, rule matching, read-only and `sed` recognition, compound-path checks, and sandbox auto-allow. Read [PowerShell authorization analyzer](references/powershell-authorization.md) with its [authorization flow](assets/powershell-authorization.drawio) for the native AST protocol, alias and parameter handling, degraded deny preservation, provider/path binding, read-only recognition, and platform-specific sandbox behavior. Requirement identifiers `PERM-*`, `PATH-*`, `SHELL-*`, `SBOX-*`, `BASH-AUTH-*`, and `PS-AUTH-*` are stable anchors.

## Authorization workflow

1. Resolve the tool by canonical name, structurally validate its input, and compute the permission projection before asking. Defer resource-dependent semantic validation until the exact input is authorized.
2. Normalize legacy tool aliases and parse every configured rule independently. Invalid rules are excluded with source-aware diagnostics rather than weakening the remaining policy.
3. Evaluate whole-tool deny and ask rules, tool-specific checks, mandatory-interaction and safety checks, mode effects, whole-tool allow, and passthrough in the documented order.
4. Convert any remaining ask into the active surface's approval protocol. `dontAsk` turns it into denial; prompt-suppressed execution gives hooks one final chance and otherwise denies.
5. If approved with modified input, clone and structurally revalidate the selected object, rebuild its classifiers, paths, tool checks, and sandbox recommendation, then recompose permission. Bound edit cycles and deny invalid, newly protected, or repeatedly unresolved replacements. Validate and persist approved rule updates separately and only to an editable selected source; managed policy remains authoritative over that settings mutation.
6. Carry only the sandbox recommendation/authorization earned by the final evaluated input into execution. Run resource-dependent semantic validation after that allow and before the tool side effect. Isolation is additive defense and never substitutes for permission.
7. Record the reason, matched rule or mode, normalized subcommands and paths, sandbox override, and final outcome for display and recovery.

## Safety invariants

- Deny dominates allow at every scope; bypass mode does not bypass explicit deny, mandatory interaction, or safety checks.
- Unknown tools, malformed rules, ambiguous paths, unsupported shell syntax, and failed safety classifiers fail closed.
- Check both the supplied path and every resolved symlink target; approval of a benign spelling must not authorize a protected target.
- Analyze every executable segment of compound shell input after environment and wrapper normalization. One unsafe segment makes the compound request unsafe.
- Auto mode must strip and later restore broad code-execution allow rules; it cannot silently inherit a previous bypass posture.
- Sandboxed commands still respect permission rules. Unsandboxing is an explicit, separately authorized outcome.
- Every platform has a supported disabled behavior. If managed policy requires isolation and the platform cannot provide it, fail before execution.

## Verification checks

- A matching deny, ask, and allow rule yields deny; a matching ask and allow yields ask unless the documented sandbox auto-allow exception applies.
- A write through a symlink to a protected settings file prompts or denies exactly as a direct path does.
- `git *` matches `git` and arguments, while a legacy `prefix:*` respects a word boundary and escaped wildcards remain literal.
- Entering auto mode stashes dangerous allows, leaving safe rules usable, and exiting restores the exact prior set.
- A compound command with one sandbox-excluded segment and one protected write does not use exclusion as authorization.
- A required-but-unavailable sandbox fails without starting the child process and returns an explicit terminal tool result.
