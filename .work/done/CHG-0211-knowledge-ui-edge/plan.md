# CHG-0211: Knowledge-UI edge — loopback + SSH tunnel + M18 hook (M20 stage 10)

Chain: `docs/adr/ADR-0086-memory-vault-openknowledge.md` D9 ->
`specs/active/SPEC-0085-memory-vault-openknowledge.md` R10 -> this plan ->
`state.yaml`.
Previous stage: CHG-0210. Next stage: CHG-0277.

## Goal

Publish the OpenKnowledge UI on a per-space loopback port with a
documented SSH-tunnel flow; no public exposure. Leave a documented hook
for the M18 one-time-link path without implementing it.

## Read before coding

1. ADR-0086 D9; SPEC-0085 R10.
2. `internal/stack` render (`ui_port` model like `browser_port`/
   `oauth_port`); `hubctl status`; `internal/stack/doctor.go`;
   `docs/operations.md` access patterns.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. Go stdlib only.
- `just check` before handoff.

## Slices

### S10.1 Loopback publish (R10.1)

Anchors: `internal/stack` compose render; port allocator.

Change:
1. `ui_port` in `agent.yaml` → `127.0.0.1:<ui_port>` publish +
   `OK_EXTERNAL_URL=http://localhost:<ui_port>`; non-loopback bind is a
   config validation error; collisions diagnosed like browser/oauth
   ports.

Tests (red first):
- `TestUIPortLoopbackOnly`, `TestUIPortRejectsNonLoopback`,
  `TestUIPortCollisionDiagnostic`.

### S10.2 Status + runbook (R10.2)

Anchors: `hubctl status`; `docs/operations.md`.

Change:
1. `hubctl status` surfaces `ui_port`; runbook documents
   `ssh -L ui_port:localhost:ui_port host` and the no-exposure default.

Tests:
- `TestStatusShowsUIPort`.

### S10.3 M18 hook (R10.3)

Anchors: ADR note; config placeholder.

Change:
1. Design note for the one-time `?t=` UI link + reserved config key —
   documented as deferred, nothing partial.

Tests:
- `TestM18HookDocumented` (docs assertion).

### S10.4 Diagnostics (R10.1)

Anchors: `internal/stack/doctor.go`.

Change:
1. Doctor checks: port conflict, health reachability, never
   non-loopback.

Tests:
- `TestDoctorUIPortChecks`.

## Red-team checklist

- Config binding `0.0.0.0` → validation error.
- Port collision across spaces → clear diagnostic, no silent remap.
- UI must never proxy to non-vault paths.

## Acceptance

A10.1–A10.4. Tunnel runbook verification recorded pending — requires
live stack (deferred — no-docker scope).

## Out of scope

- Public exposure, real authn/z, TLS, one-time-link implementation.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
