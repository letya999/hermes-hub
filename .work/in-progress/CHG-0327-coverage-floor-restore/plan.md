# CHG-0327: Restore 85% coverage floor

## Goal

CI shard coverage fell to 84.09% after CHG-0322 (firecracker, ~628 uncovered
statements) and CHG-0323 landed without package tests. Restore the repository
gate (85% overall, 75% per internal package) with focused regression tests —
no production-code changes.

## Scope

- internal/firecracker: fake-guest control-plane suite (launch, exec, mux,
  forward, delete, REST routes) — no KVM required.
- internal/runtime: firecracker guest-agent paths, metrics ring/status,
  control-body gate.
- internal/supervisor: Uno-backend calls against a fake Uno HTTP API;
  dangling-revision spawn gate.
- internal/passport: compile_v2 validators.
- internal/toolhub (+api, automation): store getters, rights doc validate,
  gateway load gate, hosting cellinit seed.
- cmd/hubctl: connector branches, dispatcher usage gates, exec-terminal frame
  protocol (linux), status token resolution.
- internal/uno: client verb wrappers and error contract.

## Verification

- `go run ./cmd/devcheck coverage coverage.out 85 75` on a merged
  -coverpkg=./... profile: 85.19% overall, all internal packages >= 75%.
- `just check` green; affected packages green under -short.
