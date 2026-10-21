# CHG-0143: OAuth resend, viewer links and grant-epoch fix

Follow-on hardening on `feat/m14-toolhub-connector-platform-merge`. Written
retroactively — the code predates this record (the diff lived uncommitted on
the branch); slices describe landed code, not planned work.

## Scope

- `connector_status` OAuth resend: `resend: true` rotates the armed prepared
  OAuth flow and posts a `link-resent` event so the gateway drops a fresh
  channel message; the rotated state never reaches the tool result
  (`prepared_oauth.go`, `control*.go`, `generic_fallback.go`).
- Stale broker grant epoch on confirm: a reused credential must consult the
  recorded grant's mint epoch, not the migrated binding record — otherwise
  `up` policy-version bumps die on `denied (403)`
  (`broker_grant_epoch_test.go`).
- Browser viewer links: `POST /v1/prepare-outcome` accepts
  `connection_id` + `viewer_url` and relays the one-time `/viewer/` link to
  the channel with a never-paste-passwords notice
  (`communication/prepare_outcome.go`, `handleViewerNotice`).
- Prepared catalog refresh: `txttsql-sql` contract + `catalog.json` revisions.
- Loopback OAuth scrub: a connector backend that prints its own interactive
  OAuth handoff (`http://localhost:8000/oauth2callback`, or a provider
  authorize URL whose `redirect_uri` points at loopback `/oauth2callback`)
  hands the user a dead link — the callback lives inside the workload
  container. `Gateway.call` now replaces those URLs in text output with a
  withheld-link notice pointing at managed onboarding; ordinary output,
  legitimate external OAuth links and non-callback loopback URLs pass
  through (`tool_output_scrub.go`, `gateway_call.go`,
  `tool_output_oauth_scrub_test.go`).

## Validation

2026-10-20 Google Calendar callback follow-up: reproduced loss of Broker
binding-scoped state after a failed readiness probe. Record the grant's binding
ID alongside its policy epoch; reuse it on retry without requiring an already
enabled binding. Preserve version/identity/policy and revoked-binding rejection.
The regression failed before the fix (credential reference rotated on retry)
and passed afterwards, including scope-negative cases.
Live dev acceptance passed: browser OAuth, enable, projected `list-calendars`
and one-day `list-events`, without printing provider data. The client project's
Calendar API was disabled and was enabled with the owner's confirmation.
`just check` passed: race tests, 86.02% own Go statement coverage, formatting,
vet, tidy, staticcheck, gosec, docs/actionlint and configuration integration.
ToolHub was updated through `hubctl dev-rebuild` and its dev image and MCP
endpoint were verified. `just docker-clean` passed; the full Docker gate was
not run. The external Google app remains in Testing; publishing was outside
this repair.

- [x] `go test ./internal/communication ./internal/toolhub/...` green (included in the full race/coverage gate)
- [x] `just check` green
- [ ] live acceptance (OAuth resend + viewer link on the dev stack)
