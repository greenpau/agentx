---
name: implementation-task-runtime
description: Implement asynchronous work and its distinct durability classes, including local shells, local and remote agents, teammates, workflows, monitors, main-session backgrounding, output storage, polling, notifications, cancellation, crash windows, recovery hints, and garbage collection. Use when work must outlive a model call or be resumed, stopped, inspected, or reported later.
---

# Implementation Task Runtime

## Workflow

1. Assign every task a stable identity, type, owner, output location, and explicit lifecycle.
2. Register starts once per live state generation and preserve UI-held state when a running task is replaced in that same generation; treat process recovery as a separate contract.
3. Store output incrementally under bounded, symlink-safe rules and report deltas by byte offset.
4. Make concrete task implementations own terminal transitions and notification enqueue attempts while the framework owns polling and safe garbage collection.
5. Resolve kill-versus-completion races, release registered resources, and state separately what survives a model call, a session clear, and a process crash.

Read [the complete task runtime contract](references/task-runtime-contract.md) before implementing or auditing this domain. Use the [architecture diagram](assets/architecture.drawio) for the ordinary lifecycle and the [durability and crash-window diagram](assets/durability-crash-windows.drawio) to trace live state, output evidence, notification loss, remote-sidecar recovery, and possible duplication.

## Boundaries

Own asynchronous work after a tool launches it. Do not merge a task's lifecycle with the initiating tool result or with ephemeral UI progress. Live task state, output files, process-local notification queues, transcripts, and remote sidecars have different recovery guarantees. Remote transport placement may extend a concrete task, but must preserve the same task identity and terminal states.

## Completion check

- Preserve all `TR-*` contracts in the reference.
- Test start, output growth, background/foreground transitions, live replacement, completion, failure, kill races, missing output, output caps, same-generation notification suppression, crash loss/duplication windows, sidecar corruption, and terminal eviction.
- Confirm no task becomes anonymous and no stale asynchronous patch can resurrect terminal work.
