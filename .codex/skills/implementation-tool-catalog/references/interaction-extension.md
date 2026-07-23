# Interaction, skills, structured output, and optional tools

## `AskUserQuestion`

Accept 1–4 questions. Each has a unique prompt, a short header, 2–4 unique options with label and description, optional preview metadata, and `multiSelect` default false. Add an automatic free-form “Other” route at presentation time rather than requiring the model to provide it.

Validate the complete batch before displaying anything. The tool is concurrency-safe, read-only, deferred, and explicitly requires interaction. Disable it when a bridge, remote controller, or other channel owns question/permission round trips and cannot support this contract. Return a deterministic map from question identity/text to selected or free-form answers, plus supported annotations. Cancellation and dismissal are terminal results, not empty answers.

## `Skill`

Input names a discovered skill and supplies raw arguments. Strip one leading slash from the name, resolve canonical identity and source, then apply permission rules in this order:

1. exact or prefix deny;
2. exact or prefix allow;
3. safe declarative allowlist;
4. ask.

For inline context, expand the skill, attach its trusted allowed-tool/model directives and deliberate messages, and continue the current model loop. For fork context, create an isolated subagent and return its result; assistant-mode forks may schedule and later enqueue a hidden completion. Unknown, disabled, user-only, malformed, or unauthorized skills fail before expansion. Skill text remains untrusted user/extension context and cannot rewrite managed policy.

## `StructuredOutput`

Inject only for noninteractive/headless execution when the caller supplied a JSON Schema. Create a dynamic strict tool schema from that response contract, permit it without an extra side effect prompt, validate the value, and accept exactly one final invocation. A second call, invalid value, or conversational use is a protocol error. Return the structured value through the SDK/headless output adapter while keeping a paired tool result in the semantic transcript.

## `Config`

Input selects one supported setting and optionally a primitive value. Omitted value is a concurrency-safe read; a supplied value is a write and requires composed permission. Reject unknown keys, non-primitive values, managed settings, invalid enum/range values, and settings not writable in the current scope. Return old/effective/new value and source attribution. This specified tool is internal-profile-only; user-facing configuration may also exist as a slash command/UI.

## `SendUserMessage`

Input requires markdown `message`, optional file-path `attachments`, and status `normal|proactive`. Validate every attachment path before sending; resolve metadata including path, size, image status, and optional uploaded file identity. Capture an ISO `sentAt` at execution. Compatibility outputs may omit attachment and timestamp fields, so resume readers must tolerate their absence.

Canonical name is `SendUserMessage`; `Brief` is a legacy alias. Exposure requires build entitlement plus session opt-in, while assistant mode may imply opt-in. The development environment override may grant entitlement but must not accidentally make ordinary sessions proactive. The descriptor is concurrency-safe/read-only because delivery is the conversation output channel; transport failures remain visible.

## Optional and internal compatibility boundaries

The registry evidences the following gated descriptors without a complete portable schema in this contract: `Tungsten`, `SuggestBackgroundPR`, `WebBrowser`, `OverflowTest`, `CtxInspect`, `TerminalCapture`, `ListPeers`, `VerifyPlanExecution`, `Workflow`, `Monitor`, `SendUserFile`, `PushNotification`, `SubscribePR`, and `Snip`.

For each shipped gate:

- define a strict canonical descriptor and schema;
- state concurrency/read/destructive/open-world/interaction classifications;
- route permissions through the shared boundary;
- specify persistence, cancellation, recovery, and disabled behavior;
- add it to the stable registry matrix and profile tests.

Omitting the capability when its compile-time gate is absent is correct. Registering a name-only stub or inferring behavior from the name is not.

## `REPL`

The internal `REPL` capability wraps primitive file/search/shell/agent operations inside a virtual-machine boundary. When active, hide the corresponding direct descriptors to avoid two permission and presentation paths for the same operation. Preserve primitive validation, permission, result, and cancellation contracts inside the VM. The virtual terminal singleton makes this capability unavailable to ordinary async agents.

## Testing tool

`TestingPermission` accepts an empty strict object, is concurrency-safe/read-only, always returns an `ask` decision with “Run test?”, and after approval returns `TestingPermission executed successfully`. It exists only in the test environment and has no production registry footprint.

## Interaction acceptance cases

- **IX-A01:** An invalid fourth option in one question rejects the entire question batch before UI state changes.
- **IX-A02:** Dismissing a question returns a terminal cancellation result and unblocks sibling scheduling.
- **IX-A03:** A denied skill prefix wins over a broader allow prefix.
- **IX-A04:** Inline skill invocation deliberately changes model context; a UI-only loading message does not enter model context.
- **IX-A05:** Structured output that violates the caller schema fails without emitting a partially valid SDK final value.
- **IX-A06:** A resumed old `SendUserMessage` result without `sentAt` or attachments renders successfully.
- **IX-A07:** Activating REPL removes its direct primitive descriptors but primitive work inside the VM still receives permission checks and paired results.
