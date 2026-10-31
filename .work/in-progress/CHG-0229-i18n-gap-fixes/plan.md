# CHG-0229: i18n gap fixes — model lang pin, skill-draft card, assignment replies, Slack commands

Chain: [ADR-0084](../../../docs/adr/ADR-0084-channel-agnostic-user-locale.md)
(D3/D4/D5) -> this plan -> state.yaml. Follow-up to done CHG-0191: the audit of
2026-10-29 found surfaces that escaped the original localization pass.

## Goal

Finish the user-locale migration so every deterministic owner-facing surface
follows the stored workspace language, and the model actually answers in it.

## Scope

1. **Model answers in the stored language.** `HTTPRunner.RunOutcome` appends
   `modelLangHint(lang)` (ADR-0084 D5) to per-run instructions resolved from
   `Spool.langOf(job.UserID)`. The hint rides only current-generation mappings
   (`RoutineOriginVersion >= 1`): legacy records replay byte-identical
   instructions under their idempotency key.
2. **Skill-draft approval card localized.** `skill_drafts.go` renders the
   pending card, publish/reject/handled/undone result pages and
   `<html lang>` from `locale.T` in the draft owner's language —
   `draft.*` keys (18) in all four catalogs.
3. **Assignment authorize replies localized.** `assignment.authorize-usage`
   and `assignment.bad-limits` replace two hardcoded Russian returns in
   `assignments.go`.
4. **Slack slash commands dispatched.** `/`-prefixed IM text in
   `ingestSlackEvent` runs through `dispatchChannelCommand` with a
   `commandRoute` carrying channel/thread ids; every command reply helper
   (`queueCommandReply`) rides that route — synchronous answers and async
   `/usage`/`/compact` results land on the originating Slack thread.
5. **Stale catalog keys removed.** `lang.status`, `chat.cancel-state`,
   `chat.usage-busy`, `chat.usage-measuring` dropped from all four catalogs;
   bare `/language` stays the picker (the catalog never needed a status key).
6. **Assignment wake prompt hints the language.** The standing-assignment
   bundle in `events.go` appends `modelLangHint(s.langOf(owner))`.

## Non-goals

- Slack `users.info` locale hint (ADR-0084 ladder permits picker fallback).
- Log-line localization — operator logs stay English by design.
- Model-generated prose is never catalog-translated; hints only.

## Test plan

- `go test ./internal/locale` — catalog parity + printf-verb parity.
- `go test ./internal/communication` — slack command routing, language
  command on slack+telegram, draft pages ×3 langs, wake-bundle hint,
  legacy-replay instruction stability.
- `just check` — full gate.
