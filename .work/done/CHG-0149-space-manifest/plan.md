# CHG-0149: declarative space manifest (M17 stage 2)

Chain: `docs/adr/ADR-0072-ops-admin-delivery.md` D2 -> `specs/active/SPEC-0069-ops-admin-delivery.md` R2
-> this plan -> `state.yaml`.

## Goal

`hubctl apply -f space.yaml` converges a space's own documents
(`agent.yaml`, `workspace.yaml`) in addition to routines/knowledge — the
declarative alternative to init -> hand-edit -> doctor -> up.

## Read before coding

1. `cmd/hubctl/apply.go` (existing validate-then-write discipline)
2. `internal/stack/space.go` `Read`, `readSpace`, `initEnvironment` — file
   vocabulary and parsers
3. `internal/stack/config.go` `Validate`, `internal/stack/doctor.go`
   `DoctorScope`
4. Issue #197

## Slices

### S2.1 Manifest schema + full validation (R2.1, R2.2, R2.4)

Anchors: `cmd/hubctl/apply.go` `applySpec`, `applyFile`.

Change:
1. `applySpec` gains `version` (optional int, must be <=2 when present) and
   `space:` block `{agent, workspace}` — each a `yaml.Node`/raw mapping that
   is re-encoded and parsed through the same strict loaders as disk files
   (`stack` space parsing), so unknown keys fail identically.
2. New `--dir` space flag on `apply` (defaults `spaces/<user>` like other
   ops); space section requires `--dir` to exist (never creates a space —
   convergence, not init).
3. Validation order: parse manifest → validate all routines (existing dry-run
   pass) → read all knowledge sources → validate space documents pairwise via
   the real space parser + `Validate()` → only then write.
4. Secret-looking keys (`*_TOKEN`, `*_KEY`, `*_SECRET`, `secrets:`) rejected
   anywhere inside `space:`.

Tests (red first):
- `TestApplySpaceInvalidWritesNothing`: bad workspace doc → no file touched.
- `TestApplySpaceRejectsSecrets`: `secrets:`/`*_KEY` leaf fails.
- `TestApplySpaceUnknownField`: unknown key rejected like the on-disk parser.

### S2.2 Convergent writes + report (R2.3)

Anchors: `applyFile` report, `writeKnowledge` atomic write pattern.

Change:
1. Diff rendered desired docs against current files; write changed ones
   atomically (temp + rename), report `created|updated|unchanged` per file.
2. All-unchanged report prints `converged: true`; `ApplyReport` gains `Space`.

Tests:
- `TestApplySpaceIdempotent`: second apply is all `unchanged`.
- `TestApplySpaceRoundTrip`: applied files re-parse to the same Settings.

## Red-team checklist

- Partial failure mid-write leaves each file either old or new (atomic rename).
- Manifest cannot nominate `secrets.*.env`, other paths or symlink targets.
- `space:` on a missing `--dir` fails before any write.

## Acceptance

SPEC-0069 A1: unchanged re-apply is a no-op with "already converged"; invalid
manifest mutates nothing.

## Out of scope

`scope.yaml` convergence, org spaces, secrets, `hubctl up` triggering.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml + issue #197 comment.
