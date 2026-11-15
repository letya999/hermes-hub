---
description: Bound Telegram delivery fan-out and ban context-echo in the prompt contract — incident telegram-837846020 (75 messages, 220K stitched dump).
---

# CHG-0295: response fan-out safety + context-echo contract

## Incident

`telegram-837846020` (artem_two, 2026-11-10 08:39–08:50): the model reproduced
injected context blocks and the retained step log (~220K chars) on its first
stream; the provider cut the stream at 4m59s with no `finish_reason`; Hermes
kept the fragment and stitched it into `final_response`; communication sent
all 75 rendered parts — ~70+ chat messages, real report buried at the tail.

Verified causal chain: fresh session `history=4` already carrying a 66K-token
compacted snapshot → unbounded echo generation → provider ~5min stream cap →
upstream `turn_truncation` stitches dropped text into the response →
`sendParts` had no part ceiling.

## Changes

- `internal/communication/render.go`: `telegramMaxParts = 5`.
- `internal/communication/gateway_delivery.go`: `sendTelegramMarkdown` caps
  any delivery above the part bound — localized `job.truncated` notice +
  final part (carries the conclusion) + `degraded` → full source `answer.md`.
  Resume edge: `SentParts > 0` collapses to the tail part only.
- `internal/locale/strings_{en,ru,kk,es}.go`: `job.truncated` (4 catalogs).
- `internal/stack/prompt_contract.go` (`contractMid`): injected context
  blocks (prior-context notices, compaction summaries, retained step logs)
  are reference metadata, never reply content; call reports list only this
  run's calls; visible tools are invoked directly — `tool_call` rejects them.
- Docs: `docs/services/communication.md` outbound-delivery bullet;
  `docs/tools.md` `tool_call` row states core-name rejection.
- Regression: `TestMarkdownDeliveryCapsPathologicalFanout`,
  `TestChannelContractBansContextEcho`.

## Out of scope (tracked, not fixed here)

- Provider-side ~5 min stream cap (cliproxy upstream) — not configurable
  by the hub; mitigation is contract + delivery bound.
- Upstream Hermes `turn_truncation` stitching pre-nudge fragments into the
  final response — upstream dependency, reported not patched.
- Slack path: 39K-part fan-out has no document escape hatch — noted, no cap
  added (silent truncation would be worse than bounded fan-out).

## Verification

- `go test ./internal/communication ./internal/stack ./internal/locale` — green.
- `just check` — green.
- Rebuilt dev: `communication-hub`, `supervisor` (soul-effective render).
- `hubctl render` regenerated `soul-effective.dev.md` for both spaces —
  new clauses present.
- Live acceptance: fresh Telegram run of the original task — pending owner
  message; assert ≤5 messages + document, no `[CONTEXT COMPACTION]` leak,
  platform_bootstrap untouched.
