# CHG-0255: `space apply` with classified diff and reconcile (M30 stage 1)

Chain: ADR-0093 D1, D5 -> SPEC-0092 R1 -> this plan -> `state.yaml`.
Evidence: `../../done/CHG-0254-passport-gap-audit/research.md`.
Previous stage: CHG-0254. Next stage: CHG-0256.

## Goal

An operator (or an agent holding the right grant, later stages) changes a
passport source and runs one command: Hub shows what will change and how it
takes effect (live / respawn / recreate / rebuild), publishes on confirm and
performs exactly that action. A failed action restores the previous
revision. `/config` inside the runtime always shows the current revision.

## Read before coding

1. CHG-0254 plan (schema, `LoadEffective`, consumer registry).
2. `cmd/hubctl/main.go` command switch (`space-migrate`, `space-audit`, `render`, `up`, `down`); `cmd/hubctl/dev_rebuild.go`.
3. `internal/supervisor/http.go` `/v1/restart-request`; `internal/toolhub/gateway_projection.go` `refreshProjection`.
4. `internal/passport/publish.go` `Publish`, `atomic`.

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first. No new dependencies.
- Reconcile actions reuse existing paths (`hubctl build`/`up` force BuildKit; supervisor restart relay); never raw `docker compose build`.

## Slices

### S1.0 Compile emits normative v2 bodies (owner decision, supersedes "M29 bodies until domain stages")

Finding: `Compile` still merged/emitted M29-shaped bodies (`stack.mode`,
`services:` list, `tools.sections`, `models.services` list) that strict
v2 `Decode`/`LoadEffective` reject — `space apply` had nothing valid to
diff. Owner directive: no backward compatibility — compile must emit the
normative shapes and the source vocabulary must express them now; each
domain stage keeps its consumer-wiring slices and refines its checks.

Change:
1. `classes` moves to the v2 vocabulary every domain S.x.1 declares:
   stack `identity.*` invariant + `services.*` keyset-narrow;
   config `runtime.*`/`supervision.*`/`ports.*`/`channels.*`/`memory.*`;
   models `default`/`routes.*`/`services.*`; workspace `writable`/
   `read_only`/`layout`/`mounts.*`; tools `core.*`/`extended.*`;
   grants `allow`. New class: keyset-narrow — map-subtree leaves where a
   ceiling layer's key set bounds the keys deeper layers may write.
2. Post-merge checks move to v2 paths: instance resolves to catalog id
   (`<id>[-suffix]`), mode allowed by catalog `instances` class
   (per-runtime->supervised, per-stack->shared|per-space,
   shared|dedicated->shared|per-space, per-space->per-space,
   per-space-env->per-space), depends_on satisfied by any instance of the
   dep id; port values unique; channel voice needs hub-stt+hub-tts
   instances, channel creds are refs only; models `default` is a route
   key, ladder entries are service keys, `via` in {cliproxy,
   openai-compatible}, cliproxy endpoint host is a per-space stack
   instance of catalog id cliproxy, `auth.upstream.kind` in
   {oauth-session, broker-key}, `proxy_access` required for cliproxy and
   forbidden otherwise, limits grammar {of in requests|tokens|
   provider_quota|spend_usd, per}, reasoning in {none,low,medium,high};
   workspace tree derived from layout (newline-join; S5.1 upgrades to the
   box-drawing renderer), mounts pass through; tools key sets resolve to
   catalog toolsets, `via` within the toolset's executor surface,
   include xor exclude, named members exist, core budget <=20 moves
   whole toolsets to extended; grants ceiling-ledger intersection kept.
3. `space-migrate` source layers (`passport-src/*.yaml`) are emitted in
   the v2 vocabulary; `space-audit`, `PromptPointer` read v2 shapes.

Tests (red first): `TestCompiledRevisionDecodesStrict` (all six docs of
one Compile output), `TestStackPassportNormativeShape`,
`TestCompileRejectsM29Vocabulary`.

### S1.1 Classified diff (R1.1)

