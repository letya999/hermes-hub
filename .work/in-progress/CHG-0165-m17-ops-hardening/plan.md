# CHG-0165: M17 ops hardening — notices, sweep split, bounded retention

Chain: ADR-0076 -> SPEC-0075 -> this plan -> state.yaml.
Follow-up to CHG-0164 (M17 closeout); driven by live Docker findings and the
owner's review list.

## Goal

Harden the M17 control plane for a ~20-user single-VPS deployment so it
stays responsive and bounded under sustained load: undeliverable hold
notices acknowledge instead of wedging, crash detection never waits behind
slow spawn work, and every durable artifact carries a time/count/size bound
with compressed archiving instead of silent deletion.

## Slices

- S1: acknowledge hold notices that can never route (no configured user or
  no delivery channel); fast notice poll only while pending, ~10 s idle.
- S2: split supervisor sweep — detection ticker (health/leases/reap) runs
  independently from reconcile ticker (RecoverMissingWork/ReconcilePins).
- S3: bounded retention — 72 h default, per-dir file caps, archive byte cap,
  gzip `.jsonl.gz` bundles, usage.jsonl baseline-preserving rewrite,
  signals.jsonl size rotation.
- S4: operator runbook for the user-onboarding capability ceremony
  (docs/operations.md).
- S5: watcher coverage re-verified; gap filed as issue #354.

## Acceptance

- Live: synthetic hold on a principal with no channel → `notice_pending`
  clears in seconds, nothing enqueued (verified 2026-10-26).
- Live: runtime kill detected ≤5 s while pin reconcile runs in parallel;
  recovery still occurs (verified 2026-10-26).
- `just check` green; communication and supervisor race tests green.

## Out of scope

Metric-anomaly watchers (latency/5xx/session thresholds) — filed as #354.
Docker image lane — CI-owned. No dependency changes.
