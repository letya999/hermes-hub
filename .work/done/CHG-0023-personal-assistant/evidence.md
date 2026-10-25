---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0023 personal assistant experience

M4 keeps Telegram Bot API and Slack App as communication-hub ingress/delivery.
In-repo tests drive shipped `handleUpdate` for private allowlisted DMs, foreign
sender isolation, groups disabled, commands, files, edits, duplicate updates,
empty-text voice envelopes, STT errors, opt-in TTS text fallback, official Slack
`v0` HMAC Events API fixtures, routine CRUD/ownership and hubctl
backup/restore/purge.

On 2026-09-14, `just check` passed, including race tests, formatting, vet,
staticcheck, documentation/workflow checks and 85.02% original Go statement
coverage. `go.mod` and `go.sum` were not changed, so `just security` was not
required. `just docker-check` could not rebuild: `docker build` failed to fetch
`github.com/korotovsky/slack-mcp-server` (`github.com:443` timeout). Evidence
used the existing pinned image `hermes-hub:0.3.0-runtime`
`sha256:c74d6046795a9b158bd89030ae25ba4f7f86a8cee07573bb1307120539eb884b`.
Standalone Docker runtime smoke passed with Hermes Agent 0.21.0; no live
provider calls. `hermes-contract` on that image passed: operator pin, idle
no-wake, due routine wake/final/handoff/sleep, host supervisor smoke, gateway
five-minute lifecycle, and the pinned Hermes 0.21.0 API contract.

faster-whisper on that image, driven with spoken fixture `hello.wav` (not a
sidecar mock), produced a non-empty `TRANSCRIPT=` line on two runs. A synthetic
440Hz tone is not treated as a transcript. Hugging Face Hub download warnings
are not treated as transcripts. Live Telegram voice send, live Slack workspace
events, and live Honcho are not claimed. Fixture Telegram updates and signed
Slack HTTP are the channel evidence. The pinned image still reports `audio_api`
and `realtime_voice` as false; TTS is an opt-in command worker with text as the
canonical delivery.
