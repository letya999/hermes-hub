---
description: M17 code closeout — durable operational protection and corrected delivery contracts.
last_verified: 2026-10-25
---
# ADR-0076: Operational protection and M17 closeout

Status: Proposed. Supplements ADR-0077; its accepted text remains immutable.

## Context

The read-only audit of commit 2757929 found gaps in replay isolation, rollout
retention, validation-before-mutation, duration metering and durable admin audit.
The milestone additionally includes #326/#327/#328/#329. Existing Go supervisor,
spool, capability gateway and model relay remain the integration boundaries.

## Decisions

D1. Repair the original surfaces against their issue contracts. Pin immutable
images with atomic writes and retain rollback dependencies during cleanup.
Replay must run with synthetic authority and isolated inputs, never an arbitrary
production supervisor carrying the original authority.

D2. Persist operational authority in the existing single-host control plane.
Resource consumers are generation-bound; observations alone never authorize
termination. Expected waits and persistent demand are explicit.

D3. Budget enforcement is separate from best-effort metering. Reserve before
chargeable work; settle once; unknown usage retains conservative reservations.
Bound per-user queues, preserve control capacity, and record local capacity
measurements without claiming multi-host or live-provider acceptance.

D4. An administrative hold is persistent, independent of model availability,
and fences admission and effects. Resume requires administrative authority.
Owner notices are durable and do not require the contained Hermes runtime.

D5. Personal supervision consumes bounded event metadata and sleeps otherwise.
Coalesce incidents, serialize per-user judgements, expire renewals and escalate
failures deterministically. It cannot grant itself permissions or clear holds.

## Boundaries

No new mandatory services or dependencies, no upstream Hermes patching, no
secret/environment/transcript diagnostic access. Docker and live acceptance
are pending by explicit owner instruction; local tests are code evidence only.
