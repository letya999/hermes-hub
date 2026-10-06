# SPEC-0033: Document and image profile

Frozen: 2026-09-28. Issue 124.
Hermes pin `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`).
Decision: [ADR-0027](../../docs/adr/ADR-0027-workspace-document-image-profile.md).
Provider, model, and delivery: [ADR-0028](../../docs/adr/ADR-0028-configurable-image-capability.md).
pdf, xlsx, pptx, and webp: [ADR-0029](../../docs/adr/ADR-0029-bounded-pdf-office-webp.md).
Two CLIProxy image calls: [ADR-0030](../../docs/adr/ADR-0030-cliproxy-image-routes.md).

1. The profile is the hub MCP server started by `hubctl tools`: `document_extract`,
   `document_create`, `document_edit`, `document_convert`, `image_inspect`,
   `image_convert`, and `artifact_remove`. `image_generate` and `image_edit` are
   registered only when the mounted Hermes config grants them at process start.
2. Extract and create are separate effects. Extract reads one existing workspace
   file and does not modify it. Create writes one new file at
   `artifacts/documents/<name>.<ext>` and refuses to overwrite. `name` is a short
   identifier: a letter or digit, then up to 63 letters, digits, `_`, or `-`.
3. Supported documents are txt, md, csv, html, pdf, xlsx, and pptx. Markdown was
   already supported. Extract accepts htm as html. HTML extract drops well-formed
   script, style, noscript, and comments; it is a text extract, not a browser.
   HTML create escapes plain text into an article and rejects markup-shaped
   input (raw html belongs to file_write). pdf, xlsx, and pptx are simple
   text packages in this process. pdf is wrapped text with an embedded Go Regular
   font. Extract reads text-showing operators and FlateDecode streams whose
   length is a direct integer. An encrypted PDF fails closed. There is no OCR.
   xlsx is one worksheet of inline text. Create and edit take CSV text; a line
   without a comma is one column. Extract returns CSV. pptx splits slides on a
   blank line, and each line is one paragraph in one text box. Edit of pdf, xlsx,
   or pptx replaces the file with a new simple document from the new text.
   Rejected without reading them as documents: doc, docx, xls, ppt, odt, rtf,
   epub, pages. The extension xslx is rejected.
4. Inspect and generation are separate effects. Inspect uses the configured
   model endpoint and `Authorization: Bearer` with `OPENAI_API_KEY`. It does not
   send `FAL_KEY`. Generation uses the `image_gen` grant. Provider `cliproxy`
   uses the same base URL and `OPENAI_API_KEY` as chat. Provider `fal` uses
   `Authorization: Key` with `FAL_KEY` and does not send the model key.
5. The grant names `provider`, `model`, and `delivery`. Empty settings mean
   provider `cliproxy`, model `gpt-image-2`, and delivery `workspace`. `model:
   chat` sends `model.default` only when that id is on the provider allowlist;
   otherwise the call fails before a request. Any other model must be on the
   allowlist. CLIProxy has two calls. The images-endpoint ids are
   `gpt-image-1.5`, `gpt-image-2`, `grok-imagine-image`,
   `grok-imagine-image-quality`, and `grok-imagine-image-2.0`. Those posts go
   once to `{base_url}/images/generations` with model, prompt, n 1, size
   `1024x1024`, and response_format. Edit posts once to `{base_url}/images/edits`
   with the same fields and one image file. The Gemini image ids are
   `gemini-2.5-flash-image`, `gemini-3-pro-image`, `gemini-3-pro-image-preview`,
   `gemini-3.1-flash-image`, and `gemini-3.1-flash-image-preview`. Those posts
   go once to `{base_url}/chat/completions` with the model, one user message,
   modalities `image` and `text`, and `image_config.aspect_ratio` `1:1`. Edit
   sends that message as text plus one image_url data URI. The image is
   `choices[0].message.images`, exactly one. A data URL is decoded to bytes and
   is not a delivery URL. An http(s) URL follows the delivery rules below. A
   chat model such as `gemini-3.7-flash-high` is not an image model. The
   CLIProxy image pin stays `ba7e55836dee959e93ec6d41395865d9ec535086`. Fal
   allows `fal-ai/flux-2/klein/9b` only. Fal posts
   the catalog body for `fal-ai/flux-2/klein/9b`: prompt, image_size
   `square_hd`, num_inference_steps 4, output_format png,
   enable_safety_checker false. `num_images` is not sent. Fal edit is rejected.
   Exactly one image is accepted. Delivery `workspace` writes
   `artifacts/images/` and drops the provider URL. Delivery `url` returns one
   http(s) URL and writes no file; inline bytes are not turned into a URL.
   Redirects are refused. A download host other than the configured endpoint
   must be public HTTPS.
