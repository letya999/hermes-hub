---
status: active
title: Personal assistant channels, voice, routines and context lifecycle
---

# Personal assistant experience

1. Telegram Bot API and Slack App remain communication ingress/delivery only.
   A verified Telegram numeric sender ID or Slack workspace+user ID maps to
   exactly one principal. Bot and app tokens stay in communication-hub and are
   never mounted into Hermes or ToolHub connectors.
2. Private DMs are in scope. Groups and Slack channels without an explicit
   private mapping are denied until a separate audience disclosure policy.
3. Conversation identity maps to a durable Hermes session through existing
   spool mappings. `/start`, `/status`, `/connections`, `/connection`,
   `/session`, `/voice`, `/cancel`, `/approve` and `/routine` have explicit
   routing. Duplicate provider events do not duplicate jobs or deliveries.
4. Slack Events API signatures use the official `v0:` HMAC over the raw body
   and timestamp. Communication permission `slack_app` does not create Slack
   read/write data tools. Events never select a personal Slack connection by
   email or display name.
5. Voice and audio updates with empty text are not dropped. Media uses a
   bounded envelope (file id, MIME, size, duration, timeout, temp path,
   transcript status, conversation identity). STT failure yields a safe
   user-visible reply; the canonical result remains text. Temporary files are
   deleted. Local faster-whisper on the pinned image is the supported STT
   path. TTS is opt-in per conversation or explicit request and falls back to
   text. Third-party voice upload does not occur unless explicitly configured.
6. Hub-owned schedules follow SPEC-0013. `routine_create`/`list`/`update`/
   `pause`/`delete` enforce server-side ownership. Occurrences are ordinary
   idempotent jobs keyed by schedule id, time and revision. Native Hermes cron
   is never dual-run; unmigrated native cron refuses hub schedules.
7. Native Hermes memory and user profile live in the owning `spaces/<id>/hermes`
   home. Another user or context cannot read them. Users can list and delete
   memory files. Honcho is opt-in via the official `$HERMES_HOME/honcho.json`
   contract and must not break native memory when unset or failing.
8. User skills are writable only in the user home. Global and organization
   skills are curated read-only mounts through `skills.external_dirs`. Install
   and update do not rebuild the Hermes image. Revoked skills leave the
   advertised set. Skill text cannot grant provider mutation.
9. `hubctl context` backup/restore/export/purge distinguish stop compute,
   delete compute and purge user data. Snapshots cover Hermes home, workspace,
   connection metadata, ToolHub bindings, schedules and delivery ledger.
   Plaintext secrets stay on `hubctl secret backup`. Restore refuses another
   user, does not reactivate revoked credentials and does not duplicate cron
   deliveries. Purge requires an explicit matching target and reports paths
   without secret values. Backups are verified before success.

# Acceptance

- Foreign Telegram/Slack identities cannot read another home.
- Replay/duplicate events create one job and one delivery.
- Groups remain disabled.
- Fixture Slack HTTP with a valid signature is sufficient; a live workspace is
  not required.
- Fixture Telegram voice updates are sufficient; live Telegram send is a
  separate owner command.
- `just check` keeps own Go statement coverage at or above 85%.
