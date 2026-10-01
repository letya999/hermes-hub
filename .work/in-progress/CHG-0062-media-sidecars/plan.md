# CHG-0062 — standalone STT/TTS media services

Issue: #170 (architecture redo — user rejected embedded command workers)

## Goal

Move speech work out of the gateway into two independently deployable Go
services — `hub-stt` (`cmd/hub-stt`, `docker/Dockerfile.stt`) and
`hub-tts` (`cmd/hub-tts`, `docker/Dockerfile.tts`) — sharing the
`internal/mediasvc` library, with swappable engines, durable async
jobs for long recordings, and ingestion from uploads, mounted user storage or
presigned/S3 https URLs.

## Design

- Two binaries (no role env flag — the binary is the role); two compose
  services scale and restart independently.
- OpenAI-compatible API contract (`/v1/audio/transcriptions`,
  `/v1/audio/speech`, `/v1/voices`) so any compatible upstream or client can
  drive it; plus `/v1/jobs` async API for long audio.
- Engines: `command` (stt-worker/tts-worker wrappers — shipped default),
  `remote` (proxy to any OpenAI-compatible upstream — Groq, OpenAI,
  Speaches, whisperx-asr-service with diarization passthrough, NVIDIA NIM;
  = bring-your-own model/provider), `sherpa` (native offline sherpa-onnx —
  whisper/paraformer STT, vits/piper TTS, pyannote diarization; built with
  `-tags sherpa`, stub otherwise).
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
- [x] cmd/hub-stt + cmd/hub-tts entrypoints (binary = role, no env flag)
- [x] gateway HTTPTranscriber/HTTPSynthesizer + env wiring + readiness probe
- [x] docker/Dockerfile.stt + Dockerfile.tts + stt/tts-worker scripts
- [x] stack render: hub-stt/hub-tts services, media.auth, gateway env
- [x] tests: formats, auth, traversal/SSRF guards, job lifecycle, sidecar
      clients
- [ ] docs/specs/changelog, just check, commit
