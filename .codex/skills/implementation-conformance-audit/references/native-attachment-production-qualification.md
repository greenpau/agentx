# Native Attachment Production Qualification

This note records sanitized, non-normative execution evidence for one
current-worktree native AgentX profile. It supports `MOD-083` through
`MOD-085`, `MOD-A14B`, `QM-068`, `QM-069`, `QM-069A`, `CONF-003`, and
`CONF-020`. It does not change those contracts or close their per-artifact,
per-deployment, per-selector, or per-platform qualification requirements.

## Evidence scope

- Executable: temporary installed current-worktree AgentX `1.0.6`
  qualification build; it was removed during cleanup and is not the unrelated
  repository `bin/agentx` artifact.
- Executable SHA-256 captured before qualification:
  `62e23faaadfdbb18a22bf87f9ff6a675d4d3c7e5b1074f2aea328ad4a881af30`.
- Provider route: Azure/OpenAI Responses adapter.
- Logical model: `gpt-5.6-sol`.
- API-selector class: one configured member of the closed
  `empty|v1|preview` qualification set. The environment-specific value,
  endpoint, deployment, account, and credential were deliberately omitted.
- Host scope: one supported POSIX development host.
- Credential source: the runtime's ordinary private application-home
  `auth.json` profile. No credential value, field, or absolute path is retained
  here.
- Artifact status: working-tree build, not a packaged or signed release
  artifact.

Twenty-two live provider requests were observed. Twenty-one were planned
qualification requests. One additional exploratory attachment-only request was
intentionally constrained to one model turn and reached that configured turn
ceiling; the deterministic replacement with a two-turn ceiling passed. This
was a turn-budget outcome, not a media or provider-modality failure.

## Passed live cases

- Text-only baseline.
- Normalized PNG screenshot input.
- Normalized JPEG screenshot input.
- Conservative two-page PDF input from the `IQ-013` supported structural
  subset.
- Ordered mixed PNG, PDF, JPEG, and PNG content.
- The exact eight-attachment per-message boundary.
- PNG magic accepted under a non-media filename extension.
- Attachment-only input under a deterministic system contract.
- Stream-JSON capability negotiation, legacy text fallback,
  begin/chunk/commit, mixed text plus attachment references, and
  attachment-only submission.
- Resume after removal of the original source file.
- Fork with destination-owned verified media.
- Successful context compaction followed by continued use.
- Tampered destination media rejected locally, with no assistant content or
  provider-duration observation, while the source session remained valid.
- Sanitized output, transcript, diagnostic, and error scans with no attachment
  bytes, base64, absolute source path, runtime storage path, provider body, or
  credential material observed.

Negative installed-runtime cases emitted no assistant content and reported no
provider API duration. Deterministic loopback counters, rather than those
surface symptoms alone, remain the authoritative zero-network evidence for
unsupported profiles, malformed/tampered media, and request limits.

## Cleanup evidence

- Four qualification sessions were removed through revision-bound native
  session deletion.
- Subsequent workspace session inventories were empty.
- No qualification AgentX child process remained.
- Approximately 246 MiB of owned temporary qualification data was removed.
- Provider price/cost evidence was unavailable and was not reported as zero.

## Not qualified by this run

- Other Azure deployments, accounts, API-selector members, logical models,
  providers, operating systems, or CPU architectures.
- A packaged, signed, or otherwise final release artifact.
- The 20 MiB per-item, 40 MiB aggregate/decoded-request, 55,927,120-byte
  encoded-media, 67,108,864-byte final-request, 100-media-item provider-context,
  100-page PDF, 512 MiB store, or 100,000-manifest/upload-lifecycle ceilings
  against the live provider. Those boundaries remain deterministic local and
  loopback evidence.
- Provider media-rejection quarantine. No closed media-specific rejection was
  deliberately induced against the live deployment; `QM-A09B` and
  `NET-033A` remain loopback evidence for rejection classification,
  quarantine, and no-resend behavior.
- Arbitrary PDFs. The passing fixture does not qualify object/xref streams,
  incremental updates, encryption, annotations, forms/XFA, embedded files,
  active/action content, OCR, conversion, or PDFs outside the conservative
  `IQ-013` subset.
- Audio, SVG, GIF, WebP, URLs, arbitrary binary, or automatic image resizing.

Before a release claims real-provider native attachments, rerun `MOD-A14B`
with the exact candidate artifact and each deployment/profile, selector,
platform, and modality named by that release. Store only the same bounded
sanitized evidence classes used here.
