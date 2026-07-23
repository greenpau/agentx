# Glossary

| Term | Contract meaning |
| --- | --- |
| Agent | A model-driven worker with explicit identity, context, authority, lifecycle, and result ownership. |
| Application state | Mutable session coordination used by running operations and presentation; not the transcript. |
| Attachment | Deliberately represented non-text context with provenance and model/presentation visibility rules. |
| Background task | Durable or retrievable asynchronous execution with lifecycle, cancellation, output, and notification. |
| Bootstrap state | Facts required before session state exists, such as entrypoint, identity, cwd, and early feature latches. |
| Bridge | A transport and permission relay connecting a local semantic session or environment to an external controller. |
| Capability | Any validated side-effect or external-read boundary. A model-callable capability is exposed as a tool. |
| Command | User-invoked routing or local control, commonly slash-prefixed; it may create a prompt, local result, or UI flow. |
| Compaction | A projection transformation that reduces model context while retaining authoritative transcript evidence. |
| Context | The ordered, deliberate model-visible projection of instructions, messages, tools, memory, and environment facts. |
| Control request | A correlated structured-protocol request requiring a host response, such as permission or MCP transport. |
| Durable transcript | Append-safe semantic history plus significant metadata used for resume, fork, review, and recovery. |
| Elicitation | A provider request for user/host input, often authentication or a structured external choice. |
| Feature profile | Independent build, runtime, identity, policy, platform, configuration, and health decisions for availability. |
| Hook | An attributed lifecycle callback that can observe or influence a declared stage without exceeding its authority. |
| MCP | A protocol for discovering and calling external tools/resources/prompts or hosting local capabilities. |
| Message projection | A purpose-specific view of history for the model, transcript, SDK, remote transport, or UI. |
| Permission decision | Allow, ask, deny, cancel, or allow-with-updated-input outcome with source provenance and persistence scope. |
| Presentation state | Ephemeral focus, viewport, overlay, animation, and rendering data that is not model-visible by default. |
| Registry | A deterministic, attributed collection of capabilities or extensions merged from several sources. |
| Semantic event | Surface-neutral record representing an input, output, lifecycle transition, decision, or result. |
| Session | A resumable conversation and its owned runtime resources, history, registries, settings, and background work. |
| Surface | An adapter such as interactive terminal, one-shot text, structured stream, SDK, bridge, remote viewer, or MCP host. |
| Task | Explicit asynchronous work distinct from a model tool call, even when a tool creates or controls it. |
| Tool | A model-callable capability with schema, enablement, validation, permission, execution, progress, and result mapping. |
| Turn | One accepted user/external input and all recursive model/tool continuations until a terminal turn outcome. |
| Worktree | An isolated source-control checkout used as an execution-placement strategy without changing semantic tool meaning. |
