# AgentX User Guide

AgentX is a local software-engineering agent. It sends your requests and selected context to the configured model, can inspect and modify the current repository through controlled tools, and stores resumable session history locally. You can use it from a terminal or through the AgentX extension for Visual Studio Code.

## Before you begin

AgentX requires:

- Go 1.26 or newer to compile and install the binary.
- Access to every Azure OpenAI deployment you configure.
- A private `auth.json` in the AgentX application home.
- VS Code 1.95 or newer for the editor extension.

## Configure authentication

AgentX stores application-owned state in `~/.agentx/` by default. Set the
public `AGENTX_HOME` environment variable to an absolute path when the whole
application home must live elsewhere. This is the only supported application-
home override. Blank values are treated as unset; a nonblank value must be an
absolute, non-root path, and an invalid override fails rather than selecting
the default. AgentX selects the application-home path once, before it inspects
command-line arguments. Credential loading pins that home while reading
`auth.json`; session and project-memory paths are derived from the same frozen
selection. Existing user plugins, output styles, and MCP configuration retain
their operating-system user-configuration root; `AGENTX_HOME` does not relocate
those extension sources.
Regardless of its basename or location, the selected application home and all
of its descendants remain protected control data. Placing `AGENTX_HOME` inside
a workspace does not make credentials, sessions, transcripts, task state,
tool results, or project memory readable or editable through broad workspace
permissions or bypass mode.
Before and after permission evaluation, before a tool can execute, AgentX
rechecks the frozen home and `sessions/` directory identities. If either
pathname was renamed or replaced—or its private mode changed on a supported
POSIX platform—AgentX denies pending and future tool use and asks you to
restart it. This check detects a sustained identity change; it is not an
atomic lock over every later descendant filesystem operation.

Every invocation creates the application home and its `sessions/` child before
command-line parsing. On supported POSIX platforms, AgentX establishes and
rechecks owner-only permissions and requires and rechecks current-user
ownership. On Windows it enforces direct-directory and stable-identity checks,
but cannot yet establish or prove owner-only DACL protection. Before full
command-line parsing, `auth.json` must exist even for malformed input,
`--help`, `--version`, and `--mcp-server`.
Informational and standalone MCP invocations check that the file exists but do
not construct a model client. A model-backed invocation and the standalone
provider-discovery operation strictly validate the complete file before any
extension discovery, persistent session creation, or network request.

The default layout begins as:

```text
~/.agentx/
├── auth.json
└── sessions/
```

When `AGENTX_HOME` selects another location, substitute that effective path for
`~/.agentx` in the examples below.

Create `~/.agentx/auth.json` with this exact versioned provider-profile shape:

```json
{
  "version": 2,
  "providers": [
    {
      "id": "sol-5.6",
      "type": "azure_openai",
      "default": true,
      "capabilities": {
        "reasoning": {
          "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "high"
        }
      },
      "azure_openai": {
        "endpoint": "https://your-resource.openai.azure.com",
        "model": "gpt-5.6-sol",
        "deployment": "gpt-5.6-sol",
        "api_key": "replace-with-your-secret",
        "api_version": "preview"
      }
    }
  ]
}
```

The complete file must be valid UTF-8 JSON no larger than 64 KiB. `version`
must be the integer `2`, and `providers` must be a nonempty array containing at
most 32 entries. The old version-1 `provider`/`azure_openai` object is no longer
supported and is not migrated automatically; rewrite it as a version-2
`providers` array before upgrading AgentX.

Each provider entry requires exactly `id`, `type`, `capabilities`, and
`azure_openai`; `default` is the only optional member. IDs are unique, exact
startup selectors of 1–64 ASCII letters, digits, dots, underscores, or
hyphens, and must start with a letter or digit. The only supported `type` is
currently `"azure_openai"`. `capabilities` contains exactly `reasoning`, whose
nonempty `efforts` array contains unique values drawn from `none`, `low`,
`medium`, `high`, `xhigh`, and `max`. Its `default_effort` must be one of those
declared values. The `azure_openai` object contains exactly the five shown
string fields. `endpoint` is the Azure OpenAI resource URL, `model` is AgentX's
logical model identity, `deployment` is the Azure deployment sent to the
Responses API, `api_key` is the subscription key, and `api_version` is the
Azure API version selector. Endpoint, model, deployment, and API key must be
nonempty; an empty API-version string selects the default v1 route without a
query. Exact nonempty selectors `v1` and `preview` retain the v1 route and are
sent literally in the query; every other nonempty selector uses the versioned
route and is likewise sent literally. AgentX never treats `preview` as a
request to discover or substitute the provider's latest preview. Unknown or
duplicate fields at any depth, trailing JSON, unsupported versions or provider
types, wrong types, repeated IDs or efforts, and missing required values are
rejected.

The reasoning capability fields are operator declarations, not results of an
Azure capability probe. AgentX treats the declared ordered subset as
authoritative for local validation and discovery, but it does not contact each
deployment to prove support. Configure only values the corresponding endpoint
actually accepts and update the profile when that deployment changes.

