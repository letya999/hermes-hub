---
description: Image generation is a configured provider, model, and delivery capability. Native Hermes vision and image_gen stay off.
last_verified: 2026-09-28
---
# ADR-0028: Configurable image capability

Accepted 2026-09-28.

## Decision

`image_gen` stays a host-managed feature on the hub MCP profile from
[ADR-0027](ADR-0027-workspace-document-image-profile.md). The mounted section
now carries three fields: `provider`, `model`, and `delivery`.

`provider: cliproxy` calls `{model.base_url}/images/generations` and
`/images/edits` with `OPENAI_API_KEY`. This is the default. Its model allowlist
is the image ids accepted by the pinned CLIProxyAPI commit `ba7e558`. `model:
chat` sends `model.default` only when that id is on the same allowlist.
`provider: fal` keeps the known `fal-ai/flux-2/klein/9b` body and `FAL_KEY`.
Fal does not edit. An unknown provider or model fails before a request.

`delivery: workspace` writes `artifacts/images/` and drops the provider URL.
`delivery: url` returns one provider URL and writes nothing. Inline bytes are
not published as a URL.

Document edit and convert, and png/jpeg conversion, stay inside the same
process and the same format bounds. Office conversion is not part of this
decision and is not added to the base image.

Native Hermes `vision` and `image_gen` remain off `platform_toolsets`.

## Why

A single Fal pin cannot use the CLIProxy account that already serves chat, and
it cannot choose between saving a file and leaving the provider URL. The
allowlist is the extension point: a new provider is a known request body, not
a free model string.

## Consequences

`FAL_KEY` is injected only when the selected provider is `fal`. Turning the
feature off, or leaving it on `cliproxy`, omits that key from the runtime env.
Each call rereads the mounted file. Adding a provider requires its payload and
a regression test. Office formats still fail closed.
