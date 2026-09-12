---
status: active
title: Durable cancellation and approval controls
---

# Ownership and commands

1. A verified private conversation may request `/cancel <job_id>` or
   `/approve <job_id> <request_id> <choice>`. Supported choices come from the
   exact native Hermes request: once, session, always or deny. A command is
   an explicit owner instruction, never a model-generated permission grant.
2. The Hub resolves the original principal, context, conversation, policy,
   runtime, generation, session and run from its durable mapping. Caller-supplied
   authority fields cannot replace these bindings.
3. A durable control intent precedes external dispatch. Duplicate delivery of
   the same intent cannot repeat its effect; conflicting approval decisions
   for one request are rejected. Unknown dispatch outcomes remain uncertain.

# Execution boundaries

1. Private authenticated supervisor control requests resolve the original job
   from supervisor records and verify ownership and current policy again.
   Active-run controls require the exact recorded generation; they never
   start a replacement runtime to approve or cancel a stale run.
2. Queued cancellation prevents claim. Cancellation during start is saved
   before admission and checked before execution. A race with admission is
   reconciled against the original run rather than a replacement run.
3. Runtime control requests verify the canonical identity, generation and
   deterministic session, then read native run status before dispatch.
   Approval additionally verifies native request_id, available choices and
   deadline. It sends request_id and choice, never resolve_all.
4. Approval transport does not expand provider or organization authorization.
   Tools continue to enforce their own current permissions.
5. The private supervised gateway pins upstream `HERMES_EXEC_ASK=true` so the
   native API run can publish a human approval request despite its unattended
   platform classification. Native hardline/deny rules remain in force; the Hub
   returns only an explicit verified owner's exact request decision.

# Expiry, leases and recovery

1. Pending approval identity, choices, deadline and decision state survive
   restart. Replaying an event must not extend its original deadline.
2. Approval responses expire explicitly. An expired positive decision fails
   closed. Expiry processing is durable and idempotent and rejects late replies.
3. Pending actionable approval holds a lease. Denial or expiry releases only
   the approval hold when safe; job, stream and uncertain holds remain until
   their own terminal handoff. A stop acknowledgement is not terminal completion.
4. Restart restores saved intents and reconciles their exact native request/run.
   It never repeats an unknown provider action or accepts a replacement
   generation's approval under an old request.

# Acceptance evidence

Regression checks cover every queued/start/run cancellation state, duplicate and
conflicting decisions, wrong principal/context/conversation, stale policy and
generation, deadline boundary, restart, failed persistence and unknown dispatch.
Real pinned-Hermes probes supplement fixtures for active approval, cancellation,
state preservation and idle protection. Fixtures alone do not prove integration.

New approval mutations must reauthorize against current host-owned user settings
and the organization policy in the selected spaces root. Actor, organization,
membership and effective policy version must still match the original task.
Unavailable or changed policy rejects approval. This check does not add provider
rights. Reconciliation observes the original run without repeating approval;
after authoritative observation an idempotent stop may be retried for cancellation.