A single provider is always selected when no selector is supplied; it does not
need `default`. With several providers, either set `"default": true` on
exactly one entry or invoke AgentX with an exact `--provider ID`. With no
selector and no configured default, startup fails and explains how to add the
`"default": true` field to one provider. More than one configured default is
invalid. An explicit provider selector overrides the configured default, but a
request failure never causes AgentX to switch to another entry automatically.

Query the registry before selecting a provider:

```sh
agentx --list-providers
agentx --list-providers --output-format json
```

The useful standalone grammar is
`agentx --list-providers [--output-format text|json]`. The required selector
and optional output option may occur in either order and each may occur only
once. A final bare `--` option terminator is accepted, but discovery accepts no
prompt or other option, including `--help` or `--version`. The text form is
intended for people. The JSON form emits one newline-terminated object for
clients such as a VS Code extension host. Discovery strictly validates every
configured profile but deliberately selects none, so a valid registry with
several providers and no default can still be enumerated. It does not inspect
a workspace, create a session or transcript, load extensions, or contact a
model endpoint.

The auth document's required `version: 2` and the discovery response's
`version: 1` identify different schemas; discovery version 1 is not the old,
unsupported auth-file version 1. The public JSON descriptor uses camelCase
compatibility fields and has this exact shape (object member order is not
significant):

```json
{
  "version": 1,
  "providers": [
    {
      "value": "sol-5-6",
      "id": "sol-5-6",
      "providerType": "azure_openai",
      "model": "gpt-5.6-sol",
      "displayName": "sol-5-6 (gpt-5.6-sol)",
      "description": "Deployment-backed model endpoint configured by AgentX-home auth.json",
      "default": true,
      "selected": false,
      "supportsEffort": true,
      "supportedReasoningEfforts": ["none", "low", "medium", "high", "xhigh", "max"],
      "defaultReasoningEffort": "high",
      "reasoning": {
        "supported": true,
        "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
        "defaultEffort": "high"
      }
    }
  ]
}
```

Each item describes a configured endpoint profile; it is not a literal URL.
The capability arrays repeat the operator declaration from `auth.json`, not
remote introspection. The response never exposes the Azure URL, deployment,
API-version selector, API key, route binding, credential path, or headers.
Automation must buffer stdout and accept it only when the process exits `0`
and the top-level `version` is integer `1`. Discard buffered stdout on every
nonzero exit; in particular, do not try to parse a prefix retained after an
output-writer failure.

For example, this file selects Sol normally while retaining a separately
addressable Terra endpoint:

```json
{
  "version": 2,
  "providers": [
    {
      "id": "sol-5-6",
      "type": "azure_openai",
      "default": true,
      "capabilities": {
        "reasoning": {
          "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
          "default_effort": "high"
        }
      },
      "azure_openai": {
        "endpoint": "https://sol-resource.openai.azure.com",
        "model": "gpt-5.6-sol",
        "deployment": "sol-5-6",
        "api_key": "replace-with-sol-secret",
        "api_version": "preview"
      }
    },
    {
      "id": "terra-5-6",
      "type": "azure_openai",
      "capabilities": {
        "reasoning": {
          "efforts": ["low", "medium", "high", "xhigh"],
          "default_effort": "medium"
        }
      },
      "azure_openai": {
        "endpoint": "https://terra-resource.openai.azure.com",
        "model": "gpt-5.6-terra",
        "deployment": "terra-5-6",
        "api_key": "replace-with-terra-secret",
        "api_version": "preview"
      }
    }
  ]
}
```

Use only effort values actually supported by each deployment. The examples
describe the schema, not a capability probe or an Azure deployment guarantee.

The endpoint must be an absolute HTTPS URL without user information, a query,
or a fragment. Model and deployment values are each limited to 256 UTF-8
bytes. The API key is limited to 16 KiB and cannot contain whitespace or unsafe
control/formatting characters. A nonempty API version is limited to 128 UTF-8
bytes. Model, deployment, and API-version values likewise reject unsafe
control/formatting characters. Endpoint, model, deployment, and API-version
values also reject surrounding whitespace instead of silently changing the
provider-visible routing identity.

`auth.json` is the sole model credential source. Keep it outside repositories,
never replace the placeholders with secrets in committed examples, and do not
paste its contents into prompts or diagnostics. AgentX places every configured
API key in its output-redaction set, but sends only the selected provider's key
to its configured endpoint. Rotate a key if it is ever exposed or committed.

On Unix-like systems, make the directories owner-only and the file readable
and writable only by its owner:

```sh
mkdir -p ~/.agentx/sessions
chmod 700 ~/.agentx ~/.agentx/sessions
chmod 600 ~/.agentx/auth.json
```

On Windows, restrict the application home and `auth.json` to the current user.
The current standalone Go profile does not yet implement the native owner/DACL
inspection required to prove that protection, so it fails closed before
reading `auth.json`; model-backed startup is unavailable on Windows until that
adapter exists.

If `auth.json` is missing—or the selected child is a directory or symbolic
link rather than a direct regular file—AgentX exits without starting a model
or persistent session. The error includes the expected path, the placeholder
JSON shape above, and this stable guide link:
<https://github.com/greenpau/agentx/blob/main/USER_GUIDE.md>.

