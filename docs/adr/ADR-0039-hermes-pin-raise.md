---
description: Proposal — raise the pinned Hermes revision once upstream merges the MCP circuit-breaker fixes.
last_verified: 2026-10-14
---
# ADR-0039: Hermes pin raise decision (D12c)

Status: proposed, 2026-10-14. This is the only D12 proposal that may proceed
under SPEC-0053 once the Hermes contract gate passes; it still requires the
owner's sign-off recorded in CHG-0096 state.

Requirements: [SPEC-0053](../../specs/active/SPEC-0053-execution-chain-reliability.md) R4.
Origin: [ADR-0036](ADR-0036-execution-chain-reliability.md) D12c,
[CHG-0093](../../.work/done/CHG-0093-tool-surface-integrity/state.yaml) S1.5.

## Context and measured evidence

The pin sits at `869228cab4a8276d3b4c78da9d9939670c47bd0f`. The known upstream
defect that motivated a raise: the MCP circuit breaker disables a whole server
for 60 s after three tool errors and counts transport timeouts toward the
breaker — a healthy transient error degrades the tool surface.

CHG-0093 S1.5 evaluation (2026-10-14): upstream PRs
`nousresearch/hermes-agent#67340` and `#17016` were closed unmerged; `#68455`
remains open. This review has not identified and verified a replacement pin.
PR #68455 configures thresholds; it is not itself the fix for counting
tool-level errors as transport failures. PR #68669 addresses that distinction.
These PR states alone do not establish the absence of another merged fix.

## Proposal

Conditional, not immediate:

- When an upstream revision lands that fixes the breaker accounting, raise the
  pin to that revision and re-run `just docker-check` — the
  `TestHermesBreakerIgnoresToolErrors` contract is already written and gated.
- Until then the pin stays put; stage 1 mitigations (surface manifest,
  readiness checks, canary detection) bound the blast radius.
- Do not carry a hub-side patch: Hermes is an upstream dependency and the
  guardrail against silent patching stands.

## Isolation impact

A pin raise changes only the fetched upstream revision in the image build; no
mount, socket or credential scope changes. It is evaluated against the full
Hermes contract suite before merge.

## Migration and rollback

- Migration: rebuild images, `hubctl up`; existing `.state` and sessions are
  upstream-managed and unmodified by the hub.
- Rollback: revert the pin commit and rebuild; no data migration.

## Test and chaos plan

- `just docker-check` full gate, including the breaker contract and
  `hermes-capability-contract`.
- Chaos `runtime @ mid-run` cell rerun on the raised pin.

## Go / no-go

Go when upstream merges a breaker fix AND the full docker gate passes on the
raised pin. No-go — keep `869228cab4a8276d3b4c78da9d9939670c47bd0f` — as long as
no such revision exists; this ADR then serves as the recorded deferral.
