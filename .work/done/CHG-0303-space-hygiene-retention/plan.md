---
description: Bound passport revision history and remove retired root-level render/auth mirrors — space layout cleanup after the M32 six-passport cutover.
---

# CHG-0303: space hygiene — bounded revisions, generated-only renders

## Why

`spaces/<user>` accumulated retired artifacts: `passport-src/` (ADR-0096
superseded compiler inputs, unread), `scope.yaml` (retired for user spaces),
root `compose.<env>.yaml`/`hermes.<env>.yaml` mirrors written for "one-release
compatibility" and `generated/passports/r-*` with no retention. The implicit
FSM audit flagged: dual-write split brain (compose canonical in generated/,
hermes canonical at root), fallback resurrection of stale root compose, and a
revision lifecycle with no terminal transition — every bundle accumulated
forever.

## Changes

- `internal/passport/prune.go` (new): `PruneRevisions(spaceDir, keepPrevious)`
  sweeps `generated/passports` — keeps the live pointer chain (`current`,
  mounted `config-view/<env>/revision`, the replaced previous) plus
  `RevisionKeepSlack` (3) newest `r-*` bundles by mtime; removes orphaned
  `.staging-*`/`.publish-*`/`.apply-journal-*` artifacts. Gates: open apply
  journal or pending/unreadable current pointer → no-op. Non-fatal hygiene.
- `internal/passport/publish.go`: `Publish` prunes after activation.
- `cmd/hubctl/space_apply.go`: `applyConfirm` prunes after journal removal on
  both exit paths; `reconcile.Report.Pruned` receipts the swept ids.
- `internal/stack/render_env.go`: renders write only `generated/`; retired
  root mirrors are removed by the same render (no resurrection path).
- `internal/stack/materialize.go`, `internal/supervisor/env.go`,
  `internal/supervisor/ensure.go`: materialize/spawn read
  `generated/hermes.<env>.yaml`.
- `cmd/hubctl/main.go`: compose selection is `generated/`-only (3 sites).
- Tests: prune sweep/gates/orphans/exact keep-set; render fixtures repointed;
  supervisor/dev-image/migration fixtures use `generated/`.
- `docs/spaces.md` (retention semantics), `docs/services/hubctl.md`
  (generated compose path).

## Space cleanup (spaces/artem_two, post-merge)

- Delete: `passport-src/`, `scope.yaml`, `nul`, `generated/migration-backup/`,
  root `compose.dev.yaml` + `hermes.dev.yaml`, `r-*` outside the keep set.
- Keep: `execution.dev.json`, `model-relay-admins.json`, `toolhub-tokens.json`
  (live, non-duplicating), `generated/config-view/` (mounted projection).

## Explicitly not done

SQLite revision store (atomic rename + bind-mount model is the right
primitive; the missing piece was a sweeper), a dedicated `space prune` CLI
(retention rides inside publish), configurable slack.

## Gates

- `go vet` touched packages; `go test -short -run` new regressions;
  `just check`; affected package suites (`internal/passport`,
  `internal/stack`, `internal/supervisor`, `internal/reconcile`,
  `cmd/hubctl`).
