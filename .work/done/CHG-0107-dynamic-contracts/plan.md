# CHG-0107: Dynamic credential contracts + credential self-service

## Context

Live replay found that installing an arbitrary connector requires an operator to
hand-write `spaces/local/broker/contracts/<id>.json` and restart the broker.
Owner directive: contracts must be registered dynamically without connector
hardcoding; a user must be able to regenerate the credential form and reset a
credential to enter a new one — self-service, but safe.

## Design (see services/credential-broker/.work/todo/dynamic-contracts)

1. Broker: `POST /v1/contracts` (audience `broker:control`) persists a contract
   as a journal mutation; replay restores it into the catalog before the
   request/grant digest drift check. Dynamic contracts are collect-and-deliver
   only: env/file/json_file deliveries, no routes, no OAuth, no state,
   `allow_shared=false`, storage defaults to the first writable provider.
   Same `id@revision` digest ⇒ idempotent; different digest ⇒ conflict.
   Audit event `contract.registered`.
2. Client: `RegisterContract`; ToolHub `bindReviewedContract` synthesizes a
   contract from the reviewed definition's credential inputs when no catalog
   contract covers them (`dyn-<digest>` id, or the pinned id when missing from
   the catalog). BoundedCLI keeps the local-form path.
3. Self-service: `rotate` already resets an onboarding to awaiting-creds and
   mints a fresh broker request/form; `status` regenerates dead requests.
   Verify end-to-end live and make the control tool descriptions explicit.

## Steps

- [x] broker change + tests (its own .work/todo/dynamic-contracts)
- [x] toolhub synthesis + tests
- [x] broker ADR-0008 + spec + scope/api docs
- [x] just check (root: green except gosec recipe hang under just→PowerShell on
     this host — standalone clean; broker module just check green 90.35%)
- [x] hubctl build/up; live: google_workspace_mcp with no contract file →
     dyn-6bd689c8 registered, form, submit, confirm, enable; rotate → fresh
     request r01045e18... + fresh form → re-enabled (bind-701d18d7),
     credential id kept, rev 1→3 supersede
- [x] state.yaml acceptance, commit (5a44d82 + 29a2f81)

## Notes

`spaces/local/broker/contracts/notion-token.json` stays: the live broker journal
binds existing notion requests/grants to that file contract's digest; deleting
it would trip the fail-closed drift check on restart. New connectors never need
files — dynamic registration covers them.
