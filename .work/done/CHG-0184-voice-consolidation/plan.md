# CHG-0184: Voice consolidation (M26 stage 2)

Chain: `docs/adr/ADR-0083-lean-images-capability-packs.md` D4 ->
`specs/active/SPEC-0082-image-role-budgets.md` R2 -> this plan -> `state.yaml`.
GitHub issue: #375. Previous stage: `CHG-0183`. Next stage: `CHG-0185`.

## Goal

hub-stt/hub-tts services become the only voice path. ~330MB of
whisper/onnxruntime/espeak and the never-taken script fallback leave every
hub image; STT/TTS images themselves get hash-pinned installs.

## Read before coding

1. `internal/stack/compose.go` (~L541-549 STT/TTS env wiring),
   `internal/communication/media.go` (worker selection),
   `internal/voice/transcription.go`, `docker/hub-stt`, `docker/hub-tts`,
   `docker/Dockerfile.stt`, `docker/Dockerfile.tts`,
   `internal/devcheck/testdata/hermes_capabilities.py`.
2. `docs/specs/audio.md`, `docs/integrations.md`, `docs/tools.md`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; Docker gates when image/runtime code changes.

## Slices

### S2.1 Single voice path (R2.1, R2.2)

Anchors: `internal/stack/compose.go` `sttTools`/`ttsTool` envs;
`internal/communication/media.go` `pickTranscriber`.

Change:
1. Render only `HUB_STT_URL`/`HUB_TTS_URL`; drop command-path envs.
2. Remove `docker/hub-stt`/`docker/hub-tts` copies and the
   `uv pip install aiohttp faster-whisper av` line from the main Dockerfile.
3. Transcription disabled -> typed `voice unavailable` status to the owner
   (not a dead exec).

Tests (red first):
- `TestComposeRendersServiceOnlyVoice`: generated compose has URL envs, no
  command envs.
- `TestVoiceUnavailableStatus`: disabled transcription reports typed status.

### S2.2 Hash-pinned STT/TTS images (R2.3)

Anchors: `docker/Dockerfile.stt`, `docker/Dockerfile.tts`.

Change:
1. Compile requirements with `uv pip compile --generate-hashes` (checked in);
   `pip install --require-hashes`.

Tests (red first):
- `TestSttRequirementsHashed`: requirements file contains hashes; Dockerfile
  uses `--require-hashes`.

## Red-team checklist

- `transcription` off: no exec path reaches a missing binary (tested).
- HF model cache stays per-space; no cross-space cache mount.

## Acceptance

- A2: telegram voice note -> hub-stt -> transcript; TTS reply via hub-tts on
  the dev stack.

## Out of scope

- Replacing faster-whisper inside hub-stt with a Go/sherpa engine.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Issue #375 closed; folder moved to `.work/done/`.
