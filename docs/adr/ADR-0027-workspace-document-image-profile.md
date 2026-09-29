---
description: Document and image effects are hub MCP tools with separate credentials, workspace artifacts, and bounded calls.
last_verified: 2026-09-28
---
# ADR-0027: Workspace document and image profile

Accepted 2026-09-28.

## Decision

Document extract, document create, image understanding, and image generation
are hub MCP tools on the existing `hubctl tools` stdio server. They are not
native Hermes `vision` or `image_gen` toolsets, and they are not extra MCP
wrappers around those tools.

`platform_toolsets` keeps an explicit list and omits `vision` and `image_gen`.
Playwright keeps `--caps vision,pdf`; that is the browser's own capability, not
the image profile. The rendered config still sets `auxiliary.vision` to
provider `main` with a 30s timeout, 15s download timeout, and concurrency 2, so
a later native vision tool cannot silently switch provider or drop its limit.

Image generation is the host-managed `image_gen` feature. It is off unless
selected. The mounted config then contains provider `fal` and model
`fal-ai/flux-2/klein/9b`, and the runtime env receives `FAL_KEY`. Turning the
feature off removes that section and omits `FAL_KEY` even when the secrets file
still holds it. Each generate call reads the mounted config again.

Supported documents are txt, md, csv, and html, the formats the Go standard
library can read and write without a package in the base image. Extract also
accepts htm. Office and PDF types are rejected by name. Inspect accepts png and
jpeg only. Work runs inside the tool call, with per-session concurrency and a
deadline, and is not a resident service.

## Why

Hermes `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`) has no document
toolset. Its local vision backend reads any container path, so a workspace
boundary there does not fail closed. Its `image_generate` tool returns a remote
URL from the Fal queue client. That URL can expire, and it is not a file in the
invoking user's workspace. Wrapping either tool would publish a second surface
for the same effect and would still miss the workspace, credential, and
retention rules. The hub server is already the stdio process Hermes launches
for workspace tools, so these effects belong there.

Understanding and generation do not share a credential. Understanding uses the
configured model endpoint and `OPENAI_API_KEY`. Generation uses `FAL_KEY` and
the catalog payload for Klein 9B. The hub calls `https://fal.run/<model>` once,
instead of the upstream queue client, so the tool deadline covers the whole
call. The provider URL is not returned.

## Consequences

Artifacts are files under the user workspace bind mount. They survive session
end. `artifact_remove` deletes one generated file; purge of the workspace still
requires an explicit matching user confirmation. A revoked grant fails the next
generate call. The tool disappears from `tools/list` on the next process start.
An httptest of this server is not a live Fal or model call. The frozen matrix
is [SPEC-0032](../../specs/active/SPEC-0033-document-image-profile.md).