Anchors: new `internal/passport/diff.go`; `consumers.go` (CHG-0254).

Change:
1. `Diff(prev, next Effective) []Change` with `Change{Path, Before, After, Effect, Consumer}`; effect from the consumer registry; the highest class wins per consumer.
2. Values under any `ref`/`auth` leaf print as `<ref:name>` — never resolved material.

Tests (red first):
- `TestDiffClassifiesEffects`: tools core move → live; workspace mount → respawn; config port → recreate; stack instance → rebuild.
- `TestDiffNeverPrintsResolvedSecrets`.

### S1.2 `hubctl space apply` (R1.1)

Anchors: new `cmd/hubctl/space_apply.go`; `cmd/hubctl/main.go` switch; `internal/migration/passport.go` (reuse the Inputs assembly as `migration.CompileSources(dir, env)`).

Change:
1. `hubctl space apply --dir spaces/<u> --env <e> [--confirm] [--json]`: compile from `passport-src/`, load current, print diff grouped by effect, exit 0 without `--confirm` (writes nothing).
2. With `--confirm`: publish, then run S1.3 reconcile; JSON receipt lists revision, prior revision, actions, results (no secrets).

Tests (red first):
- `TestSpaceApplyDryRunWritesNothing`
- `TestSpaceApplyConfirmPublishesAndReportsActions`

### S1.3 Reconcile with rollback (R1.2)

Anchors: new `internal/reconcile/reconcile.go`; `internal/supervisor/http.go` restart relay; `cmd/hubctl` up/build helpers.

Change:
1. `Plan(changes) []Action` → ordered unique actions: `rebuild(services)`, `recreate(services)`, `respawn(runtime)`, `live(consumers)`.
2. Handlers are an interface registry; this stage ships `respawn` (supervisor restart-request), `recreate`/`rebuild` (hubctl up/build for named services) and a `live` no-op hook that domain stages fill (toolhub, relay, pdp, file PEP).
3. On any action error: re-point `current` to the prior revision, re-run the prior plan's `live` hooks, report `rolled_back`.

Tests (red first):
- `TestReconcileFailureRestoresPreviousRevision`
- `TestReconcileRunsMinimumActionPerClass`: only the touched service is recreated.

### S1.4 `/config` directory view (R1.3)

Anchors: `publish.go` `Publish`; `internal/stack/compose.go` runtime volumes (`:61-93`); `internal/supervisor/ensure.go` mounts (`:503`, `:658`, `:601`).

Change:
1. `Publish` also refreshes `generated/config-view/<env>/` (six passports + `system-prompt.md` + `SOUL.md`/`USER.md`/`AGENTS.md`) by per-file temp+rename inside the directory; the directory inode never changes.
2. Compose and supervisor bind the directory read-only at `/config`; the separate `/config/config.yaml`/`SOUL.md` single-file binds are removed (Docker Desktop single-file binds go stale on rename).

Tests (red first):
- `TestConfigViewDirectoryInodeStable`
- `TestSupervisorMountsConfigViewDirectoryReadOnly`

## Red-team checklist

- Agent-writable path cannot reach `passport-src/` or `generated/` (mount absent; `/config` ro).
- Apply without confirm never publishes; interrupted publish leaves the prior `current`.
- Reconcile rollback never leaves a half-applied revision active.

## Acceptance

A1 live: on the dev stack, apply a revision whose `recreate` step is forced
to fail (unknown port owner) → receipt shows `rolled_back`, `current` equals
the prior revision, the runtime `/config/*.yaml` shows the prior revision.
Then apply a benign live change and see the new revision in `/config`
inside the running container without restart.

## Out of scope

Domain consumers (stages 2–7) — this stage provides the pipeline and the
`/config` view only.

## Done checklist

- [ ] Tests red then green; `just check`; `just docker-check` (compose/supervisor mounts changed) + `just docker-clean`.
- [ ] Live A1 recorded in state.yaml.
- [ ] docs/services/hubctl.md, docs/operations.md updated.
- [ ] Issue closed; state done; folder moved to `.work/done/`.
