# CHG-0150: live job watch (M17 stage 3)

Chain: `docs/adr/ADR-0077-ops-admin-delivery.md` D3 -> `specs/active/SPEC-0076-ops-admin-delivery.md` R3
-> this plan -> `state.yaml`.

## Goal

`hubctl watch` streams real phase/status transitions for a job or a scope —
admitted, running, delivered, idle — instead of hand-polling logs.

## Read before coding

1. `internal/supervisor/http.go` `/v1/jobs/<id>`, `/v1/runtimes` shapes
2. `internal/communication/job_mapping.go` — durable mapping status values
3. `cmd/hubctl/main.go` op wiring (flags: `--supervisor-url`,
   `--supervisor-auth`, `--spool`)
4. Issue #197

## Slices

### S3.1 Watch loop (R3.1, R3.2)

Anchors: `cmd/hubctl` new `watch.go`.

Change:
1. `hubctl watch --job <id>` and `hubctl watch --scope <user>`; sources:
   supervisor `/v1/jobs/<id>` + `/v1/runtimes` when `--supervisor-url`+auth are
   given, plus local spool mapping status when `--spool` is given. At least
   one source required.
2. Poll every second; print `timestamp source status` only on change;
   exit 0 on terminal job status, non-zero on auth failure.
3. `terminalRunStatus`-equivalent set duplicated minimally in the command.

Tests (red first):
- `TestWatchJobTerminal`: fake supervisor HTTP serving status sequence →
  transitions printed once each, loop exits on terminal.
- `TestWatchScopeTransitions`: runtime list changes produce transitions.

## Red-team checklist

- Supervisor unreachable → clear error, retry within bound, `--timeout` caps.
- Watch never writes; spool reads are read-only file reads.

## Acceptance

SPEC-0076 A2: transitions reflect real state within seconds; terminal exits.

## Out of scope

Log streaming (`trace` already does forensics), delivery-content display.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml + issue #197 comment.
