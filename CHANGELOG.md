# Changelog

## Unreleased

- Media generation sidecar `hub-media` (CHG-0063, SPEC-0040): image and video
  generation moved out of the user runtime into a standalone service sharing
  `internal/mediasvc` with `hub-stt`/`hub-tts`. Sync endpoints
  `/v1/images/generations` and `/v1/images/edits`, `GET /v1/models`, and
  durable async video jobs on the existing job store (`/v1/jobs`, persisted
  provider `RemoteID` resumes polling after a restart). Engines: `remote`
  (any OpenAI-compatible upstream, CLIProxy default) and `fal` (`fal.run`
  images, `queue.fal.run` video queue). Provider credentials are materialized
  from the Credential Broker grant named by `HUB_MEDIA_BROKER_GRANT` (identity
  `media`, `broker-secrets-media` volume) — the runtime mounts only
  `media.auth` + `HUB_MEDIA_URL` and never receives `FAL_KEY`; compose adds
  `hub-media` on the internal network with no published port when
  `image_gen` is enabled. Agent tools keep `image_generate`/`image_edit` and
  gain `video_generate` (immediate job id) and `media_fetch` (stages finished
  results under `artifacts/videos/`). Artifact delivery (SPEC-0037) gains the
  `videos` bucket at 48 MiB and Telegram `sendVideo` with a `sendDocument`
  fallback; `artifact_remove` accepts `artifacts/videos/`. Direct provider
  calls stay as the fallback when `HUB_MEDIA_URL` is unset.
