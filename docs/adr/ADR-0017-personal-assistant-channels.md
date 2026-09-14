---
description: Telegram and Slack stay communication channels; voice, routines and context data stay hub-owned around native Hermes.
last_verified: 2026-09-14
---
# ADR-0017: Personal assistant channels, voice and context lifecycle

Accepted 2026-09-14.

## Decision

Keep Telegram Bot API and Slack App as communication-hub ingress and delivery
only. Verified sender identifiers map to one principal. Bot and app tokens
never enter Hermes runtime env or ToolHub connectors. Slack Events API uses
the official `v0` HMAC. Slack data tools remain the separate `slack` feature.
Personal Telegram MCP remains a later connector.

Canonical conversation results stay text. Voice is a bounded envelope plus
replaceable STT/TTS workers. The pinned image already ships faster-whisper
for STT; it does not advertise `audio_api`/`realtime_voice`, so the hub does
not invent a second native TTS pipeline. TTS is opt-in and falls back to text.

The hub remains the only clock for routines (ADR-0014 / SPEC-0013). Native
Hermes memory, profile and skills stay in the owning home. Honcho is an
official opt-in config file, not a guessed API. Backup/restore/purge are
hubctl operations that exclude plaintext secrets.

## Consequences

Groups stay disabled until a disclosure policy. Live Telegram/Slack/Honcho
account login or sending still needs an explicit later owner command. Fixture
evidence must not be reported as live provider success.

## Related records

SPEC-0021, SPEC-0013, ADR-0014, ADR-0007, ADR-0009 and issues #29-#36.
