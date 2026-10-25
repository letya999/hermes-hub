# CHG-0159: group modes — topics vs mention

Chain: ADR-0074 -> SPEC-0071 -> this plan -> state.yaml. Follow-up to #207.

## Goal
A configured Telegram group runs in one of two explicit modes chosen per chat:

- `topics` — the group is a personal interface. Each forum topic adopts its own
  task/session inside the sender's personal home; every message and command is
  processed like a private chat. This restores the pre-M16 behavior that
  #207's opt-in gate removed.
- `mention` (default) — the M16 behavior: passive member, bounded transcript,
  @-gated execution inside a dedicated group space.

Unconfigured groups stay silent in both cases.

## Configuration

`communication.users.yaml` accepts the compact map form

```yaml
groups:
    -1009999: topics
    -1008888:
        space: team          # mention mode needs a dedicated space
        members: [11, 22]    # optional; empty = every configured Telegram user
        mention: "@helper"   # optional; empty = the bot's own @username (getMe)
```

and keeps the legacy list-of-objects form working.

## Slices
- `Group.Mode` + `Groups` YAML type accepting map and list forms.
- Mode-aware validation: topics rejects extra fields; unknown modes rejected;
  mention keeps the dedicated-space contract; members/mention now optional.
- Mode-aware routing: topics resolves the sender via `telegram_ids` and enters
  the ordinary message/command path; mention keeps `handleGroupUpdate`.
- `user:` scope for topics jobs, `group:` scope for mention jobs; matching
  `authorizeGroupMapping` re-verification.
- `getMe` on TelegramAPI + cached bot username as the default mention tag.
- Regression tests for both modes; docs/operations.md and SPEC-0071 R5 updated.

## Red-team checklist
Foreign sender in both modes, unconfigured group silence, topics jobs keeping
personal scope at claim-time re-authorization, empty-members admission bounded
to configured users, dedup/transcript bounds preserved for mention mode.

## Acceptance
Unit + contract tests green; `just check` passes; live acceptance on the real
Telegram group with the real model.
