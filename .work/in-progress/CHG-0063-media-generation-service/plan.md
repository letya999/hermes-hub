# CHG-0063: hub-media — sidecar для генерации медиа

## Контекст

Генерация картинок сейчас живёт внутри user-runtime (`internal/media` → прямые вызовы
cliproxy/fal), ключи провайдеров лежат в env рантайма. Выносим это в отдельный сервис
`hub-media` — соседний контейнер рядом с `hub-stt`/`hub-tts`, на общем каркасе
`internal/mediasvc` (engine-абстракция, bearer auth, durable job store, requeue, TTL).

Не MCP: нативные тулы `image_*`/`media_*` в рантайме ходят в сервис по внутреннему
HTTP. Артефактный конвейер SPEC-0037 не меняется по сути — добавляется только bucket
`artifacts/videos`.

## Контракт сервиса (порт 8090, bearer, internal network only)

```
POST /v1/images/generations   {model, prompt, size?}      → image bytes + Content-Type
POST /v1/images/edits         multipart {image, model, prompt} → image bytes
POST /v1/jobs                 {kind:"video", model, prompt}  → 202 {id, status}
GET  /v1/jobs/{id}            → {id, status, kind, error?}
GET  /v1/jobs/{id}/result     → media bytes + Content-Type
DELETE /v1/jobs/{id}          → 204 (queued/done/failed; running → 409)
GET  /v1/models               → {image_models, video_models}
GET  /healthz                 → engine Ready()
```

## Фаза 1 — картинки

- `internal/mediasvc/config.go`: `RoleMedia`, поля `ImageModel`/`VideoModel` +
  allowlist `ImageModels`/`VideoModels` (env `HUB_MEDIA_IMAGE_MODEL[S]`,
  `HUB_MEDIA_VIDEO_MODEL[S]`); role media → engine `remote|fal`.
- `internal/mediasvc/gen.go`: `GenEngine` (Generate/Ready), `AsyncEngine`
  (Submit/Poll с resume по RemoteID), реализации:
  - `remote` → `{upstream}/v1/images/generations` + `/images/edits` (multipart),
    key из `HUB_MEDIA_UPSTREAM_KEY` || `OPENAI_API_KEY`.
  - `fal` → sync `fal.run/{model}` → `images[0].url` → fetch байтов через
    allowlist `HUB_MEDIA_FETCH_HOSTS` + private-IP refuse (реюз fetch.go dial).
    Async video через `queue.fal.run/{model}` (request_id/status/result).
    Key: `HUB_MEDIA_UPSTREAM_KEY` || `FAL_KEY`.
- `server.go`: поле `gen`, routes для role media, `/v1/models`, jobs принимают
  `kind:"video"` только в media-роли; `Job` += `Kind,Prompt,RemoteID,Mime`;
  `runGenJob`: submit → poll (3s, cap 30min) → `result.bin`+mime. Restart:
  RemoteID set → resume poll, иначе resubmit.
- `cmd/hub-media/main.go` — копия каркаса hub-stt + subcommand `health`.
- `docker/Dockerfile`: собрать `hub-media`, COPY в образ (тот же image +
  `entrypoint: ["hub-media"]` в compose — новый Dockerfile не нужен).
- `internal/media/service.go`: `HUB_MEDIA_URL`+`HUB_MEDIA_AUTH` →
  generate/edit через сервис; bytes → `artifacts/images/name.ext` (mime→ext).
  Service mode = всегда workspace delivery, provider credential не нужен.
  Fallback на прямой провайдер остаётся при пустом `HUB_MEDIA_URL`.
- `internal/stack/render.go`: при feature `image_gen` в infra-проекте —
  сервис `hub-media` (тот же image tag, env_file media.auth),
  volume `hub-media-data` + `broker-secrets-media` (ro); в runtime env
  `HUB_MEDIA_URL` + env_file media.auth; `ensureMediaAuth` также при
  `image_gen`.
- Provider-ключи — через Credential Broker, не env-файлом:
  `internal/mediasvc/broker.go` (acquire/materialize/release + кеш 5 мин),
  `HUB_MEDIA_BROKER_GRANT`, identity `media`/`hermes-media` (audiences
  broker:control+runtime), `HUB_CREDENTIAL_BROKER_MEDIA_*` в compose;
  `media.<env>.env` больше не рендерится. Env-fallback
  (`HUB_MEDIA_UPSTREAM_KEY`/`OPENAI_API_KEY`/`FAL_KEY`) только для
  standalone-запуска вне стека.

## Фаза 2 — async видео

- `internal/mediasvc`: `runGenJob` (выше) + `GET .../result` отдаёт `result.bin`.
- `internal/media`: `VideoGenerate(name,prompt)` → POST /v1/jobs → job_id;
  `MediaFetch(jobID,name)` → status; done → GET result → `artifacts/videos/`.
- `tools.go`: `video_generate`, `media_fetch` под `ImageGranted()`.
- `internal/runtime/artifacts.go`: bucket `videos` (mp4/webm/mov, лимит 48MiB),
  stageMediaArtifact по mime `video/` → videos.
- `internal/communication`: `SendVideo` для video/mp4|webm (fallback Document),
  лимиты: photo/voice 8MiB, document/video ≤48MiB (Telegram bot cap 50MB).

## Не делаем

Локальные модели (ComfyUI), роутинг/fallback по цене, webhooks, dashboard,
документы-сервис — фаза 3, issue в GitHub.

## Тесты

mediasvc: auth, генерации/edits контракт (httptest upstream), fal sync+queue
(mock), job lifecycle + restart resume по RemoteID, /v1/models.
media: service-mode generate/edit/fetch через httptest сервис.
artifacts: videos bucket + лимиты. communication: SendVideo multipart.
render: сервис только при image_gen, ключи не в runtime env.

## Гейты

`go test ./...`, `just check`, `just docker-check` (если Docker доступен).
