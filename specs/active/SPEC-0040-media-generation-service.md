---
status: active
title: hub-media generation sidecar (phase 3 tracked in issue #182)
---

# Media generation sidecar

1. Image and video generation run outside the user runtime in one standalone
   service: `cmd/hub-media` (`hub-media` binary inside the shared hub image,
   entrypoint-selected), sharing the `internal/mediasvc` library with
   `hub-stt`/`hub-tts`. Provider credentials never enter an env file: the
   service materializes them per call from the Credential Broker grant named
   by `HUB_MEDIA_BROKER_GRANT` (identity `media`/`hermes-media`, audiences
   `broker:control` + `broker:runtime`, keypair in the `broker-secrets-media`
   volume). The presented `policy_version` is the stable string `hub-media`,
   not the per-config policy hash — a settings change must not orphan the
   provisioned grant. The runtime receives the service URL and the shared `media.auth`
   bearer — never `FAL_KEY` or an upstream key. Without a grant the engines
   fall back to `HUB_MEDIA_UPSTREAM_KEY`/`OPENAI_API_KEY`/`FAL_KEY` process
   env, which exists only in non-rendered standalone runs.

2. The HTTP surface is `POST /v1/images/generations` and
   `POST /v1/images/edits` (multipart `image` + `model` + `prompt`),
   synchronous, returning media bytes with the provider-declared
   `Content-Type`; `GET /v1/models` lists the allowlisted image and video
   model ids; `GET /healthz` reports engine readiness. Video is asynchronous
   on the existing durable job API: `POST /v1/jobs` with `kind: "video"`,
   `GET /v1/jobs/<id>`, `GET /v1/jobs/<id>/result`, `DELETE /v1/jobs/<id>`.
   Jobs persist under `HUB_MEDIA_DATA`, survive restarts (a stored provider
   `RemoteID` resumes polling instead of resubmitting) and expire by
   `HUB_MEDIA_JOB_TTL`; `HUB_MEDIA_JOB_TIMEOUT` caps one job.

3. Engines are swappable via `HUB_MEDIA_ENGINE`:
   - `remote` — any OpenAI-compatible upstream (`HUB_MEDIA_UPSTREAM`; key
     from the broker grant, or `HUB_MEDIA_UPSTREAM_KEY`/`OPENAI_API_KEY`
     env fallback): CLIProxy,
     OpenAI, or a self-hosted images server. Image allowlists
     (`HUB_MEDIA_IMAGE_MODELS`) route images-endpoint ids to
     `/v1/images/generations` and `/v1/images/edits`; chat-image ids
     (`HUB_MEDIA_CHAT_MODELS`) go to `/v1/chat/completions` with modalities
     `image`+`text`. Readiness probes `/v1/models`.
   - `fal` — `fal.run` for synchronous images; `queue.fal.run` submit +
     status/response polling for video jobs. The credential comes from the
     broker grant (or `FAL_KEY`/`HUB_MEDIA_UPSTREAM_KEY` env fallback). Fal
     edit is rejected.

4. Provider result bytes are validated before they reach a caller: the
   declared mime must be one of image/png, image/jpeg, image/webp for images
   or video/mp4, video/webm, video/quicktime for jobs; the body is bounded
   (images 8 MiB, job results 48 MiB — under the 50 MB Telegram bot upload
   bound). Remote fetch requires `HUB_MEDIA_FETCH_HOSTS` allowlisting and
   refuses private/non-routable resolved IPs — the endpoint cannot become an
   SSRF proxy. Prompts are bounded (2000 runes) and a known credential value
   inside a prompt or payload fails the call.

5. Runtime integration: `HUB_MEDIA_URL` + `HUB_MEDIA_AUTH` select the service
   client inside `internal/media`; an empty URL keeps the direct-provider
   fallback for non-stack runs. The agent-facing tools keep their names —
   `image_generate`, `image_edit` — plus `video_generate` (returns a job id
   immediately) and `media_fetch` (polls a job and stages the finished result
   under `artifacts/videos/`). Service mode is always workspace delivery: a
   provider URL is never returned to the agent.

6. Compose renders `hub-media` on the internal network with no published
   port, its own `hub-media-data` volume, `media.auth` env_file, the
   `HUB_CREDENTIAL_BROKER_MEDIA_*` client env, the `HUB_MEDIA_BROKER_GRANT`
   passthrough and a read-only `broker-secrets-media` mount when `image_gen`
   is enabled. No `media.<environment>.env` is rendered. The runtime's
   env_file list gains `media.auth` in the same condition. Secondary spaces
   reach the one deployed instance over the shared network, like toolhub.

# Acceptance

- Unit/integration fixtures cover bearer enforcement, model allowlists,
  unsafe mime and oversize rejection, fetch host allowlisting, the durable
  video lifecycle including restart resume, credential absence from runtime
  env, broker grant materialization (acquire/materialize/release against a
  TLS fake broker, lease release on failure, short-TTL memoization) and the
  chat fallback path. Live evidence (one generated image and one delivered
  video through a deployed stack) remains a deployment gate.
