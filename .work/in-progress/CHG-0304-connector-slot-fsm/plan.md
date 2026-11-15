# CHG-0304: Connector slot FSM

## Incident

2026-11-13: the owner accumulated six onboarding records for two connectors
(github-mcp-server, playwright-mcp). `connector_list` showed 2 records while
the model reported `not_found` and invented a backend desync — the selector
contract accepted names but the model sent wrong argument keys, and three
competing action vocabularies (`state`/`next_action`/`pending_action` plus
raw `onboarding_phase`) made the confusion likely rather than accidental.
`delivery_status:delivered` was reported while notices sat in a mock sink.

## Change

Implements ADR-0099.

1. `Stage {pending|live|failed|gone}` + `Gate {kind,nonce,expires}` replace
   `Phase` (14) and five nonce pairs on `api.Onboarding`; `transition()` owns
   the edge table; load-time migration maps legacy phases.
2. `enabled/disabled` stop being onboarding stages — the view reads
   `connection.status`.
3. `claimOnboardingSlot` (principal,context,runtime,definition) is the sole
   find-or-create; repeat install attaches + resends.
4. Model surface: `state` + `you_must` + `delivery`/`channel`; remove
   `next_action`, `onboarding_phase`, `link_sent`, `pending_action`; one
   changeset with catalog bump + rebind + respawn.
5. `sent` only on channel-confirmed transmission; mock reports
   `channel:"mock"`.
6. Single resolver for list/status; `id` argument alias; stale-epoch records
   addressable.
7. Load migration quarantines duplicate live records
   (`superseded_by=winner`); regression suite; dev-rebuild + live run.

## Verification

- go test -short internal/toolhub internal/communication
- just check; go vet -p=2 touched packages
- dev-rebuild toolhub + communication-hub; live install→confirm→enable→remove
  on github-mcp-server for artem_two
