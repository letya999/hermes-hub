# CHG-0152: hermes-admin diagnostics principal (M17 stage 5)

Chain: `docs/adr/ADR-0072-ops-admin-delivery.md` D5 -> `specs/active/SPEC-0069-ops-admin-delivery.md` R5
-> this plan -> `state.yaml`.

## Goal

A deliberately slow, fully audited `hermes-admin` bearer can read any
principal's onboarding state, diagnose where a failure happened and inspect
the pinned upstream source for the credential recipe — inside the system,
never mutating anything.

## Read before coding

1. `internal/serviceauth/tokens.go` — enrolled-token contract
2. `internal/identity/identity.go` `Envelope`
3. `internal/toolhub/gateway.go` `connectorAuth`, `Mux`, `Tokens`
4. `internal/toolhub/api/grants.go` `Onboarding`; `internal/toolhub/store.go`
   onboarding listing
5. `internal/toolhub/api/artifact_source.go` `FetchRepositoryArtifactContext`
6. `internal/audit/audit.go` — ledger
7. Issue #141

## Slices

### S5.1 Token scopes (R5.1)

Anchors: `internal/identity/identity.go` `Envelope`; `internal/serviceauth/tokens.go` `ParseTokenEnvelopes`.

Change:
1. `Envelope` gains `Scopes []string` (`json:"scopes,omitempty"`).
2. `ParseTokenEnvelopes` validates scope tokens (`^[a-z][a-z0-9-]{0,31}$`,
   <=8 entries); `HasScope("admin")` helper on Envelope.
3. Scope-bearing entries stay wire-compatible: old readers ignore the field.

Tests (red first):
- `TestTokenScopesValidate`, `TestScopedEntryParses`.

### S5.2 Admin surface (R5.2, R5.3, R5.4)

Anchors: `internal/toolhub/gateway.go` `Mux`/`connectorAuth`; store listing;
`internal/audit.Ledger`.

Change:
1. `GET /v1/admin/onboardings` — all onboardings, fields: ids, principal,
   phase, error, error_code, definition, source_url, commit_sha, created.
2. `GET /v1/admin/diagnose/<id>` — one record plus finalizers, credential
   names/shapes (never values), prepared-catalog match and a synthesized
   `proposal` object (credential_groups/proxy_environment/network_transport
   fields the recipe model already knows).
3. `POST /v1/admin/inspect-source` — body `{onboarding_id, grep?}`; fetches
   the pinned upstream commit via `FetchRepositoryArtifactContext`, returns
   bounded file list + grep hits (no file bodies beyond bounded snippets).
4. Gate: `connectorAuth` + `auth.HasScope("admin")`; else 403. Admin calls
   run serialized on a dedicated mutex with a minimum interval (1s) — slow
   by construction.
5. Every admin call appends an audit event `admin.diagnostics` naming the
   admin principal and the target onboarding.
6. `communication` users.yaml may carry a `hermes-admin` entry; docs describe
   enrolling its token with `scopes: ["admin"]` in `toolhub-tokens.json`.

Tests (red first):
- `TestAdminRequiresScope`: envelope without scope → 403.
- `TestAdminDiagnoseCrossPrincipal`: admin reads another principal's record.
- `TestAdminRateLimited` / `TestAdminAuditTagged`.
- `TestAdminNeverReturnsSecrets`: response contains names, not values.

## Red-team checklist

- `X-Hub-Principal` control-plane bearer does NOT get admin unless its own
  enrolled envelope carries the scope.
- inspect-source path/grep bounded: max files/bytes, timeout, no regex DoS
  (literal substring or bounded regexp with cap).
- Admin scope cannot call any mutating endpoint — it is checked only on the
  read surface; existing mutating paths ignore scopes entirely.

## Acceptance

SPEC-0069 A4: non-admin bearer → 401/403 on every admin endpoint; every admin
call leaves an audit entry with admin principal + target.

## Out of scope

Admin write/approve powers, new channels for the admin, UI.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml + issue #141 comment.
