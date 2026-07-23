# Command-workflow acceptance scenarios

## Contents

1. [Test harness rules](#test-harness-rules)
2. [History and session workflows](#history-and-session-workflows)
3. [Integration and extension workflows](#integration-and-extension-workflows)
4. [Account, feedback, settings, and diagnostics](#account-feedback-settings-and-diagnostics)
5. [Capability configuration workflows](#capability-configuration-workflows)
6. [Opaque and optional handoff baseline](#opaque-and-optional-handoff-baseline)
7. [Specialized specified workflows](#specialized-specified-workflows)

## Test harness rules

Run each scenario through command lookup and dispatch, not by invoking a workflow helper directly. Capture registry source, descriptor gate, input/arguments, presented states, authority calls, ordered durable effects, app-state changes, model-visible messages, command terminal result, and late asynchronous events. A scenario passes only when both the visible result and absence/presence of effects match.

For cancellation tests, inject cancellation at the named state. For failure tests, fail the named authority after earlier stages have succeeded. For disabled tests, make lookup/enablement reject the descriptor and assert *zero* workflow authority calls.

## History and session workflows

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-001 | normal | `/insights` with two valid substantive sessions, valid facets, all narrative sections | Private HTML is committed; returned prompt points to it; no report-analysis tool authority exists. |
| CW-AC-002 | cancel | Cancel outer turn while facet requests are running | UI/turn cancels; test records whether detached requests settle; result never falsely claims those requests were aborted. |
| CW-AC-003 | failure | One remote host, one facet, one narrative, and upload fail | Local report still commits with partial/fallback semantics; successful caches remain; no full-success claim. |
| CW-AC-004 | disabled | Report support absent or external profile invokes internal-only `--homespaces` authority | Disabled command scans nothing, or external command runs local-only without remote CLI calls. |
| CW-AC-005 | normal | `/clear` with one foreground shell, one running background agent, messages, file history, MCP clients | foreground task killed/removed; background agent preserved/relinked; new session ID/transcript pointer; messages replaced only by SessionStart-hook output; MCP reconnect generation preserved. |
| CW-AC-006 | cancel | SessionEnd hook exceeds configured bound | Hook is timed out per policy and clear either proceeds with explicit hook result or terminates before message clearing; no indefinite wait. |
| CW-AC-007 | failure | Transcript pointer reset fails after state clear | Session enters explicit recovery/error; it does not continue with new in-memory ID and old file pointer. |
| CW-AC-008 | disabled | Invoke `/clear` from a surface that filters interactive clear | No hook, task, cache, message, or session-ID mutation. |
| CW-AC-009 | normal | `/compact retain API decisions` with enough messages | memory path skipped, reactive/legacy receives exact instruction, typed compact result commits through caller, progress finalizer clears. |
| CW-AC-010 | cancel | Abort during reactive compact | Stable `Compaction canceled.` result; authoritative pre-compact messages unchanged; SDK status cleared. |
| CW-AC-011 | failure | Model returns incomplete compact response | Stable incomplete-response failure; no compact replacement installed; hook/model side effects are not called rolled back. |
| CW-AC-012 | disabled | `DISABLE_COMPACT` effective | Descriptor absent/disabled; no hooks, model call, or cleanup. |
| CW-AC-013 | normal | `/branch Design review` on transcript with sidechain and content replacements | New owner-only transcript contains only main chain, rewritten parent/session/fork provenance and replacement record; unique title saved; resume called with mode `fork`. |
| CW-AC-014 | cancel | Close before branch dispatch | No file created. Once copy starts, UI must not promise cancellability it cannot provide. |
| CW-AC-015 | failure | Title save fails after fork transcript write | Error identifies branch failure; valid orphan branch file remains and is discoverable/recoverable; no deletion. |
| CW-AC-016 | disabled | Dedicated fork profile owns `/fork` alias | `/branch` remains; `/fork` resolves profile command, never the branch alias. |
| CW-AC-017 | normal | `/resume <UUID>` absent from index but transcript file exists | Direct-file fallback loads and hands exact session/log to resume; success is display-skip. |
| CW-AC-018 | cancel | Cancel no-argument resume picker | System result says resume cancelled; current session/messages unchanged. |
| CW-AC-019 | failure | Exact title yields two sessions | Ambiguity result; neither resume nor clipboard called. |
| CW-AC-020 | disabled | Interactive UI unavailable | Resume descriptor filtered; no log discovery. |
| CW-AC-021 | normal | Select cross-project session | Correct external resume command copied; current process does not resume/mutate. |
| CW-AC-022 | cancel | Cancel rewind selector | Command emitted skip and selector leaves messages/files unchanged. |
| CW-AC-023 | failure | Restore owner fails midway | Selector/recovery owner reports last consistent checkpoint; command itself never reports success. |
| CW-AC-024 | disabled | Context lacks message selector | `/rewind` returns skip and performs no file-history action. |
| CW-AC-025 | normal | `/export notes.md` with existing `notes.txt` | Snapshot rendered once; destination resolves to `notes.txt`; existing file overwritten/flushed; absolute path reported. |
| CW-AC-026 | cancel | In export dialog Escape from filename, then Escape from choice | First Escape returns to choice; second cancels; no file/clipboard effect. |
| CW-AC-027 | failure | File write fails after truncation begins | Error shown; destination may be partial and is not described as restored. |
| CW-AC-028 | disabled | Filesystem/interactive export surface filtered | No render, clipboard, or write. |

## Integration and extension workflows

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-029 | normal | GitHub CLI authenticated/admin; no workflow; OAuth selected; both templates | App page opened; timestamp branch based on default head; two sequential workflow PUTs reference OAuth secret; secret set after files; compare URL opened; result says merge still required. |
| CW-AC-030 | cancel | Existing workflow decision `exit` | Cancel result; no branch/workflow/secret; earlier App-page navigation may exist. |
| CW-AC-031 | failure | Second workflow PUT fails | First workflow and branch remain; no rollback claim; secret/compare steps do not run. |
| CW-AC-032 | disabled | install gate off or headless surface | No `gh` or browser invocation. |
| CW-AC-033 | normal | `plugin install demo@store`, project scope, two unconfigured options | Plugin installed; each option saved in order; reload guidance emitted. |
| CW-AC-034 | cancel | Cancel second plugin option after first saved | First remains; second skipped/cancelled; no rollback. |
| CW-AC-035 | failure | Batch plugin install succeeds, fails, succeeds | Sequential order observed; mixed result lists each; caches cleared; refresh occurs because at least one succeeded. |
| CW-AC-036 | disabled | Plugin policy/build disables subsystem | Command absent; no marketplace/network/cache action. |
| CW-AC-037 | normal | Add marketplace URL succeeds through registration and settings save | Source persisted, caches cleared, marketplace browse sees it. |
| CW-AC-038 | cancel | Decline external plugin trust warning | No install/settings change. |
| CW-AC-039 | failure | Marketplace registration succeeds then settings save fails | Partial downloaded/registered data is reported; no false rollback. |
| CW-AC-040 | disabled | Auto-update managed off | Marketplace remains manageable but auto-update control absent/read-only. |
| CW-AC-041 | normal | `/mcp disable server one` where name contains spaces and client is active | Exact remaining text resolves target; toggle intent issued; immediate result is configuration acknowledgement, not disconnect proof. |
| CW-AC-042 | cancel | Close MCP settings after one earlier saved toggle | Saved toggle remains; unsaved selection discarded. |
| CW-AC-043 | failure | Reconnect manager rejects after command closes | Late provider failure updates MCP state/status; original result is not rewritten as confirmed connection. |
| CW-AC-044 | disabled | Interactive component/dialog surface unavailable | No MCP setting or reconnect call. |
| CW-AC-045 | normal | `/reload-plugins` with two providers, one reconnects | Registry rebuilt consistently; provider failure isolated; current in-flight calls retain old definitions. |
| CW-AC-046 | cancel | Cancel before invalidation | Registry/cache untouched. |
| CW-AC-047 | failure | Discovery fails after invalidation | Registry marked explicitly unavailable/partial, never served as mixed-generation merge. |
| CW-AC-048 | disabled | Plugin subsystem absent | Command absent. |

## Account, feedback, settings, and diagnostics

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-049 | normal | Feedback description, consent, upload success with ID and generated title; Enter at done | Private report includes disclosed/sanitized bounded data; browser opens redacted issue draft under 7,250 encoded chars; issue not claimed submitted. |
| CW-AC-050 | cancel | Escape at consent | Cancel system result; no upload or title call. |
| CW-AC-051 | failure | Upload fails but title succeeds | Returns to editable input with description preserved; retry available; no done/success analytics. |
| CW-AC-052 | disabled | essential-traffic-only or product-feedback policy denied | Descriptor disabled; no transcript/error/raw-file read. |
| CW-AC-053 | normal | `/model custom-case-ID`, allowed and remotely valid while incompatible fast mode on | Exact case validated/preserved; base model changed; session override cleared; fast off only in app state; result mentions downgrade/extra usage as applicable. |
| CW-AC-054 | cancel | Cancel model picker | Existing model/effort/fast retained; `Kept model` result. |
| CW-AC-055 | failure | Account lacks requested 1M model | No validation/mutation after entitlement rejection; precise unavailable result. |
| CW-AC-056 | disabled | Surface has no picker and no supported immediate mode | Command filtered locally. |
| CW-AC-057 | normal | Config toggles theme, model, permissions, then Enter | Live writes persist; summary enumerates changes; app and settings agree. |
| CW-AC-058 | cancel | Config toggles same values, leaves search/submenu, then Escape | Theme/global/local/user/app/brief snapshots restored in required order; close is system dismissal. |
| CW-AC-059 | failure | One compensating settings write fails during Escape | UI reports incomplete revert and identifies source; does not claim clean dismissal. |
| CW-AC-060 | disabled | Managed policy makes row immutable | Row omitted/read-only; toggle causes no write. |
| CW-AC-061 | normal | `/stats`, cycle all→7d→30d while prior load is late, then Ctrl+S | Late result ignored; current filter correct; snapshot copied. |
| CW-AC-062 | cancel | Escape while stats loading | Closes; late promise cannot update unmounted view or transcript. |
| CW-AC-063 | failure | 7-day load fails after all-time success | Prior valid data remains; spinner stops; filter error visible. |
| CW-AC-064 | disabled | Local stats support absent | Empty/unavailable; no invented counts. |
| CW-AC-065 | normal | Team user lacks billing, eligible, no request | One admin request created; correct enable/increase wording. |
| CW-AC-066 | cancel | Interactive extra-usage login/confirmation interrupted | Interrupted result; no browser/admin create beyond effects already confirmed. |
| CW-AC-067 | failure | Eligibility lookup and prior-request lookup fail, create rejects | Falls back to contact-admin; all failures logged; no success claim. |
| CW-AC-068 | disabled | overage eligibility false | Both duplicate-name descriptors disabled/filtered; no visited flag. |
| CW-AC-069 | normal | Doctor with stale lock and one plugin error | Stale lock cleaned; plugin error and other diagnostics rendered; dismissal does not claim other repairs. |
| CW-AC-070 | cancel | Dismiss doctor after cleanup | Screen closes; stale lock stays removed. |
| CW-AC-071 | failure | Distribution network check fails | Network diagnostic shown; other checks complete. |
| CW-AC-072 | disabled | doctor-disable flag true | No lock cleanup, network, filesystem, or settings check. |
| CW-AC-073 | normal | OAuth login switches org | API-key callback and signature stripping; cost/cache/policy/feature/device/permission refresh sequence; authVersion increments; completion need not await detached refreshes. |
| CW-AC-074 | cancel | Cancel login dialog | Reports interrupted but still calls key-change notification and strips signature-bound blocks; success-only refreshes absent. |
| CW-AC-075 | failure | Detached policy refresh later fails | Login remains successful; late failure observable/logged; old policy is not asserted current. |
| CW-AC-076 | disabled | third-party provider or login gate | No OAuth dialog/key callback/message strip. |
| CW-AC-077 | normal | `/logout` with credentials/caches | Telemetry flush precedes deletion; secure storage/caches/config cleared; success renders; graceful shutdown scheduled with zero. |
| CW-AC-078 | cancel | Attempt input after logout dispatch | No cancellation path; destructive sequence continues or fails explicitly. |
| CW-AC-079 | failure | Remote-settings cache clear fails after credential deletion | Credentials remain deleted; error/restart recovery; no restoration from memory. |
| CW-AC-080 | disabled | third-party provider or logout gate | No telemetry flush or credential/config change. |

## Capability configuration workflows

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-081 | normal | `/add-dir ../shared`, normalized valid, remember selected | Session permission/bootstrap/sandbox updated before local persistence; success identifies saved local scope. |
| CW-AC-082 | cancel | Cancel add-dir confirmation | No permission/bootstrap/sandbox/persistence change. |
| CW-AC-083 | failure | Persistence fails after session authorization | Session access remains; result explicitly says save failed; no rollback. |
| CW-AC-084 | disabled | Interactive filesystem context unavailable | No path validation or permission update. |
| CW-AC-085 | normal | Edit custom agent tools/model/color | Agent file written first; color/live registry updated; success names agent. |
| CW-AC-086 | cancel | Escape from tool editor then list | First Escape returns to agent menu; second closes; no unsaved file write. |
| CW-AC-087 | failure | Agent file write fails | Live agent/color state unchanged; editor shows failure. |
| CW-AC-088 | disabled | Agent subsystem gated | Command absent. |
| CW-AC-089 | normal | Permissions rule saved then denied commands selected for retry | Typed rule persists; one model-visible retry message appended; commands are not executed directly. |
| CW-AC-090 | cancel | Close permission editor with unsaved form | Saved earlier rules remain; unsaved form discarded. |
| CW-AC-091 | failure | Permission persistence rejects managed override | Effective old rules remain; no retry/bypass. |
| CW-AC-092 | disabled | Noninteractive surface | No permission UI or message append. |
| CW-AC-093 | normal | `/sandbox exclude "npm run test:*"` on supported/unlocked platform | Quotes removed once; exact pattern appended to local excluded commands; path reported. |
| CW-AC-094 | cancel | Cancel sandbox menu without save | No new settings change. |
| CW-AC-095 | failure | Missing dependency in menu | Warning shown; unsafe enable not falsely confirmed. |
| CW-AC-096 | disabled | WSL1, excluded platform, or policy lock | Precise local rejection; no exclusion/settings write. |
| CW-AC-097 | normal | Select missing user memory file | Directory/file created exclusively, existing content preserved, editor opened; editor source reported. |
| CW-AC-098 | cancel | Cancel memory selector | No create/editor call. |
| CW-AC-099 | failure | File created then editor launch fails | Empty file may remain; error shown; no deletion. |
| CW-AC-100 | disabled | Memory subsystem unavailable | No cache prime or filesystem action. |

## Opaque and optional handoff baseline

For integration, browser, remote-environment, product-experience, privacy, web-setup, and other provider-owned workflows whose server implementation is not implemented here, apply this four-case baseline in addition to provider-specific tests:

| Scenario | Mode | Required assertion |
| --- | --- | --- |
| CW-AC-101 | normal | Local prerequisites are validated, the minimum authority is handed off, and terminal wording says `opened/requested/pending` until external confirmation. |
| CW-AC-102 | cancel | Before handoff there is no effect; after handoff the result admits external work may continue and does not claim rollback. |
| CW-AC-103 | failure | Local validation, URL/browser/transport, external rejection, and callback-refresh failures remain distinct; prior local state survives where possible. |
| CW-AC-104 | disabled | Feature/account/policy/platform gate prevents descriptor execution and all provider/browser/network calls. |

## Specialized specified workflows

### Thinkback bootstrap and public menu

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-105 | normal | `/think-back`; marketplace missing, plugin missing; installation succeeds; no `year_in_review.js`; choose initial generation | Marketplace add precedes plugin install; caches clear at each committed stage; enabled skill directory resolves; only regenerate is offered; command completes with the exact `mode=regenerate` model/Skill handoff and `shouldQuery=true`, not a local generation claim. |
| CW-AC-106 | cancel | Plugin is already installed but disabled; installer enables it; artifact exists; Escape from four-action menu | Enablement remains committed; menu closes exactly once with display skipped; no play subprocess and no generative query; cancellation does not claim plugin rollback. |
| CW-AC-107 | failure | Marketplace add succeeds, then plugin installation reports two failed entries | Error aggregates both plugin failures and suggests `/plugin`; marketplace/cache effects remain; no skill lookup/menu/model query; no rollback claim. |
| CW-AC-108 | disabled | `tengu_thinkback` false, exact `/think-back` invocation | Descriptor is unavailable; no marketplace, plugin, filesystem, terminal, browser, or model authority call. |

### Hidden thinkback playback

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-109 | normal | `/thinkback-play`; first installed path has data/player/html and renderer; player exits nonzero | Resolve first install; read both prerequisites; enter/exit alternate screen exactly once; inherited `node` run is treated complete despite nonzero; platform HTML opener is fire-and-forget; text result says animation complete. |
| CW-AC-110 | cancel | User interrupts the player subprocess | Interruption is caught; alternate screen always exits; optional HTML open and completion semantics follow the specified playback contract; cancellation does not leave terminal mode corrupted. |
| CW-AC-111 | failure | Plugin installed but `player.js` missing | Dedicated missing-player text result; no alternate-screen entry, subprocess, or browser open. A `/think-back` menu-play promise rejection/result-suppression fixture separately verifies its documented compatibility quirk. |
| CW-AC-112 | disabled | Gate false or noninteractive surface attempts hidden playback | Command rejected before installed-plugin metadata and filesystem reads. |

### Ultraplan detached remote planning

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-113 | normal | Ordinary interactive prompt `please ultraplan this`; confirm launch; remote emits one rejection then teleport sentinel with plan | Pre-expansion keyword routes once, first word becomes `plan`, launch latch prevents duplicates, remote task registers, phases/reject count update, and a still-running task installs pending local choice. Choice owner archives/clears/completes; the poll itself does not prematurely complete the task. |
| CW-AC-114 | cancel | `/ultraplan investigate` then cancel pre-launch; separately stop a registered run while poll is sleeping | Pre-launch cancel clears pending state and creates no session. Stop kills/archives once, clears launch/URL/choice, sends visible plus meta notifications; late poll stop produces no failure or resurrected dialog. |
| CW-AC-115 | failure | Remote creation succeeds and URL is stored, then registration/poll setup throws; later a stale poll from an old URL fails after a new launch | Created orphan is best-effort archived; launch latch clears; old failure cannot clear new URL due captured-URL compare; terminal failure remains attributable if archive itself fails. |
| CW-AC-116 | disabled | Build/profile gate absent, headless keyword input, or active launch latch | No disallowed keyword route/teleport/task/poll. Active invocation reports already launching/polling without opening terms dialog or changing its seen state. |

### Hidden rate-limit recovery menu

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-117 | normal | Pro subscriber, extra usage disabled but eligible, upgrade eligible, buy-first flag true | Menu orders extra-usage then upgrade then wait; labels match account state; selecting extra usage delegates the same context/completion and replaces the menu with returned child UI. |
| CW-AC-118 | cancel | Escape or choose wait | Cancellation analytics fire once; result is display skipped; no upgrade, billing, browser, or admin-request effect. |
| CW-AC-119 | failure | Delegated upgrade promise rejects before returning child UI | No success result or invented child UI; specified no-catch behavior is observed as an unsettled/error lifecycle and documented, never as rollback. |
| CW-AC-120 | disabled | Non-subscriber exact invocation, or Team non-admin with depleted org cap | Descriptor is absent for non-subscriber. In the Team depleted-cap case the extra-usage action is omitted; choosing cannot bypass billing authority. |

### Bridge fault injection

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-121 | normal | Live handle; queue `register fail 2`, then `close 1002`, then inspect `status` | Exactly two transient register faults are queued before the close trigger; command outputs acknowledge injection/trigger only; status is handle-authored and recovery success is not asserted. |
| CW-AC-122 | cancel | Cancel input before dispatch, then separately attempt to cancel after `poll 404` returns | Before dispatch no fault exists. After dispatch the one fatal not-found poll fault remains until consumed/teardown; command offers no rollback. |
| CW-AC-123 | failure | `injectFault` throws after one prior composite fault was queued | Local command error is surfaced; prior queued fault remains; no reset/rollback claim and no unrelated bridge startup. |
| CW-AC-124 | disabled | External/demo/noninteractive profile or no live debug handle | Filtered profiles make no handle call; an enabled command with no handle returns connection guidance and does not implicitly connect. |

### Brief-only mode

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-125 | normal | Entitled non-Kairos user toggles off→on then on→off | Each toggle orders message opt-in before matching app state, changes the tool list, logs success, attaches the correct next-turn hidden reminder, and reports enabled/disabled. Off remains allowed if entitlement flips. |
| CW-AC-126 | cancel | Cancel input before immediate dispatch; attempt cancellation after dispatch | Pre-dispatch state is unchanged. Once dispatched there is no cancel path; the ordered transition finishes or fails explicitly rather than reverting. |
| CW-AC-127 | failure | On-transition lacks entitlement; separately inject failure after opt-in before app-state completion | Entitlement failure changes nothing and returns the gated message. The injected later failure can leave opt-in partial; no success or rollback claim, and reconciliation detects opt-in/app-state mismatch. |
| CW-AC-128 | disabled | Build contribution absent, malformed config object, or config boolean false | Descriptor is unavailable; malformed config falls back to all-default disabled; no entitlement, opt-in, app-state, or reminder mutation. |

### Ultrareview cloud review

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-129 | normal | Consumer has one free review; branch mode has merge base and nonempty diff; bundle succeeds | Quota/utilization start concurrently; billing note identifies free ordinal; bounded bughunter env and merge-base SHA are sent; remote task type is ultrareview; model-bound completion includes target/URL once and asks only brief acknowledgement. |
| CW-AC-130 | cancel | Free quota exhausted; proceed starts launch; Escape while teleport is pending | Dialog-local signal suppresses late `onDone` and session confirmation. Because it is not the teleport signal, a created remote session/task may remain; test records this compatibility race and never claims remote cancellation. |
| CW-AC-131 | failure | Branch has empty diff; separately quota fetch rejects and PR mode runs outside GitHub | Empty diff returns precise model-bound precondition text without teleport. Quota rejection is a command error. Non-GitHub PR mode returns generic launch failure; none registers a task. |
| CW-AC-132 | disabled | Live config lacks `enabled=true` | Descriptor absent; no quota/utilization, git, teleport, billing dialog, or task registration. |

### Transcript-backed tags

| Scenario | Mode | Given / action | Required result |
| --- | --- | --- | --- |
| CW-AC-133 | normal | Active session has `#old`; invoke `/tag new` containing compatible Unicode; then invoke same normalized tag and confirm removal | NFKC/dangerous-character sanitation precedes comparison; replacement appends `tag:new` without confirmation and updates cache; same-tag route confirms, appends `tag:""`, clears cache, and reports removal. Transcript entries are append-only. |
| CW-AC-134 | cancel | Same normalized tag, choose `No, keep tag` or Escape | Cancellation event and `Kept tag` result occur once; no `saveTag`, transcript append, or cache change. Empty/no argument separately shows help rather than empty-tag error. |
| CW-AC-135 | failure | Nonempty raw tag sanitizes to empty; separately `saveTag` rejects during add and confirmed remove | Sanitized-empty returns the dedicated error without write. A save rejection produces no success and may leave lifecycle unsettled or an append partially committed; specified no-catch behavior is explicit and rollback is not claimed. |
| CW-AC-136 | disabled | Non-internal profile or no active session | Profile-disabled lookup makes no session/transcript call. Enabled command with no session returns `No active session to tag` and performs no append. |
