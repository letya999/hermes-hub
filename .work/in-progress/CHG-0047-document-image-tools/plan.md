---
description: Workspace document and image capability with a configured provider, model, and delivery.
last_verified: 2026-09-28
---

# CHG-0047 Document and image tools

## Change

- Add extract, create, edit, and convert for txt, md, csv, html, pdf, xlsx, and
  pptx. Markdown was already supported. pdf, xlsx, and pptx are simple text
  packages. doc, docx, xls, and ppt stay rejected. csv is a conversion target
  only from xlsx.
- Add image inspect through the model credential, local png, jpeg, and webp
  conversion, and opt-in image generation and edit. A webp edit source is sent
  to the provider as png.
- Configure `image_gen` with provider, model, and delivery. Default is
  `cliproxy`, `gpt-image-2`, and `workspace`. Fal remains
  `fal-ai/flux-2/klein/9b` and does not edit. `model: chat` is sent only when
  `model.default` is on that provider's allowlist. CLIProxy image ids use two
  calls: the images-endpoint family stays on `/images/generations` and
  `/images/edits`, and the Gemini image ids use `/chat/completions`.
- Delivery `workspace` writes `artifacts/images/` and drops the provider URL.
  Delivery `url` returns one provider URL and writes nothing.
- Keep native Hermes `vision` and `image_gen` out of `platform_toolsets`.
- Delete artifacts only through `artifact_remove` or an explicit purge.

## Contract

[SPEC-0032](../../../specs/active/SPEC-0032-document-image-profile.md),
[ADR-0027](../../../docs/adr/ADR-0027-workspace-document-image-profile.md),
[ADR-0028](../../../docs/adr/ADR-0028-configurable-image-capability.md),
[ADR-0029](../../../docs/adr/ADR-0029-bounded-pdf-office-webp.md), and
[ADR-0030](../../../docs/adr/ADR-0030-cliproxy-image-routes.md).

## Verification

Targeted `go test` of the image routes and `TestDocumentImageProfile`, without
a coverage profile, plus `devcheck docs`. A live `gemini-3.1-flash-image` call
on the local container is part of this change. Live Fal, a live images-endpoint
model, and a rebuilt Hermes container are not.
