---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0052 Document and image profile

Local tests call the hub MCP server Hermes launches as `hubctl tools`. They
read, edit, and convert one document, round-trip a simple text PDF, a
CSV-shaped xlsx, and text slides, convert PNG, JPEG, and webp locally, inspect
one PNG through an httptest model endpoint, generate one PNG through an
httptest Fal endpoint, and generate and edit through an httptest
CLIProxy-style images endpoint, including URL delivery that writes no file.
A Gemini image id is covered by a separate httptest chat-completions route.
Credential values stay out of the results, a second workspace cannot read the
first, an unknown `image_gen` field fails closed, and removing the section
fails the next generate call. An httptest double is not a live provider call.
No live FAL account was exercised, and the Hermes image was not rebuilt.

A live local Hermes container, using a newly built `hubctl` because the image
binary is older, called `gemini-3.1-flash-image` through chat completions.
One JPEG was created. png and webp came from local conversion of that JPEG.
jpeg, png, and webp were inspected, and each was edited back through the same
model. The first png inspect hit the 30 second deadline; a retry succeeded.
The images-endpoint models were not called live: this CLIProxy account has no
provider for them.
||||||| 1c398d3
