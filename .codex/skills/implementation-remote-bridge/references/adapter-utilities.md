# Remote Adapter Utilities

This reference specifies the local adapters that surround remote transport and placement: crash pointers, inbound content, credentials, diagnostics, eligibility, output persistence, resume identifiers, and plan handoff. They are part of the remote contract because a transport that reconnects perfectly can still corrupt a session through an unsafe filename, stale pointer, ambiguous token, or false plan-terminal decision.

Use the [adapter utility diagram](../assets/adapter-utilities.drawio) to see trust boundaries, persistence lifetimes, and the plan-handoff state machine.

## Contents

- [Contract map](#contract-map)
- [Crash-recovery pointer](#crash-recovery-pointer)
- [Inbound messages and attachments](#inbound-messages-and-attachments)
- [Ingress credential selection](#ingress-credential-selection)
- [Diagnostics and redaction](#diagnostics-and-redaction)
- [Remote eligibility](#remote-eligibility)
- [Turn-output persistence](#turn-output-persistence)
- [Resume identifier parsing](#resume-identifier-parsing)
- [Remote plan handoff](#remote-plan-handoff)
- [Keyword launch detection](#keyword-launch-detection)
- [Conformance scenarios](#conformance-scenarios)

## Contract map

- **RB-AUX-001 — Pointer lifecycle.** Persist only the remote session, environment, and source needed to offer crash recovery; freshness is file metadata, invalid or stale records are cleared, and every pointer operation is best-effort.
- **RB-AUX-002 — Inbound message normalization.** Accept only nonempty user messages and repair malformed base64 image media-type spelling before the content reaches the shared input pipeline.
- **RB-AUX-003 — Attachment materialization.** Validate attachment descriptors, download with scoped OAuth, write beneath the session upload directory using sanitized collision-resistant names, and inject quoted file references into the content position actually consumed by input normalization.
- **RB-AUX-004 — Secret-safe diagnostics.** Redact named credential fields before truncation, flatten multiline debug strings, extract bounded server error details, and keep diagnostic failure outside correctness paths.
- **RB-AUX-005 — Ingress credential precedence.** Resolve a session token in the order environment override, one-shot descriptor/cache, then well-known file; select cookie or bearer headers by credential class and support in-process renewal.
- **RB-AUX-006 — Turn-output persistence.** In eligible bring-your-own-container sessions, scan only the session output subtree for regular files modified during the turn, reject escape and excessive fanout, upload with bounded concurrency, and report partial success.
- **RB-AUX-007 — Remote eligibility.** Fail immediately on managed policy denial; otherwise evaluate login, environment, and repository facts concurrently and apply bundle-seeding and repository-host rules explicitly.
- **RB-AUX-008 — Repository access tier.** Prefer repository-app access, optionally fall back to synchronized user credentials behind its independent gate, and otherwise report no access.
- **RB-AUX-009 — Remote plan scanner.** Classify ExitPlanMode calls and results with deterministic precedence, cursor-based polling, bounded transient failures, explicit UI phases, and distinct terminal reasons.
- **RB-AUX-010 — Launch keyword grammar.** Match only directive-like Unicode word occurrences outside protected delimiter, path, option, extension, question, and slash-command contexts; replace only the first valid trigger while preserving the user's `plan` casing.
- **RB-AUX-011 — Resume identifier grammar.** Distinguish transcript files, plain UUIDs, and ingress URLs in that order; preserve file/URL payloads while allocating a fresh local UUID when no local UUID was supplied.

## Crash-recovery pointer

### Record and location

The pointer record contains exactly:

```text
sessionId: string
environmentId: string
source: standalone | repl
```

Store it beside the working-directory-scoped transcript state, under a path derived from the sanitized working directory. Do not add the local transcript UUID, delivery cursor, worker epoch, pending controls, token, or message content. Those data have different durability and authority contracts.

### Write, refresh, read, and clear

Under `RB-AUX-001`:

1. Write immediately after the bridge session exists and rewrite the same record periodically. The rewrite's modification time is the freshness heartbeat.
2. Create the parent directory if needed. A write failure is logged and swallowed; crash recovery must not crash the active session.
3. On read, obtain modification time and content directly. Missing, unreadable, unparsable, or schema-invalid data returns no pointer. Invalid records are cleared best-effort.
4. Compute age as `max(0, now - mtime)`. A record older than four hours is stale, is cleared, and is not offered.
5. Clear by unlinking. Missing is success; other failures are diagnostic only.

For `continue`, check the launch directory first. Only on a miss enumerate sibling Git worktrees, with a five-second enumeration timeout inherited from the worktree adapter. If there are more than 50 worktrees, do not fan out. Otherwise deduplicate the launch directory using the same path-sanitization rule, read candidates concurrently, and select the valid pointer with the smallest age. Return both pointer and directory so failure cleanup targets the correct record.

This pointer is an offer to reconcile with the service, not proof that the remote environment or transcript still exists.

## Inbound messages and attachments

### User-message admission and image repair

Under `RB-AUX-002`, ignore any remote event whose type is not `user`, whose content is absent, or whose content-block array is empty. Preserve an optional string UUID only when present. A string body passes unchanged.

For content-block arrays, scan for base64 image blocks lacking the canonical `media_type` field. If none need repair, return the original array reference. Otherwise copy only the affected structures and choose the media type from a nonempty compatibility field named `mediaType`, falling back to format detection from the base64 bytes. Never mutate the untrusted input object in place.

### Attachment descriptor and download

Under `RB-AUX-003`, accept `file_attachments` only when it validates as an array of records containing string `file_uuid` and `file_name`. Malformed attachment metadata becomes an empty list; it does not reject the text message.

For each valid descriptor, independently:

1. Require the bridge OAuth access token. Missing token skips that file.
2. issue an OAuth-authenticated `GET` to `/api/oauth/files/{percent-encoded UUID}/content` with a 30-second timeout and binary response;
3. accept status exactly 200, even if the HTTP library is configured to return other status codes without throwing;
4. strip directory components from the supplied filename, replace characters outside ASCII letters, digits, dot, underscore, and hyphen with underscore, and substitute `attachment` if empty;
5. prefix the safe name with a sanitized first eight UUID characters, or eight random UUID characters if the supplied UUID has no characters;
6. write under the configuration upload root at `uploads/{local session ID}/`.

Network, configuration, status, directory, and write failures are logged and skip only that attachment. Resolve attachments concurrently. Format successful paths as quoted at-references so spaces cannot truncate them, separated by one space and followed by one trailing space.

Prepend the references to a string body. For block content, prepend them to the last text block because the shared input path consumes the final processed block; if no text block exists, append a final text block containing the trimmed references. With no attachment metadata, return the original content reference and perform no I/O.

## Ingress credential selection

Under `RB-AUX-005`, resolve credentials with this precedence:

1. a nonempty process environment session-access token, which can be replaced in-process after reconnect;
2. the descriptor selected by the descriptor environment variable, read once and cached in bootstrap state;
3. the explicitly configured token file, otherwise the platform's well-known CCR session-token file.

If no descriptor is declared, proceed directly to the file. If its spelling is not an integer, cache `null` and fail closed. On macOS or BSD read `/dev/fd/{n}`; on Linux-like systems read `/proc/self/fd/{n}`. Trim the content and reject empty. After a successful descriptor read, persist it through the protected subprocess-token adapter so descendants that cannot inherit the descriptor can use the file. A descriptor read failure falls back to the well-known file and caches that result. Because a descriptor may be consumable only once, `undefined` means not attempted while `null` means attempted and unavailable.

Header projection depends on token class:

- a token starting with `sk-ant-sid` becomes `Cookie: sessionKey={token}` and, when available, `X-Organization-Uuid`;
- every other nonempty token becomes `Authorization: Bearer {token}`;
- no token yields an empty header map.

Transport authentication does not grant model-tool permission.

## Diagnostics and redaction

Under `RB-AUX-004`, diagnostic helpers must remain safe for arbitrary server data:

- Before logging serialized bodies, locate JSON string fields named `session_ingress_token`, `environment_secret`, `access_token`, `secret`, or `token`. Values shorter than 16 characters become `[REDACTED]`; longer values retain only their first eight and last four characters separated by an ellipsis.
- Collapse actual newline characters to the two-character sequence `\n` for plain debug messages.
- Limit a flattened or redacted body to 2,000 characters. When truncated, append a marker containing the original post-normalization length.
- For HTTP-library errors, start with the ordinary error message and append `response.data.message`, otherwise `response.data.error.message`, when it is a string. Status extraction returns a number only from a numeric response status.
- A centralized bridge-skip helper may emit both a debug message and a stable skip-reason event. Event metadata must never contain a raw credential or response body.

The reference redactor recognizes named JSON string fields, not arbitrary plaintext secrets. An implementation may use a stronger structured redaction layer, but it must preserve the same observable shortening and must not weaken it.

## Remote eligibility

Under `RB-AUX-007`, background remote eligibility returns all applicable failures except when policy denies the feature. Policy denial returns only `policy_blocked` and performs no network or repository checks.

Otherwise start the login-refresh check, remote-environment lookup, and repository-with-host detection concurrently. Record `not_logged_in` and `no_remote_environment` independently. Evaluate repository requirements as follows:

1. Determine bundle-seed availability from explicit force/enable controls or its runtime gate, unless the caller explicitly skips bundle support.
2. If not inside a Git repository, record `not_in_git_repo`.
3. If inside a repository and bundle seed is enabled, no remote or repository app is required.
4. Otherwise a missing supported remote records `no_git_remote`.
5. For a GitHub remote, a missing repository app records `github_app_not_installed`. Non-GitHub supported remotes do not use the GitHub-app check.

Network errors in environment or access probes degrade to a false result and a diagnostic entry, not an exception that erases the other failures. Repository application checks require an OAuth token and organization UUID, use a 15-second request timeout, accept the app only when the response status object says `app_installed: true`, and treat client errors as unavailable.

Under `RB-AUX-008`, `check repository access` first tests the repository app. Only if that fails and the credential-sync experiment is enabled does it query synchronized GitHub authentication. Return `{hasAccess, method}` where method is `github-app`, `token-sync`, or `none`; do not collapse the provenance into a boolean internally.

## Turn-output persistence

### Enablement and scan boundary

Under `RB-AUX-006`, persistence is enabled only when all are true:

- the file-persistence build feature is present;
- environment kind is exactly bring-your-own-container;
- a session ingress token is available;
- the remote session ID is present.

The output root is `{effective working directory}/{remote session ID}/outputs`. An unknown environment kind, first-party cloud kind, missing token, missing session ID, or pre-aborted signal produces no persistence result. The first-party cloud path is intentionally unimplemented in the reference and returns no files; do not invent file identifiers.

Recursively enumerate the output root. Enumeration failure means no files. Keep regular files only and skip directory entries already identified as symbolic links. Stat candidates concurrently using a no-follow operation; skip files that disappeared or became symbolic links. A file is in the turn set when its modification time is greater than or equal to the captured turn-start timestamp.

### Upload and result accounting

If the modified-file count exceeds the configured file-count limit, upload none and return one failure for the output root. Convert each path to a relative path and reject any path beginning with `..` before upload. Recheck cancellation after scanning. Upload the remaining files with the configured default concurrency.

Preserve partial results:

```text
files[]  = { filename: source path, file_id: service identifier }
failed[] = { filename: source path, error: bounded message }
```

Return no event when both lists are empty. An orchestration exception becomes one failure naming the output root. Emit start/completion operational metrics, but metric failure must not change persistence correctness. The wrapper invokes its callback only for a non-null result and swallows any final adapter exception after logging.

## Resume identifier parsing

Under `RB-AUX-011`, parse in this order:

1. A case-insensitive `.jsonl` suffix is a transcript file, even when its spelling is also accepted by a generic URL parser, as with a Windows drive path. Preserve the path and allocate a random local session UUID.
2. A valid plain UUID is the local session UUID; it has no ingress URL or transcript path.
3. A syntactically valid URL preserves its normalized full URL as the ingress URL and allocates a random local session UUID.
4. Anything else is invalid.

A URL's path UUID is not silently promoted to the local transcript UUID. This preserves `RB-ID-001` and keeps compatibility association explicit.

## Remote plan handoff

### Pure event scanner

Under `RB-AUX-009`, keep a chronological list of ExitPlanMode tool-use identifiers, a result map keyed by tool-use identifier, a rejected set, and an optional terminal error subtype. A successful generic session result is a turn boundary, not session termination. Only non-success result subtypes mark termination.

After ingest, classify with precedence:

```text
approved or teleport > terminated > rejected > pending > unchanged
```

Always target the newest ExitPlanMode call not already rejected. No result means pending. A non-error tool result is approved and must contain an approved-plan marker. An error result containing the teleport sentinel plus newline is a local-execution handoff; an error result without it is a normal rejection. Record a rejection before returning a simultaneous terminal error, and force a rescan on the next call because rejection exposes an older candidate. An approved result wins even if the same batch also contains a later remote error.

Approved markers are, in precedence order, `## Approved Plan (edited by user):` and `## Approved Plan:` followed by newline. The teleport marker is the exact sentinel followed by newline. Content may be a string or an array of text blocks. Missing approved marker is a classified extraction failure, not an empty approved plan.

### Poll loop and UI phase

Poll by explicit cursor every three seconds until the supplied deadline. Reset the consecutive-failure counter after each successful poll. Retry transient network failures, but fail on a nontransient error or the fifth consecutive failure. A caller stop check produces the distinct `stopped` reason.

Derive the visible phase independently from terminal classification:

- `plan_ready` when a nonrejected ExitPlanMode call awaits a result;
- `needs_input` when the service says idle or requires action and the current poll returned no new events;
- `running` otherwise.

The no-new-events condition prevents a lagging idle snapshot from hiding active work. Emit the callback only on phase change. On timeout, distinguish `timeout_pending` when a pending plan was ever seen from `timeout_no_plan` otherwise. Every poll error carries the rejection count accumulated so far. Approval returns execution target `remote`; teleport sentinel returns target `local`.

## Keyword launch detection

Under `RB-AUX-010`, detect `ultraplan` and `ultrareview` case-insensitively at Unicode letter/digit/underscore word boundaries. A leading slash makes the entire input ineligible. Exclude matches:

- inside paired backticks, double quotes, braces, parentheses, and brackets;
- inside tag-like angle brackets, where `<` is followed by an ASCII letter or slash;
- inside single quotes only when the opener is not an apostrophe and the closer is not followed by a word character;
- immediately adjacent to slash, backslash, or hyphen;
- followed by question mark;
- followed by a dot and a word character, which denotes a file extension.

Nested `[` resets the opening position so pasted-text placeholders use the innermost bracket range. Unclosed delimiters do not create a completed protected range. Return every valid position with original spelling and offsets.

Replacement affects only the first valid `ultraplan`: remove the `ultra` prefix and retain the original `plan` suffix, including case. If removing the sole trigger leaves only whitespace, return the empty string. Inputs without a valid trigger are returned unchanged.

## Conformance scenarios

### `RB-U01` — Freshest worktree pointer wins

Create valid pointers in three sibling worktrees with different modification times and no pointer in the launch directory. The reader returns the youngest pointer and its containing directory. At 51 enumerated worktrees, it returns none instead of launching a stat burst. **Contracts:** RB-AUX-001, RB-RST-001.

### `RB-U02` — Invalid and stale pointers self-clear

Test malformed JSON, schema mismatch, a negative wall-clock delta, exactly four hours, and greater than four hours. Only greater than four hours is stale; malformed and stale files are cleared best-effort. **Contracts:** RB-AUX-001.

### `RB-U03` — Image repair preserves the fast path

A valid image-block array is returned by identity. A camel-case media type is copied into canonical spelling; a missing type is inferred; non-user and empty-user events are ignored. **Contracts:** RB-AUX-002.

### `RB-U04` — Attachment failure is per file

Mix a successful attachment, a 404, a timeout, an unsafe filename, and a filename containing spaces. The text message survives, only the successful file is referenced, the output remains inside the session upload root, and the quoted reference reaches the final text block. **Contracts:** RB-AUX-003, RB-SEC-001.

### `RB-U05` — Credential sources do not blur

Exercise environment override, valid descriptor, invalid descriptor spelling, failed inherited descriptor with file fallback, cached null, session-key cookie, bearer token, and in-process refresh. The selected source and headers match precedence without reading a descriptor twice. **Contracts:** RB-AUX-005, RB-SEC-001.

### `RB-U06` — Redaction precedes truncation

Place a long secret across the 2,000-character boundary and a short token in a nested error body. No raw secret appears; long secrets retain only 8+4 characters; server detail precedence and reported original length remain deterministic. **Contracts:** RB-AUX-004, RB-SEC-001.

### `RB-U07` — Bundle eligibility changes repository requirements

With policy allowed and a local-only Git repository, bundle enabled passes repository access while bundle skipped reports no remote. With policy denied, verify no login, environment, or repository probe executes. **Contracts:** RB-AUX-007, RB-OFF-001.

### `RB-U08` — Repository access retains provenance

Test repository app success, app failure plus gated token-sync success, and both unavailable. Results identify `github-app`, `token-sync`, and `none` respectively. **Contracts:** RB-AUX-008.

### `RB-U09` — Output scan resists races and escape

Include old files, files exactly at turn start, symlinks, a file replaced by a symlink between enumeration and stat, deletion races, and a relative escape. Only safe in-root regular files at or after turn start reach upload. **Contracts:** RB-AUX-006, RB-SEC-001.

### `RB-U10` — Output persistence preserves partial failure

Return mixed upload success and failure, pre-abort, post-scan abort, count overflow, and an orchestration exception. Verify terminal evidence is neither all-success nor silently empty. **Contracts:** RB-AUX-006, RB-CAN-001.

### `RB-U11` — Plan batches obey precedence

Feed synthetic batches containing pending, rejection, an older exposed call, approval plus terminal error, success result events, teleport sentinel, and missing approved marker. Verify precedence, rejection count, rescan, and classified failure. **Contracts:** RB-AUX-009, RB-SEQ-001.

### `RB-U12` — Keyword grammar rejects incidental text

Test slash commands, tags, comparisons, apostrophes, quoted text, pasted placeholders, paths, flags, extensions, questions, sentence punctuation, Unicode neighbors, mixed case, and multiple triggers. Only directive-like matches remain and replacement changes the first one. **Contracts:** RB-AUX-010.

### `RB-U13` — Identifier classes remain separate

Test a Windows `.jsonl` path, upper-case suffix, plain UUID, URL containing a UUID, and invalid text. File and URL inputs receive fresh local UUIDs; the plain UUID is preserved. **Contracts:** RB-AUX-011, RB-ID-001, RB-CMP-001.
