---
status: active
title: Voice readiness reporting (issue #170)
---

# Voice readiness

1. `/voice` reports incoming STT and outgoing TTS readiness separately. A
   direction is "available" only when its worker is configured *and* the
   configured command resolves to a runnable binary (`HUB_STT_COMMAND`,
   `HUB_TTS_COMMAND`). A set-but-missing binary reads "недоступно", not
   "configured".
2. TTS also reads unavailable while `HUB_TTS_UPLOAD_URL` is set: the
   third-party upload path is parked and `sendVoice` is skipped, so promising
   spoken replies would be false.
3. `/voice on` on a host without usable TTS still records the per-conversation
   opt-in, but the reply warns that answers stay textual. It never claims
   voice is ready.
4. Voice input on a host without STT gets an explicit "not configured" reply
   without downloading the attachment. A configured-but-failing STT keeps the
   existing failure reply. Text remains the canonical record; a failed TTS
   never eats or duplicates the text answer.
5. Live evidence (real Telegram voice note → transcript → text answer, and
   opt-in reply → `sendVoice` in the same topic, with provider, language,
   latency and size recorded) is a separate deployment gate. Fixture tests are
   not live proof; the unavailable gate is documented in
   `docs/validation.md` until a real-account run lands.

# Acceptance

- `/voice` output distinguishes STT and TTS and matches actual reachability.
- No message claims voice availability when a worker command is unset,
  unresolvable, or the upload path is parked.
- `just check` keeps own Go statement coverage at or above 85%.
