# CHG-0164: M17 code closeout

Chain: ADR-0076 -> SPEC-0075 -> this plan -> state.yaml.

## Goal

Complete the code, regression tests and operator contracts for milestone 18
(M17), preserving the outstanding Docker and live acceptance boundary.
The owner explicitly forbids Docker deployment/build/rollout during this work.

## Slices

1. Original surfaces (#141/#195/#197/#198/#206): audit-ledger failure closes
   diagnostics; actual duration metering; pre-mutation manifest validation;
   delivered-state watch; immutable, atomic, cleanup-protected rollout pins;
   replay with an isolated execution contract, complete safe context and diffs.
2. #326: extend supervisor resource demand/generation/health contracts and
   bounded recovery, including shared consumers and expected waits.
3. #327: durable atomic hierarchical reservations and settlement, bounded
   fair admission with reserved control capacity and measured local load.
4. #329: persistent administrative holds, effect fencing and independent
   durable owner notices; only a separate administrative authority resumes.
5. #328: event-driven, coalesced, single-run, bounded personal supervision
   with expiring decisions and deterministic escalation.

## Anchors and regression oracles

- cmd/hubctl/{apply,watch,image,replay}.go; internal/communication/usage_journal.go;
  internal/toolhub/admin.go; internal/devcheck/docker_clean.go.
- internal/supervisor/{lease,reconcile,execute,http,supervisor}.go;
  internal/runtime/{model_relay,toolhub_relay}.go;
  internal/communication/{gateway_delivery,streams}.go.
- Invalid input changes no production state; retries settle once; foreign
  consumers and unmanaged resources survive; cancellation/holds fence late
  effects; unavailable models cannot disable deterministic protection.
- Keep existing tests. Add red regression witnesses before each implementation
  slice; review authored tests using test-critic before execution.

## Verification

Current checkpoint is partial. See [acceptance.md](acceptance.md) for the
remaining code requirements; local regression success is not milestone closure.

Targeted Go tests, race tests, local synthetic admission/load measurements,
then just check (own Go coverage >=85%). No new runtime dependencies planned.
Docker and real Telegram/provider/LLM acceptance remain explicitly pending.
Live matrix: two test scopes, normal/long-running requests, subscriptions,
cancel/restart, candidate/rollback, model outage, noisy tenant, durable hold,
admin diagnostics and isolated replay. Capture only allowlisted receipts.

## Out of scope

Deploying or touching running Docker resources; account login, external sending,
GitHub publication/issue closure; upstream Hermes modifications; mandatory
databases/queues; claiming live success from local fixtures.
