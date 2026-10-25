# CHG-0163: Stop observation after recovery budget notice

User requested diagnosis of 502 and missing progress, and minimal dev deployment.
Contract: SPEC-0053 R3, bounded reconciliation without replaying input.

## Scope

- RequeueObservations stops after the recovery budget and durable uncertainty
  notice in any outbox lifecycle state. Preserve unknown outcome and input safety.
- Without a notice, retain the final observation path so the worker can notify.
- Regression covers the exact deadline, pre-deadline observation, absent notice,
  all delivery lifecycle states, repeat polling and preservation of mapping.
- Recheck existing goal routing, recovery and progress tests; run just check.
- Deploy only communication-hub using hubctl dev-rebuild for spaces/local dev.
  Verify image, health and cessation of stale synthetic observations.

No relay timeout tuning without evidence of a configuration defect; do not
replay uncertain external deliveries. No dependency changes.