6. Inspect accepts png, jpeg, and webp. Rejected: gif, bmp, svg, tif, tiff, heic,
   avif. Width and height come from the image header. An edge is checked before
   the pixel product. webp decode and encode stay in process. A generated webp
   may be stored as webp. `image_edit` re-encodes a webp source to png before
   the provider upload.
7. Every path is a relative file inside the invoking user's workspace root.
   Absolute paths, backslash, a colon, NUL, and `..` fail closed. One user root
   cannot read or write another user's root. Generated images go only under
   `artifacts/images/`.
8. Bounds fail closed and do not queue. Documents: 2 MiB, 20 pages of 3000 runes,
   concurrency 2, 5 seconds. A PDF also fails when it has more than 20 pages. A
   slide file fails above 20 slides. An xlsx sheet allows 2000 rows and 64
   columns. The xlsx and pptx zip allows 64 entries and 8 MiB uncompressed, and
   rejects `..`, an absolute name, and an encrypted entry. Images: 5 MiB, edge
   4096, 4000000 pixels. Inspect: concurrency 2, 30 seconds. Generate:
   concurrency 1, 60 seconds. Prompt: 1 to 2000 runes. A known credential value
   is not written into a new artifact.
9. `image_gen` is host-managed. It is not a self-service connector and not a
   default feature. Enabled, the Hermes config receives the normalized provider,
   model, and delivery. On a rendered stack the generation call runs in the
   `hub-media` sidecar (SPEC-0040): provider credentials are materialized
   from the Credential Broker grant named by `HUB_MEDIA_BROKER_GRANT`
   (identity `media`, `broker-secrets-media` volume) — no
   `media.<environment>.env` file carries them — and the runtime env never
   contains `FAL_KEY` for any provider. Outside a stack, or when
   `HUB_MEDIA_URL` is unset, the embedded direct-provider path stays as the
   fallback and `FAL_KEY` enters the runtime env only for provider `fal`.
   Disabled, the section and the keys are
   omitted even if the secrets file
   contains them. The rendered config does not contain a key value. Native
   `vision` and `image_gen` stay out of `platform_toolsets`. Playwright
   `--caps vision,pdf` stays.
10. The live grant is the `image_gen` section of the mounted config
    (`HUB_HERMES_CONFIG`, otherwise `/state/hermes/config.yaml`). A missing file,
    a missing provider, an unknown field, or a provider, model, or delivery
    outside the allowlist fails closed and does not stop the MCP server. A call
    rechecks the file.
    `tools/list` drops `image_generate` and `image_edit` on the next process
    start. `document_edit` updates one existing file under
    `artifacts/documents/` and refuses any other path. `document_convert` writes
    a new file in txt, md, html, pdf, xlsx, or pptx. csv is a conversion target
    only when the source is xlsx. Same-format conversion is refused.
    `image_convert` rewrites png, jpeg, and webp locally and does not call a
    provider.
11. Values of `OPENAI_API_KEY` and `FAL_KEY` of at least 8 characters are removed
    from tool results and errors. Extract redacts returned text and leaves the
    source file unchanged. Create and generate refuse a known value. Results
    include `secret_values_included: false`. A missing credential does not ask
    for the value in chat.
12. Artifacts are files on the user workspace mount. They remain after the
    session ends. `artifact_remove` deletes one regular file under
    `artifacts/documents/`, `artifacts/images/`, or `artifacts/videos/`. Removing the workspace remains
    purge with an explicit matching user confirmation. These calls run in the
    existing hub process. They are not an always-on document or GPU service.
13. An httptest double of the model and Fal endpoints is not a live provider
    call. Local tests do not prove a live FAL account, a live model account, or
    a rebuilt Hermes image.