## Install the AgentX binary

Compile and install the latest published source with Go:

```sh
go install github.com/greenpau/agentx@latest
```

To install the exact source in a checked-out repository, run this from its root:

```sh
go install .
```

`go install` compiles AgentX and writes the executable to `GOBIN` when configured. Otherwise, it uses `$(go env GOPATH)/bin` on Unix-like systems and `%USERPROFILE%\go\bin` on Windows. Add that directory to `PATH`, then verify the installation:

```sh
agentx --version
agentx --help
```

Re-run `go install .` after updating a local checkout to replace the installed binary with the newly compiled version.

## Use AgentX in a terminal

Change to the repository you want AgentX to work in, then start an interactive session:

```sh
agentx
```

Enter a request at the prompt, for example:

```text
Explain how configuration is loaded and identify the relevant tests.
```

AgentX streams the response and shows tool activity. If a requested operation needs approval, review the exact operation and choose whether to allow or deny it.

### Enable repository instructions and skills

Workspace-defined behavior is disabled unless you explicitly trust the workspace:

```sh
agentx --trust-workspace
```

Trust enables the repository's `AGENTS.md`, its root `.codex/skills` hierarchy, and project `.agentx` plugins, hooks, output styles, and MCP configuration. AgentX discovers skills only from the active repository's root `.codex/skills`; it does not load user-global, plugin-provided, remote, bundled, nested-repository, or additional-directory skills.

Only trust repositories whose configuration and executable extension files you have reviewed.

### Run a one-shot request

Use `--print` for scripts or a single noninteractive request:

```sh
agentx --print "summarize the repository architecture"
agentx --print --trust-workspace "review the current changes"
```

Choose an output format when another program will consume the result:

```sh
agentx --print --output-format text "explain this project"
agentx --print --output-format json "summarize test coverage"
agentx --print --input-format stream-json --output-format stream-json
```

Structured stdout contains protocol records only; diagnostics are written separately to stderr.
Cost fields are `null` when the configured deployment has no authoritative price;
numeric `0` is reserved for a known zero cost.

### Attach images and PDFs

Use repeatable `--attachment PATH` arguments for explicit caller-selected
files. Prompt text is optional, order follows the attachment arguments, and
the complete set is admitted atomically:

```sh
agentx --print --attachment screenshot.png "explain the error shown here"
agentx --print --attachment before.jpg --attachment after.png \
  "compare these images in order"
agentx --print --attachment design.pdf --attachment screenshot.png \
  "check the implementation against the document"
agentx --print --attachment report.pdf
```

`--attachment` selects headless execution for an attachment-only invocation.
It cannot be combined with the standalone MCP server or provider-free session
management modes. A slash/local-command prompt with attachments rejects
locally; attachments are neither left pending nor accidentally routed through
the command.

The first supported media and limits are:

| Kind | Exact MIME | Validation/transform | Limits |
| --- | --- | --- | --- |
| Image | `image/png` | magic signature, full decode, re-encode, metadata removed; no resizing | 20 MiB, 8,192 pixels per dimension, 20,000,000 pixels |
| Image | `image/jpeg` | magic signature, full decode, JPEG quality 90 re-encode, metadata removed; no resizing | 20 MiB, 8,192 pixels per dimension, 20,000,000 pixels |
| Document | `application/pdf` | magic plus conservative, decoded-name, classic-xref and page-tree validation; no execution, OCR, or conversion | 20 MiB, 100 pages |

One message may contain at most 8 attachments and 40 MiB of decoded media.
The session may retain at most 100,000 durable committed manifests and 512 MiB
of unique committed blobs. Independently, the in-process terminal
upload-attempt ledger is capped at 100,000 accepted upload lifecycle IDs. A
provider request is additionally limited to 100 retained media items, 40 MiB
decoded media, 55,927,120 encoded media bytes, and 67,108,864 bytes of final
JSON. Display names are limited to 255 bytes and MIME values to 64 bytes.

AgentX accepts only explicit paths. It resolves and opens each file as a
regular, single-link snapshot; symbolic links, hard links, directories, empty
files, unreadable files, replacement, truncation, growth, identity churn,
malformed content, claimed-MIME/magic mismatch, and every exceeded bound fail
before admission. File extensions alone do not establish MIME. The original
path is short-lived import input and is never sent to the model or persisted.

PDF input is intentionally narrower than arbitrary PDF 1.x/2.0 syntax. AgentX
requires one complete classic cross-reference table whose in-use offsets match
the declared indirect objects, a real catalog and internally consistent
parented page tree, direct bounded stream lengths, and an exact page count.
It decodes PDF `#xx` name escapes before policy checks and treats comments and
strings as inert rather than structural evidence. It rejects encryption,
JavaScript and action/navigation/launch/URI constructs, annotations, forms and
XFA, embedded/file-spec/associated-file/collection content, rich media,
object streams, xref streams, and incremental `/Prev` updates. Accepted PDF
bytes remain byte-identical; AgentX does not execute or decompress stream
content, render, OCR, convert, or claim to sanitize arbitrary PDF semantics.

