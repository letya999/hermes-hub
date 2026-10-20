# CHG-0071 — Managed compression, settings tools, image gen, STT/TTS, second user

## Goal

Operator request (verbatim, translated):

1. Enable context compaction and add settings management that is as safe as
   tools (capability-gated, audited, bounded).
2. Diagnose why image generation failed and enable it.
3. Add hub-stt/hub-tts containers to the stack and bring them up.
4. Enable a second Telegram user from the existing allowlist (480637186).
5. Ensure arbitrary GitHub MCP install (prepare_source) works for both users.
6. Ensure both users can use files, web, images, documents, browser, STT, TTS.
7. Verify /usage, sessions, connections commands end-to-end. Docker changes go
   through server-side (GitHub Actions) builds.

## Findings that shape the implementation

- `compression.enabled: false` was a deliberate guard: detached gateway
  hygiene/curator sub-agents receive literal `enabled_toolsets` and bypass the
  denylist. Verified against the pinned image: the hygiene agent lives in
  `gateway/run_turn.py` (platform message path). Managed runtimes serve only
  the api_server platform (platform_toolsets all empty) and all ingress is the
  Go communication-hub, so the detached agent is unreachable; the in-loop
  `ContextCompressor` is a plain auxiliary LLM call with no tool surface.
  Enabling compression is safe on this path; `curator` stays off.
- `image_generate` fails because the managed effective config never renders an
  `image_gen:` block, so `media.Session.ImageGranted()` fails closed. The
  capability grant, hub-media sidecar, broker grant and cliproxy upstream are
  all already deployed.
- STT/TTS render paths already exist, gated on `transcription` tool entry:
  `hub-stt`/`hub-tts` services, `HUB_STT_URL`/`HUB_TTS_URL` gateway wiring,
  media.auth shared bearer. Missing: the tools entry and published images.
- Multi-user is `communication.users.yaml` (mounted when present) plus a
  per-user space dir under spaces/<user>; the shared ToolHub store keys
  bindings/profiles by principal_id.
- Settings overlay model: `self-settings.json` under the managed runtime
  state dir is writable by the in-runtime executor and readable by the
  supervisor at spawn-time materialization and by the runtime attestation,
  keeping both sides of the DeepEqual consistent.

## Changes

### Code

- `internal/selfsettings` (new): reviewed allowlist of tunable managed
  settings (compression.* family), typed validation, atomic JSON overlay,
  dotted-key merge into the effective config map.
- `internal/stack`:
  - `managedHermesConfig`: `compression: {enabled, in_place, micro_compact,
    threshold_tokens}` on; `image_gen:` block rendered when the operator set
    `image_gen` in agent.yaml (normalized provider/model/delivery).
  - `MaterializeOptions.SelfSettingsPath` + `ImageGen` plumbed through
    materialize/validate so all three producers (render, supervisor spawn,
    runtime preflight) compute the identical file.
  - `tools.go`: `settings` ToolHub family; `transcription` already a native
    pseudo-toolset.
- `internal/agenttools`: `settings_get`/`settings_set` tools
  (capabilities `settings.read`/`settings.update`), overlay write +
  supervised restart.
- `internal/runtime/toolhub_config.go`: read `HUB_SELF_SETTINGS_PATH` /
  `HUB_MANAGED_IMAGE_GEN` for attestation parity.
- `.github/workflows/ci.yml` + `internal/devcheck`: publish stt/tts images
  server-side alongside dev/prod.

### Deployment

- workspace.yaml: `settings: toolhub`, `transcription: native`;
  agent.yaml: `image_gen:` cliproxy chat-image model.
- Republish capability policy + profile (new implementation digest) and
  re-register the agent-tools definition/binding.
- `spaces/telegram-480637186/` space + users file entry + own policy/profile
  + agent-tools binding; comm-hub users file lists both identities.
- Re-render, pull server-built images, recreate the stack, respawn runtimes.

## Verification

- `go test` for touched packages; `just check`; `just security`.
- Live: supervisor spawn, `settings_get`/`settings_set` roundtrip through the
  audited ToolHub path, `image_generate` producing an artifact, voice note
  STT + `VOICE:` TTS reply, `/usage`, `/sessions`, `/connections`, a
  `prepare_source` install from a public GitHub repo per user, second-user
  isolation.
