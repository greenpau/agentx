# Session, account, and artifact workflow contracts

## Contents

1. [CMD-WF-CLEAR-001 — Clear and regenerate the conversation](#cmd-wf-clear-001-clear-and-regenerate-the-conversation)
2. [CMD-WF-COMPACT-001 — Manual conversation compaction](#cmd-wf-compact-001-manual-conversation-compaction)
3. [CMD-WF-BRANCH-001 — Copy and enter a conversation branch](#cmd-wf-branch-001-copy-and-enter-a-conversation-branch)
4. [CMD-WF-REWIND-001 — Checkpoint selector handoff](#cmd-wf-rewind-001-checkpoint-selector-handoff)
5. [CMD-WF-RESUME-001 — Resolve and resume a prior session](#cmd-wf-resume-001-resolve-and-resume-a-prior-session)
6. [CMD-WF-EXPORT-001 — Plain-text conversation export](#cmd-wf-export-001-plain-text-conversation-export)
7. [CMD-WF-FEEDBACK-001 — Consent, feedback upload, and public issue draft](#cmd-wf-feedback-001-consent-feedback-upload-and-public-issue-draft)
8. [CMD-WF-LOGIN-001 — First-party OAuth login and auth-context refresh](#cmd-wf-login-001-first-party-oauth-login-and-auth-context-refresh)
9. [CMD-WF-LOGOUT-001 — Credential destruction and shutdown](#cmd-wf-logout-001-credential-destruction-and-shutdown)
10. [CMD-WF-DIFF-001 — Session/file-history diff viewer](#cmd-wf-diff-001-sessionfile-history-diff-viewer)

## CMD-WF-CLEAR-001 — Clear and regenerate the conversation

`/clear`, `/reset`, and `/new` accept no meaningful arguments and are interactive-only. The workflow is a session transition, not merely an empty message list.

Perform these phases in order:

1. Run SessionEnd hooks with reason `clear`, an independent timeout taken from configuration, and an abort signal bounded by that timeout.
2. If the previous main request ID exists, emit a cache-eviction hint for the old conversation.
3. Classify tasks before clearing. Kill and remove only tasks explicitly marked foreground (`isBackgrounded === false`). Preserve other shell/agent/teammate/main-session background work and collect preserved agent identities.
4. Empty visible messages and, for proactive builds, clear the context-blocked latch. Regenerate the presentation conversation ID.
5. Clear session caches while retaining per-agent state required by preserved tasks. Clear user/system/git context, file suggestions, command and dynamic-skill caches, prompt/cache-break state where safe, memory/file/image/session-ingress/LSP/repository/tool-description/agent-definition caches, system injection, session environment, and other session-scoped derived state. Request asynchronous clearing for lazily loaded capability caches.
6. Restore the original working directory; clear file-state, discovered-skill, and nested-memory tracking.
7. Rebuild app state: preserve eligible background tasks, kill/abort/cleanup foreground tasks best-effort, evict their task output, reset attribution, standalone agent identity, file history, and MCP clients/tools/commands/resources while preserving the plugin reconnect generation.
8. Clear plan slugs and session metadata. Regenerate the durable session ID with the old session as analytics parent, update the internal session environment when applicable, and reset the transcript file pointer.
9. Repoint output symlinks for preserved *running* local agents to their new-session transcript locations. Do not replace completed-task output links.
10. Re-persist coordinator/normal mode and active worktree facts that survived the clear.
11. Run SessionStart hooks with reason `clear`; if they return messages, those messages become the new conversation.

There is no rollback after messages are cleared. Hook failure is bounded and reported according to hook policy; cleanup errors are logged/scoped so the transition can finish. If session-ID or transcript reset fails after state clearing, enter explicit recovery/error rather than continuing under mismatched IDs. Successful terminal result is an empty local text result; it does not append a user/model message.

## CMD-WF-COMPACT-001 — Manual conversation compaction

`/compact [optional custom summarization instructions]` is available in interactive and supported noninteractive modes unless disabled. Trim arguments; retain all remaining text as custom instructions. Project the message list after the latest compact/snip boundary. Zero messages fail locally before mutation.

The compaction state machine is:

| State/path | Behavior |
| --- | --- |
| `C0 choose` | With no custom instructions, try session-memory compaction first. With instructions, skip that path. If session-memory returns a result, clear context cache, run post-compact cleanup, notify cache-break detection where enabled, mark post-compaction, suppress warnings, and commit. |
| `C1 reactive` | When reactive-only mode is enabled, concurrently run PreCompact hooks and build cache-sharing parameters. Merge hook-provided instructions after user instructions. Set SDK/UI progress to compacting, call reactive compaction, translate `too_few_groups`, `aborted`, `exhausted`, `error`, or `media_unstrippable` into stable failures, and always clear progress/status in a finalizer. On success reset summarized-message identity, cleanup, warning state, and user-context cache. |
| `C2 legacy` | Microcompact messages first, then call traditional compaction with the effective system prompt, user/system context, tools, and custom instructions. On success reset summarized-message identity, warning state, context cache, and post-compact state. |
| `C3 commit` | Return a typed compact result containing the replacement summary/messages and display text. The caller, not this command, atomically installs the compacted conversation and resets microcompact tracking. |

The abort controller is authoritative. An observed abort becomes exactly a compaction-cancelled failure. Too-few-messages and incomplete-response errors retain distinct messages. Other exceptions are logged and wrapped as compaction errors. Pre/post-hook display text can be appended to the compact result but hook instructions cannot bypass prompt/tool permissions. Failure before typed result leaves the caller's authoritative messages unchanged, though model calls or hook side effects may have occurred. Disabled execution performs no projection or hook.

## CMD-WF-BRANCH-001 — Copy and enter a conversation branch

`/branch [name]` and the conditional `/fork` alias trim the entire argument as an optional title. When the dedicated fork feature owns `/fork`, the alias is absent.

1. Create a new random session identity and resolve the current transcript and target project/session paths. Create the target project directory with owner-only access.
2. Read and parse the current append-only transcript. Empty/missing transcript fails `No conversation to branch`.
3. Select only main-conversation message entries; exclude sidechains and non-message entries. Preserve content-replacement records belonging to the original session.
4. Rewrite each selected entry with the new session ID, a rebuilt main-chain parent link, `isSidechain=false`, and `forkedFrom {original session ID, original message UUID}`. Progress entries do not advance the parent pointer. Rewrite one content-replacement entry under the new session ID.
5. Write the complete new transcript with owner-only access. This is the branch artifact commit point.
6. Derive the first prompt from the first user text, collapse whitespace, and cap it at 100 characters; default to `Branched conversation`. Choose `<base> (Branch)`, then the first unused `<base> (Branch N)` from N=2 upward. Save the custom title.
7. Build the resumable log descriptor and, when the context exposes resume, hand ownership to `resume(new ID, log, "fork")`. Otherwise remain in the current session and show `/resume <new ID>`.

The copy is non-transactional across transcript write, title write, analytics, and resume. A title or resume failure can leave a valid unentered branch. Never delete it automatically. Success after resume explains how to resume the original. Cancellation is only possible before dispatch/UI teardown; once file copy begins there is no specified abort signal. Headless UI is absent.

## CMD-WF-REWIND-001 — Checkpoint selector handoff

`/rewind` and `/checkpoint` take no arguments and are interactive-only. If the context supplies a message/checkpoint selector, invoke it and immediately return a skip result so no command message is appended. The selector/file-history subsystem owns checkpoint listing, choice of conversation-only versus file restore, confirmation, restore ordering, and rollback. If no selector exists, return skip without mutation. Canceling the selector preserves conversation and files. A restore failure must surface the last consistent checkpoint state; this command cannot claim restoration itself.

## CMD-WF-RESUME-001 — Resolve and resume a prior session

### Entry modes

With no argument, load resumable sessions from worktrees associated with the original working directory and the same repository. Exclude sidechains and the current session. The picker can toggle all projects, reload after session metadata changes, and use agentic search. With an argument, trim it and resolve in this order: exact UUID among enriched same-repository logs; direct transcript-file lookup for that UUID; exact custom title when custom-title search is enabled.

### Picker and resolution states

| State | Behavior |
| --- | --- |
| `R0 load` | Discover worktree paths, then load lightweight logs. Loading failure terminates `Failed to load conversations`; an empty set terminates `No conversations found to resume`. |
| `R1 select` | Display logs. Toggle all-project scope by reloading. Cancel reports `Resume cancelled` as a system result. Selection loads the full log; failure reports `Failed to resume conversation`. |
| `R2 placement-check` | Same-directory or same-repository worktree sessions can resume in-process. A session from a different project produces the exact external command needed to resume there, copies it to clipboard, and terminates without changing this session. |
| `R3 handoff` | Call the context resume function with session identity and full log. On success complete with `display=skip`; the resume owner replaces session/transcript/caches. On exception show `Failed to resume: <message>`. |

For exact-title search, one match resumes, multiple matches fail with an ambiguity list, and zero matches fail not-found. Do not pick the first ambiguous title. Direct file lookup exists so a valid old session not present in enriched indexes can still resume.

The command itself has no rollback after `R3`; session resume/recovery owns atomicity. Do not append a success message into the newly resumed transcript. Picker cancellation and cross-project command copying are terminal without session mutation. Interactive UI is required in the specified descriptor.

## CMD-WF-EXPORT-001 — Plain-text conversation export

At invocation, snapshot current messages and current tool renderers, then render the conversation to plain text. Later session changes are not part of this export.

With a nonempty trimmed argument, treat it as the destination filename. If it does not end in `.txt`, strip only its last extension and append `.txt`. Resolve it against the current working directory using the platform path join. Write synchronously as UTF-8 with flush enabled. Existing files are overwritten without confirmation; nested or absolute semantics follow the platform resolver. Report the absolute resolved path or the write error.

With no argument, derive a local timestamp `YYYY-MM-DD-HHmmss`. Take the first line of the first user text, trim it, cap at 50 characters using a one-character ellipsis, lowercase it, remove non-ASCII-alphanumeric/space/hyphen characters, collapse whitespace and repeated hyphens, and trim edge hyphens. Use `<timestamp>-<slug>.txt`, otherwise `conversation-<timestamp>.txt`. The dialog offers clipboard or file. Escape from filename entry returns to destination choice; Escape from choice cancels. Clipboard uses the terminal/clipboard adapter. File uses the same flushed overwrite semantics.

No atomic temporary-file rename or rollback exists. A write failure can leave a truncated/partially replaced destination according to filesystem semantics. Clipboard success means the adapter accepted/emitted the copy operation, not that an external clipboard manager persisted it. This command is interactive even when direct filename arguments bypass the dialog.

## CMD-WF-FEEDBACK-001 — Consent, feedback upload, and public issue draft

### Gates and captured data

Disable the command for cloud-provider authentication modes, explicit feedback/bug disable flags, essential-traffic-only privacy, internal users, or policy denying product feedback. The optional argument is the initial free-form description without trimming. Capture an abort signal and a snapshot of current main messages; background agent/teammate transcripts can also be supplied.

The report includes description, timestamp, platform/terminal/version, whether the workspace is a repository, normalized main transcript, message count, latest main assistant request ID, sanitized in-memory errors, last API-request diagnostics, disk subagent transcripts, in-process teammate transcripts, and the raw transcript JSONL only when its file size does not exceed the configured transcript-read limit. Raw-transcript read failure is nonfatal. Redact recognized first-party, AWS, and Google keys; authorization/bearer values; and generic key/token/secret/password assignments from error/public-issue text.

### UI state machine

| State | Transition |
| --- | --- |
| `F0 userInput` | Show/edit description, preserving any prior submission error. Enter moves to consent. Escape cancels. Environment repository metadata loads asynchronously and may be absent. |
| `F1 consent` | Disclose description, environment, available repository metadata, and current session transcript. Enter or Space consents and submits; Escape cancels. |
| `F2 submitting` | Load bounded raw/subagent data. In parallel, submit the private feedback report and request a concise public issue title. Refresh OAuth first when needed. The upload uses authenticated first-party transport and the outer abort signal. |
| `F3 success` | Store feedback ID when returned, emit bounded analytics, and show thanks. Enter creates a redacted GitHub issue URL and opens it; any other key closes. Opening the URL drafts a public issue but does not submit it. |
| `F4 retry` | Submission failure returns to `userInput` with description preserved. A zero-data-retention/custom-retention denial receives a distinct unavailable message. Editing clears the error; Enter retries. |

The public issue URL is capped at 7,250 encoded characters. Preserve description before diagnostic errors when truncating, avoid splitting percent-encoding triplets, and add a truncation note. Title generation failure uses a bounded description-derived fallback. The consent screen summarizes categories but the implementation must treat all captured diagnostics as covered by that consent and policy; adding a new field requires updating disclosure tests.

Cancellation during submission propagates through the supplied abort signal where transports honor it. No local rollback is needed for an upload rejected before commit; a timed-out client may not know whether the server stored a feedback record, so retry can create another record. Public issue navigation is a separate, optional, non-rollbackable browser effect. Disabled execution captures and uploads nothing.

## CMD-WF-LOGIN-001 — First-party OAuth login and auth-context refresh

`/login` is hidden/disabled by its explicit gate and is not registered for third-party model providers. It opens an interactive first-party console OAuth dialog. Escape/cancel reports unsuccessful login; OAuth completion reports success.

On *both* success and cancellation, notify the model client that API-key material may have changed and strip signature-bound thinking/connector blocks from existing messages, because stale signatures cannot be reused under a new key. On success additionally:

1. reset session cost accounting;
2. start remote-managed-settings and policy-limit refreshes without awaiting them;
3. clear user cache before refreshing feature evaluation;
4. clear any previous trusted-device token, then start trusted-device enrollment without awaiting it;
5. reset permission-bypass and optional automatic-mode kill-switch checks, then rerun them against the new organization without awaiting completion;
6. increment `authVersion` so auth-dependent hooks/providers refetch.

Login completion does not wait for those asynchronous refreshes. They may later disable features or permissions. OAuth failure remains in the OAuth flow; cancel is `Login interrupted`, not an error. No rollback restores stripped signature blocks or old cost caches. Credential storage/refresh-token protocol is owned by the auth/network skill.

## CMD-WF-LOGOUT-001 — Credential destruction and shutdown

`/logout` is disabled by its explicit gate and unavailable for third-party model providers. It has no confirmation in the specified command. Execute in this order:

1. lazily load and await telemetry flush *before* clearing credentials, preventing old-organization data from being emitted under a later identity;
2. await removal of the API key/tokens;
3. delete all secure-storage data;
4. clear OAuth/trusted-device/beta/tool-schema/user/feature/Grove/remote-managed-settings/policy caches, awaiting remote settings and policy cache clearing;
5. save global configuration with OAuth account removed; when invoked by `/logout`, also reset onboarding/subscription notices and clear approved custom-key responses;
6. render success, then after roughly 200 ms request synchronous graceful shutdown with exit status zero.

This is destructive and non-transactional. Failure after credential removal can leave some caches/configuration uncleared; never restore deleted credentials from memory. Report the failure if presentation remains possible, and require a fresh process for coherent recovery. Cancel is not available after dispatch. Disabled execution performs no telemetry, credential, or config action.

## CMD-WF-DIFF-001 — Session/file-history diff viewer

Snapshot the session's tracked file history and current repository state, compute displayable diffs without changing files, and open the interactive diff surface. The file-history service owns implementation of before/after content and omission rules for unavailable/binary data. Escape closes with no mutation. Per-file read or diff failure is isolated and visible; repository-wide failure shows an unavailable state. The command must not stage, restore, or write a file.
