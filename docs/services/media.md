---
description: hub-media, hub-stt, hub-tts — media sidecars carrying provider credentials; sync image gen plus durable async jobs.
last_verified: 2026-10-20
---

# media services (hub-media, hub-stt, hub-tts)

Three standalone sidecars, one code family (`internal/media`,
`internal/mediasvc`). Each listens on its own container `:8090`, joins only
the space network — never the docker socket or broker secrets — and shares
the `media.auth` bearer.

| Service | Binary | Role |
|---|---|---|
| `hub-media` | `cmd/hub-media` | Synchronous image generation/edits + durable async video jobs. Engines: `remote` (OpenAI-compatible) or `fal` (fal.run sync + queue.fal.run), via `HUB_MEDIA_ENGINE`/`HUB_MEDIA_COMMAND`. SPEC-0061. |
| `hub-stt` | `cmd/hub-stt` | OpenAI-compatible `/v1/audio/transcriptions` + durable async jobs for long recordings (uploads, mounted storage, presigned https). Engines: `command`/`remote`/`sherpa` via `HUB_MEDIA_ENGINE` (mediasvc.RoleSTT). |
| `hub-tts` | `cmd/hub-tts` | Text-to-speech counterpart to hub-stt, same engine-swap config family. |

## Why separate containers

Provider credentials (e.g. FAL_KEY) live **only** in these services. The
runtime and gateway get a service URL (`HUB_MEDIA_URL=http://hub-media:8090`,
`HUB_STT_URL`, `HUB_TTS_URL`) plus the shared `media.auth` bearer — never the
upstream key. The `image_gen` grant reconstructs an identical normalized
config block inside spawned runtimes via `HUB_MANAGED_IMAGE_GEN` JSON, never
from agent-editable state; a non-empty value is the feature-enabled marker.

## Data

Each role keeps durable job data on its own volume (`hub-<role>-data` →
`/data`). STT/TTS also set `HF_HOME=/data/hf` and `XDG_CACHE_HOME=/data/cache`
for model caches. In-container healthcheck: `hub-media health` hits
`http://127.0.0.1:8090/healthz` with a 5s client timeout.

## Fallback

`HUB_STT_COMMAND`/`HUB_TTS_COMMAND` stay configured as the embedded fallback
(`/usr/local/bin/hub-stt`, `hub-tts`); when the URL is set the sidecar wins at
selection time — an in-process command worker, not a second transport.
