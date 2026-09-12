---
description: Persist job and runtime mappings without a database dependency.
---
# ADR-0015: Durable job/runtime mappings

## Decision

Persist job, conversation and runtime lifecycle metadata in the existing local file
stores using JSON records, atomic replacement and the existing process lock. The
communication spool owns job mappings; the host supervisor owns runtime and lease
state. Hermes homes remain the source of truth for Hermes session content.

Mappings contain routing and lifecycle metadata only. They do not contain prompts,
provider credentials or channel tokens. Idempotency is checked by key plus immutable
request fingerprint. Lease operations are fenced by runtime generation.

## Consequences

The single-host deployment gets restart-safe recovery with no new service. A future
multi-writer or multi-host deployment must replace the file store with a transactional
shared store after measured need appears. Unknown external outcomes remain `uncertain`
and require reconciliation instead of retry.
