---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0061 voice readiness (issue #170)

`/voice` now reports incoming STT and outgoing TTS readiness separately. A
direction reads "available" only when its worker command is configured *and*
resolves to a runnable binary; a parked `HUB_TTS_UPLOAD_URL` still reads TTS
unavailable because `sendVoice` is skipped on that path. Voice input on a
host without STT gets an explicit "not configured" reply without downloading
the attachment. Covered by unit tests over the readiness matrix.

Delivery semantics tightened after live use: the per-conversation
`/voice on|off` mode was removed entirely — speech is a per-reply tool. A
`VOICE:` marker line in the model reply requests speech: marker-only replies
are voice-only, visible text plus a marker sends both. Failed synthesis or a
parked upload URL falls back to sending the spoken payload as text.
Generated audio is held in memory and sent directly to Telegram; it is never
written to the spool, workspace or the TTS sidecar `/data`, so it needs no
TTL — the spoken text remains the durable content. Stored `voice_replies`
flags in old conversation mappings are dead data and ignored.

The image itself was the real blocker: `hub-stt` shipped in the image but
`faster-whisper` was deliberately excluded from the pip extras, so a
configured `HUB_STT_COMMAND` always failed at import. The image now installs
`faster-whisper==1.2.1` alone (not the whole `voice` extra) and ships
`hub-tts` (espeak-ng → ffmpeg → OGG/Opus), which the `transcription` feature
exports as `HUB_TTS_COMMAND`.

Live Telegram evidence is **not** claimed: this host has no Docker runtime and
no real bot account, so the end-to-end voice note → transcript → text answer →
`sendVoice` round-trip stays unproven until a rebuild + deploy. The exact
gate to run on a deployed host: image rebuilt with faster-whisper,
`transcription` feature on, a real Telegram DM voice note, `/voice on`, and a
reply — record model/provider, language, latency and file size.
