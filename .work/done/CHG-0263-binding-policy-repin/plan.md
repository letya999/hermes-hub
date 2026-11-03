# CHG-0263: Managed binding policy_version repin in DeriveRights

Chain: SPEC-0092 R2.1 -> live incident 2026-11-04 (artem_two zero-tool projection) -> this plan -> `state.yaml`.
Previous stage: CHG-0262 (milestone closed). Standalone hardening change.

## Goal

A policy_version rotation must not silently zero a managed space's tool
projection. Today `DeriveRights` repins the capability profile and the
runtime token enrollment follows, but the `hub-agent-tools` binding stays
on the old pv — `resolveLocked` demands pv equality, so every managed
selection fails authorization and `tools_list` reports zero. Observed
live on artem_two (83 selections -> 0 tools) and m29-acc-01.

## Scope

- `internal/toolhub/rights_ledger.go`: after the profile loop, repin the
  principal's existing active bindings for credential-free definitions
  referenced by a profile that ends at the target (environment,
  policy_version) — via `store.Enable` with a platform envelope, which
  restamps the deterministic binding row in place.
- Never installs a definition the principal never bound (operator act).
- Never touches credential-bearing bindings (connector repins keep their
  documented manual path).

## Slices

### S1 Regression test

`internal/toolhub/rights_test.go`: fixture = policy+profile+binding at
pv-1, selection referencing a credential-free `hub-agent-tools` def;
`DeriveRights` to pv-2; assert binding pv follows and
`ListProjectedTools` under a pv-2 envelope returns the tool. Red first.

### S2 Implementation

Repin helper in `rights_ledger.go` + call after profile loop. gofmt,
package tests, `just check` legs feasible locally.

## Acceptance

- New regression test passes.
- `go test ./internal/toolhub/` green.
- Live: re-run `DeriveSpaceRights` on a fixture space shows binding pv
  tracking without manual `capability --kind agent-tools`.
