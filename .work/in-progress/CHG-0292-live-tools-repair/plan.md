# CHG-0292: Five-prompt live tools repair

## Goal

Repair the actual terminal, media and speech failures reported by the owner on
2026-11-09; verify file revision editing, PDF extraction, browser and web calls
through the same admitted MCP surface.

## Scope

- Reject malformed agent-tool arguments before backend admission and effects.
- Wire scoped audio transcription to hub-stt and voice synthesis to the existing
  owner-bound communication outbox, preserving uncertain delivery semantics.
- Publish an immutable agent-tools version, prompt guidance and documentation.
- Correct authored deployment passports, start shared media/speech services,
  rebind and respawn the existing dev runtime, then collect live evidence.

## Acceptance

- A malformed terminal call cannot execute or create an uncertain operation;
  a corrected call computes the real fixture hash and line count.
- STT cannot escape its admitted filesystem scope; timestamps survive the API.
- Voice delivery uses the live run's recorded destination and is idempotent per
  tool operation; a sibling bearer cannot select another owner's destination.
- Real image generation/edit/inspection and Russian voice synthesis work, with
  queued versus sent outcomes reported separately; no mock is provider proof.
- Existing file/PDF/web/browser tools remain functional. Run just check and the
  affected package tests once after the final edit; no publication is requested.

## Final live evidence

- Managed passport applied without rollback; catalog 18.3.2 selected and enabled for both spaces.
- Terminal SHA-256 matches file revision; five lines. PDF extraction, browser open/read/capture/close, vision, web fetch/search/cache and STT passed through MCP.
- Speech delivered through media_speak: durable Telegram receipt voice_sent=true, outbox/done.
- Shared gateway user policy and user egress relay reconciled to the published configuration.
- Image generation/edit remain blocked by upstream authorization. Current Devin catalog and image endpoint probes do not establish a working image-generation route; do not mark this change done until actual image bytes and edits succeed.
- just check, all affected package suites, security gate, Docker breaker check and scoped Docker cleanup passed. Remote CI was not run for unpublished changes.
