# CHG-0178: Channel-agnostic user locale (i18n + a11y)

Chain: ADR-0082 -> this plan -> state.yaml. Owner direction: 4 languages
(en default, ru, kk, es-ES); workspace-persisted preference; detection ladder
(strong platform evidence auto-applies, otherwise a picker); all deterministic
surfaces localized including operator tooling and Credential Broker; LLM-bound
text becomes English instructions plus a language hint.

## Goal

Every deterministic user-facing and operator-facing string flows through a
canonical catalog. The user's language lives in `<WorkspaceDir>/preferences.json`
(workspace state, not identity), resolves per inbound event through an
evidence ladder, and reaches the model as `HUB_USER_LANG` plus an explicit
reply-language hint. Channel adapters only supply a locale hint and a picker
rendering; Telegram keeps its keyboard/command-menu extras in the adapter.

## Read before coding

1. README.md, docs/index.md, docs/services/communication.md, docs/channels.md
2. docs/adr/ADR-0082 (this change's decision record)
3. OpenClaw locale research (external reference, `_research-harnesses`)

## Preconditions and rules

- Red test first, then fix. Go stdlib only — no new i18n dependency.
- English catalog is canonical; ru/kk/es are generated derivatives kept in
  parity by test (same keys, same printf-verb multisets).
- Existing tests asserting Russian text keep passing only if fixtures pin
  `preferences.json` to `ru` or pass `LangRU` explicitly — no silent renames.
- Worktree `hermes-hub-i18n` on `feat/i18n-user-locale`; the main worktree's
  four dirty toolhub files are not ours to touch.
- `just check` before handoff; >=85% own Go statement coverage.

## Slices

### S0. Locale core + preference ladder

Anchors: `internal/locale/*`; `internal/communication/gateway.go` update loop.

Change:
1. `internal/locale`: `Lang`, `Parse` (BCP-47 prefix), `T(lang,key,args...)`,
   four registered catalogs, `All`, `Names`.
2. `Prefs{Lang,Source}` at `<WorkspaceDir>/preferences.json` (`chosen`,
   `detected`, `default`), atomic-ish write 0600, malformed->default.
3. `Resolve(workspaceDir, hint)` ladder: chosen>detected>hint>default+picker;
   `StoreResult` persists detected/chosen, upgrades default->detected.
4. Gateway: per-update `langFor(user, hint)`; `/language` command
   (status, `set <code>`, bare picker); Telegram keyboard `lang:<code>`
   callback routes into the same handler; `Delivery.Lang` stamped at
   dispatch; `HUB_USER_LANG` into runner env; `setMyCommands` per
   `language_code` for all four.

Tests (red first):
- `internal/locale`: Parse prefix/fallback, missing-key->key passthrough,
  catalog parity (keys + verb multisets), prefs round-trip, ladder
  (chosen wins, detected persists, default upgraded, picker on fresh).
- Gateway: `/language` status/set/picker; `lang:` callback; hint adoption;
  mock-channel picker (channel-agnostic proof); per-language setMyCommands.

### S1. Command/reply surfaces

Anchors: `task_commands.go`, `gateway*.go`, `stream.go`, `job_mapping.go`,
`tasks.go`, `media.go`, `quota.go`, `containment.go`, `communication/slack.go`.

Change:
1. All deterministic replies -> `locale.T(lang, key, ...)`.
2. Remove `strings.HasPrefix(journal, "Используйте /usage")` sniff — typed
   signal instead.
3. Slack: intercept `/language` before job ingest; minimal `users.info`
   locale lookup or picker fallback.

### S2. Progress/background surfaces

Anchors: `server_observe.go`, `commops/health.go`, `commops/canary.go`,
`model_relay_quota.go`, `routines/*`, `channel/slack.go`, `channel/telegram.go`,
`agenttools` next_step.

### S3. Hub HTML + a11y

Anchors: `control_http.go`, `telegram_auth.go`, `credential_forms.go`.
Dynamic `<html lang>`, `aria-live="polite"` on wait/poll regions, label/alt audit.

### S4. Broker pages

Anchors: `services/credential-broker/httpapi/browser.go` + broker-local
`webtext`; `?lang=` propagated by hub when minting form URLs; module tests.

### S5. LLM-bound text + contracts

Anchors: `prepare_outcome.go`, `gateway_goal.go`, `agenttools` next_step;
`title_i18n`/`label_i18n` in prepared contract JSON, `app/init.go`, examples.

### S6. Operator tooling

Anchors: `cmd/hubctl`, `internal/secrets/secrets.go`, `scripts/*.ps1`,
`docker/telegram-account-login.py`. Operator locale from `HUB_LANG`/`LANG`/OS.

### S7. Voice, fixtures, glossary, docs

TTS_VOICE_{RU,KK,ES} fallback map; e2e/legacy test fixtures pinned ru;
translation terminology review; docs/index/ADR re-verify.

## Red-team checklist

- Prefs file corrupt/foreign-lang: falls back to default, never panics.
- Chosen language must never be silently overwritten by a later hint.
- `lang:` callback forgery: callback only sets the acting user's own prefs.
- `?lang=` on broker URLs: unsupported values fall back, no injection into
  rendered HTML (escape all interpolations).
- Picker prompt must not leak to group contexts lacking a resolvable owner.

## Acceptance

`go test ./internal/locale` green; gateway locale tests green; `just check`
green; honest report of untested live-channel behavior.

## Out of scope

- Model-generated prose localization (the model replies in language itself).
- Plural-form engines (strings are number-invariant; x/text is the upgrade).
- STT model changes; real provider/channel acceptance.
- Deployments, publication, upstream Hermes changes.

## Done checklist

- [ ] Slices S0–S7 implemented with red-first tests.
- [ ] `just check` green; coverage >=85%.
- [ ] Translations reviewed for terminology consistency (glossary in doc.go).
- [ ] Docs + ADR index updated; state.yaml evidence recorded.
