---
status: active
title: Standalone STT/TTS media services (issue #170)
---

# Media sidecars

1. Speech work runs outside the gateway in two independently deployable Go
   services built from one binary: `hub-media` with `HUB_MEDIA_ROLE=stt` or
   `tts`. Each has its own data volume, restart policy and scaling; the
   gateway talks to them over authenticated HTTP.

2. The HTTP surface is the OpenAI audio contract —
   `POST /v1/audio/transcriptions`, `POST /v1/audio/speech`,
   `GET /v1/voices`, `GET /healthz` — so any compatible client or upstream
   can drive it, plus a durable async job API:
   `POST /v1/jobs` (multipart upload or JSON `source_url`/`source_path`),
   `GET /v1/jobs`, `GET|DELETE /v1/jobs/<id>`,
   `GET /v1/jobs/<id>/result?format=txt|json|srt`. Jobs persist under
   `HUB_MEDIA_DATA`, requeue after restart and expire by `HUB_MEDIA_JOB_TTL`.

3. Engines are swappable via `HUB_MEDIA_ENGINE`:
   - `command` — local worker binaries (`hub-stt`: faster-whisper,
     `hub-tts`: espeak-ng + ffmpeg to OGG/Opus); the shipped default.
   - `remote` — proxies to any OpenAI-compatible speech server
     (`HUB_MEDIA_UPSTREAM`/`HUB_MEDIA_UPSTREAM_KEY`), e.g. speaches or a
     bespoke model host. This is the "bring your own model" adapter.
   - `sherpa` — native sherpa-onnx (whisper/paraformer STT incl. Russian,
     vits/piper/kokoro TTS, pyannote segmentation + speaker-embedding
     diarization). Requires building with `-tags sherpa` and the sherpa-onnx
     C library; the default build refuses the engine name.

4. Async STT pipeline: materialize the source (upload, path under
   `HUB_MEDIA_INPUT_ROOT`, or https URL — presigned S3/GCS links work),
   decode through ffmpeg to 16 kHz mono wav, transcribe in 30-second windows,
   stitch global timestamps, optionally label speakers when the engine
   supports diarization (`diarize=true`), and write text/json/srt results.
   Recordings of an hour or more run this path, never the sync endpoint.

5. Security boundaries: every endpoint requires the shared bearer token
   (rendered once into `media.auth` and mounted into the gateway as
   `HUB_STT_AUTH`/`HUB_TTS_AUTH` and the sidecars as `HUB_MEDIA_AUTH`).
   Remote fetch requires `HUB_MEDIA_FETCH_HOSTS` allowlisting and refuses
   private/non-routable resolved IPs — the endpoint cannot become an SSRF
   proxy. Local sources must resolve inside `HUB_MEDIA_INPUT_ROOT` after
   symlink evaluation. Uploads and sources are byte-bounded; job ids are
   charset-validated; transcript/audio content is never logged.

6. Gateway integration: `HUB_STT_URL`/`HUB_TTS_URL` select the HTTP clients
   and win over `HUB_STT_COMMAND`/`HUB_TTS_COMMAND`, which stay as the
   documented embedded fallback. `/voice` readiness probes the sidecar
   `/healthz`; a dead sidecar reads "недоступно". TTS failure never eats the
   canonical text answer. Compose renders `hub-media-stt`/`hub-media-tts`
   with the `transcription` feature on infra deployments, built from
   `docker/Dockerfile.media`.

# Acceptance

- Unit/integration fixtures cover auth enforcement, traversal and SSRF
  guards, sync formats, async lifecycle incl. speaker-labelled srt output and
  the gateway client switch. Live evidence (real voice note and spoken reply
  on a deployed stack) remains the SPEC-0038 deployment gate.
