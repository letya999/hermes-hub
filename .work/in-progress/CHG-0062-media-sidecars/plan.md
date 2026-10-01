# CHG-0062 — standalone STT/TTS media services

Issue: #170 (architecture redo — user rejected embedded command workers)

## Goal

Move speech work out of the gateway into two independently deployable Go
sidecars — `hub-media-stt` and `hub-media-tts` — sharing one binary
(`cmd/hub-media`, `internal/mediasvc`) with swappable engines, durable async
jobs for long recordings, and ingestion from uploads, mounted user storage or
presigned/S3 https URLs.

## Design

- One binary, `HUB_MEDIA_ROLE=stt|tts` picks the surface; two compose
  services scale and restart independently.
- OpenAI-compatible API contract (`/v1/audio/transcriptions`,
  `/v1/audio/speech`, `/v1/voices`) so any compatible upstream or client can
  drive it; plus `/v1/jobs` async API for long audio.
- Engines: `command` (hub-stt/hub-tts wrappers — shipped default),
  `remote` (proxy to speaches/OpenAI-compatible server = bring-your-own
  model), `sherpa` (native offline sherpa-onnx — whisper/paraformer STT,
  vits/piper TTS, pyannote diarization; built with `-tags sherpa`, stub
  otherwise).
- Async pipeline: materialize source -> ffmpeg decode to 16k mono wav ->
  30s windows -> engine per window -> stitched timestamps -> text + json +
  srt. Optional diarization labels speakers (`speaker-N:` prefixes).
- Security: bearer auth on every endpoint, fetch allowlist + private-IP
  refusal (no SSRF), path containment under HUB_MEDIA_INPUT_ROOT, bounded
  uploads/sources, durable job store with restart requeue.
- Gateway: `HUB_STT_URL`/`HUB_TTS_URL` select HTTP clients; embedded
  `HUB_*_COMMAND` remain the documented fallback. `/voice` readiness probes
  sidecar `/healthz`.

## Steps

- [x] internal/mediasvc: config, engines (command/remote/sherpa-stub), fetch
      guards, job store + worker pipeline, HTTP server
- [x] cmd/hub-media entrypoint
- [x] gateway HTTPTranscriber/HTTPSynthesizer + env wiring + readiness probe
- [x] docker/Dockerfile.media + media worker scripts
- [x] stack render: hub-media-stt/tts services, media.auth, gateway env
- [x] tests: formats, auth, traversal/SSRF guards, job lifecycle, sidecar
      clients
- [ ] docs/specs/changelog, just check, commit
