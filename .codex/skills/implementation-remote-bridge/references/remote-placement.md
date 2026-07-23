# Remote placement adapters

This reference covers remote viewing and control, direct-connect servers, SSH-hosted execution, and teleport/resume. Each adapter changes process placement or event transport while preserving the shared session, tool, permission, and transcript contracts.

## Contents

- [Contract map](#contract-map)
- [Placement invariants](#placement-invariants)
- [Remote viewer and controller](#remote-viewer-and-controller)
- [Direct-connect server](#direct-connect-server)
- [SSH-hosted session](#ssh-hosted-session)
- [Teleport and cross-machine resume](#teleport-and-cross-machine-resume)
- [Placement equivalence matrix](#placement-equivalence-matrix)
- [Failure and cleanup](#failure-and-cleanup)

## Contract map

| ID | Requirement |
| --- | --- |
| RB-VIEW-001 | A remote viewer projects session events and relays controls; it does not own the semantic query engine. |
| RB-VIEW-002 | Viewer permission requests are correlated, time-bounded, and evaluated through the viewer's local approval surface without fabricating tool knowledge. |
| RB-VIEW-003 | Viewer WebSocket reconnect behavior distinguishes permanent authorization closure from transient disconnection. |
| RB-DIR-001 | Direct connect creates a remote session through HTTP and speaks newline-delimited semantic/control messages over its returned WebSocket. |
| RB-DIR-002 | Direct-session persistence tracks logical/transcript identity and lifecycle without treating a socket as the session. |
| RB-SSH-001 | SSH moves runtime/tool execution to the remote host while keeping the terminal UI and credential authority local. |
| RB-SSH-002 | The reverse Unix-socket auth proxy isolates credentials from the remote filesystem and environment. |
| RB-TEL-001 | Teleport validates repository identity and source-control safety before creating or resuming remote work. |
| RB-TEL-002 | Teleport resume repairs incomplete streamed tool-use structures and records cross-machine continuation explicitly. |
| RB-PLC-001 | Placement adapters preserve permission meaning, cancellation, transcript attribution, and terminal result semantics. |

## Placement invariants

Before implementing an adapter, declare:

| Concern | Must remain authoritative at |
| --- | --- |
| Conversation/query state | The semantic session runtime that invokes the model |
| Transcript | Durable session persistence associated with the logical session |
| Tool execution | Host selected by the placement adapter |
| Permission policy | Composed local/managed policy for that execution host, with a correlated interactive relay if UI is elsewhere |
| Credentials | The narrowest trusted endpoint; proxy rather than copy when possible |
| Presentation | Local terminal, remote viewer, or SDK adapter; never the source of transcript truth |
| Cancellation | Correlated control path into the semantic runtime |
| Completion | Semantic runtime terminal state projected to every active adapter |

Messages arriving from any remote adapter pass through normal input normalization and queueing. Tool results use normal tool-result correlation. A remote disconnect is not itself a model cancellation unless the surface's documented ownership contract says loss of controller terminates the session.

## Remote viewer and controller

### Subscription protocol

Connect to:

```text
GET WebSocket /v1/sessions/ws/{session_id}/subscribe?organization_uuid={organization_uuid}
headers:
  Authorization: Bearer <credential>
  agentx-version: <supported version>
```

Validate dynamic identifiers before path interpolation. The subscriber accepts any event with a string `type` for forward compatibility, then narrows recognized SDK and control events. Its presentation state is:

```text
connecting -> connected -> closed
```

### Reconnect constants and behavior

| Constant | Value |
| --- | ---: |
| Ordinary reconnect delay | 2,000 ms |
| Ordinary maximum attempts | 5 |
| Ping interval | 30,000 ms |
| Forced reconnect delay | 500 ms |
| Authorization-expired close retries | 3 |

Rules:

- Close code 4003 is permanent; expose authorization/eligibility failure.
- Close code 4001 is retried at most three times with delay `2 seconds × attempt`, refreshing credentials if available.
- Other closures reconnect only if the connection had previously reached `connected`; initial setup errors are surfaced rather than looped forever.
- Ordinary reconnect uses the fixed two-second delay and five-attempt ceiling.
- A forced reconnect explicitly closes/reopens after 500 ms and resets only the state documented by the caller, not session identity or pending permission correlations.

### Viewer session manager

The viewer manager:

- sends user messages using the Sessions HTTP API;
- receives streamed SDK/session events and control requests by WebSocket;
- keeps a `request_id -> pending permission` map;
- sends permission responses and interrupt requests as control envelopes;
- counts remote task lifecycle from `task_started` and `task_notification` events;
- maintains a recent sent-user UUID cache of 50 to suppress server echoes;
- ignores/logs unknown SDK events;
- omits rendering a success result that merely closes a completed stream, but renders an error result.

Any inbound WebSocket event clears the current response timer before echo filtering because it proves remote liveness even if its semantic content is a duplicate. Normal response timeout is 60 seconds; compaction-related response timeout is 180 seconds.

Viewer-only mode disables mutating conveniences such as interrupt, reconnect-timeout behavior, and title update according to its read-only contract. It must not display enabled controls and then silently discard them.

### Viewer permissions

Recognized `can_use_tool` control requests become local viewer approval items. If the tool is unknown to the viewer, create a safe stub that still requires permission; never default unknown remote tools to allowed.

On allow, return the potentially updated input. On deny, return the denial message. On remote cancellation, remove/dismiss the pending item. Unsupported control requests receive a correlated error. Control-response acknowledgements from the service are not rendered as conversation messages.

Viewer permission lifecycle:

```text
remote request
  -> pending map entry
  -> local display
  -> allow | deny | remote cancel | timeout | disconnect reconciliation
  -> correlated response when channel permits
  -> remove pending entry
```

An interrupt is a new correlated control request, not an unframed socket signal.

## Direct-connect server

Direct connect is independently build-gated. It exposes a remote session host with a small HTTP setup API and a semantic WebSocket.

### Session creation

```text
POST {server_url}/sessions
headers:
  Authorization: Bearer <token>   # only when configured
body {
  cwd: string
  dangerously_skip_permissions?: boolean
}
response {
  session_id: string
  ws_url: absolute WebSocket URL
  work_dir?: string
}
```

Reject missing/invalid response fields before opening the socket. `dangerously_skip_permissions` is honored only if both local invocation and server policy permit it; a request flag is not itself authorization.

### WebSocket framing

The socket carries newline-delimited JSON records. The canonical user record is:

```text
{
  type: "user",
  message: {
    role: "user",
    content: <normalized content>
  },
  parent_tool_use_id: null,
  session_id: ""
}
```

The empty `session_id` field is a wire compatibility value; the connection already selects the session. Do not replace it with a different identity without negotiating a protocol change.

Permission control response:

```text
{
  type: "control_response",
  response: {
    subtype: "success",
    request_id: string,
    response: {
      behavior: "allow",
      updatedInput: object
    }
  }
}
```

or the same success envelope whose operation payload is
`{behavior:"deny", message:string}`. This direct viewer/controller uses the
required-input subset of [the canonical SDK permission wire catalog](../../implementation-headless-sdk/references/sdk-permission-wire.md); `{}` means retain the original tool input. An interrupt is a `control_request` with a newly generated request ID.

Only `can_use_tool` opens a permission prompt. Unknown control subtypes receive an error. Response/keepalive/cancel/streamlined/post-turn-summary records are protocol plumbing and are filtered from conversation rendering.

The simple direct client has no implicit reconnect. Socket close terminates that client adapter and returns the application to a clear terminal/disconnected state; a higher-level resume must explicitly reopen by persisted session identity.

### Server lifecycle and persistence

```text
starting -> running -> detached -> running
running|detached -> stopping -> stopped
starting --failure--> stopped with error
```

Persisted session index entry:

```text
{
  sessionId: string
  transcriptSessionId: string
  cwd: string
  permissionMode?: string
  createdAt: timestamp
  lastActiveAt: timestamp
}
```

The index key is stable across detach/resume. Never use current process ID or socket identity as the persisted key.

Server configuration includes TCP host/port or Unix-socket placement, optional auth token, idle timeout (`0` means never), maximum sessions, and workspace boundary. Validate requested working directories inside policy/workspace boundaries before starting a session.

## SSH-hosted session

SSH mode is independently build-gated and interactive-only. Reject one-shot/print operation rather than changing output semantics.

### Invocation contract

```text
ssh <host> [directory]
```

Recognize these options wherever the command parser permits options:

- `--local` to exercise the local server/auth-proxy path without SSH deployment;
- `--dangerously-skip-permissions` subject to policy;
- `--permission-mode <mode>`;
- `-c` or `--continue`;
- `--resume <session-uuid>`;
- `--model <model>`.

### Startup sequence

1. Parse and validate host, optional directory, continuation/resume, model, and permission flags.
2. Probe the remote host/platform and verify interactive support.
3. Deploy or locate a compatible remote runtime binary.
4. Create a local authentication proxy listening on a protected Unix socket.
5. Open SSH with reverse Unix-socket forwarding from the remote runtime to that local proxy.
6. Start the remote semantic runtime with remote working directory and forwarded identity/session arguments.
7. Attach the local terminal presentation to remote structured session events.
8. Route remote permission prompts to the local approval UI and return structured decisions.

`--local` skips SSH and binary deployment but still exercises the auth-proxy/socket and remote-adapter protocol.

### Credential isolation

The remote runtime receives only a socket endpoint such as `AGENTX_UNIX_SOCKET`. It must not receive reusable account credentials or local proxy settings in its environment, command line, transcript, repository, or diagnostic output. The local proxy authenticates upstream on its behalf and constrains which protocol destinations/operations it relays.

Remote filesystem and tools operate on the remote host. The local UI does not initially assemble local tools for that session. Permission decisions still combine managed policy, remote-path/tool analysis, declared mode, and local user response.

### Reconnect and termination

Treat SSH process exit, socket disconnect, and semantic session completion as separate signals. Reconnect is bounded and uses the stable remote/transcript session identity; it must not launch a second semantic runtime accidentally. On permanent SSH failure:

1. stop accepting prompts;
2. terminally resolve pending local permission controls;
3. close forwarding and auth-proxy resources;
4. leave recoverable session identity for explicit resume when possible;
5. restore local terminal state.

## Teleport and cross-machine resume

Teleport creates or resumes a remote-control session from a local repository while preserving repository identity and transcript continuity.

### Preconditions for creation

1. Require eligible first-party OAuth and organization identity.
2. Resolve and validate the selected remote environment.
3. Verify source-control repository identity and supported host/remote access, or prepare the explicit bundle-transfer path.
4. Require no tracked working-tree changes. Untracked files are not treated as tracked dirty state, but they are not silently transferred unless the transfer contract says so.
5. Resolve branch and remote revision needed to reproduce the work.
6. Create the remote session and transfer repository/task context without credentials or local-only paths.

### Resume sequence

```text
validating
  -> fetching_logs
  -> fetching_branch
  -> checking_out
  -> done
```

Steps:

1. Fetch remote session metadata and identify repository host, owner, repository, branch, and transcript/session identity.
2. Normalize repository URLs, including equivalent protocol spellings and ports, then require the current checkout to match host/owner/repository.
3. Fetch the required remote branch/revision.
4. Check out the branch and configure its upstream safely.
5. Fetch logs/events page by page. If a later page fails, preserve already validated earlier pages and surface partial recovery rather than erasing them.
6. Fall back to the documented session-ingress event source only when the preferred version-2 log endpoint is unavailable, not for schema corruption.
7. Filter or repair incomplete streamed `tool_use` structures so every admitted tool request has coherent content/result pairing.
8. Append an explicit system/user continuation notice that the session moved machines and filesystem/process state may have changed.
9. Resume through the shared query engine with implemented transcript attribution.

Do not treat source checkout as proof that uncommitted files, running processes, credentials, ports, or external service state moved with the session.

## Placement equivalence matrix

| Semantic capability | Local | Bridge | Direct | SSH | Teleport/resume |
| --- | --- | --- | --- | --- | --- |
| User input normalization | Shared | Shared after inbound normalization | Shared after NDJSON decode | Shared remotely | Shared after recovery |
| Query loop | Local core | Local/child core | Remote core | Remote core | Destination core |
| Tool schema/result pairing | Shared | Shared | Shared | Shared | Specified/shared |
| Permission composition | Local | Local authority via control relay | Server policy plus client relay | Remote analysis plus local relay | Destination policy with restored session mode |
| Transcript authority | Local durable store | Owning session store | Server durable store, indexed locally | Remote session store with local identity link | Implemented destination store with provenance |
| Cancellation | Local abort | Correlated interrupt/control | Correlated control | Remote control over session adapter | Destination session cancellation |
| Disconnect meaning | Presentation-specific | Reconnect/recovery; not automatically completion | Simple adapter terminal unless explicit resume | Bounded reconnect/explicit resume | Retrieval failure with partial evidence |
| Terminal result | Shared result | Projected before archive | Streamed from server | Streamed remotely | Produced after resumed query |

## Failure and cleanup

| Condition | Required behavior |
| --- | --- |
| Viewer unknown event | Ignore/log; preserve socket |
| Viewer 4003 close | Stop reconnect and report permanent authorization failure |
| Viewer permission cancellation/disconnect, or a separately host-imposed timeout | Remove or reconcile the pending request and never infer allow; the specified bridge itself supplies no local permission deadline |
| Invalid direct create response | Do not open a socket; report setup error |
| Direct socket closes | Stop simple adapter clearly; retain explicit resume identity if supported |
| Requested direct cwd outside workspace/policy | Deny before session launch |
| SSH build absent or print mode | Reject with supported-mode guidance |
| SSH probe/deploy failure | Tear down partial forwarding/proxy and restore terminal |
| SSH auth proxy loss | Stop/fence upstream requests; never fall back to copying credentials remotely |
| Teleport tracked dirty tree | Refuse creation until user resolves it |
| Teleport repository mismatch | Refuse checkout/resume; report expected and actual normalized identities |
| Later log page fails | Retain earlier validated pages and mark recovery partial |
| Incomplete remote tool-use stream | Repair/filter to coherent transcript; never feed dangling tool use to model |
| Any adapter shuts down | Resolve pending controls, close readers/writers/processes, release locks/sockets, flush durable transcript, restore presentation resources |