The current provider qualification is Azure/OpenAI Responses with logical
model exactly `gpt-5.6-sol` and `api_version` exactly empty, `v1`, or
`preview`. Other providers/models/selectors are text-only and media fails
before network I/O. Do not infer media support from an Azure provider type or
declared reasoning capabilities. Configured non-sol endpoints remain text-only
unless their exact profile is separately qualified. AgentX maps qualified
images to Responses `input_image` data URLs and PDFs to `input_file` data URLs,
following the official
[Azure Responses API input
schema](https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/responses).
Loopback tests verify exact JSON construction. Azure deployment modality
eligibility is not introspected. One current-worktree profile passed
representative installed-runtime PNG, JPEG, conservative two-page PDF,
mixed-order, stream, resume, fork, compaction, and privacy checks; the
[sanitized evidence](.codex/skills/implementation-conformance-audit/references/native-attachment-production-qualification.md)
does not qualify another artifact, deployment, selector, provider, account, or
platform. Repeat `MOD-A14B` for every release profile that claims
real-provider attachments.

Remote quarantine is deliberately evidence-bound. AgentX classifies a failure
only when a media-bearing request receives HTTP 413/415; code
`media_rejected`, `unsupported_media`, `invalid_image`, `invalid_image_url`,
`invalid_file`, `invalid_file_data`, `image_too_large`, or `file_too_large`;
or an exact/suffix-qualified `input_image`, `input_file`, `image_url`, or
`file_data` parameter. An ordinary HTTP 400, terminal SSE failure, or message
prose alone does not quarantine a valid attachment. Every provider-owned
diagnostic and correlation field from a media-bearing failure is replaced with
a fixed runtime message before output, logging, retry observation, or
persistence, preventing wrapped request base64 from becoming diagnostic
content.

#### Upload attachments over stream JSON

A stream-JSON client must wait for `system/init` or a successful fieldless
`initialize` response and inspect `input_capabilities.attachments`. Absence
means text-only; do not infer support from the AgentX version. The qualified
capability is:

```json
{
  "protocol_version": 1,
  "sources": [
    {"source":"file_path","scope":"initial_cli"},
    {"source":"stream_json_v1","scope":"per_turn"}
  ],
  "media_types": [
    {"kind":"image","mime_type":"image/png","max_bytes":20971520,"max_dimension":8192,"max_pixels":20000000,"transform_policy":"decode_reencode_strip_metadata_reject_oversize_no_resize"},
    {"kind":"image","mime_type":"image/jpeg","max_bytes":20971520,"max_dimension":8192,"max_pixels":20000000,"transform_policy":"decode_reencode_strip_metadata_reject_oversize_no_resize"},
    {"kind":"document","mime_type":"application/pdf","max_bytes":20971520,"max_pages":100,"transform_policy":"validate_structure_no_execute_no_ocr_no_conversion"}
  ],
  "limits": {
    "max_attachments_per_message": 8,
    "max_concurrent_uploads": 8,
    "max_uploads_per_session": 100000,
    "max_item_bytes": 20971520,
    "max_aggregate_bytes": 41943040,
    "max_storage_bytes": 536870912,
    "max_model_request_media_bytes": 41943040,
    "max_chunk_decoded_bytes": 262144,
    "max_chunk_encoded_bytes": 349528,
    "max_display_name_bytes": 255,
    "max_mime_type_bytes": 64,
    "max_image_dimension": 8192,
    "max_image_pixels": 20000000,
    "max_pdf_pages": 100,
    "upload_timeout_ms": 120000
  },
  "provider_limits": {
    "max_request_items": 100,
    "max_encoded_media_bytes": 55927120,
    "max_request_bytes": 67108864,
    "max_ndjson_record_bytes": 8388608
  }
}
```

`file_path` is advertised only for the initial CLI prompt;
`stream_json_v1` is the per-turn structured upload route. The
`max_uploads_per_session` value aligns two independent ceilings in protocol
version 1: at most 100,000 durable committed manifests in the session store,
including selected-path and committed stream imports, and at most 100,000
terminal accepted upload lifecycle IDs in the current in-process ledger.
Reaching one ceiling does not consume or expand the other, and neither expands
the 512 MiB unique-blob storage limit.

Choose one canonical RFC 4122 version 1–5 prompt UUID and keep it through
import, queueing, transcript admission, and terminal result. Send one JSON
object per physical line:

```json
{"type":"attachment_import","version":1,"operation":"begin","prompt_uuid":"3f1e7948-5c1f-4c1f-8e2c-88bc9839ec27","upload_id":"upl_image1","attachment_id":"att_image1","name":"screen.png","size_bytes":12345,"mime_type":"image/png","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
{"type":"attachment_import","version":1,"operation":"chunk","upload_id":"upl_image1","sequence":0,"data":"<strict-padded-standard-base64>"}
{"type":"attachment_import","version":1,"operation":"commit","upload_id":"upl_image1"}
```

Every member shown for the selected operation is required and no other member
is accepted. Duplicate member names, including escape-equivalent spellings,
are rejected at every depth.

