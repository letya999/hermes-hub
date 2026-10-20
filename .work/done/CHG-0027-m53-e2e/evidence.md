---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0027 M5.3 onboarding (in progress)

- A generic control-MCP regression proves that an ordinary canonical GitHub
  repository URL is resolved to a full commit SHA before the reviewer sees it,
  and that only the repository plus SHA are persisted on onboarding.
- The streamable-HTTP control MCP now delivers standard progress notifications
  for source review/build, credential/OAuth requirement and workload readiness;
  its regression uses the real HTTP MCP transport.
- These repository tests do not prove Telegram delivery, provider login,
  Hermes dynamic discovery or a Notion data read. Those remain live acceptance
  gates and require explicit account-login instruction. Separate local Docker
  evidence below covers the immutable Notion artifact and ToolHive admission.
- The GitHub read guard resolved the public Notion repository to commit
  `1d38420769c8a1fe2d583ff1e7d2d108d4eb0b30`. The restricted Docker builder
  produced OCI manifest `sha256:6dbad51ce3168352093f49e796717079d5e173130339aaf9a4dfbc3b8508730d`
  with provenance and SBOM evidence, without credentials. This proves the
  immutable import/build slice, not ToolHive start or Notion account access.
- An isolated `--network none`, non-root, read-only-root MCP initialize and
  `tools/list` succeeded for that image. The live response exposed both explicit
  read-only and destructive annotations. A regression fixed preflight to trust
  read-only only when `readOnlyHint=true` and `destructiveHint` is not true;
  destructive or unannotated tools now fail closed to write policy.
- Stock ToolHive v0.49.0 admitted the built image through the explicit Docker
  fallback. The proxied endpoint completed MCP initialize and `tools/list`,
  exposed only the reviewed `API-get-self` tool, and repeated admission reused
  the same workload and endpoint; all disposable resources were removed after
  the probe. Credential-bearing onboarding now validates this receipt before
  publishing its binding/projection, with regression coverage for rollback and
  re-enable.
- `just docker-check` also passed the production image build, standalone
  Docker smoke, and the pinned Hermes 0.21.0 lifecycle/API contract. The
  faster-whisper fixture was unavailable on the shipped image because
  `/usr/local/bin/hub-stt` was absent; no external provider account was used.
- Live local ToolHub acceptance initially failed because the administrator-provisioned
  `spaces/local/runtime/artifacts` directory was missing. Runtime rendering now
  provisions it; after repair, the real Notion source resolved to commit
  `1d38420769c8a1fe2d583ff1e7d2d108d4eb0b30`, built to OCI, and passed MCP
  preflight with generic `NOTION_TOKEN` discovery. Preflight no longer duplicates
  discovered credentials into the ordinary environment contract, and wildcard
  listeners now emit protected-form links at `127.0.0.1`.
- The live onboarding is currently `awaiting-credentials`; no Notion token was
  entered, no workload was enabled, and no Notion data read or Telegram delivery
  is claimed. The loopback form is intentionally usable only from the same host.
- Retrying a self-install for the same repository commit now reuses the existing
  user-owned immutable definition instead of rebuilding it; expired credential
  forms rotate their nonce and expiry on resume.
- Final post-fix gates: `just check` passed with 85.06% Go statement coverage;
  `just docker-check` passed the prod image, standalone smoke and pinned Hermes
  contract/lifecycle. The pre-existing `/usr/local/bin/hub-stt` fixture warning
  remains unrelated.
- hub-tts voice upgrade: Silero `v5_5_ru` (snakers4, MIT) baked into the
  image on CPU torch — auto-stress + homographs for Russian; piper voices
  irina/ruslan/dmitri and espeak remain as fallbacks. Voice routing:
  request `voice` picks a silero speaker, `piper/<name>` or `espeak`;
  `HUB_TTS_LANG_VOICES` maps bare language codes to a primary voice;
  `HUB_TTS_DEVICE` switches cpu/cuda; `HUB_TTS_BACKENDS` orders fallbacks.
  External vendors: `remote` engine covers OpenAI-compatible TTS; new
  `elevenlabs` engine (HUB_MEDIA_UPSTREAM_KEY + voice id) calls the native
  ElevenLabs API. E2E verified on the live stack: silero xenia synthesized
  a Russian sentence which whisper-small then transcribed verbatim.
