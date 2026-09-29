---
description: Bounded pdf, xlsx, pptx, and webp stay inside the hub process. Word and a user zip tool stay out.
last_verified: 2026-09-28
---
# ADR-0029: Bounded pdf, spreadsheet, slides, and webp

Accepted 2026-09-28.

## Decision

The hub profile in [ADR-0027](ADR-0027-workspace-document-image-profile.md) and
[ADR-0028](ADR-0028-configurable-image-capability.md) now also accepts pdf, xlsx,
and pptx, and accepts webp for inspect and local conversion. Markdown was
already accepted. The work stays in the `hubctl tools` process. LibreOffice,
pandoc, poppler, and a Word package are not added to the base image. doc, docx,
xls, ppt, odt, rtf, epub, and pages stay rejected.

pdf is wrapped text with the Go Regular font, at most 20 pages. Extract reads
text-showing operators and FlateDecode streams whose length is a direct integer.
An encrypted PDF fails closed. There is no OCR. Edit replaces the file with a
new simple PDF from the new text.

xlsx is one worksheet of inline text. Create and edit take CSV; a line without
a comma is one column. Extract returns CSV. Conversion to csv is allowed only
from xlsx. The package allows 64 zip entries and 8 MiB uncompressed, and rejects
`..`, an absolute name, and an encrypted zip. A sheet allows 2000 rows and 64
columns.

pptx splits slides on a blank line. Each line is one paragraph in one text box,
at most 20 slides. Edit replaces the deck.

webp decode and encode use `github.com/HugoSmits86/nativewebp` v1.3.0. The PDF
font comes from `golang.org/x/image` v0.46.0. A generated webp may be stored as
webp. Edit of a webp source is re-encoded to png before the provider upload,
because that upload accepts png and jpeg.

The zip reader used for xlsx and pptx is not a tool that creates or unpacks a
user archive.

## Why

These four types were requested on top of the text and png/jpeg profile. A
general office suite in the base image would add another conversion service.
The standard library, the Go font, and one pure-Go WebP codec cover a text
round trip and an image transcode inside the existing call bounds.

## Consequences

ADR-0028 still keeps a general office converter out of that decision and out of
the base image. This decision adds only the subset above. A scanned PDF, a PDF
with a custom encoding, and a designed workbook or deck can fail closed or lose
layout. Recognition of several images, and creating or unpacking a user zip,
are separate future work.
