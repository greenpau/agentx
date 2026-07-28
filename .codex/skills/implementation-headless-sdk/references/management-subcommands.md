# Noninteractive management subcommands

This reference specifies the command adapters that inspect or mutate
runtime-owned local state or product configuration without starting an
ordinary model turn. Their domain services remain owned by continuity,
transcript, authentication, MCP, plugin, settings, permission, and platform
contracts; this document owns argument-to-service routing, output channels,
partial-mutation boundaries, and process status.

## Contents

1. [Shared exit contract](#shared-exit-contract)
2. [Agent inventory](#agent-inventory)
3. [Authentication commands](#authentication-commands)
4. [Automatic-mode rules](#automatic-mode-rules)
5. [MCP commands](#mcp-commands)
6. [Plugin and marketplace commands](#plugin-and-marketplace-commands)
7. [Setup, doctor, and installer](#setup-doctor-and-installer)
8. [Native session inventory and deletion](#native-session-inventory-and-deletion)
9. [Acceptance scenarios](#acceptance-scenarios)

## Shared exit contract

**CLIM-001 — Human management channel.** Management commands use human stdout and stderr, never SDK NDJSON. A successful terminal helper writes its optional message plus one newline to stdout and selects status 0. An error helper writes its optional message to stderr and selects status 1. A handler that needs cleanup uses graceful shutdown instead of immediate termination; in particular, an MCP health probe must close connection subprocesses before exit.

**CLIM-002 — No semantic session.** Validation, listing, configuration mutation, authentication, diagnostics, and installation commands load only their required services and do not create a query engine, append a conversation message, or interpret their operands as prompts.

**CLIM-003 — Mutation accounting.** Validate every argument and collect every required secret before the first durable mutation. If a downstream domain operation has already committed, report its exact partial outcome; never roll it back by deleting unrelated configuration or claim the whole command was atomic.

## Agent inventory

**CLIM-010 — Source-grouped agent listing.** Resolve every authored and built-in agent candidate in the current directory, determine the active winner for each type under normal source precedence, and retain shadowed candidates for display. Iterate the fixed source groups in their declared order and sort each group by case-insensitive display name with a stable tie rule. A row contains type, then optional resolved model, then optional `<scope> memory`, separated by a centered-dot delimiter. Prefix shadowed rows with the winning source label. The heading counts active winners, not all candidates; no candidates prints `No agents found.`.

The listing is evidence only. It does not initialize required MCP servers, invoke an agent, or change the winning registry.

## Authentication commands

**CLIM-020 — Token installation transaction.** Installing acquired OAuth tokens first clears prior authentication without clearing onboarding, then stores account profile data from the profile endpoint or falls back to token-exchange account data. Save tokens, clear token caches, report storage warnings through privacy-filtered telemetry, and fetch roles best-effort. A first-party subscription token fetches first-token metadata best-effort; a Console token must create and store an API key, and absence of the returned key is fatal. Clear authentication-derived caches last.

**CLIM-021 — Login selection and cleanup.** Reject simultaneous Console and subscription flags before opening a browser. Managed `forceLoginMethod` overrides command preference; managed organization identity is passed to the flow and validated after token installation. When a refresh token is supplied by environment, require a nonempty space-separated scope environment value, exchange it without a browser, install tokens, validate the forced organization, mark onboarding complete, and exit. Otherwise run the browser service with optional login hint and SSO method, print both the opening notice and fallback URL, and always clean up the OAuth listener in a finalizer. SSL-aware failures use stderr and status 1.

**CLIM-022 — Authentication status.** Compute logged-in state from, in priority-independent union, an auth token, configured API-key source, direct API-key environment value outside the managed home surface, or third-party provider. Classify display method in this order: third party, subscription token, API-key helper, other OAuth token, API-key environment/source, managed login key, none. Text mode prints nonempty account/provider properties and an actionable not-logged-in message. Machine-readable mode emits one formatted JSON object with `loggedIn`, `authMethod`, and provider, conditionally key source, and subscription account fields only for subscription auth. Exit 0 only when logged in.

**CLIM-023 — Logout.** Clear credentials and derived caches without resetting onboarding. On any failure print a fixed failure and exit 1; otherwise print confirmation and exit 0.

## Automatic-mode rules

**CLIM-030 — Rules projection.** `defaults` writes the complete external default rule object as formatted JSON. `config` resolves each of `allow`, `soft_deny`, and `environment` independently: a nonempty user list replaces that section, while absent or empty uses the default. Do not concatenate user and default entries within one section.

**CLIM-031 — Rule critique side query.** If every custom rule list is empty, print guidance and return without a model request. Otherwise resolve the optional model or main-loop model, construct a side query containing the complete classifier prompt plus, for each nonempty section, both the replacing custom rules and displaced defaults. Use the dedicated critique system instruction, omit the ordinary system-prefix, cap output at 4,096 tokens, and print the first text block. Transport failure sets exit status 1 without throwing past the handler; a response without text prints a retry suggestion.

## MCP commands

**CLIM-040 — MCP host launch.** Validate the current directory before setup. An inaccessible directory fails before setup; otherwise run noninteractive setup and start the standalone MCP host with the declared debug and verbose values. Startup failure is status 1. The host owns its long-lived exit after successful start.

**CLIM-041 — Scope-safe MCP removal.** Capture the effective pre-removal server so HTTP/SSE OAuth tokens and client registration can be cleared after configuration removal. With an explicit scope, validate and remove only that scope. Without one, inspect local, project `.mcp.json`, and user scopes. Remove automatically only when exactly one contains the name; none is an error, and multiple prints every scope/path plus exact scoped commands and makes no mutation. Secure-storage cleanup follows successful removal only.

**CLIM-042 — MCP inspection and bounded health.** Listing resolves all configs, probes connection health concurrently under the MCP connection batch limit, and renders connected, needs-auth, failed, or exception status. Display HTTP, SSE, proxy, and stdio endpoints according to transport; omit internal IDE-only transport. `get` additionally prints winning scope, transport fields, configured headers/environment, and only whether OAuth secret material exists—not the secret value. Both paths use graceful shutdown after probes so child transports are not orphaned.

**CLIM-043 — MCP JSON add secret ordering.** Parse the supplied JSON and validate scope. If the caller requests a client secret and the object is an HTTP/SSE OAuth configuration with client ID and URL, acquire the secret before writing configuration so cancelled input leaves no partial config. Add config, then store the secret under the server/transport identity, and report the effective transport (`stdio` when absent). Any validation or persistence failure exits 1.

**CLIM-044 — MCP desktop import and approval reset.** Desktop import validates scope, loads platform-specific desktop configuration, exits successfully when none exists, or mounts the selection dialog and remains alive until it unmounts. Reset clears the enabled list, disabled list, and approve-all flag together in project config, leaving server declarations intact so the next startup asks again.

## Plugin and marketplace commands

**CLIM-050 — Plugin validation statuses.** Validate the supplied plugin or marketplace manifest. For a plugin manifest immediately inside its metadata directory, also validate plugin content definitions. Print every error and warning with path. Exit 0 for success with or without warnings, 1 for validation failure, and 2 for an unexpected validator failure. Cowork selection is latched before validation when requested.

**CLIM-051 — Complete plugin inventory.** Load installed records and the active editable-scope set, then load enabled, disabled, session-inline plugins, and load errors once. JSON output includes one record per installation, optional MCP server declarations and load errors; session-inline records use scope `session` and no fabricated install timestamps. A path-level inline failure becomes a disabled error record even when no plugin object was created. `available` adds only marketplace plugins not already installed; marketplace discovery failure is deliberately a best-effort empty addition. Human output presents installed and session-only sections and never hides inline path failures behind the no-installed early exit.

**CLIM-052 — Marketplace declaration lifecycle.** Add accepts parsed repository, Git, URL, directory, or file sources; sparse paths are legal only for Git/GitHub. Validate scope as user, project, or local, materialize/resolve the source, then persist declaration intent at that scope and clear caches. List is deterministic by name and exposes source-specific location plus install path in JSON. Remove and refresh clear caches after success. Refresh without a name exits successfully when none exist; otherwise refreshes all declared marketplaces.

**CLIM-053 — Plugin mutation scopes.** Install/uninstall accept user, project, or local scope, default user, and optional retained data on uninstall. Enable/disable may infer scope; Cowork forces or requires user scope. Disable-all conflicts with a plugin operand and with explicit scope; omitting both plugin and `--all` is an error. Update accepts only the update-scope set and defaults user. Telemetry routes plugin and marketplace identities only through the privileged PII-tagged fields and never general metadata.

## Setup, doctor, and installer

**CLIM-060 — Setup-token dialog.** Render the long-lived subscription-token flow under application/keybinding providers. If another environment/helper credential exists, show a warning but continue. Resolve only when the flow reports done, then unmount and exit 0.

**CLIM-061 — Doctor lifetime.** Render doctor under application, keybinding, plugin-management, and MCP-connection owners. Remain alive until doctor reports done, then unmount and exit 0 so diagnostics do not leave clients or terminal handlers registered.

**CLIM-062 — Installer status adapter.** Run ordinary setup first, then invoke the install command with optional target and force flag. The callback text is the compatibility status boundary: select status 1 when it contains the failure marker and 0 otherwise. Do not start a model turn.

## Native session inventory and deletion

**CLIM-070 — Workspace-authoritative adapter.** Route the additive `CLIG-033`
selectors through one runtime-owned native-session service after validating the
explicit nonempty `--cwd` and normalizing it with the ordinary absolute
workspace logic. Give the service only the frozen sessions-root authority and
normalized workspace, never caller-supplied application-home, workspace hash,
session path, or transcript path. Preserve the common application-home and
auth-presence bootstrap, but do not fully parse credentials or construct a
semantic session, provider, query engine, transcript store, workspace
partition for an empty inventory, project memory, extensions, tools, or MCP
connections. Reject every repeated management option before validating its
final parsed value so a later occurrence cannot overwrite an earlier empty or
forbidden selection.

**CLIM-071 — Bounded inventory projection.** List only the selected workspace
through bounded pages; page size defaults to 100 and accepts 1 through 500.
Return an opaque continuation token when more entries remain and a stable
`stale` result when a supplied token no longer identifies the inventory
generation. Text output prints `No sessions found.` for an empty inventory,
otherwise one tab-separated `session_id`, canonical `updated_at`, and opaque
revision row per item, followed by a `next_page_token` row when present. JSON
emits exactly one version-1 object with closed status `ok`, `stale`, or
`store_unsafe`, a `sessions` array containing only `session_id`, optional
`updated_at`, and `revision`, plus optional `next_page_token`. No projection
contains conversation or filesystem metadata.

**CLIM-072 — Revision-bound single deletion.** Require the opaque revision
returned by inventory and delete exactly one ID in the selected workspace.
Text output is one tab-separated status and session ID. JSON emits exactly one
version-1 object with the session ID and one closed status: `deleted`,
`not_found`, `stale`, `session_locked`, `delete_incomplete`, or
`store_unsafe`. Return `deleted` only after the runtime-owned directory is
absent. A stale revision, active nonblocking session lock, committed detach
whose cleanup remains pending, and unsafe store identity remain distinct
machine-readable outcomes; do not collapse them into human error text.

**CLIM-073 — No implied duplex control.** Provider-free CLI dispatch is the
required integration. Do not register `list_sessions` or `delete_session` as
duplex controls merely because the service exists. The synchronous initialized
control handler is not a safe deletion owner; a future control adapter must
first move work off the input-reader path and specify correlation, interrupt,
permission-response, cancellation, timeout, completion, and shutdown ordering.

## Acceptance scenarios

### `CLIM-A01` — Ambiguous MCP scope

Place one server name in local and user scope, remove without a scope, and verify both paths and exact scoped examples are printed, no config/token is removed, and status is 1.

### `CLIM-A02` — Refresh-token login is all-or-fail

Supply a refresh token without scopes and verify no browser, credential clearing, or token write occurs. Repeat with scopes and a forced-organization mismatch; verify installed credentials are not misreported as successful and status is 1.

### `CLIM-A03` — Inline plugin failure remains visible

Point a session plugin directory at an absent path with no installed plugins. Verify both human and JSON inventory surface the path-level error and do not print the ordinary empty-inventory message.

### `CLIM-A04` — Content validation exit distinction

Validate a syntactically valid plugin manifest whose bundled command is invalid and verify exit 1; make the validator itself throw and verify exit 2.

### `CLIM-A05` — Auto-rule replacement

Configure only a custom allow list. Verify effective JSON uses custom allow and default deny/environment, and critique explicitly shows both the replacing allow rules and displaced defaults.

### `CLIM-A06` — Health probe cleanup

List a stdio MCP server that starts a child, then complete the listing. Verify bounded concurrency, status output, and child/connection cleanup before status 0.

### `CLIM-A07` — Provider-free empty native inventory

Place a malformed but present `auth.json` in the frozen application home and
select an existing workspace whose session partition is absent. Run
`--list-sessions --cwd <workspace>` in text and JSON modes. Verify the
credential document is not parsed, no model/provider connection or semantic
session starts, the result is empty, and no workspace partition, project
memory, or transcript is created.

### `CLIM-A08` — Workspace-scoped revision deletion

Create the same valid native session ID in two workspace partitions. List each
workspace and retain its opaque revision. Delete the first using its revision;
verify only that directory disappears and the second remains listable. Repeat
with a syntactically valid wrong revision and with the target lock held by
another process; verify the JSON statuses are respectively `stale` and
`session_locked`, each as the sole stdout object.

### `CLIM-A09` — Bounded minimal projections

List enough sessions to require a continuation token with page size one, then
drain every page. Verify deterministic, duplicate-free coverage without silent
truncation. Change the inventory before reusing a token and verify `stale`.
For both text and JSON, verify no transcript/prompt/title/topic/tool content,
path, workspace hash, or application-home value is exposed; JSON contains
exactly one versioned object.

### `CLIM-A10` — Management grammar isolation

Combine list and delete, omit or empty `--cwd`, omit a deletion revision, put a
revision on list, put pagination on delete, request `stream-json`, add a prompt
before or after the selector, and explicitly supply every ordinary option,
including values equal to defaults. Verify each fails before runtime mutation.
Put each management selector immediately after every scalar option that lacks
its value and verify the selector is rejected rather than consumed as that
value or routed into ordinary conversation startup. Keep a selector after a
standalone `--` literal when no management mode was selected.
Repeat each management scalar with an earlier empty or forbidden value and
verify the later valid-looking occurrence cannot override it.
For valid management invocations, verify print/headless inference stays false
and no duplex SDK initialization or control record appears.

## Non-normative provenance

Evidence came from provider-free native-session management and noninteractive
command adapters for agents, authentication, automatic mode, MCP, plugins,
marketplaces, setup-token, doctor, installation, and the centralized exit
helper. Paths and private symbols are not implementation requirements.
