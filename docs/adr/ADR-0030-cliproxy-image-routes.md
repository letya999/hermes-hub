---
description: CLIProxy image models use two calls. Images-endpoint ids stay on /images. Gemini image ids use chat completions.
last_verified: 2026-09-28
---
# ADR-0030: Two CLIProxy image calls

Accepted 2026-09-28.

## Decision

[ADR-0028](ADR-0028-configurable-image-capability.md) keeps provider, model, and
delivery. The CLIProxy allowlist now has two calls. The default model stays
`gpt-image-2`.

`gpt-image-1.5`, `gpt-image-2`, `grok-imagine-image`,
`grok-imagine-image-quality`, and `grok-imagine-image-2.0` still post to
`{model.base_url}/images/generations` and `/images/edits`.

`gemini-2.5-flash-image`, `gemini-3-pro-image`, `gemini-3-pro-image-preview`,
`gemini-3.1-flash-image`, and `gemini-3.1-flash-image-preview` post once to
`{model.base_url}/chat/completions`. The body is the model, one user message,
modalities `image` and `text`, and `image_config.aspect_ratio` `1:1`. Edit sends
that message as text plus one `image_url` data URI. A webp source is still
re-encoded to png first.

The image is `choices[0].message.images`, exactly one. A `data:` URL is decoded
to bytes and is not returned as a provider URL. An http(s) URL follows the
existing delivery rules. A chat model such as `gemini-3.7-flash-high` is not on
this list and fails before a request.

The CLIProxy image pin stays `ba7e55836dee959e93ec6d41395865d9ec535086`. This
decision does not change that pin.

## Why

The local Antigravity account lists `gemini-3.1-flash-image` and returns a JPEG
from chat completions. The images endpoint answers unknown provider for the five
images-endpoint ids and rejects the Gemini id on that path. One route cannot
serve both families.

## Consequences

Workspace delivery stores the sniffed bytes, so a JPEG is a `.jpg`. png and webp
remain local conversion. Delivery `url` still requires an http(s) URL. An
unknown id still fails closed. Fal is unchanged.
