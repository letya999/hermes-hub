# CHG-0133 — M13 P6: Credential Broker as single credential authority

Issue: https://github.com/letya999/hermes-hub/issues/287
Branch: feat/m14-toolhub-connector-platform
Depends on: CHG-0128 (recipes), CHG-0129 (action surface), CHG-0130 (journal), CHG-0132 (tiers)

## Scope (contract-testable slices)

### S6.1 Boundary validation of submitted credentials
- Per-field `format` regex + `max_length` on `ConnectionField`; values
  rejected on `\x00\r\n` for env/header/token delivery, JSON parsed for
  `json`/`file` fields. Table test on the credential-submit path.

### S6.2 Refresh lease
- One durable lease per (connection, provider): a store-level lease record
  taken before an OAuth refresh; a second in-flight caller joins/fails
  instead of double-spending a rotating refresh token; revoked connections
  can never be resurrected by an in-flight refresh.

### S6.3 Proxy delivery class
- `ConnectionField.Delivery="proxy"`: workload env receives the stub
  `hub-managed`; the egress-proxy plan carries the real reference and the
  allowlisted hosts. Contract: plan/env show only the stub.

### S6.4 Recipe credential drift → reauth
- A recipe change of provider/endpoint/client purges stored tokens and moves
  the connection to `reauth` (extend the P4 recipe-drift reconciler path).

### S6.5 Migration
- `hubctl credentials migrate`: dual-read (Broker first, legacy credstore
  fallback) then write-through to Broker; rollback keeps legacy reader.

### S6.6 Docs + closeout
- ADR "Broker as the single credential authority" appended to ADR-0022's
  record; threat-model + architecture updates.

## Out of scope (live gates)
- Real egress-proxy header injection inside a running container.
- Live OAuth against providers; fixture AS only.

## Done checklist
- [ ] Tests red then green per slice; `just check` green.
- [ ] ADR appended; docs updated.
- [ ] Live acceptance deferred honestly to owner deploy.
