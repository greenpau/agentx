# Integration and extension workflow contracts

## Contents

1. [CMD-WF-GITHUB-APP-001 — GitHub App and Actions installation](#cmd-wf-github-app-001-github-app-and-actions-installation)
2. [CMD-WF-PLUGIN-001 — Plugin, marketplace, validation, and option management](#cmd-wf-plugin-001-plugin-marketplace-validation-and-option-management)
3. [CMD-WF-PLUGIN-RELOAD-001 — Plugin registry reload](#cmd-wf-plugin-reload-001-plugin-registry-reload)
4. [CMD-WF-MCP-001 — MCP settings and connection control](#cmd-wf-mcp-001-mcp-settings-and-connection-control)
5. [CMD-WF-INTEGRATION-HANDOFF-001 — First-party integration installation handoff](#cmd-wf-integration-handoff-001-first-party-integration-installation-handoff)
6. [CMD-WF-BROWSER-INTEGRATION-001 — Browser/Chrome integration setup](#cmd-wf-browser-integration-001-browserchrome-integration-setup)
7. [CMD-WF-IDE-INTEGRATION-001 — IDE discovery and connection](#cmd-wf-ide-integration-001-ide-discovery-and-connection)
8. [CMD-WF-REMOTE-ENV-001 — Remote environment selection](#cmd-wf-remote-env-001-remote-environment-selection)
9. [CMD-WF-REMOTE-SESSION-001 — Remote/mobile session status and handoff](#cmd-wf-remote-session-001-remotemobile-session-status-and-handoff)
10. [CMD-WF-OUTPUT-STYLE-001 — Legacy output-style selection](#cmd-wf-output-style-001-legacy-output-style-selection)
11. [CMD-WF-KEYBINDINGS-001 — Keybinding configuration handoff](#cmd-wf-keybindings-001-keybinding-configuration-handoff)
12. [CMD-WF-TERMINAL-SETUP-001 — Terminal capability setup](#cmd-wf-terminal-setup-001-terminal-capability-setup)
13. [CMD-WF-WEB-SETUP-001 — Remote web execution setup](#cmd-wf-web-setup-001-remote-web-execution-setup)
14. [CMD-WF-PRODUCT-EXPERIENCE-001 — Optional product experience handoff](#cmd-wf-product-experience-001-optional-product-experience-handoff)
15. [CMD-WF-SKILLS-001 — Skill browser](#cmd-wf-skills-001-skill-browser)

## CMD-WF-GITHUB-APP-001 — GitHub App and Actions installation

### Entry and inputs

`/install-github-app` is an interactive-only command available to first-party and API-console accounts unless the installation command is disabled. It accepts no command-line workflow options. The dialog collects repository, workflow selection, secret name, and authentication choice.

Initialize the workflow with these values: step `check-gh`; repository unset; current-repository flag false; selected workflows `agentx` and `agentx-review`; secret name `AGENTX_API_KEY`; authentication choice `existing` when a local first-party API key is available, otherwise `oauth` when OAuth is enabled, otherwise `new`; authentication type `api_key`; secret token unset.

### State machine and authority handoffs

| State | Transition and effect |
| --- | --- |
| `G0 check-gh` | Run `gh --version` and `gh auth status -a` without treating nonzero status as an unhandled process exception. Parse authentication/scopes. Missing executable, unauthenticated client, or missing required scopes becomes a warning or blocking error with exact remediation. Detect the current repository independently. Warnings route to `G1`; otherwise route to repository choice. |
| `G1 warnings` | User may cancel or continue. Continue routes to `G2 install-app`; cancellation terminates without product-owned repository mutations. |
| `G2 install-app` | Open the GitHub App installation page in the browser. This is an external authority handoff, not proof that installation succeeded. Continue to `G3 choose-repository`. |
| `G3 choose-repository` | Accept `owner/repository` or a GitHub repository URL and normalize it. Reject malformed input locally. Query repository permissions through `gh api`; distinguish not-found from lack of admin rights. Detect an existing `.github/workflows/agentx.yml`. Validation warnings can be acknowledged; hard lookup/admin failures cannot. |
| `G4 existing-workflow` | If a workflow exists, user chooses `update`, `skip`, or `exit`. `exit` terminates as user-cancelled. `skip` preserves the existing workflow but can still continue to secret setup. `update` allows branch content replacement. A new repository goes directly to workflow selection. |
| `G5 select-workflows` | Select either or both specified workflow templates: `agentx` and `agentx-review`. Persist selection only in dialog state. |
| `G6 check-secret` | Query whether the chosen repository already has the selected secret. Existing-secret use sends no token to the setup stage. Otherwise offer local existing API key, newly entered API key, or OAuth where enabled. Secret names must contain only letters, digits, and underscore. |
| `G7 OAuth/API-key` | New-key flow validates and captures a token. OAuth cancellation returns to authentication selection rather than mutating the repository. OAuth success changes the secret name to `AGENTX_OAUTH_TOKEN` and authentication type to OAuth. |
| `G8 create` | Recheck repository facts. Resolve default branch and its head commit. Unless workflow action is `skip`, create a timestamped branch named `add-agentx-github-actions-<milliseconds>`. For each selected template, sequentially PUT `.github/workflows/<template-file>` on that branch, passing an existing file SHA when updating. Render template secret references for OAuth or a custom secret name. After workflow writes, set the repository secret when a token was supplied. Finally open a compare URL prefilled for pull-request creation. Increment setup analytics only after this success path. |
| `G9 success/error` | Success explains that the pull request still must be reviewed and merged. Error identifies the failed stage and gives manual recovery. A final key closes the dialog. |

The browser, GitHub CLI, repository API, and compare page are separate authorities. Opening the App page does not establish installation; writing a branch does not merge it; opening a compare URL does not create or merge a pull request.

### Cancellation and partial failure

Cancel before `G8` leaves no product-created branch/secret, although browser navigation and any user-completed App installation may already exist. Choosing `exit` at the existing-workflow decision reports installation cancelled. OAuth cancel returns to the API-key choice. The standard interactive cancel key terminates the dialog.

`G8` is intentionally non-transactional. There is no rollback across branch creation, first/second workflow PUT, secret creation, and browser opening. A later failure can leave a branch, one workflow file, or a secret. Error output must name the repository/branch when known and must never claim rollback. Retrying may update existing timestamped or target files only according to repository API semantics; it must not blindly create duplicate secrets/branches.

Headless, bridge, and surfaces without local interactive/browser/CLI support filter this command before execution. Disabled outcome performs none of the checks or browser operations.

## CMD-WF-PLUGIN-001 — Plugin, marketplace, validation, and option management

### Argument grammar

Parse the trimmed argument string into one of these routes:

- empty or `manage`: open the plugin settings/menu;
- `help`, `-h`, or `--help`: show usage and terminate;
- `install` or `i`, optionally followed by a target;
- `uninstall <plugin>`, `enable <plugin>`, or `disable <plugin>`;
- `validate <path>` where the rest of the string is one path;
- `marketplace` or `market`, followed by `add`, `remove`/`rm`, `update`, or `list` and the remaining target;
- any unknown first token: open the menu rather than failing as an unknown slash command.

For install targets, `plugin@marketplace` addresses one known marketplace entry. A URL or local path is a marketplace-source candidate. Other text is a plugin search/name candidate.

### Route state machine

| Route/state | Required behavior and effects |
| --- | --- |
| `P0 menu` | Open tabs for discovery, installed plugins, marketplaces, and errors. Tab state is UI-only. Escape returns to the command caller. |
| `P1 discover-batch` | User selects multiple discovered plugins. Install sequentially at user scope. Record each result independently. Clear discovery caches after the batch. Refresh the current registry only when at least one install succeeded. Terminal result distinguishes all-success, mixed, and all-failed. Do not enter per-plugin options during a batch. |
| `P2 install-single` | Resolve plugin and scope (`user`, `project`, or `local`), show trust/dependency/policy warnings, then install. Reload the just-installed manifest. If it can be found, enter `P3 configure`; otherwise terminate with install success plus reload guidance. |
| `P3 configure` | Snapshot the sequence of unconfigured top-level manifest options followed by unconfigured channel options. Load current values. Save or skip each step in order. Each save commits immediately; unchanged sensitive values remain preserved. Cancel skips the current option or exits according to the dialog action. A later save failure does not undo earlier options. |
| `P4 manage-installed` | Display effective installation scopes and enabled state. Built-ins can only enable/disable; managed installations can update. Project/shared uninstall may divert to a confirmation and offer local disable. If removing the last scope while persistent plugin data exists, ask separately whether data should be removed. Apply enable, disable, update, or uninstall through the plugin service, surface dependency warnings, clear caches, and enter configuration if the resulting enabled plugin still has missing options. An already-current update is a successful no-op. |
| `P5 add-marketplace` | Accept owner/repository shorthand, URL, or local path. Resolve/download/register the source, then persist the marketplace source in settings and clear caches. External sources require the applicable trust warning. |
| `P6 manage-marketplaces` | List sources, inspect details, stage update/removal, confirm destructive removal, and apply pending changes. Show auto-update control only when global policy permits it. Errors keep the user in a recoverable menu. Marketplace browsing can route back to `P1`/`P2`. |
| `P7 validate` | Require a path. For a directory containing both recognized manifests, validate the marketplace manifest before the plugin manifest. Report schema/semantic failures. Exit code is 0 for valid, 1 for validation failure, and 2 for unexpected execution failure. This is the only route with a command-style numeric validation outcome. |

### Activation, cancellation, and failure

Plugin installation/update/uninstall mutates installation state but does not retroactively rebuild the already assembled session registry. The terminal result must instruct the user to run `/reload-plugins` when activation in the current session is needed. A refresh callback can update discovery presentation, but it is not equivalent to full runtime reload.

Trust, policy, dependency, manifest, network, filesystem, and settings failures are distinct. Batch installation is partial by design. Option configuration and marketplace addition are also non-transactional: earlier option saves remain, and downloaded/registered marketplace data may remain if the later settings save fails. Destructive removal requires confirmation; cancellation before confirmation has no removal effect. No route may silently enable a plugin prohibited by policy.

The UI variant is interactive-only. A future headless interface must expose explicit subcommands and structured confirmation; it must not simulate acceptance. Base absence is correct when the plugin subsystem is not compiled or permitted.

## CMD-WF-PLUGIN-RELOAD-001 — Plugin registry reload

On `/reload-plugins`, invalidate plugin manifest, marketplace, command, skill, tool, hook, and error caches owned by the extension plane; bump the plugin/MCP reconnect generation; rediscover under current policy; and then complete. Existing in-flight tool calls retain the definition under which they started. Cancellation before invalidation is a no-op; after invalidation, finish rebuilding or leave the registry explicitly unavailable rather than serving a half-old/half-new merge. A provider reconnect failure is reported per provider while other providers remain usable. Headless support is absent in the specified local descriptor.

## CMD-WF-MCP-001 — MCP settings and connection control

`/mcp` is interactive and immediate. Parse these exact command routes before opening UI:

- no arguments: in the external build open MCP settings; in an internal build the default route may redirect to the plugin-installed tab;
- `no-redirect`: force the MCP settings surface;
- `reconnect <server name>`: retain all remaining text, resolve one server, and request reconnect;
- `enable [server name]` or `disable [server name]`: target one named server or all applicable servers;
- anything else: open the settings surface with ordinary filtering rather than treating it as shell input.

Exclude IDE-owned MCP clients from enable/disable targets. `enable` selects currently disabled targets; `disable` selects targets not already disabled. If no target remains, report `already enabled/disabled` or `not found` without mutation. The specified toggle loop issues each settings change without awaiting connection settlement and immediately reports intent. Therefore “enabled” or “disabled” acknowledges configuration mutation, not successful connect/disconnect. Reconnect completion is owned by the MCP connection manager and may fail after the command closes.

MCP settings, OAuth, transport, and tool/resource discovery belong to the MCP/LSP skill. This command owns only route parsing, target resolution, configuration intent, and completion wording. Canceling the settings dialog makes no additional change, although settings saved before cancel remain. Disabled or noninteractive surfaces do not open the settings UI.

## CMD-WF-INTEGRATION-HANDOFF-001 — First-party integration installation handoff

This contract covers `/install-slack-app` and similarly shaped first-party installation entrypoints whose server flow is not represented locally. Gate by first-party account, policy, build inclusion, and browser availability. Snapshot the return/callback identity, request a short-lived installation URL from the owning service, open it, and report that setup continues in the browser. Cancellation before URL creation is a no-op; after browser open, external authorization can outlive the command and cannot be rolled back locally. URL creation, browser failure, callback denial, and server rejection remain distinct. Do not report installation complete until the owning service callback confirms it. The specified server-side grant protocol is opaque and must be supplied by the integration provider.

## CMD-WF-BROWSER-INTEGRATION-001 — Browser/Chrome integration setup

Gate by supported platform, feature, policy, and first-party account. Inspect current browser integration state, then offer install/connect/repair instructions or open the owning browser surface. Local extension installation and browser consent are external authority handoffs. Cancel before choosing an action has no effects. Browser navigation may remain after cancel. Connection failure must preserve the prior integration state and show a retry route. The command must not claim browser control merely because an extension page opened.

## CMD-WF-IDE-INTEGRATION-001 — IDE discovery and connection

Discover supported IDE endpoints for the current process/workspace, show current connection, and allow connect/disconnect or installation guidance. Verify endpoint identity before persisting/session-binding it. The IDE transport owns handshake and liveness. Cancel before selection is a no-op; disconnect commits immediately; connect failure retains the prior connection when possible. Unsupported terminals or absent IDE endpoints show a bounded unavailable result rather than waiting indefinitely.

## CMD-WF-REMOTE-ENV-001 — Remote environment selection

Gate by remote-execution eligibility. Load available environments, distinguish create/configure/select routes, and hand mutations to the remote environment service. Selection changes placement for future work, not already running work. Cancel leaves selection unchanged. Authentication/network failure leaves the prior environment active. Environment creation can succeed server-side even if the final refresh fails; report that as partial and offer reload. Provider-owned provisioning details are an opaque boundary.

## CMD-WF-REMOTE-SESSION-001 — Remote/mobile session status and handoff

For `/session` and `/mobile`, read the current remote/bridge session identity and show URL, QR representation, connection status, or setup instructions. Starting or opening a session is delegated to the remote/bridge runtime. Copy/open operations acknowledge local handoff only. Canceling the display has no remote teardown effect. A missing session routes to setup; an expired token requires refresh; offline status remains explicit. Never place a durable bearer token in model-visible transcript text.

## CMD-WF-OUTPUT-STYLE-001 — Legacy output-style selection

Load only output styles available under the current compatibility gate. With no argument, show a picker; with a valid style name, select directly. Persist through the settings layer and update the session prompt transformation only after a successful save. Cancel preserves the current style. Invalid name or save failure leaves it unchanged. Hidden/deprecated status affects discovery, not exact-name invocation when still enabled.

## CMD-WF-KEYBINDINGS-001 — Keybinding configuration handoff

Open the local keybinding configuration flow, validate action/context/key sequences, and persist only a schema-valid complete mapping. The editor/file-system boundary owns the physical write. Cancel before save is a no-op; an external editor can leave user edits even if the command later loses focus. Parse/save errors preserve the previously loaded active bindings and identify the invalid entry. Remote-safe routing may display/edit controller-local configuration only when that surface explicitly owns the terminal.

## CMD-WF-TERMINAL-SETUP-001 — Terminal capability setup

Detect terminal family and native support before showing the command. When needed, present the terminal-specific sequence for shift-enter/key protocol configuration and persist any product-side acknowledgment only after user confirmation. External terminal settings cannot be rolled back. Cancel keeps product-side state unchanged. Unsupported or already-native terminals hide the command; exact invocation returns a local unavailable result without emitting control sequences.

## CMD-WF-WEB-SETUP-001 — Remote web execution setup

Gate by remote-setup feature and first-party account. Resolve account/repository prerequisites, open or authenticate the remote-web setup surface, and wait only for bounded local confirmation. Browser/server provisioning may outlive the command. Cancel before handoff is a no-op; after handoff, report that server-side setup may still exist. Authentication, policy, and provisioning failures retain local session placement. The provider-owned provisioning protocol is opaque.

## CMD-WF-PRODUCT-EXPERIENCE-001 — Optional product experience handoff

This shared entry contract covers gated experience commands such as stickers, voice, desktop installation, upgrade, and passes where the local command primarily opens a feature-owned UI or browser/service flow. Re-evaluate the descriptor gate at invocation, snapshot current feature/account state, and pass only the minimum callback/context to the owning experience. The feature owns its internal state machine. Local completion distinguishes `opened`, `changed`, `cancelled`, `unavailable`, and `handoff-failed`; `opened` is never synonymous with account grant or installation. External/browser effects are not rolled back by closing the command. If the feature package is absent, absence is the correct base behavior.

## CMD-WF-SKILLS-001 — Skill browser

Snapshot the session-scoped, policy-filtered skill registry, display source attribution and availability, and allow read-only inspection. Invoking a skill is a separate prompt/tool route and must pass through its ordinary argument and permission contracts. Cancel has no effect. A discovery failure is scoped by source; valid skills from other sources remain visible. The browser itself does not install, edit, or silently inject a skill.