- Standalone media services (issue #170): speech work moved out of the
  gateway into two independent services with separate binaries and images —
  `hub-stt` (`docker/Dockerfile.stt`, faster-whisper toolchain) and
  `hub-tts` (`docker/Dockerfile.tts`, espeak-ng+ffmpeg), sharing
  `internal/mediasvc`.
  OpenAI-compatible API (`/v1/audio/transcriptions`, `/v1/audio/speech`) plus
  a durable async job API (`/v1/jobs`) that ingests uploads, mounted user
  storage and presigned https sources, decodes via ffmpeg, transcribes
  hour-long recordings in 30 s windows and supports engine diarization.
  Engines are swappable (`command`/`remote`/`sherpa`); remote proxies to any
  OpenAI-compatible upstream — Groq, OpenAI, Speaches, whisperx with
  diarization, NVIDIA NIM. Gateway uses
  `HUB_STT_URL`/`HUB_TTS_URL` (command workers stay as fallback); `/voice`
  probes sidecar `/healthz`. Compose renders both services with per-service
  volumes and a shared `media.auth` bearer when `transcription` is on.
- Working voice path (issue #170): the image now actually carries the STT
  backend — `faster-whisper==1.2.1` is installed into the runtime venv (the
  `voice` pip extra is still excluded) and `hub-tts` (espeak-ng → ffmpeg →
  OGG/Opus) ships as the local TTS worker; the `transcription` feature now
  exports both `HUB_STT_COMMAND` and `HUB_TTS_COMMAND` to the gateway.
- `MEDIA:` artifact markers (issue #172): upstream reply lines
  `MEDIA:<workspace-path>` are extracted by the runtime — never shown to the
  user — and the named file is staged into `artifacts/images|documents`
  (copied when a tool wrote it to the workspace root) and delivered through
  the normal artifact pipeline. Works for recovered runs too since it needs
  no run-start window; unusable references surface as explicit error notices.
- Voice readiness honesty (issue #170): `/voice` now reports incoming STT and
  outgoing TTS availability separately — a direction reads "available" only
  when the worker command is configured *and* resolvable, and a parked
  `HUB_TTS_UPLOAD_URL` still counts as unavailable. `/voice on` without usable
  TTS warns that replies stay textual instead of promising voice, and voice
  input on a host without STT gets an explicit "not configured" reply without
  downloading the attachment. Live Telegram voice evidence remains a
  deployment gate; the unavailable state is recorded in docs/validation.md.
- Artifact delivery to chat (issue #172): a completed run attaches refs for
  files written under `workspace/artifacts/{documents,images}` during the run
  window; the gateway fetches bytes through the new bounded `POST /v1/artifact`
  runtime endpoint (envelope-routed at the supervisor) into `outbox/blobs`, and
  `deliverOne` sends them after the text parts as Telegram photos/documents
  with durable `sent_artifacts` progress. Slack receives an explicit
  per-artifact notice until a Slack file-upload surface exists. Fetched
  failures produce an explicit notice — a generated file is never silently
  lost. Staged blobs are removed when the delivery completes.
- Channel-specific rendering for model replies (issue #166): Markdown is
  parsed once and rendered per channel — Telegram `sendMessage` now uses
  `parse_mode=HTML` and Slack gets mrkdwn — so `###`/`**`/backticks no longer
  reach users raw. Replies longer than the channel limit split on block
  boundaries (4000 UTF-16 units Telegram, 39000 runes Slack) with durable
  per-part progress in the outbox record; a mid-sequence failure leaves an
  uncertain delivery with the exact sent boundary instead of a blind resend.
  Tables and raw model HTML degrade to monospace grids plus a one-time
  `answer.md` source document on Telegram. Hub-authored command/error replies
  stay plain text. `SendDocument` is now an explicit API surface; the hidden
  4096→document fallback inside `SendMessage` is removed.
- Added the standard web capability (issue #123), feature-gated per space:
  `features: [web]` enables the upstream Hermes `web_search`/`web_extract`
  toolset — without it the tools do not exist in the runtime
  (`agent.disabled_toolsets` also strips them from upstream composite
  fallbacks such as api_server) and `web:` settings are a validation error —
  while `features: [deep_research]` (requires `web`) mounts the bundled
  `deep-research-embedded` skill into the space's filtered `generated/skills`
  and keeps it out of `skills.disabled`. Both are init defaults and
  org-removable via `scope.yaml`. `settings.yaml` `web:` configures providers
  (tavily, exa, parallel, perplexity, firecrawl, searxng, brave-free, ddgs,
  keenable, xai, nous) with validated backend names, provider tiers, keyless
  and cache policy; keys reach the runtime through secrets env or the
  protected self-env form, never prompts. `web.search_backend` is the default
  engine and `web.search_providers` allowlists engines for parallel fan-out:
  the hub-shipped `hub-web` plugin (mounted read-only at
  `/opt/hermes/plugins/hub-web` only while `web` is on) adds `web_providers`
  (list engines, availability, default) and `web_search_multi` (one query to a
  named subset, `all`, or the default — parallel, bounded to 8 providers/60 s,
  URL-deduplicated, per-provider failure reporting; names outside the
  allowlist are rejected). The skill is a bounded state machine on disk
  (perspective plan, source registry, gap-driven rounds, distilled notes,
  outline → sections → cited report with a verify pass) whose `web.research`
  limits can only tighten the ceilings (16 rounds / 100 pages / 10 pages per
  host / 240 minutes); long runs continue across durable hub-routine wakes and
  resume cold from `state.json`.
- Fixed `hubctl supervisor` ignoring `HUB_ENV`: the shared `-env` flag
  defaults to `prod` for render/up, so an unset flag silently forced prod
  mounts on spawned runtimes; the supervisor now falls back to `HUB_ENV`
  unless `-env` is passed explicitly.
- Added a prepared DataLens connector (issue #120): exact-source entry for the
  official `datalens-tech/datalens-mcp` @96b3d6b (MIT). Five-tool gateway with
  server-side OpenAPI `x-mcp-scope` enforcement — read/write/privileged stay
  distinct effects; org scope is env-pinned outside model arguments. Broker
  contract `datalens-auth` (organization ID + Authorization header, optional
  API URL override whose host extends egress). Fixed envs select static auth
  (no `yc` in the image), bound responses, and `NODE_USE_ENV_PROXY=1` so Node
  fetch honors the workload Squid ACL.
- Added reviewed `preflight_network` catalog flag: a prepared entry whose
  server must reach its API before answering `tools/list` (remote schema fetch
  at startup) runs the unauthenticated probe with egress and placeholder
  credentials. Unprepared sources keep `--network none`.
- Added a workspace document and image capability on the hub MCP profile:
  extract, create, edit, and convert for txt, md, csv, html, pdf, xlsx, and
  pptx; inspect and local png, jpeg, and webp conversion; opt-in image
  generation and edit with a configured provider, model, and delivery. pdf,
  xlsx, and pptx are simple text packages. doc and docx stay unsupported.
  CLIProxy image models use two calls: the images-endpoint ids stay on
  `/images/generations` and `/images/edits`, and the Gemini image ids use
  `/chat/completions`. The default model stays `gpt-image-2`.

## 0.3.0 � M1 native execution � 2026-09-12

- Removed the `hermes -z` executor and persistent-mode toggle after publication
  and real-runtime acceptance of the v0.2.1 compatibility artifacts.
- Kept native static execution for explicit selection and unmigrated cron;
  existing legacy selections read as static, and CLI-only contexts remain idle.
- Enforced supervisor drain for environment-selected migration and fixed Linux
  fixture host resolution. Homes, sessions, spool and uncertainty remain preserved.

## 0.2.1 — M1 compatibility release — 2026-09-12

- Added host-selected per-context supervisor rollout and rollback without moving
  Hermes homes, queued jobs, session/run mappings or delivery state.
- Added authenticated job admission/resume, cancellation, streamed events and
  approval fencing with durable uncertainty and automatic infrastructure recovery.
- Verified two-user isolation, idempotency, leases, automatic five-minute idle stop,
  cold session restoration, orphan ownership and due routine wake/sleep against
  pinned Hermes 0.21.0 in real Docker containers.
- Retained static/one-shot rollback for this compatibility release. Legacy removal
  follows acceptance of this released artifact; enabled native cron blocks migration.

- Added the channel-neutral `hub-communication` gateway with Telegram sender mapping,
  durable job/reply spool, scoped Hermes subprocesses, commands and best-effort secret
  message deletion.
- Added Telegram-visible service catalog and self-service connector enablement with
  allowlisted env provisioning, isolated feature state and supervisor restart.
- Added bundled GitLab `glab` with PAT env auth, Google read-only default with explicit
  write opt-in, and Atlassian Rovo personal API-token auth.
- Added organization/user scope overlay: membership checks, policy narrowing,
  separate organization secrets, read-only organization documents and action gates.
- Separated Telegram bot transport from personal-account MCP and added explicit
  user-runtime `env_update` with constrained persistent overlay and supervisor restart.
- Replaced local Jest/Python tooling and the Python container supervisor with Go and Just.

## 0.2.0 — 2026-09-08

- Standalone Hermes: removed embedded CareerGo and JobFetch adapter.
- spaces/user, separate dev/prod env files, Docker targets and persistent volumes.
- Generic MCP, native skills/hooks/memory and source mounts excluding private spaces.
- Original local CI and coverage gates, self-hosted Runner workflow and AGPL-3.0-only.

## 0.1.0 — 2026-09-07

- Private per-person Hermes deployments with Go CLI and Docker/VPS configuration.
- Pinned MCP connectors, browser desktop, Meet caption and local speech setup.
- Bounded files/archive, HH application contract and JobFetch tenant paging.
- Bundled career-go source and authenticated native tools companion.
- Memory Bank documentation, regression tests, just gates and GitHub workflows.
- Live account and container validation limits recorded separately.
