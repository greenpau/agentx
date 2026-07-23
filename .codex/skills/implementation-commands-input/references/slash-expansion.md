# Slash parsing and prompt expansion

## Leading-slash grammar

1. Trim the complete string for routing.
2. If it does not start with `/`, use ordinary input processing.
3. Remove the first slash and split on the literal space character. The first segment is the command token.
4. If the second segment is exactly `(MCP)`, lookup name becomes `<first> (MCP)` and arguments begin after that marker. Otherwise join remaining segments with spaces as raw arguments.
5. `/` alone yields `Commands are in the form \`/command [args]\``.
6. A valid command token contains only `[A-Za-z0-9:_-]`. Lookup is canonical, displayed name, then alias, exact case-sensitive.

If a valid-looking token is unknown and is not an absolute path, fail locally with `Unknown command: <token>` and do not query the model. This implementation intentionally uses the command-specific noun instead of the reference-compatible legacy `Unknown skill` wording so the error does not conflate user commands with model-invoked skills. If the slash text contains invalid command characters or resolves as an absolute path, preserve it as an ordinary prompt. This distinction lets file paths and prose beginning with slash reach the model.

`skipSlashCommands` forces plain input. Bridge-origin input carries that defense-in-depth flag but may override it only after resolving a known bridge-safe command; known unsafe commands get the explicit bridge error, and unknown slash text stays plain.

## Command dispatch

- Local command: lazy-load, call with raw args, apply text/compact/skip result, and complete locally.
- Local UI command: install the UI node, wait for exactly one `onDone`, then project its display/query/meta/next-input options.
- Prompt command: verify user invocation and surface eligibility, expand prompt blocks, extract references, attach command metadata and scoped directives, then query or fork.
- Any command error is bounded local stderr unless a deliberate prompt message was already committed. Do not partially append expansion blocks.

## Argument lexer

Maintain both the raw argument string and a shell-aware token list. Honor quoting while leaving `$KEY` tokens literal for substitution. If lexing fails, fall back to whitespace splitting. Discard non-string shell operators from positional/named argument values.

Support these placeholders:

| ID | Form | Value |
| --- | --- | --- |
| AS-001 | `$ARGUMENTS` | Complete raw argument string. |
| AS-002 | `$ARGUMENTS[n]` | Zero-based token at index `n`. |
| AS-003 | `$n` | Zero-based token at index `n`. |
| AS-004 | `$name` | Token mapped by declared frontmatter argument name. |

Named argument declarations cannot be numeric-only. Missing positions/names substitute an empty string. An omitted argument envelope leaves content untouched; an explicitly present but empty argument string replaces placeholders with empty strings. If content has no placeholder and raw args are nonempty, append exactly `\n\nARGUMENTS: <raw>` when append behavior is enabled.

Autocomplete may show the remaining named argument as `[name]` without altering actual expansion.

## Inline expansion message contract

After successful expansion, form a transactional bundle:

1. visible command/loading metadata using `command-message`, `command-name`, and optional `command-args` tags;
2. hidden model-visible expanded content blocks;
3. attachment/reference messages extracted from expanded content;
4. scoped command permissions from `allowedTools`;
5. optional model and effort override;
6. registered trusted hooks and invoked-skill identity.

Commit the bundle together. Prompt commands loaded from untrusted locations cannot install arbitrary trusted hooks merely by emitting similarly named metadata.

## Forked expansion

Create a child with separate context and token budget, selected agent/model/effort, and the expanded prompt. Foreground invocation returns the result into the current turn. Scheduled assistant/background invocation returns promptly and later requeues a hidden completion with stable identity. A coordinator receives a delegation summary rather than worker-specific full content.

## Submit hooks

Run `UserPromptSubmit` hooks only after base processing determines that a model query should occur.

- A blocking hook replaces API-bound original content with a warning that includes the safe original user intent; it does not erase the user-visible event.
- `preventContinuation` retains prompt content but adds an explicit stop decision.
- Extra hook context is capped at 10,000 characters; append `… [output truncated - exceeded 10000 characters]` after the cap.
- Hook failure is attributed and follows hook policy; it cannot be mistaken for user text.

## Slash acceptance cases

- **SE-A01:** `/foo  a` preserves the raw double spacing for `$ARGUMENTS` while positional tokens resolve predictably.
- **SE-A02:** `/server (MCP) x y` looks up `server (MCP)` and passes `x y`; lowercase `(mcp)` is ordinary arguments.
- **SE-A03:** `/$bad` and an absolute slash path remain prompts; `/valid-but-missing` returns `Unknown command` locally without a model request.
- **SE-A04:** Quoted `"two words"` is one positional value; malformed quoting falls back without crashing.
- **SE-A05:** Explicitly present empty arguments erase placeholders, while an omitted argument envelope preserves template text.
- **SE-A06:** A prompt expansion failure commits none of metadata, content, attachments, or scoped permissions.
