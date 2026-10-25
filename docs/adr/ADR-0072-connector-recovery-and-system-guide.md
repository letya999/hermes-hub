---
description: Owner-bound credential recovery, truthful inventory and mandatory system guidance.
last_verified: 2026-10-23
---
# ADR-0072: Connector recovery and system guidance

Status: Accepted for implementation; acceptance tracked in CHG-0149.

## Context

The owner authorized the combined recovery and system-guide plan. Existing
connector inventory mixes historical onboarding with projected tools. Broker
credential reads deliberately mask owner mismatch as not_found. Recovery must
not interpret a masked response as permission to recreate another owner's data.
The current journal already encrypts and fsyncs records, rejects corrupt replay,
and drops leases on restart. These properties remain required.

## Decisions

1. Broker/provider recovery owns encrypted secret backups. ToolHub stores only
   references and safe recovery evidence; no plaintext escrow or master key.
2. Recovery binds deployment, environment, owner, connection, contract and
   revision. Current revocation/deletion evidence fences older backups. Missing
   ownership, key or revocation evidence prevents automatic restoration.
3. A previously initialized deployment must detect an empty/replaced primary
   store. Recovery state and keys survive primary-volume loss separately.
   Plaintext materialization and staging remain disposable.
4. Reconciliation distinguishes unavailable, unauthorized/unknown, missing,
   revoked and drift outcomes. Recoverable degraded connections are revisited;
   retries never override disabled/revoked/deleted state or current grants.
5. Inventory has one caller-scoped model shared by list/status/channel routes.
   Operation completion, admission, workload state and provider verification are
   independent facts. Catalog and history are separate views.
6. An English, versioned, first-party reference skill is available to every
   runtime, including managed profiles without a native skill loader. Managed
   delivery uses an explicitly reviewed read-only guide surface; arbitrary
   native extensions remain denied. Core routing instructions are always present.
7. Diagnostics use authenticated clients with TLS verification and safe field
   allowlists. Recovery never returns secrets or asks the model to choose an owner.

## Alternatives

ToolHub-held plaintext recovery escrow would expand the existing secret trust
boundary. Re-enrolling every connection after each outage loses recoverable
state. Merely installing a skill cannot correct inaccurate API responses.

## Invariants

Hermes remains an upstream dependency. No new mandatory database or queue.
Team ownership and workload sharing remain distinct. Recovery does not replay
external sends or silently refresh provider permissions. Unknown outcomes remain
unknown until verified. Total loss of keys and all copies is not recoverable.