`upload_id` matches `upl_[A-Za-z0-9][A-Za-z0-9_-]{0,62}` and
`attachment_id` uses the corresponding `att_` grammar. `begin` declares the
positive raw decoded size, MIME claim, and lowercase SHA-256. Chunks start at
sequence 0, increase by exactly one, contain nonempty canonical padded
standard base64, decode to at most 262,144 bytes, and contain at most 349,528
encoded characters. Every NDJSON record is at most 8,388,608 bytes. Abort an
accepted upload with:

```json
{"type":"attachment_import","version":1,"operation":"abort","upload_id":"upl_image1"}
```

Accepted `begin` emits a nonterminal `attachment_import_result`; valid chunks
emit no output. Commit emits exactly one terminal result with
`status:"committed"` and a complete normalized `attachment` manifest. Image
normalization can change the committed size and digest from the raw upload, so
reference only that returned manifest. Timeout, EOF, cancellation, explicit
abort, validation, digest, size, or commit failure settles the upload once and
removes its reservation and temporary artifact. No user record may reference
an upload before successful commit.

Submit the returned manifest in the closed provider-neutral user union:

```json
{
  "type": "user",
  "uuid": "3f1e7948-5c1f-4c1f-8e2c-88bc9839ec27",
  "priority": "next",
  "message": {
    "role": "user",
    "content_version": 1,
    "content": [
      {"type": "text", "text": "Explain this screenshot."},
      {
        "type": "attachment_ref",
        "attachment_id": "att_image1",
        "kind": "image",
        "name": "screen.png",
        "mime_type": "image/png",
        "size_bytes": 12003,
        "sha256": "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
        "storage_id": "blob_sha256_abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
      }
    ]
  }
}
```

`content_version` is required when an attachment reference appears. Blocks
retain array order and the array may contain attachments only. Version 1
accepts both `type:"text"` and the compatibility spelling
`type:"input_text"` as one text variant; replay emits canonical
`type:"text"`. Every shown manifest field is required; unknown or duplicate
fields are rejected. All references in one user message must be committed for
that prompt UUID, and the message must include every successfully committed
attachment correlated to that UUID. Omitting one rejects the complete message
instead of silently dropping part of the imported set. The whole message and
queue reservation are validated before priority `now` cancels healthy work.
Queue capacity is 128 records and 16 MiB of aggregate admission accounting.
Legacy string, content-string, and text/`input_text` array messages remain
compatible.

