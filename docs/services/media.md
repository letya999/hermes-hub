---
description: hub-media, hub-stt, hub-tts — media sidecars carrying provider credentials; sync image gen plus durable async jobs.
last_verified: 2026-10-24
---

# media services (hub-media, hub-stt, hub-tts)

Three standalone sidecars, one code family (`internal/media`,
`internal/mediasvc`). Each listens on its own container `:8090`, joins only
the service network — never the docker socket — and shares the `media.auth`
bearer. `hub-media` alone mounts its read-only `broker-secrets-media` key volume.

| Service | Binary | Role |
|---|---|---|
| `hub-media` | `cmd/hub-media` | Synchronous images/edits + durable async video. Engines: `remote`, `fal`, opt-in `comfy`, or an operator `command`. SPEC-0061/SPEC-0069. |
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

The gateway picks the synthesis voice per reply language:
`HUB_TTS_VOICE_<LANG>` (`EN`/`RU`/`KK`/`ES`) overrides `HUB_TTS_VOICE`, which
stays the default for unmapped languages. With `transcription` on, compose
passes all five variables to communication-hub unchanged.

## Model routing and independent accounts

`HUB_MEDIA_ENGINES=remote,comfy,fal` builds an ordered router.
`HUB_MEDIA_MODEL_ROUTES` maps a logical allowlisted name to explicit provider IDs,
for example `portrait=comfy:portrait,remote:gpt-image-2;clip=fal:fal-ai/video`.
Duplicate logical routes, unsafe IDs and more than eight hops are rejected.
Fallback never changes an explicitly mapped provider model ID.

`HUB_MEDIA_PROVIDERS` is a JSON object keyed by engine name:

```json
{"remote":{"upstream":"https://provider.example/v1","broker_grant":"grant-images"},"comfy":{"upstream":"http://comfy:8188"},"fal":{"upstream":"https://fal.run","queue_upstream":"https://queue.fal.run","broker_grant":"grant-video"}}
```

Each explicit entry owns its endpoint, queue endpoint and credential source.
An entry without `key_env`/`broker_grant` is anonymous; it does not inherit a
global grant, `OPENAI_API_KEY` or `FAL_KEY`. `key_env` is an alternative for
standalone operation, must be present, and cannot accompany `broker_grant`.
Compose passes provider references, not their secret values. Rendered media
stacks obtain secrets through Broker grants delivering `HUB_MEDIA_UPSTREAM_KEY`.
The existing `HUB_CREDENTIAL_BROKER_MEDIA_*` identity/CA configuration is required.

Single-engine legacy env remains supported. Secondary Fal defaults to its own
`fal.run`/`queue.fal.run` endpoints and `FAL_KEY`; it never inherits another
engine's endpoint/key/grant. Secondary remote/ComfyUI requires its own explicit
endpoint. A provider lacking image-edit support rejects the edit instead of
discarding its source image. Fal result URLs still pass the HTTPS host allowlist
and byte bounds when Fal sits behind the router.

## Local ComfyUI (opt-in)

Run an operator-managed ComfyUI service at the configured fixed endpoint. Hub
does not install ComfyUI, checkpoints, custom nodes or ML packages into its base
image or Hermes runtime. Keep ComfyUI weights and outputs on that service's own
volume. Export an **API-format** workflow as `/data/models/<provider-model>.json`
on the `hub-media-data` volume (standalone: `HUB_MEDIA_MODEL_DIR`). These workflow
files are also service-owned; they are never read from the user's workspace.

Replace the intended text input value with the literal `${PROMPT}`. Hub injects
the requested text as JSON without changing other workflow fields. Limit the
workflow to 1 MiB and exactly one saved output. The adapter uses the documented
[`/prompt`, `/history` and `/view` protocol](https://github.com/Comfy-Org/ComfyUI/blob/master/script_examples/websockets_api_example.py),
probes `/system_stats`, rejects redirects, and accepts bounded image/video bytes
(48 MiB). ComfyUI's standard saved video is exposed under `outputs.*.images`,
as defined by its [PreviewVideo output](https://github.com/Comfy-Org/ComfyUI/blob/master/comfy_api/latest/_ui.py).
Image requests wait for history within the job deadline; video uses durable
Submit/Poll handles. Optional explicit provider credentials are used only at the
configured service. This adapter does not upload edit-source files or support
arbitrary custom-node output formats.

## Durable video and uncertain outcomes

The job records `submitting` before sending the POST, then persists the accepting
engine, provider model and remote ID before polling. A restart resumes that same
handle. A lost/malformed acceptance response or provider server error can mean
the provider already accepted the job: the router stops and the job becomes
`uncertain`. An interrupted `submitting` record (or old running video without a
remote ID) is quarantined as `uncertain` on restart and is never auto-resubmitted.
Check the provider before deliberately creating a replacement job. Known
rejections may fall back; a synchronous ComfyUI request also stops on uncertain
submission. Neither job records nor receipts contain provider credentials.
