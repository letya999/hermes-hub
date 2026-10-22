# CHG-0151: agent-image rollout discipline (M17 stage 4)

Chain: `docs/adr/ADR-0072-ops-admin-delivery.md` D4 -> `specs/active/SPEC-0069-ops-admin-delivery.md` R4
-> this plan -> `state.yaml`.

## Goal

The pinned agent image can never move without a recorded smoke pass; the
previous verified image is a one-command rollback; one context may canary a
candidate while the rest stay on the pin.

## Read before coding

1. `internal/supervisor/ensure.go` `devRuntimeImage` call sites (the single
   spawn-time image seam)
2. `internal/supervisor/dev_image.go` — pin-file validation precedent
3. `internal/devcheck` `DockerSmoke` — the existing smoke gate
4. `cmd/hubctl/main.go` build/up flag set (`--runtime-image`)
5. Issue #198

## Slices

### S4.1 Pin store (R4.1)

Anchors: `internal/supervisor` new `rollout.go` (pin file model).

Change:
1. `RolloutPins{Current, Previous RolloutPin{Image, SmokedAt, Digest?},
   Canary *{Image, ContextID, SetAt}}` persisted at
   `<space>/generated/image-pins.json`, written atomically.
2. Validation: image refs bounded charset/length, env key in {dev,prod};
   malformed file fails closed (spawn falls back to flag default? — no:
   fail closed, an unreadable pin must not silently run the flag image).
   Decision: parse error → flag default + loud log; a missing file is normal.

Tests (red first):
- `TestRolloutPinsRoundTrip`, `TestRolloutPinsRejectsGarbage`.

### S4.2 Spawn override (R4.1, R4.4)

Anchors: `internal/supervisor/ensure.go` three `devRuntimeImage` call sites.

Change:
1. `Manager.runtimeImage(binding, env)` wraps the existing resolution:
   canary (binding context matches) → dev pin (existing) → rollout
   `current` → flag. Runtime record/logs carry the resolved source
   (`pin|canary|dev|default`) so a canary failure is visible in job status.

Tests:
- `TestRuntimeImageResolutionOrder` table test over pin states.

### S4.3 `hubctl image` (R4.2, R4.3, R4.4)

Anchors: `cmd/hubctl` new `image.go`; `internal/devcheck.DockerSmoke`.

Change:
1. `hubctl image status` — prints pins + resolved image.
2. `hubctl image promote --image <ref>` — runs `devcheck.DockerSmoke` on the
   candidate (real gate; `--skip-smoke` does not exist), on pass shifts
   current→previous and writes the pin with the smoke timestamp.
3. `hubctl image revert` — restores previous as current and runs the same
   smoke on it; refuses when no previous exists.
4. `hubctl image canary --image <ref> --context <id>` sets/overwrites the
   canary override; `canary --clear` removes it. Canary image is recorded as
   unverified-by-design; `image status` marks it explicitly.
5. Docker access stays behind the existing `dockerOutput` seam for tests.

Tests:
- `TestPromoteRequiresSmoke` (smoke stub fail → pin unchanged).
- `TestRevertRestoresPrevious`.
- `TestCanaryScopedToContext`.

## Red-team checklist

- Concurrent promote/revert serialize on a file lock like the spool lock.
- Pin file world-writable scenarios: parse failure never picks an attacker
  image — resolution falls back to flag default.
- Canary context id validated (`identity.ValidID`); canary never promotes.

## Acceptance

SPEC-0069 A3: pin cannot move unsmoked; revert one command + re-verify;
canary confined and explicit.

## Out of scope

Multi-host rollouts, automatic rollback on health metrics, registry polling.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml + issue #198 comment.