Attachment-bearing replay preserves `content_version:1`, block order, and the
complete bounded manifest including opaque content-addressed `storage_id`, so
it decodes without losing attachment identity. Replay and terminal output
never expose bytes, base64, source paths, temporary paths, runtime storage
paths, provider request bodies, or complete data URLs. See the
[normative wire contract](.codex/skills/implementation-headless-sdk/references/sdk-wire-protocol.md#versioned-user-content-and-attachment-import)
for the closed rejection and acknowledgement unions.

#### Attachment storage, cleanup, resume, and fork

Committed attachments live under the owner-private native session
`attachments/` store. Normalized blobs are immutable and content-addressed;
transcripts persist manifests and blob identities only. The store has the
100,000 durable-manifest ceiling described above even when multiple manifests
deduplicate to one blob. Shutdown and store open abort incomplete uploads and
clean their temporary artifacts. Failed turns retain committed blobs while
durable history references them; orphan collection never deletes a referenced
blob.

Resume verifies and reuses the same session-owned blob without consulting the
original source path. Fork verifies and copies referenced blobs into the
destination session while preserving stable identities, so it does not depend
on a mutable shared path or the source file. Missing or tampered durable media
fails with its attachment identity; AgentX does not invent a placeholder as
authoritative content. Deleting a native session removes its local attachment
store through the normal recoverable deletion protocol, but is not secure
erasure and does not remove backups, remote copies, or descendant forks.

Attachment content is untrusted model input. It grants no tool, filesystem, or
instruction authority. Do not attach secrets unless the configured model
provider is authorized to receive them.

### Troubleshoot a turn

Successful turns do not write routine lifecycle records at the default INFO
threshold. Enable DEBUG diagnostics to write one correlated start and terminal
record for each model-backed turn, together with detailed troubleshooting
context:

```sh
agentx --trust-workspace --print --output-format text --debug \
  "investigate this repository" 2>agentx-debug.log
```

DEBUG adds session construction, model-iteration, stream, retry, tool, usage,
timing, and terminal-state metadata. WARN and ERROR conditions can still appear
without debug. For a persistent session, the durable session record is not the
diagnostic stream: `transcript.jsonl` stores the accepted user event as the turn
start, provider usage under the same turn ID, and one terminal `turn_result`
when finalization succeeds. Diagnostics do not include prompts, model text,
tool arguments or results, file contents, request headers or bodies, or
configured credentials. stdout remains reserved for the requested text, JSON,
or NDJSON result.

### Control reasoning and turn limits

Reasoning capabilities belong to each provider profile. AgentX starts with the
selected profile's `capabilities.reasoning.default_effort`, then applies a
nonempty `AGENTX_REASONING_EFFORT`, and finally an explicit `--effort`. The
result must be listed in that same profile's `capabilities.reasoning.efforts`;
AgentX rejects an unsupported value locally instead of sending it to another
endpoint. The interactive `/effort` command uses the same selected-profile
list.

```sh
agentx --provider terra-5-6
agentx --provider terra-5-6 --effort medium
agentx --provider sol-5-6 --print --effort xhigh --max-turns 20 \
  "investigate this failure"
```

`--provider` matches the exact provider `id` and selects its endpoint,
deployment, model, key, and capabilities. A `--model` value, when supplied,
must match the selected provider's `azure_openai.model`; it is a consistency
check and never silently reroutes to a different endpoint. Provider selection
is startup-bound, so switch profiles by starting a new AgentX process with a
different `--provider ID`.

For programmatic selection, first run:

```sh
agentx --list-providers --output-format json
```

Choose `providers[].value` (identical to `providers[].id`) and pass it verbatim
to `--provider`. Discovery reports `selected:false` for every entry because it
does not start a model-backed process. Apply the exit-status and response-
version checks from [Configure authentication](#configure-authentication)
before using any returned ID. On the subsequent structured session,
`system/init` reports only the selected profile. A fieldless correlated
`initialize` control request returns the complete provider catalog and marks
exactly the chosen profile selected.

### Choose a permission mode

```sh
agentx --permission-mode default
agentx --permission-mode acceptEdits
agentx --permission-mode plan
agentx --permission-mode dontAsk
```

- `default` asks whenever policy requires approval.
- `acceptEdits` pre-authorizes eligible file edits, while all other safety checks remain active.
- `plan` allows analysis and planning without mutation.
- `dontAsk` denies operations that would require interactive approval.

You can further restrict capabilities with startup rules:

```sh
agentx --allowed-tools 'Read,Glob,Grep'
agentx --disallowed-tools 'Bash,Write,Edit'
```

Denials and mandatory safety checks take precedence over broad allow rules.
Bash remains approval-sensitive, and protected paths such as
`~/.agentx/auth.json`, `.git`, a workspace `.agentx/`, and `.codex/` do not
become readable or writable merely because a broad rule was allowed. The user
application home `~/.agentx/` and a repository's workspace-extension
`.agentx/` directory are different trust domains. If AgentX reports that its
home identity changed, stop modifying that directory, restore it if
appropriate, and restart AgentX before using tools again.

### Use bare mode

Bare mode suppresses implicit repository instructions, skills, plugins, MCP configuration, memory, and output styles:

```sh
agentx --bare
```

Use this for a minimal session when you do not want workspace customization loaded.

### Resume, continue, and fork sessions

AgentX persists sessions by default under
`<application-home>/sessions/<workspace-hash>/<session-id>/`. The workspace
hash keeps `--continue` and session discovery scoped to the selected workspace;
the session identifier names one private session directory. Project memory is
stored separately and remains project-scoped rather than being inferred from
or copied into a session directory.

```sh
agentx --continue
agentx --resume SESSION_ID
agentx --resume SESSION_ID --fork-session
```

- `--continue` opens the latest eligible session.
- `--resume` opens a specific session.
- `--fork-session` creates a new session from the selected durable history.
- `--no-session-persistence` uses a temporary, nonresumable headless session:
  it writes no transcript, cannot combine with resume/continue/fork, and does
  not load or expose project memory.

Resume, continue, and fork are bound to the profile that created the durable
records. AgentX repeats the provider ID, type, logical model, and an opaque
fingerprint of the noncredential route (normalized endpoint route, deployment,
and exact API selector) on every transcript event and validates them before
replay, fork publication, attachment restoration, or provider I/O. Rotating
only that profile's API key preserves the fingerprint and remains resumable;
the key is neither stored nor recoverable from it. Selecting another provider
fails with the recorded `--provider ID` as remediation. Changing the recorded
profile's type, model, endpoint route, deployment, or API selector also fails
closed until the original routing is restored. An unbound legacy session
cannot be guessed onto the current default.

`--continue` first selects the latest eligible session in the current
workspace; it does not search for the latest session matching the currently
selected provider. If that latest session belongs to another profile, restart
with the provider ID named by the binding error or resume a different session
explicitly.

### List and delete native sessions

Use the provider-free management flags to inspect or delete native AgentX
sessions without starting a model connection or semantic session. Both
operations require `--cwd`; AgentX normalizes that workspace and scopes the
operation to its local session partition.

```sh
agentx --list-sessions --cwd WORKSPACE [--output-format text|json]
agentx --list-sessions --cwd WORKSPACE --session-page-size 100 \
  [--session-page-token TOKEN] --output-format json

agentx --delete-session SESSION_ID --session-revision REVISION \
  --cwd WORKSPACE [--output-format text|json]
```

List pages default to 100 entries and accept sizes from 1 through 500. When
more entries remain, pass the returned opaque `next_page_token` as
`--session-page-token`. Each listed session includes an opaque `revision`;
deletion requires that exact value so a changed or replaced target returns
`stale` instead of deleting the wrong directory.

Text output is intended for people and includes session ID, update time, and
revision. JSON writes exactly one versioned object to stdout, with diagnostics
on stderr. List status is one of `ok`, `stale`, or `store_unsafe`. Delete
status is one of `deleted`, `not_found`, `stale`, `session_locked`,
`delete_incomplete`, or `store_unsafe`; non-success outcomes remain
machine-readable even when the process exits nonzero. `session_locked` means
another process owns the session lock. `delete_incomplete` means cleanup is
still pending and retained data has not been reported as deleted.

Deletion removes only the selected directory from AgentX's local native
session store. It is not secure media erasure and does not delete backups,
remote copies, project memory, worktrees, authentication or configuration,
fork descendants, or any AgentX VS Code extension presentation cache.

AgentX discovers sessions and project memory only in the current application
home. It does not scan, migrate, or delete data from another layout or
directory. Back up and move any such data manually before relying on it, while
preserving owner-only directory and file permissions.

AgentX never assumes an interrupted side effect succeeded and does not automatically replay an uncertain tool call during recovery.

### Useful interactive commands

Inside the terminal session, use `/help` to see the current command catalog. Common commands include:

- `/status` — show the current runtime and session status.
- `/skills` — list skills available from `.codex/skills`.
- `/tasks` — show registered background tasks.
- `/cost` — show current usage accounting.
- `/doctor` — run runtime diagnostics.
- `/compact` — compact conversation context.
- `/mcp status` — show MCP server state.
- `/mcp reload` — reload eligible MCP configuration.
- `/plugin` — inspect plugin state.
- `/memory list`, `/memory recall`, `/memory remember` — work with local memory.
- `/output-style` — inspect or select an output style.
- `/clear` — clear active model context while retaining the durable transcript.
- `/exit` — close the session cleanly.

Availability can vary by mode and build. A command reports an explicit unavailable result when its backing feature is not operational.

## Use AgentX in Visual Studio Code

The AgentX extension opens an editor-native chat backed by the same AgentX binary and session runtime.

### Install or select the binary

The extension resolves AgentX in this order:

1. The absolute path in `agentx.binaryPath`.
2. A platform-specific binary bundled in the installed VSIX.
3. `agentx` on the extension-host `PATH`.

If you are developing locally, run `go install .` from the repository root and ensure Go's binary installation directory is on the extension host's `PATH`. To locate that directory, run:

```sh
go env GOBIN
go env GOPATH
```

If `go env GOBIN` is nonempty, the binary is `<GOBIN>/agentx` (or `agentx.exe` on Windows). Otherwise, use the `bin` directory beneath `go env GOPATH`. You can set **AgentX: Binary Path** to that executable's absolute path.

Run **AgentX: Run Installation Diagnostics** from the Command Palette if the extension cannot find or start the binary.

### Trust the workspace

VS Code must trust the workspace before the extension can launch AgentX. In Restricted Mode, the AgentX view remains visible so it can explain the restriction, but no AgentX process is started and workspace-controlled launch settings are blocked.

After reviewing the repository, use VS Code's **Workspace: Manage Workspace Trust** command and trust it. The `agentx.trustWorkspaceFeatures` setting separately controls whether a trusted workspace's `AGENTS.md`, `.codex/skills`, and `.agentx` extensions are passed to AgentX.

### Open and use the chat

Select the AgentX icon in the Activity Bar or run **AgentX: Open Chat**. Enter a request in the composer and submit it. The view shows:

- Streaming assistant text.
- Tool calls and correlated results.
- Permission requests.
- Structured questions.
- Context usage and turn status.
- Queued follow-up requests.

Use **AgentX: Stop Current Turn** to cancel active work. Cancellation is sent to the AgentX runtime; closing a visual row alone does not redefine session state.

### Add editor context

AgentX offers editor and source-control commands:

- **AgentX: Add Selection Reference to Chat**
- **AgentX: Add Current File to Chat**
- **AgentX: Add Current File Problems to Chat**
- **AgentX: Explain Selection**
- **AgentX: Fix Selection**
- **AgentX: Generate Tests for Selection**
- **AgentX: Review Workspace Changes**

A file or selection adds a workspace-relative path and optional range. The extension does not silently copy the entire file into the prompt. AgentX must still read it through its ordinary tools and permission policy. Problems are included only when you explicitly add them.

### Respond to permissions and questions

For a permission request, the extension offers:

- Allow once.
- Edit the complete input and allow.
- Deny.
- Deny and stop the active turn.

There is no permanent-approval button in the current extension protocol. Edited input is treated as a complete replacement and is validated again by AgentX.

For model-generated questions, choose one or more listed options or provide free-form text when offered, then submit the response.

### Manage sessions

Use the Command Palette or chat controls:

- **AgentX: New Chat**
- **AgentX: Continue Latest Session**
- **AgentX: Resume Session by ID**
- **AgentX: Fork Session by ID**

The extension's session picker contains sessions previously observed by that extension plus a manual-ID option. It is not a complete transcript browser. AgentX owns authoritative transcript storage; the extension retains only a bounded, redacted presentation cache.

### Configure the extension

At process startup, the AgentX binary's `system/init` event publishes only the
selected provider ID and type, logical model, and operator-declared reasoning
capabilities. A fieldless correlated `initialize` control request returns the
complete safe `providers` catalog, including selected and effective-default
state for every profile. The catalog gives an editor host enough metadata to
validate a prospective selection but never includes API keys, endpoint URLs,
deployments, or API-version selectors. Provider switching is not a live
control: a host starts a new AgentX process with `--provider ID`.

Before starting that process, an extension host can invoke
`agentx --list-providers --output-format json`. This prelaunch query works even
when multiple profiles have no default and returns the same provider
descriptor schema used by structured initialization, with no profile selected.
It performs no model request and emits no credential or Azure routing fields.
This is a binary discovery API available to VS Code integrations; it does not
by itself mean the currently installed companion extension exposes a provider
picker or setting.

Open **AgentX: Open Settings** to change:

| Setting | Purpose |
| --- | --- |
| `agentx.binaryPath` | Absolute AgentX executable path. |
| `agentx.reasoningEffort` | Reasoning effort for newly started sessions. |
| `agentx.permissionMode` | `default`, `acceptEdits`, `plan`, or `dontAsk`. |
| `agentx.maxTurns` | Maximum recursive model turns per request. |
| `agentx.trustWorkspaceFeatures` | Enable trusted repository instructions and extensions. |
| `agentx.bare` | Disable implicit instructions, skills, extensions, MCP, and memory. |
| `agentx.outputStyle` | Select a discovered output style. |
| `agentx.allowedTools` | Startup capability allow rules. |
| `agentx.disallowedTools` | Startup capability deny rules. |
| `agentx.followUpMode` | Queue follow-ups or cancel the active turn before running the next request. |
| `agentx.composerEnterBehavior` | Choose whether Enter sends or inserts a newline. |
| `agentx.todoCodeLens` | Show AgentX actions above TODO and FIXME comments. |
| `agentx.startOnViewOpen` | Start AgentX when the chat view opens instead of on first use. |
| `agentx.historyLimit` | Bound cached presentation records per known session. |
| `agentx.maxRenderedTextBytes` | Bound text retained for a rendered message or tool payload. |
| `agentx.completionNotifications` | Control completed-turn notifications. |

Startup settings apply to the next AgentX process. Start a new chat after
changing one of those restart-bound settings. The documented settings above do
not currently include an `agentx.provider` selector; do not put endpoint URLs,
deployments, or credentials into an invented editor setting. Provider-aware
hosts should use the discovery protocol and pass the selected ID as the exact
`--provider` launch argument.

### Diagnose extension problems

Use these commands in order:

1. **AgentX: Run Installation Diagnostics** — verify binary selection and compatibility.
2. **AgentX: Show Output** — inspect bounded extension-host diagnostics.
3. **AgentX: Copy Diagnostic Report** — copy a redacted report for support.
4. Confirm that the workspace is trusted.
5. Confirm that `<application-home>/auth.json` exists and matches the exact schema in [Configure authentication](#configure-authentication).
6. Run `agentx --version` in the same local or remote environment where the extension host runs.

For Remote SSH, Dev Containers, or WSL, the extension and AgentX binary run in
the remote workspace extension host. Install the binary and create
`~/.agentx/auth.json` in that environment, not only on the desktop host.

## Security guidance

- Never paste credentials into prompts, tool inputs, chat context, or diagnostics.
- Never commit `~/.agentx/auth.json` or another secret-bearing file.
- Review permission requests carefully, especially shell commands and writes.
- Treat attachment content as untrusted model input. Its presence does not
  grant tool or filesystem permission, and source paths must be selected
  explicitly.
- Prefer `plan` or `dontAsk` when inspecting an unfamiliar repository.
- Review `AGENTS.md`, `.codex/skills`, and the workspace `.agentx/` directory before enabling trusted workspace features.
- Use `--bare` or `agentx.bare` when repository customization is not required.
- Remember that the extension is a presentation adapter: the AgentX binary owns permissions, tools, transcripts, and recovery.

## Current limitations

- Native attachments are limited to headless CLI and stream-JSON PNG, JPEG,
  and PDF input under the exact provider qualification above. Interactive REPL
  and VS Code composer attachment input remain text-only.
- Audio, SVG, GIF, WebP, URLs, arbitrary binary, OCR, PDF conversion, and
  automatic image resizing are not supported. PDF object/xref streams,
  incremental updates, encryption, forms, annotations, embedded files, and
  active/action content are also outside the conservative accepted PDF subset.
- Loopback tests prove exact request construction and zero-call preflight. One
  current-worktree profile has separate live PNG/JPEG/conservative-PDF
  evidence; release-artifact and per-deployment/selector/platform
  qualification remains outstanding.
- Provider OAuth, delegated agents and teams, cloud handoff, and automatic binary updates are unavailable.
- The VS Code extension does not provide a complete session-history browser.
- Provider selection, reasoning effort, permission mode, output style,
  allow/deny rules, bare mode, and trust loading are restart-bound in VS Code.
- MCP support is stdio-only in the current runtime profile.

For implementation boundaries and exact compatibility status, see the repo-local [runtime architecture](.codex/skills/coding-directives/references/runtime-architecture.md), [runtime conformance profile](.codex/skills/coding-directives/references/runtime-conformance.md), and the standalone extension's [VS Code host protocol](https://github.com/greenpau/agentx-vscode-extension/blob/main/docs/PROTOCOL.md).
