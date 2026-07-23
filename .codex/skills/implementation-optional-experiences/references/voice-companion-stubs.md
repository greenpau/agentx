# Voice, Terminal Companion, and External Stubs

## Contents

1. [Voice availability](#voice-availability)
2. [Voice enable and disable](#voice-enable-and-disable)
3. [Push-to-talk state machine](#push-to-talk-state-machine)
4. [Audio capture backends](#audio-capture-backends)
5. [Transcription transport and finalization](#transcription-transport-and-finalization)
6. [Attempt ownership, retry, and focus mode](#attempt-ownership-retry-and-focus-mode)
7. [Language and keyterms](#language-and-keyterms)
8. [Terminal companion identity](#terminal-companion-identity)
9. [Companion presentation](#companion-presentation)
10. [External-build stubs](#external-build-stubs)
11. [Failure behavior](#failure-behavior)
12. [Acceptance scenarios](#acceptance-scenarios)
13. [Non-normative provenance](#non-normative-provenance)

## Voice availability

Voice has independent checks:

1. Build includes voice support.
2. Cached remote kill-switch does not disable it. Missing/stale cached value defaults to not killed so visibility does not wait for network feature initialization.
3. Authentication provider is the first-party account/OAuth provider.
4. A current access token exists; provider selection alone is insufficient.
5. Voice stream endpoint is available.
6. Platform can record audio through a supported native or external backend.
7. User has enabled voice in settings.

- **OPT-VOI-001 — Visibility versus use.** Command/config visibility may use the cached kill-switch check. Command-time activation rechecks authentication and token availability.
- **OPT-VOI-002 — Provider restriction.** API-key and alternate-cloud-provider authentication cannot use the account voice-stream endpoint.
- **OPT-VOI-003 — Lazy services.** Do not load audio and stream-transcription modules until enablement or use requires them.

## Voice enable and disable

The voice command is a persistent preference toggle.

Disable path:

- Set the user voice setting false without microphone/dependency checks.
- Notify live settings subscribers.
- Report success or a settings-syntax/write error.

Enable path, in order:

1. Recheck full feature/auth eligibility.
2. Probe whether recording is possible in the environment.
3. Confirm the authenticated voice stream is available.
4. Check native/audio-command dependencies and provide a platform-appropriate installation hint when absent.
5. Probe microphone access immediately so the OS permission dialog appears during enablement, not first dictation.
6. On denial, report the platform's microphone-settings location.
7. Persist `voiceEnabled=true`, notify live settings and report the configured push-to-talk binding.
8. Resolve dictation language. If unsupported, fall back to English and say so. Otherwise show the language hint at most twice per resolved language; reset the counter when language changes.

- **OPT-VOI-004 — Transactional enable.** Persist enabled state only after every preflight check succeeds.
- **OPT-VOI-005 — Settings failure.** A settings write/parse failure leaves the previous live preference effective.
- **OPT-VOI-006 — Notice cap.** The general “voice is available” startup notice is shown at most three times and only while voice is eligible but not enabled.

## Push-to-talk state machine

Voice presentation state is `idle`, `recording`, or `processing`, plus a transient warming-up flag, audio levels, interim transcript range and optional error.

Bare-character bindings require hold detection because a single press may be ordinary typing:

- Key repeats separated by at most 120 ms count as a hold sequence.
- Show warmup feedback after two rapid events.
- Begin recording after five rapid events.
- Modifier or nontext bindings may activate on first press.
- Use a 2,000 ms first-press fallback for modifier combinations so a long operating-system repeat delay does not fragment recording.
- If no keybinding provider exists, default to Space. If a provider exists and Space is unbound/reassigned, do not silently fall back to Space.

- **OPT-VOI-007 — Modal suppression.** Do not begin hold-to-talk while a permission dialog, select/menu overlay, or other modal owns input.
- **OPT-VOI-008 — Character leak cleanup.** Track the prompt anchor and count of hold-key characters that reached the editor. Remove only characters belonging to activation/recording while preserving intentional preexisting characters.
- **OPT-VOI-009 — Focus recording.** Recording started through focus/non-hold mode does not swallow ordinary typing as though the push-to-talk key were held.
- **OPT-VOI-010 — Release transition.** When repeat/hold ends, stop audio and move `recording → processing`; clear hold state immediately so new spaces typed during processing are not swallowed.
- **OPT-VOI-011 — Transcript insertion.** Interim transcription is dim-highlighted in the prompt at a recorded anchor. Final text replaces the interim range and continues through the ordinary editor/submission path.
- **OPT-VOI-012 — Failure cleanup.** If activation fails and state remains idle, undo any artificial gap/anchor and remove leaked activation characters.

## Audio capture backends

Recording backend preference:

- Use an available native in-process recorder across supported desktop platforms.
- On Linux, probe `arecord` rather than trusting executable presence; use it when it can open a capture device.
- Fall back to SoX recording where supported.
- Windows has no external fallback when native capture is unavailable.
- Cache the Linux device probe for the session because device availability is treated as stable.

The recorder emits raw mono signed 16-bit linear PCM at 16,000 samples per
second. It does not emit a container header. Capture implementation is lazy:
the native module is imported only after use-time eligibility. A Linux native
backend is considered viable only when the system sound-card listing contains
at least one actual card. Executable presence alone does not prove an external
backend can open a capture device.

Linux external fallback probes `arecord` by performing a bounded real capture
of approximately 150 ms, caches that result, and when selected requests raw
16-bit little-endian mono at 16 kHz. SoX uses an audio buffer size of 1,024 and,
when silence-stop is requested, a silence threshold of 3 percent sustained for
two seconds. Stopping the `arecord` child sends graceful termination before
ordinary process cleanup. Windows has no command-line fallback after native
capture failure.

Microphone permission probing opens a real recorder without silence-stop and
then closes it. Success means capture could start; it does not persist an open
stream. The platform-specific dependency/permission diagnosis remains separate
from the user preference.

- **OPT-VOI-016 — PCM invariant.** Every backend produces raw linear16,
  16,000-Hz, one-channel audio with equivalent chunk ordering.
- **OPT-VOI-017 — Backend proof.** Select native first; on Linux prove card
  presence or probe `arecord` by opening capture; then try SoX. Do not infer
  usability from executable presence alone, and do not add a Windows external
  fallback.
- **OPT-VOI-018 — Permission probe cleanup.** Enablement's real capture probe
  closes all recorder/process resources and emits no transcript request.

## Transcription transport and finalization

Voice stream contract:

- Connect to the account speech-to-text WebSocket endpoint with authenticated headers.
- Send audio chunks only while the connection accepts audio; drop late chunks after close/finalization rather than causing protocol errors.
- Send `KeepAlive` every 8,000 ms while the stream is open.
- On release, send one `CloseStream` and await final transcript resolution.
- Resolve finalization from an explicit post-close endpoint/message, a 1,500 ms no-data timeout when no transcript arrives, a 5,000 ms safety timeout, WebSocket close, or already-closed state.
- Expose transcript callbacks with final/interim flag, readiness, close and fatal/nonfatal error.

The connection URL derives from the configured first-party API base and uses
its secure WebSocket form. Query parameters declare linear16 encoding, sample
rate 16,000, one channel, a 300 ms endpoint, a 1,000 ms utterance-end interval
and the normalized language. A gated newer recognizer may select its designated
model and repeated keyterm parameters. Authentication uses a current OAuth
token and participates in the common proxy/TLS boundary.

On open, send an initial keepalive and begin the 8-second keepalive interval.
Audio sends an owned byte copy so recorder buffer reuse cannot mutate queued
network data. Finalization is one state transition. It schedules `CloseStream`
after already queued audio, never sends it twice, and resolves on explicit
endpoint/utterance completion, close, already-closed state, no-data timeout or
safety timeout. If only interim text exists at finalization, promote it to the
final result.

The legacy recognizer may auto-finalize a completed transcript segment only
when neither prefix nor continuation evidence says the utterance continues.
The newer recognizer disables that heuristic. Upgrade rejection in the 4xx
range is fatal, except a bogus status that is actually a valid switching-
protocols response. Errors arriving after finalization begins are contained so
they cannot settle the attempt a second time.

- **OPT-VOI-013 — One finalization.** Concurrent release, close, timeout or error paths settle the finalization promise once.
- **OPT-VOI-014 — Silent audio.** No-data timeout is a valid empty transcription outcome, not a hung processing state.
- **OPT-VOI-015 — Cleanup.** Stop recorder, timers, keepalive and socket on disable, unmount, error or session shutdown.
- **OPT-VOI-019 — Owned audio bytes.** Network sends cannot observe later
  mutation of a recorder-owned buffer, and audio received after close is
  dropped rather than raising a second protocol failure.
- **OPT-VOI-020 — Ordered close.** Release queues one close-stream control
  after buffered audio; explicit completion, timeouts, socket close and errors
  race through one settlement gate.
- **OPT-VOI-021 — Interim promotion.** A finalizing stream with useful interim
  text but no later final segment returns that text once instead of discarding
  it or remaining in processing.

## Attempt ownership, retry, and focus mode

Starting capture synchronously enters `recording` before awaiting recorder or
socket setup, preventing a second key event from creating a duplicate session.
Audio begins as soon as the recorder is ready. Until the WebSocket opens,
chunks accumulate in an attempt-local buffer. On connection they flush in
ordered slices of approximately 32 KiB before live chunks continue.

Two monotonically changing identities guard callbacks: a recording-session
generation and a connection-attempt generation. Stop/cleanup invalidates both.
Every recorder, socket, retry-timer and transcript callback compares its
captured identity before changing prompt text or state, so a late callback from
an abandoned retry cannot affect the current recording.

An early connection failure before any transcript may retry once after 250 ms
unless classified fatal. Recorder capture continues and its buffered audio is
carried into the new connection. A distinct silent-drop recovery applies once
when the service returns no-data despite captured audio and an otherwise
established socket. Outside focus mode it waits 250 ms, reconnects, and replays
the bounded captured audio in approximately 32-KiB chunks. It cannot recurse
and does not run for focus recordings.

Release detection uses a short 200 ms gap after held-key repeats. A general
fallback ends a recording after 600 ms without renewed activation evidence;
modifier-combination activation has a 2,000 ms first-press allowance. These
timers belong to the session generation and are cancelled on finish.

Focus/non-hold mode is explicitly ended by terminal blur. It does not use the
held-key release timer. Interim or final transcript activity rearms a five-
second silence timer; silence ends capture. Finishing focus mode flushes the
current transcript immediately through the same finalization boundary. Finish
captures duration/audio/attempt metrics before asynchronous cleanup can clear
the counters.

- **OPT-VOI-022 — Immediate capture and ordered flush.** Recorder setup and
  capture do not wait for WebSocket readiness. Pre-open chunks remain ordered
  and are flushed before live audio in bounded slices.
- **OPT-VOI-023 — Generation ownership.** A callback may mutate state only
  when both its session and attempt identities still match. Cleanup invalidates
  identities before releasing resources.
- **OPT-VOI-024 — One early retry.** A nonfatal pre-transcript failure retries
  once after 250 ms while retaining captured audio; fatal or post-transcript
  failure does not use this path.
- **OPT-VOI-025 — One silent replay.** No-data with captured audio and a live
  connection may reconnect/replay once outside focus mode. Replayed chunks
  preserve capture order and cannot trigger another replay.
- **OPT-VOI-026 — Focus lifecycle.** Blur ends focus recording; transcript
  activity rearms five-second silence; held-key release timers do not govern
  focus mode.

## Language and keyterms

Configured dictation language is resolved by normalized language name, exact
code, then base code (for example a regional code falling back to its language
family). The supported public set contains the fixed 20 configured languages.
An unrecognized value falls back to English and drives the bounded warning
behavior described above. The normalized code is used consistently in the
stream URL and metrics.

Keyterms combine a stable coding vocabulary with current project evidence:
the project directory basename, words from the current branch, and word-like
fragments of recent file-name stems. Identifiers are split at camel-case,
snake-case, kebab-case and path boundaries. Admit fragments longer than two and
no longer than 20 characters, cap project-derived candidates at 50, deduplicate
while retaining stable priority, and cap the final transmitted list at 50.
Never send file contents, full paths or unlimited repository vocabulary as
keyterms.

- **OPT-VOI-027 — Language normalization.** Name, exact-code and base-code
  resolution produce one supported code; unknown input selects English with a
  bounded user notice.
- **OPT-VOI-028 — Bounded keyterms.** Keyterms contain the stable vocabulary
  plus sanitized project/branch/recent-file fragments, no content or full
  paths, and at most 50 stable unique entries.

## Terminal companion identity

Companion identity separates deterministic “bones” from persisted “soul”.

Persist only name, personality and hatch timestamp. Recompute rarity, species, eye, hat, shiny flag and five stats from a stable user identity plus a product salt on every read. OAuth account ID wins over local user ID; anonymous is the fallback.

- Rarity weights: common 60, uncommon 25, rare 10, epic 4, legendary 1.
- Rarity stat floors: common 5, uncommon 15, rare 25, epic 35, legendary 50.
- Select one peak stat, one different dump stat and scatter the rest. Clamp peak at 100 and dump at a minimum of 1.
- Common companions have no hat; noncommon companions select from the hat catalog.
- Shiny probability is 1 percent.
- Stats are DEBUGGING, PATIENCE, CHAOS, WISDOM and SNARK.
- Cache the deterministic roll for the current identity because prompt/render/observer paths query it frequently.

- **OPT-BUD-001 — Tamper resistance.** Persisted configuration cannot manufacture rarity/species/stats because recomputed bones overwrite stale stored bone fields.
- **OPT-BUD-002 — Migration tolerance.** Regenerating bones allows species catalog changes without invalidating stored soul data.
- **OPT-BUD-003 — Explicit model context.** Companion personality affects the model only through a deliberate typed companion-introduction attachment. Animation, name display and reactions are otherwise UI-only.

## Companion presentation

- Feature flag, hatched companion and unmuted preference are all required to render.
- Animate on a 500 ms tick.
- Speech reaction remains for 20 ticks, approximately 10 seconds, and fades during the final 6 ticks, approximately 3 seconds.
- Pet hearts animate for 2,500 ms.
- At terminal width below 100 columns, render a compact one-line face/name or quoted reaction. Truncate a narrow reaction to 24 display characters with an ellipsis.
- At width 100 or greater, render the full sprite. Reserve prompt columns for sprite/name padding and, in nonfullscreen speaking mode, the inline speech bubble.
- In fullscreen, render the bubble through a floating slot so it does not consume prompt width or become static scrollback.
- Focused footer selection highlights the name; muted state reserves no columns.
- A startup teaser, when product/date policy allows and no companion exists, uses a stable notification key and expires after 15,000 ms.
- Highlight occurrences of the exact `/buddy` command token in prompt input only when the feature exists.

- **OPT-BUD-004 — Layout parity.** Prompt wrapping subtracts exactly the columns reserved by the currently visible companion representation.
- **OPT-BUD-005 — Reaction replacement.** A new reaction resets age synchronously so no stale faded frame appears.
- **OPT-BUD-006 — Timer cleanup.** Animation, speech-clear and pet timers are removed on unmount or feature disappearance.
- **OPT-BUD-007 — Sprite catalog integrity.** Each supported species supplies
  three body frames of compatible dimensions. A hat supplies a blank first
  frame and may extend above the body only through the documented safe vertical
  shift; face/eye composition never changes terminal column accounting.
- **OPT-BUD-008 — Teaser boundary.** In the external profile, teaser
  eligibility is local April 1–7, 2026 and live availability begins in April
  2026. The notification uses one stable key, lasts 15 seconds and is absent
  when a companion already exists or the feature/profile is unavailable.
- **OPT-BUD-009 — Command-token highlight.** Highlight only `/buddy` followed
  by a word boundary and only while the companion feature exists. Similar
  prefixes and ordinary prose do not create a command affordance.

## External-build stubs

An internal-only optional hook may be replaced in external builds by a self-contained no-op with the same callable shape:

- before-query returns true so ordinary query dispatch proceeds
- turn-complete resolves without mutation
- render returns no presentation
- it imports no missing internal types or modules

- **OPT-STUB-001 — Behavioral neutrality.** A stub cannot consume input, add messages, block a query, change permissions, create tasks, or keep process handles alive.
- **OPT-STUB-002 — Signature parity.** Callers do not branch merely to satisfy the stub; enabled and absent builds share the same integration points.

## Failure behavior

- Voice ineligible: hide or return an explicit unavailable/authentication message; do not show a recording UI that cannot connect.
- Missing audio dependency: remain disabled and provide bounded installation guidance.
- Microphone denial: remain disabled and provide platform settings path.
- Socket/transcription error: stop processing, preserve prompt text outside the interim range, show a temporary error and return to idle.
- Companion data absent/muted: render nothing and reserve zero columns.
- Invalid persisted companion soul: fail closed to no companion or a migration path; never trust stored bone fields.
- Stub build: ordinary query lifecycle continues unchanged.

## Acceptance scenarios

- **OPT-VOI-A01 — Provider and enable transaction.** Authenticate with API key
  only and verify no recording UI. With first-party auth, deny microphone and
  verify the setting remains false, the real probe closes, and guidance is
  shown.
- **OPT-VOI-A02 — Bare-key activation.** Hold Space with repeat gaps under
  120 ms; verify warmup at two events and recording at five, with only leaked
  activation spaces removed. Type one Space normally and verify it remains.
- **OPT-VOI-A03 — Binding ownership.** Unbind the voice shortcut while a
  keybinding provider exists; verify there is no fallback Space activation.
  Activate a modifier binding and verify the 2,000 ms first-press allowance.
- **OPT-VOI-A04 — Backend matrix.** Exercise native success, Linux no-card,
  successful/failed 150 ms `arecord` probe, SoX fallback and native failure on
  Windows; verify the exact preference order and identical PCM shape.
- **OPT-VOI-A05 — Preopen audio.** Delay WebSocket open while capture emits
  more than 64 KiB; verify ordered ~32-KiB flush slices precede live audio and
  source-buffer mutation cannot alter sent bytes.
- **OPT-VOI-A06 — Release finalization.** Release while recording; race an
  endpoint, socket close, no-data timeout and safety timeout; verify one queued
  CloseStream, one final result, interim promotion and return to idle.
- **OPT-VOI-A07 — Early reconnect.** Fail the first connection before any
  transcript and succeed after 250 ms; verify one retry with continuous capture
  and no callback from attempt one can mutate attempt two.
- **OPT-VOI-A08 — Silent replay.** Return no-data after captured audio on an
  established nonfocus connection; verify one reconnect/replay in ordered
  chunks and no recursive replay. Repeat in focus mode and verify no replay.
- **OPT-VOI-A09 — Focus mode.** Start focus capture, rearm silence with interim
  and final text, then blur; verify immediate finalization, no held-key release
  behavior, metric snapshot and complete timer/recorder/socket cleanup.
- **OPT-VOI-A10 — Language and keyterms.** Resolve language by name, regional
  code and invalid input; verify stable code/fallback. Feed camel/snake/kebab
  branch and file stems and verify sanitized deduplicated keyterms capped at 50.
- **OPT-BUD-A01 — Deterministic identity.** Recompute a companion after editing
  stored rarity; verify deterministic bones win while name/personality persist.
- **OPT-BUD-A02 — Responsive reaction.** Resize from 120 to 80 columns during a
  reaction; verify prompt reservation and compact rendering update without
  transcript mutation, then verify fade and timer cleanup.
- **OPT-BUD-A03 — Explicit companion context.** Hatch a companion and construct
  model context repeatedly; verify one typed introduction containing current
  soul/personality, no duplicate attachment, and no animation/reaction state.
- **OPT-BUD-A04 — Catalog and teaser.** Render every species across its three
  frames with and without valid hats, then cross the external teaser date and
  `/buddy` token boundaries; verify safe dimensions, exact feature/date gate,
  one 15-second notification and word-boundary-only highlighting.
- **OPT-STUB-A01 — External neutrality.** Run the external stub; verify
  before-query allows dispatch, completion is a no-op, render is null and no
  handle or context mutation remains.

## Non-normative provenance

Evidence was specified from the reference voice eligibility and toggle command, voice integration and recording/transcription services, terminal voice indicators, companion deterministic generator/types/sprites/notifications, companion prompt attachment and an external-build internal-feature stub. Paths and implementation-language choices are non-normative.
