---
name: implementation-optional-experiences
description: Implement optional or build-specific user experiences without weakening the core runtime, including persistent assistant/viewer mode, paged remote assistant history, voice push-to-talk and transcription, terminal companions, and supported external-build stubs. Use when implementing, gating, disabling, or testing these experiences.
---

# Implementation Optional Experiences

## Objective

Add optional experiences as independently gated adapters over shared session and terminal contracts. Preserve a fully functional core build when any optional module is absent.

See the [optional-experience architecture diagram](assets/architecture.drawio) for gating, viewer, voice, companion, and stub boundaries.
See the [browser and computer-use lifecycle diagram](assets/browser-computer-use.drawio) for native-message framing, bridge selection, exclusive desktop ownership, abort, and restoration paths.
See the [derived-assistance lifecycle diagram](assets/derived-assistance-services.drawio) for maintained-document confinement, focus/turn-aware away summaries, advisor wire isolation, desktop import, and supported disabled profiles.
See the [voice capture and retry diagram](assets/voice-state-and-retry.drawio) for recorder-before-socket capture, generation ownership, early reconnect, silent replay, focus-mode finalization, and idempotent cleanup.

## Load references by task

- Read [assistant-viewer.md](references/assistant-viewer.md) to implement assistant activation, viewer-only remote sessions, paged history, scroll anchoring, and supported absence.
- Read [voice-companion-stubs.md](references/voice-companion-stubs.md) to implement voice eligibility and push-to-talk, transcription lifecycle, companion generation and rendering, and no-op external-build stubs.
- Read [browser-computer-use.md](references/browser-computer-use.md) to implement browser-extension automation, native-message framing, secure local transport, exclusive desktop control, native input and capture, abort handling, and turn-end restoration.
- Read [derived-assistance-services.md](references/derived-assistance-services.md) to implement maintained-document discovery and exact-path edit confinement, away-summary focus/turn scheduling, advisor gates and server-block projection, external feedback no-op behavior, Desktop MCP import, and supported build stubs.

## Core contracts

- **OPT-001 — Independent gates.** Evaluate build inclusion, runtime feature flag, authentication, account eligibility, policy, platform support, dependency availability, and user opt-in separately.
- **OPT-002 — Supported absence.** Excluded modules register no unusable UI or command, consume no input, and leave ordinary interactive/headless behavior unchanged.
- **OPT-003 — Shared boundaries.** Optional experiences reuse normalized messages, session adapters, settings, notifications, keybindings, terminal layout, and cleanup contracts.
- **OPT-004 — Lazy cost.** Delay optional network, audio, animation, and remote-history work until the feature is active or visibly needed.
- **OPT-005 — Explicit degradation.** Missing credentials, microphone permission, native dependencies, remote history, or internal-only implementation produces a bounded unavailable/no-op state rather than corrupting the session.
- **OPT-006 — Reversible UI.** Enabling or disabling an experience updates persistent preference and live presentation without requiring transcript mutation.

## Implementation workflow

1. Define the build-time module boundary and safe no-op replacement.
2. Evaluate visibility independently from command-time or use-time eligibility.
3. Load expensive services only after activation.
4. Adapt results into ordinary session messages, prompt text, notifications, or presentation state.
5. Register cleanup for streams, timers, remote subscriptions, and temporary terminal layout reservations.
6. Test enabled, disabled, unauthenticated, unsupported-platform, missing-dependency, and mid-session-disable cases.

## Boundary rules

- Optional experiences may decorate a prompt or presentation but cannot bypass permissions or directly mutate authoritative model responses.
- Assistant viewer mode displays a remotely running semantic session; it does not execute that session's tools locally.
- Voice inserts transcribed text through the normal prompt editor and submit path.
- Companion animation and reactions are presentation-only unless an explicit attachment is deliberately added to model context.

## Non-normative provenance

Evidence came from optional assistant/session-history, voice, companion, and external-stub areas plus their interactive integration points. Some internal modules are build-excluded from the specified source; only independently restated contracts are normative.
