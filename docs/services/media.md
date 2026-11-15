---
description: hub-media, hub-stt, hub-tts — media sidecars carrying provider credentials; sync image gen plus durable async jobs.
last_verified: 2026-11-12
---

# media services (hub-media, hub-stt, hub-tts)

Three standalone sidecar roles, one code family (`internal/media`,
`internal/mediasvc`). Each listens on its own container `:8090` and never
mounts the docker socket. The shared `hub-media` uses the infra owner's
Broker grant; a managed space may instead declare `hub-media: per-space` and
use its own media bearer and model relay.

Declare shared services in the infra owner's authored stack passport and
references to them in participating user stacks. A private image account uses
`hub-media: per-space` in its own stack passport. Selecting `media` alone does
not start undeclared containers. Set the channel voice in/out contract in
config.yaml. Managed `media_transcribe` receives `HUB_STT_URL` and the shared
media bearer when hub-stt is declared; its multipart upload uses the admitted
filesystem root and bounded bytes. `media_speak` runs in ToolHub against the
verified live run and queues the communication worker's voice delivery. It never
accepts a model-selected recipient or returns a queued record as proof of sending.

The broker workload/binding is `credentialbroker.MediaBinding(infra-owner)`,
stable per owner so a retired infra owner's grant cannot block its replacement.
TTS uses the hash-pinned `torch==2.8.0+cpu` wheel; its image asserts CUDA is absent.
The sidecar command engine requests canonical WAV from the worker through
`HUB_TTS_OUTPUT_FORMAT=wav`; direct gateway workers retain their OGG/Opus output.

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

For `hub-media: per-space`, the runtime uses `hub-media-<owner>:8090` and the
owner's `media.auth`. The sidecar calls `model-relay-<owner>:8318` using a
separate generated `media-relay.auth` file containing only that owner's relay
bearer. The provider credential stays behind the owner's model relay and
Broker grant. This mode requires a managed space and a concrete allowlisted
chat-image model in `image_gen`; the owner's `models.yaml` must publish an
`image` route to that provider model. It does not mount the infra media signing key
or accept the infra media grant. A shared `hub-media` remains available for
spaces that deliberately use the shared image account.
Changing this service mode recreates the owner's egress relay before the
runtime respawns, so its allowlist includes the private media hostname.

## Data

Each role keeps durable job data on its own volume (`hub-<role>-data` →
`/data`). STT/TTS also set `HF_HOME=/data/hf` and `XDG_CACHE_HOME=/data/cache`
for model caches. Shared media uses `hub-media health`, which checks upstream
readiness at `/healthz`. Per-space media uses `hub-media health-local` at
`/livez`: during `space apply`, ToolHub deliberately denies model decisions
until the recovery journal is removed, so waiting for upstream readiness
inside that transaction would prevent it from finishing. Verify the provider
with a real generation after publication.

## Fallback

Voice configured in `config.channels.*.voice` renders the shared `hub-stt` and
`hub-tts` services and their auth file, even without a selected native
`transcription` flag. The gateway uses those service URLs; a missing service
returns unavailable. The resident local STT worker returns the recognizer's
segment start/end times and honors a supplied two-letter language. Local TTS
requests allow 90 seconds for model startup and synthesis.

The gateway picks the synthesis voice per reply language:
`HUB_TTS_VOICE_<LANG>` (`EN`/`RU`/`KK`/`ES`) overrides `HUB_TTS_VOICE`, which
stays the default for unmapped languages. With channel voice configured, compose
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
