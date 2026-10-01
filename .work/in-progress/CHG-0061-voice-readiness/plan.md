# CHG-0061 voice readiness honesty (issue #170)

## Goal
`/voice` reports incoming STT and outgoing TTS readiness separately and never
promises a channel that cannot run. Text stays canonical. Live Telegram voice
evidence remains a separate deployment gate and is recorded as unavailable here.

## Changes
- `media.go`: `commandAvailable` — worker binary resolvable via `exec.LookPath`.
- `communication.go`: `voiceReadiness` — STT ready = configured transcriber +
  resolvable `HUB_STT_COMMAND`; TTS ready = configured synthesizer + resolvable
  `HUB_TTS_COMMAND` + no parked `HUB_TTS_UPLOAD_URL`.
- `voiceCommand`: status reports both directions; `/voice on` warns when TTS is
  unavailable instead of promising voice.
- `handleVoice`: unconfigured STT replies "not configured" without downloading.
- Docs: SPEC-0038, SPEC-0021 cross-ref, validation.md evidence, CHANGELOG.

## Not in scope
- Shipping `hub-stt`/TTS binaries in the base image (image bloat decision).
- Live Telegram voice round-trip — requires a real account and Docker; recorded
  as the exact unavailable gate in docs/validation.md.
