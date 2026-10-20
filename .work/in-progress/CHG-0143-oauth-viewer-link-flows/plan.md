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

## Validation

- [ ] `go test ./internal/communication ./internal/toolhub/...` green
- [ ] `just check` green
- [ ] live acceptance (OAuth resend + viewer link on the dev stack)
