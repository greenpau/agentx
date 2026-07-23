# Remote bridge acceptance and provenance

Use these scenarios as executable conformance specifications. Each scenario names the contracts it exercises. Test implementations should inject faults at the indicated boundary rather than mocking away ordering, persistence, or credential behavior.

## Contents

- [Architecture and eligibility scenarios](#architecture-and-eligibility-scenarios)
- [Work, capacity, and lease scenarios](#work-capacity-and-lease-scenarios)
- [Ordering, replay, and transport scenarios](#ordering-replay-and-transport-scenarios)
- [Control and permission scenarios](#control-and-permission-scenarios)
- [Placement scenarios](#placement-scenarios)
- [Shutdown and recovery scenarios](#shutdown-and-recovery-scenarios)
- [Traceability checklist](#traceability-checklist)
- [Non-normative provenance](#non-normative-provenance)

## Architecture and eligibility scenarios

### RB-A01 — Architecture and transport are independent

**Contracts:** RB-ARCH-001, RB-ARCH-002, RB-ARCH-004, RB-CMP-001

Given an environment-backed bridge whose work secret enables `use_code_sessions`, when remote control starts, then it still registers/polls/acknowledges through the environment architecture while using CCR version 2; with that selector absent it uses Hybrid. Neither transport choice calls the environment-less session-creation sequence.

### RB-A02 — Executed selector outranks mode labels

**Contracts:** RB-ARCH-001, RB-ARCH-003, RB-OFF-001

Given an SDK control executable calls the shared bridge initializer with `tengu_bridge_repl_v2` enabled and perpetual false, when it starts, then it selects environment-less even if a mode label or nearby comment calls the path daemon-like or print-like. Given perpetual true, the same selector chooses environment-backed.

### RB-A03 — Gate-off produces no side effect

**Contracts:** RB-OFF-001, RB-SEC-001

Given the build includes bridge code but the runtime gate is off, when local interactive startup completes, then no environment, code session, worker registration, or remote credential is created and the local session remains usable.

### RB-A04 — Unsupported credential fails before registration

**Contracts:** RB-OFF-001, RB-SEC-001

Given an API-key or unsupported provider identity, when bridge eligibility is evaluated, then startup identifies the unsupported account class and performs no environment/session registration.

### RB-A05 — Minimum versions are architecture-specific

**Contracts:** RB-OFF-001, RB-ARCH-002

Given different legacy and environment-less minimum versions, when one passes and the other fails, then only the passing architecture is eligible; no shared boolean erases the distinction.

### RB-A06 — Local and remote session identities are not durably associated

**Contracts:** RB-ID-001, RB-ID-002, RB-RST-001

Given remote session `R` was previously viewed under local transcript UUID `L1`, when its URL/history is resumed in a new process, then the runtime may hydrate it under fresh local UUID `L2`; it does not infer `L1`. An environment-backed pointer, when present, restores only remote session ID, environment ID, and source—not cursor, epoch, deduplication state, pending controls, or a local-transcript mapping.

### RB-A07 — Generic SDK transport overrides do not choose bridge topology

**Contracts:** RB-ARCH-002, RB-ARCH-003, RB-ARCH-004

Given both SSE and Hybrid generic SDK overrides are enabled, when the SDK transport factory runs, then SSE wins; when neither is enabled it chooses WebSocket. Those outcomes do not change the independently selected environment-backed or environment-less bridge topology.

## Work, capacity, and lease scenarios

### RB-W01 — Invalid secret never owns work

**Contracts:** RB-WORK-001, RB-CAN-001

Given a polled work item whose secret version is unknown or whose ingress token is empty, when the worker validates it, then it neither acknowledges nor spawns and reports a redacted diagnostic.

### RB-W02 — Ack failure allows reclaim

**Contracts:** RB-WORK-001, RB-WORK-002

Given valid work whose acknowledgement request fails before confirmed success, when reclaim age elapses, then redelivery can occur and no previous local child exists.

### RB-W03 — Crash after ack is reconciled

**Contracts:** RB-WORK-001, RB-CAN-001, RB-END-001

Given acknowledgement succeeds and the process crashes before child launch, when the bridge recovers, then the durable owned-work evidence causes a failed/retried reconciliation rather than treating the item as unowned.

### RB-W04 — Duplicate poll has one owner

**Contracts:** RB-WORK-002, RB-ID-001

Given the same work ID is delivered twice, when the second delivery arrives while the first owner exists, then the bridge joins/reconciles that owner and never launches a second child.

### RB-W05 — Unsafe path ID fails locally

**Contracts:** RB-SEC-001, RB-WORK-001

Given a work or environment ID containing path separators or punctuation outside the safe identifier grammar, when an API operation is built, then the call fails before URL interpolation.

### RB-W06 — Capacity wake beats long poll interval

**Contracts:** RB-CAP-001, RB-SPN-001

Given the bridge is full and waiting on the ten-minute at-capacity interval, when one child terminates, then capacity wake causes an immediate authoritative capacity check and work poll.

### RB-W07 — At-capacity liveness remains enabled

**Contracts:** RB-LSE-002, RB-CAP-001

Given remote poll configuration sets both heartbeat and applicable at-capacity poll to zero, when configuration validates, then the whole configuration falls back to defaults rather than allowing invisible expiration.

### RB-W08 — Lease loss fences child output

**Contracts:** RB-LSE-002, RB-CAN-001

Given a running work item loses its service lease, when a later child event is produced, then remote writes are fenced and the child is interrupted/reconciled before a terminal state is reported.

### RB-W09 — Spawn modes preserve documented isolation

**Contracts:** RB-SPN-001

Given two concurrent accepted work items, when mode is `worktree`, then their directories are distinct; when mode is `same-dir`, they share the directory and conflict risk is surfaced; when mode is `single-session`, the bridge accepts no second item and exits with its child.

## Ordering, replay, and transport scenarios

### RB-T01 — History precedes concurrent live input

**Contracts:** RB-FLG-001, RB-ORD-001

Given initial history is uploading, when two live local messages arrive, then they remain queued and remote order is all selected history followed by the two live messages in arrival order.

### RB-T02 — Recoverable replacement retains gate

**Contracts:** RB-FLG-002, RB-REC-001

Given live writes are queued during same-process credential replacement, when the old transport closes and the new transport connects without process death, then the in-memory queue survives and drains once in FIFO order. The test does not claim crash durability.

### RB-T03 — Teardown accounts for dropped queue

**Contracts:** RB-FLG-002, RB-END-001

Given final teardown occurs with nonterminal events still queued, when the gate is dropped, then diagnostics record the exact discarded count and no completion claim relies on them.

### RB-T04 — Echo cache suppresses local reflection

**Contracts:** RB-DED-001, RB-RPL-001

Given a locally posted UUID returns on the inbound stream, when it is within the recent outbound ring, then it is acknowledged as required but not re-admitted as a user prompt.

### RB-T05 — Evicted UUID still relies on cursor

**Contracts:** RB-DED-001, RB-SEQ-001

Given more events than the dedup ring capacity and then same-process reconnect, when an old UUID is outside the ring, then the observed sequence/cursor supplies the transport replay boundary; the implementation does not assume UUID absence means new or describe the cursor as processed evidence.

### RB-T06 — SSE resumes from high-water mark

**Contracts:** RB-SEQ-001, RB-SEQ-002, RB-CCR-001

Given valid frame IDs 1 through 42 were observed before disconnect, when CCR transport is rebuilt in the same process, then its subscription starts from observed high-water 42. Delivery reports and transcript persistence do not alter that cursor.

### RB-T07 — Hybrid serializes stream and non-stream events

**Contracts:** RB-HYB-001, RB-ORD-001

Given buffered streaming text followed by a terminal non-stream result, when the result is enqueued inside the 100 ms batching window, then the text batch uploads before the result and only one POST is in flight.

### RB-T08 — Hybrid permanent 4xx does not loop

**Contracts:** RB-HYB-001, RB-CAN-001

Given an upload receives a non-429 permanent 4xx, when retry classification runs, then that batch terminally fails with visible accounting and later work follows only if transport/session validity remains.

### RB-T09 — Hybrid retry remains bounded and ordered

**Contracts:** RB-HYB-001

Given a network error then 429 then success, when retry runs, then the same head batch remains ahead of later batches, delays are capped, and no duplicate parallel POST is created.

### RB-T10 — Sleep gap resets reconnect budget

**Contracts:** RB-HYB-001

Given Hybrid has spent part of its reconnect budget and the host sleeps for more than 60 seconds, when it wakes, then the active retry budget resets rather than treating sleep time as ten minutes of failed attempts.

### RB-T11 — Stale epoch is fenced

**Contracts:** RB-OWN-001, RB-CCR-001, RB-V2-002

Given worker epoch 7 is replaced by epoch 8, when an epoch-7 operation receives conflict, then all epoch-7 writes stop and the worker implements or exits; it never retries with epoch 7.

### RB-T12 — Concurrent refresh triggers register once

**Contracts:** RB-V2-003, RB-REC-001

Given proactive expiry and SSE 401 occur together in one process, when both request recovery, then they await one recovery operation and `/bridge` is called once, producing one new epoch. No durable transaction is implied.

### RB-T13 — Late replacement cannot survive teardown

**Contracts:** RB-END-001, RB-REC-001

Given teardown starts while worker registration is awaiting a response, when a fresh transport is returned, then it is closed immediately and no handlers, timers, or queued sends attach.

### RB-T14 — Malformed SSE payload still advances observed cursor

**Contracts:** RB-SEQ-001, RB-SEQ-002, RB-FWD-001

Given the SSE frame ID is 43 and parses successfully but its payload is invalid JSON, when the frame is handled, then the in-memory cursor becomes 43 before parsing fails, the malformed event is ignored safely, and same-process reconnect resumes after observed ID 43. The test records that this is an observation high-water and a potential semantic-loss boundary, not a processed checkpoint.

### RB-T15 — Immediate processed report can hide crash loss

**Contracts:** RB-DEL-001, RB-DEL-002, RB-DEL-003, RB-CRS-001

Given the subprocess bridge callback returns for event 44 and the adapter immediately reports `received` and `processed`, when the child crashes before durable transcript admission, then the server may not replay 44. Diagnostics identify the adapter's acknowledged crash-loss interval; the implementation does not claim ordinary remote-I/O processing evidence existed.

### RB-T16 — Orderly close loses memory-resident queued writes

**Contracts:** RB-FLG-002, RB-REC-002, RB-CRS-001

Given Hybrid or CCR has queued writes when an orderly close begins, when the bounded drain expires in the still-live process, then the adapter reports the exact still-observable pending/loss count or failure class, settles its live waiters according to their transport contract, releases the memory, and never marks those writes durably delivered.

### RB-T17 — Abrupt process death leaves an unknown-loss class

**Contracts:** RB-REC-002, RB-CRS-001, RB-LSE-001, RB-CAN-001

Given queued writes and pending controls exist only in process memory, when the process dies without orderly close, then restart does not claim their exact count, invoke their erased callbacks, or mark them terminal. It reports the documented unknown-loss/orphan window, reconciles only independent transcript/session/remote evidence, and identifies a durable outbox/correlation journal as a safer divergence if stronger accounting is required.

## Control and permission scenarios

### RB-C01 — Unknown control receives error

**Contracts:** RB-CTL-001, RB-FWD-001

Given a well-framed unknown control subtype, when processed, then a correlated unsupported-control error is sent and the connection remains usable.

### RB-C02 — Malformed ordinary event is isolated

**Contracts:** RB-FWD-001

Given invalid JSON or an event without a string type, when received, then it is safely logged/ignored and does not terminate the query loop.

### RB-C03 — Outbound-only rejects mutation

**Contracts:** RB-CTL-001, RB-AUTH-001

Given an outbound-only mirror, when `interrupt` or `set_permission_mode` arrives, then it returns the outbound-only error and applies no mutation.

### RB-C04 — Managed policy denies remote mode change

**Contracts:** RB-CTL-001, RB-AUTH-001

Given a remote `set_permission_mode` request conflicts with managed policy, when evaluated, then a correlated error is returned and the prior effective mode remains.

### RB-C05 — Permission allow selects updated input once

**Contracts:** RB-PERM-001, RB-AUTH-001

Given the local user allows with an updated tool input, when the response returns in the exact compatibility profile, then the selected object reaches remote execution for that tool-use ID without another schema, semantic, permission, safety, classifier, sandbox, or prompt pass. The original request remains correlation/audit evidence. Exercise an invalid and a newly protected selected path to make the specified gap explicit; test any safer revalidation/reprompt profile separately as an intentional divergence.

### RB-C06 — Permission denial is not transport error

**Contracts:** RB-PERM-001

Given the user denies a valid tool request, when relayed, then the outer control interaction succeeds with inner behavior `deny`; the session connection remains healthy.

### RB-C07 — Remote cancellation wins race

**Contracts:** RB-CTL-001, RB-CAN-001

Given a pending permission dialog and a remote cancellation, when the local user clicks allow afterward, then cancellation is the sole terminal outcome and the late allow is discarded.

### RB-C08 — Recovery loss window is visible

**Contracts:** RB-LSE-001, RB-REC-001

Given a control request arrives during the declared credential-recovery drop window, when it is dropped, then diagnostics identify the correlation/loss class without secrets and no success is emitted.

### RB-C09 — Stale orphan permission response exposes the compatibility gap

**Contracts:** RB-CTL-002, RB-PERM-002, RB-ID-002

Given an old surface response has no exact pending request but carries a successful permission result whose camel-case `toolUseID` still matches an unresolved local transcript tool use, when the compatibility orphan handler runs, then it may accept the response without proving remote session, epoch, or surface generation. Verify that request-side snake-case `tool_use_id` is not treated as a response alias. A hardened implementation may instead reject the orphan using the documented generation fence, but records that outcome as an intentional safer divergence rather than reference behavior.

## Placement scenarios

### RB-P01 — Viewer 4003 is permanent

**Contracts:** RB-VIEW-003, RB-OFF-001

Given the viewer socket closes with 4003, when reconnect policy runs, then it performs no reconnect attempts and shows authorization failure.

### RB-P02 — Viewer echo still clears liveness timeout

**Contracts:** RB-VIEW-001, RB-DED-001

Given a response timeout is active and an echoed sent-user UUID arrives, when processed, then the timeout clears before the echo is suppressed.

### RB-P03 — Unknown viewer tool requires approval

**Contracts:** RB-VIEW-002, RB-AUTH-001

Given `can_use_tool` names a tool unknown to the viewer, when presented, then a permission-requiring safe stub is used and no default allow occurs.

### RB-P04 — Direct client uses exact framing

**Contracts:** RB-DIR-001, RB-ID-001

Given a direct session was created, when a user prompt is sent, then one newline-delimited user record with the specified nested message, null parent tool-use ID, and compatibility empty session field is written.

### RB-P05 — Direct socket close is not silent reconnect

**Contracts:** RB-DIR-002

Given the simple direct client socket closes, when no explicit resume is requested, then the client reaches a clear terminal/disconnected state and does not create another remote session.

### RB-P06 — SSH keeps credentials local

**Contracts:** RB-SSH-001, RB-SSH-002, RB-SEC-001

Given an SSH session starts, when inspecting remote environment, arguments, files, and transcript, then only the forwarded protected socket endpoint is present; reusable local credentials are absent.

### RB-P07 — SSH print mode fails early

**Contracts:** RB-SSH-001, RB-OFF-001

Given print/one-shot mode, when SSH surface is selected, then it is rejected before deployment or proxy creation with interactive-only guidance.

### RB-P08 — Teleport refuses tracked dirty state

**Contracts:** RB-TEL-001

Given tracked working-tree modifications, when teleport creation validates the repository, then it stops before remote session creation and explains the required clean state.

### RB-P09 — Teleport detects repository mismatch

**Contracts:** RB-TEL-002, RB-ID-001

Given remote metadata names a different normalized host/owner/repository, when resume runs, then it refuses checkout despite a matching branch name.

### RB-P10 — Partial paginated logs survive later failure

**Contracts:** RB-TEL-002, RB-RPL-001

Given two valid log pages and a network failure on page three, when recovery reports failure, then the first two pages remain available as explicitly partial evidence and are not duplicated on retry.

### RB-P11 — Dangling tool use is repaired before resume

**Contracts:** RB-TEL-002, RB-CORE-001

Given specified stream ends midway through a tool-use block, when transcript is implemented, then the dangling structure is filtered/repaired so the resumed model receives coherent tool-use/result pairing.

## Shutdown and recovery scenarios

### RB-S01 — Result precedes archive and close

**Contracts:** RB-END-001, RB-CAN-001

Given an environment-less session completes, when teardown runs, then terminal result is enqueued before archive, archive is bounded, and transport closes after the archive attempt.

### RB-S02 — Archive 401 retries once

**Contracts:** RB-END-001

Given archive returns 401, when teardown handles it, then OAuth refresh occurs once and archive retries once; further failure does not keep shutdown alive indefinitely.

### RB-S03 — Already archived is cleanup success

**Contracts:** RB-END-001

Given archive returns conflict indicating already archived, when teardown handles it, then cleanup continues as terminal success without creating a new session or repeating result.

### RB-S04 — Teardown is idempotent

**Contracts:** RB-END-001, RB-CAN-001

Given user exit, transport error, and process shutdown all call teardown concurrently, when they settle, then one terminal result/archive sequence occurs and every local resource is released once.

### RB-S05 — Specified cursor neither skips nor duplicates

**Contracts:** RB-RPL-001, RB-SEQ-001, RB-SEQ-003, RB-REC-002, RB-RST-001

Given a disconnect at each boundary between observation, dispatch, delivery reporting, and processing while the process remains live, when transport replacement runs, then it uses the in-memory observed cursor and documented deduplication behavior. Given process death, no client cursor or recovery transaction is restored: a fresh remote session starts from zero or an environment-backed restart follows only its explicit pointer/service state, and every known loss window is reported rather than described as durable replay.

## Traceability checklist

For every implementation, record evidence for:

- architecture selection (`RB-ARCH-*`);
- eligibility and disabled behavior (`RB-OFF-*`);
- identity, compatibility, and epoch fencing (`RB-ID-*`, `RB-CMP-*`, `RB-OWN-*`);
- process restart and memory-only crash boundaries (`RB-RST-*`, `RB-CRS-*`);
- work ownership, spawn, capacity, and leases (`RB-WORK-*`, `RB-SPN-*`, `RB-CAP-*`, `RB-LSE-*`);
- flush ordering, deduplication, delivery, and replay (`RB-FLG-*`, `RB-ORD-*`, `RB-DED-*`, `RB-DEL-*`, `RB-RPL-*`, `RB-SEQ-*`);
- Hybrid and CCR protocol behavior (`RB-HYB-*`, `RB-CCR-*`, `RB-TRN-*`);
- control, permissions, and local authority (`RB-CTL-*`, `RB-PERM-*`, `RB-AUTH-*`);
- credential recovery and teardown (`RB-REC-*`, `RB-V2-*`, `RB-END-*`);
- viewer, direct, SSH, teleport, and placement equivalence (`RB-VIEW-*`, `RB-DIR-*`, `RB-SSH-*`, `RB-TEL-*`, `RB-PLC-*`).

An implementation is incomplete if a requirement has only a happy-path unit test. Include at least one crash/reconnect, duplicate/replay, denial, and disabled-path test for every active surface.

## Non-normative provenance

The behavioral contracts above are normative. These source locations are audit provenance only and are not required by a standalone implementation:

- Bridge orchestration and lifecycle: `bridge/bridgeMain.ts`, `bridge/remoteBridgeCore.ts`, `bridge/sessionRunner.ts`, `bridge/replBridge.ts`, `bridge/initReplBridge.ts`, `bridge/replBridgeHandle.ts`.
- Environment/work APIs and configuration: `bridge/bridgeApi.ts`, `bridge/codeSessionApi.ts`, `bridge/bridgeConfig.ts`, `bridge/pollConfig.ts`, `bridge/pollConfigDefaults.ts`, `bridge/workSecret.ts`, `bridge/capacityWake.ts`.
- Identity, pointers, inbound content, and permissions: `bridge/sessionIdCompat.ts`, `bridge/bridgePointer.ts`, `bridge/inboundMessages.ts`, `bridge/inboundAttachments.ts`, `bridge/bridgePermissionCallbacks.ts`, `bridge/flushGate.ts`.
- Environment-less transport and authentication: `bridge/replBridgeTransport.ts`, `bridge/envLessBridgeConfig.ts`, `bridge/createSession.ts`, `bridge/trustedDevice.ts`, `bridge/jwtUtils.ts`.
- Shared event transports: `cli/transports/HybridTransport.ts`, `cli/transports/SSETransport.ts`, `cli/transports/ccrClient.ts`.
- Remote viewer: `remote/SessionsWebSocket.ts`, `remote/RemoteSessionManager.ts`, `remote/remotePermissionBridge.ts`, `remote/sdkMessageAdapter.ts`.
- Direct connect: `server/createDirectConnectSession.ts`, `server/directConnectManager.ts`, `server/types.ts`.
- SSH presentation adapter: `hooks/useSSHSession.ts` and the entrypoint/command routing that invokes it.
- Teleport: `utils/teleport.tsx`, `utils/teleport/api.ts`, `utils/teleport/environments.ts`, `utils/teleport/environmentSelection.ts`, `utils/teleport/gitBundle.ts`, `commands/teleport/index.js`.
- Architecture-selection audit note: a source comment describes daemon/print execution as environment-backed, while an SDK control executable can call the shared gate/perpetual selector and choose environment-less. This prose discrepancy is provenance only; `RB-ARCH-003` specifies the behavior of the executed selector.
