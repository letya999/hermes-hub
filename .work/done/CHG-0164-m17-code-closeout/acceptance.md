# M17: implementation and acceptance ledger

Updated: 2026-10-25. Status: in progress. No issue is declared 100% complete.
Docker builds, deployments and live acceptance are deferred by the owner.

| Issue | Implemented and locally exercised in this change | Remaining code / verification |
| --- | --- | --- |
| #141 | Audit failures deny reads; actual endpoint writes attributed durable admin records; source hits omit adjacent bytes; error DTOs use declared codes rather than arbitrary controller text. | Complete proposal payload and runtime-facing diagnostic integration; review every diagnostic field and rejected-call audit path. |
| #195 | Gateway attempt duration is journaled; terminal usage writer regression passes. | Verify full run duration across reconnect/restart, concurrent settlement, export totals and unavailable measurements. Attempt duration is not yet proof of complete runtime lifecycle duration. |
| #197 | Rejected space manifests leave an absent spool absent; validation is rechecked under the writer fence. Watch observes mapped outbox delivery and explicit uncertain sends. | Version boundaries, complete desired-space convergence/diff contract, spool-only scope watch, CLI timeout and the completion-to-outbox handoff gap. |
| #198 | Revert clears canary; current/previous/canary tags survive cleanup; pin replacement is atomic. | Immutable image identity throughout smoke/promotion/spawn/cleanup; successful Hermes execution smoke and failure/rollback matrix. |
| #206 | Existing implementation inspected; no live replay executed. | Replace arbitrary replay supervisor authority with enforced isolation; safe task/context cloning, versions, tool traces, redaction and reply/tool divergence. Current remote replay path must not be treated as provider-isolated. |
| #326 | Failed generic MCP cleanup keeps its slot reserved; verified master containment retries without deleting foreign resources. | Full owned-resource/consumer registry, persistent demand, expected waits, bounded recovery and incident contracts across MCP/watchers/runtime; overhead measurements. |
| #327 | Durable scope rotation prevents the tested tenant burst from starving another scope; waiting generic admission no longer holds the release mutex. | Durable hierarchical reservation/settlement, model/tool enforcement, queue/storage ceilings, background aging, reserved control capacity and 1/10/100-user measurements. |
| #328 | Requirements and integration boundaries recorded. | Event-driven personal supervision is not implemented; bounded execution, coalescing, expiring decisions and escalation remain. |
| #329 | Separate administrative bearer, persistent holds and epochs, verified runtime containment, conservative ambiguous jobs, versioned resume, independent durable owner notices, mapped reply/wake/job fences. | Automatic triggers, shared-resource demand, all producer/stream/ToolHub/model effect boundaries, complete fresh-policy resume checks and adversarial race matrix. Current hold work is a foundation, not complete installation-wide containment. |

## Required local gates

- Run the complete affected package suites after fixture updates; then `just check`.
- Own Go statement coverage must remain at least 85%.
- Run race verification for new containment, notice handoff, queue and MCP control paths.
- Keep new behavioral evidence separate from the original 86.25% baseline.
- Do not close the CHG or GitHub issues while the code requirements above remain.

## Deferred live matrix

Use two dedicated test scopes with explicit authorization for external actions.
Record only scoped IDs, versions, status, timestamps, counters and safe receipts.

1. Real Telegram: ordinary and long-running requests, topics, artifacts, approvals,
   cancellation, reconnect and uncertain send. Verify exactly one owner notice
   without the stopped runtime and continued service for the other user.
2. Real subscriptions/watchers: valid persistent demand, parked human question,
   two consumers with one cancelled, controlled owned orphan and a hung MCP.
   Confirm bounded incidents/recovery and preservation of unrelated demand.
3. External LLM: healthy endpoint, timeout/outage, unknown usage/pricing, small
   configured budgets, children/memory/supervision attribution and model fallback.
   Mandatory limits and containment must continue without the model.
4. Overload: one noisy tenant plus normal foreground/background work; bound queue
   growth and admission latency while stop/status/notices remain available.
5. Image delivery: exact candidate identity, successful Hermes smoke, failing
   canary confined to one context and rollback to retained verified image.
6. Replay: prove absence of production mounts, credentials and provider/network
   access; compare status, reply and tool traces across the recorded versions.

These are future acceptance scenarios, not results of the local fixtures.
