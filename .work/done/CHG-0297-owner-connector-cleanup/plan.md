# CHG-0297: Owner connector cleanup across policy epochs

Existing decisions: ADR-0055 (journal and typed results), ADR-0056 (confirmed
remove and finalizers), ADR-0058 (team ownership). Requirements:
SPEC-0023 (owner and team control), SPEC-0049 (verified cleanup).

## Goal

An owner can select and remove an abandoned onboarding or an owned connection
after a policy rotation, even when its current credential reference is absent.
No model call may purge by itself. Removal requires the existing protected
owner confirmation; authorization is cut before resource cleanup. A missing or
unreachable credential backend remains visible as cleanup incomplete, never a
false verified-zero receipt.

## State and authority

1. Onboarding and connection are independent records. An onboarding may be
   preparing, failed or stale while a connection remains active; an owned
   connection remains selectable when its onboarding is absent.
2. `connector_edit action=remove` requires current `connectors.enable` grant
   and exact personal ownership. A team member cannot remove a team-owned
   connection; only the existing operator path can. Name ambiguity fails with
   candidate IDs; exact onboarding/connection IDs take precedence.
3. Selection of an older, same-owner/context/runtime onboarding for removal
   migrates only the cleanup authority to the current policy and invalidates
   old setup links. No credential or connection is changed before confirmation.
4. Confirmation commits `deleting` and revokes bindings before the idempotent
   workload, Broker, local secret and verified-zero finalizers. Missing
   resources are idempotent success; unknown credential identity or unavailable
   backend is `cleanup_incomplete` with a retryable step.
5. A removed tombstone and receipt remain addressable. Repeated requests for
   the same generation do not mint another link or purge another connection.

## Slices

1. Resolve owner cleanup targets across policy epochs and orphan states;
   safely rebase a selected onboarding or create one cleanup-only record for a
   connection without onboarding. Cover ownership, ambiguity and concurrency.
2. Ensure confirmed finalizers delete Broker ciphertext, never swallow Broker
   errors, and continue to report incomplete cleanup until deletion is proved.
   Broker exposes a principal/context-scoped metadata inventory by connection
   so a missing ToolHub credential reference does not hide a managed secret.
3. Document transitions and run local tests plus non-destructive live status
   checks. Release 31 and hub-agent-tools@18.3.13 require rebind; an idle
   managed runtime uses the new base image on its next spawn. Do not delete
   the owner's current GitHub connection as a smoke test.

## Red-team checks

- Foreign principal, context, runtime and team member cannot adopt a record.
- A stale setup link cannot confirm after cleanup selection.
- Lost reference cannot cause an unrelated credential ID to be selected.
- Broker request 404 is tolerated only when a known connection ID can still
  be inventoried; a masked credential 404 never proves deletion.
- Failed cleanup never emits a verified-zero receipt or re-enables a binding.

## Acceptance

Focused state/authorization/finalizer regressions; `just check` and
`just test ./internal/toolhub` after the final edit. Live verification uses
read-only status and a disposable fixture, not personal credentials.
