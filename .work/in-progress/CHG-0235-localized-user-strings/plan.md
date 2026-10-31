# CHG-0235: Localize remaining hardcoded user-facing strings

Chain: ADR-0084 (every human surface localized) -> this plan -> `state.yaml`.

## Goal

Audit found four spots that still emitted hardcoded English to the user,
bypassing the locale catalog: the grant-acknowledge page, the browser
viewer title/heading, the `/usage` "unknown" placeholder and the hosting
share bootstrap page. Move every string through `internal/locale` keys in
all four catalogs and codify the rule in AGENTS.md.

## Read before coding

1. `internal/locale/locale.go` — `T`, `Negotiate`, `Fallback`, `LangOf`
2. `internal/toolhub/control_http.go` `pageLang` (1495), `serveViewerPage`
   (806-848)
3. `internal/toolhub/governance_ops.go` `serveGrantAck`/`grantAckPage`
   (79-164)
4. `internal/communication/task_commands.go` `usageNum` (480)
5. `internal/hosting/http.go` `bootstrap` const + `/_hosting/exchange`
   flow (44-62, 218-222)

## Preconditions and rules

- Red test first per surface, then fix. Catalog-parity test forces keys
  into all four locales — land en/ru first, let parity name the missing
  kk/es keys, then fill them.
- Grant page stays nonce-bound loopback-only; bootstrap keeps the
  hash-token exchange untouched — only display strings move.
- JS-embedded strings are marshalled to JSON literals, never interpolated
  raw (viewer `page.viewer-retry` precedent).

## Slices

### S1 Grant acknowledge page

`grantAckPage(grant, nonce, lang)`; `pageLang(r)` at the handler; both
`credentialResultPage` results take the negotiated lang instead of
`locale.LangEN`. Keys `page.grant-*` ×11.

Test: `TestGrantAckPageLocalized` (ru catalog strings present, no
`lang="en"`/"Grant access" literal).

### S2 Browser viewer title

`<title>` and `<h1>` render `page.viewer-title`; heading keeps the
` — <connectionID>` suffix. `locale_test.go` viewer assertion switched
from the English literal to `page("page.viewer-title")`.

### S3 /usage placeholder

`usageNum(lang, v)` returns `locale.T(lang, "usage.unknown")`; call
sites pass the resolved `lang`.

Test: `TestUsageNumLocalized`.

### S4 Hosting bootstrap page

`bootstrap` const -> `bootstrapPage(lang)`; `bootstrapLang(r)` negotiates
Accept-Language with `locale.Fallback()` last (anonymous surface, no
identity). Keys `hosting.bootstrap-*` ×3.

Test: `TestShareBootstrapLocalized` (Accept-Language: ru -> Russian
title, no English literal).

### S5 Rule + records

AGENTS.md gains the localization rule (all user-visible strings incl.
units/placeholders go through the catalog in all four languages);
index row + state.yaml.

## Acceptance

- New keys in en/ru/kk/es; catalog parity green.
- Affected package tests green: toolhub, communication, hosting, locale.
- `devcheck docs` green (AGENTS.md/index edits).

## Out of scope

- Translating operator-facing hubctl stdout (CLI diagnostics follow the
  existing English convention).
- New locales; locale selection UX.
