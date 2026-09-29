# CHG-0049: install progress on the form and in Telegram

## Problem

A credential-form POST blocks on a real admission probe (~35s for Slack).
The page gives no feedback during that window, so it reads as a stuck
refresh. Telegram is equally silent: one settle notice after prepare, then
nothing until the agent happens to speak again.

## Design

- No fake percentages — the probe is atomic. The form shows an
  indeterminate spinner plus a live elapsed-seconds counter.
- Form JS runs under a per-response CSP nonce (`script-src 'nonce-…'`);
  `default-src 'none'` and the rest of the policy stay as strict as before.
- Telegram progress rides the existing authenticated
  `POST /v1/prepare-outcome` channel extended with an `event` whitelist
  (`prepare-started`, `credentials-check`, `credentials-rejected`,
  `binding-failed`) and an optional `tools` count on `enabled`.
- Interim events deliver a channel notice only — they never enqueue the
  continuation job, because they describe a phase that has not settled.
- Dedup keys include the event; `credentials-rejected` also hashes the
  detail, so each distinct rejection reason notifies once.
- Best-effort, like the existing settle path: a down communication-hub
  loses the notice, the store stays the source of truth.

## Emit points (toolhub)

- start of the background prepare goroutine → `prepare-started`
- after the "probe running" state is persisted, before `AdmitWithCredentials`
  → `credentials-check`
- probe or registration failure → `credentials-rejected` (persisted detail)
- `finishAuthorization` failure after a saved credential → `binding-failed`
- `finishAuthorization` success → `enabled` settle notice with tool count
