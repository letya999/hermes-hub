---
description: Accepted channel-agnostic user locale — workspace-stored preference, evidence ladder, canonical English catalog with generated ru/kk/es derivatives, LLM reply-language hints.
last_verified: 2026-10-27
---
# ADR-0084: Channel-agnostic user locale

Status: Accepted 2026-10-27 — owner directed the four-language migration
(en default; ru, kk, es-ES), the workspace-not-identity preference location,
the evidence-ladder detection, and full coverage including operator tooling
and the Credential Broker. Evidence:
[CHG-0189](../../.work/done/CHG-0189-i18n-user-locale/plan.md).

## Context

Deterministic user-facing strings were hardcoded in Russian across the
communication gateway, routines, runtime progress, hub HTML pages, the
Credential Broker module and operator tooling. The project also has a blind
spot in the opposite direction: some code inspected its own Russian output
(`strings.HasPrefix(journal, "Используйте /usage")`), coupling behavior to a
human string. Channels beyond Telegram (Slack, mock, future adapters) share
the same gateway pipeline, so localization belongs to the channel-neutral
core, not to one adapter.

## Decisions

- **D1 — Canonical English catalog, generated derivatives.** `internal/locale`
  holds `Lang` (`en`, `ru`, `kk`, `es`), BCP-47-prefix `Parse`, and
  `T(lang, key, args...)` over flat dotted keys. English is the canonical
  authored catalog and the fallback for any missing translation; ru/kk/es are
  generated artifacts kept honest by a compile-visible parity test (same key
  set, same printf-verb multiset). No third-party i18n dependency: strings
  are number-invariant, so no plural engine; `x/text/message` is the upgrade
  path if real plural forms appear.
- **D2 — Preference lives in the workspace, not identity.** Each user's
  `<WorkspaceDir>/preferences.json` stores `{lang, source}` with
  `source ∈ {chosen, detected, default}`. `commodel.User` and identity
  envelopes stay language-free. The runtime sees the same file and also gets
  `HUB_USER_LANG` injected like `HUB_WORKSPACE`. Losing the workspace loses
  the preference — an accepted, recoverable cost.
- **D3 — Evidence ladder, channel-agnostic.** Resolution order: stored
  `chosen` wins permanently; stored `detected`/`default` language is reused;
  a strong platform hint (Telegram `language_code`, Slack `users.info`
  `locale`) that parses to a supported language is adopted and persisted as
  `detected` — a hint never overrides `chosen`; with no evidence the user
  gets the English default plus a one-time picker. `/language` shows status,
  sets a language by code, or re-opens the picker, on every channel. Telegram
  additionally renders the picker as inline `lang:<code>` buttons and
  registers `setMyCommands` per `language_code`; those are adapter details.
- **D4 — Language travels on Delivery, not on Job.** `Delivery.Lang` is
  stamped once per user at dispatch time so channel adapters and formatters
  never re-resolve; command handlers take the resolved `lang` as a parameter.
- **D5 — LLM-bound text is English instruction plus a language hint.**
  Deterministic service prompts sent to the model (continuation prompts,
  goal verdict wrappers, next_step guidance) are rewritten in English with an
  explicit hint naming the user's language self-name and code
  (`The user's language is <self-name> (<code>); answer the user in that
  language.`); the model renders in language itself. Credential contract
  `title`/`label` gain `*_i18n` maps with fallback to the English field, and
  TTS replies pick a per-language voice from `HUB_TTS_VOICE_{EN,RU,KK,ES}`
  falling back to `HUB_TTS_VOICE`.
- **D6 — Operator tooling follows the operator's locale, not any user's.**
  Go surfaces (`hubctl`, `internal/secrets` status strings) resolve
  `HUB_LANG` -> `LC_ALL`/`LANG` -> `HUB_DEFAULT_LANG` -> English; the
  PowerShell bootstrap scripts resolve `HUB_LANG` -> `LC_ALL`/`LANG` -> OS UI
  culture -> English and pass the normalized tag into the Telegram
  account-login container as `HUB_LANG`.
- **D7 — No string-sniffing of own output.** Behavior never depends on the
  text of a rendered message; the `/usage` journal prefix check is replaced
  by a typed signal before localization lands.

## Consequences

- Every existing test fixture that asserts Russian output pins its synthetic
  user to `ru` (one seeded `preferences.json` or an explicit `LangRU`);
  the e2e registry stays Russian under a pinned fixture.
- `lang:` callback data and broker `?lang=` are new trust-boundary inputs:
  both are validated by `Parse` and rendered through the escaping templates.
