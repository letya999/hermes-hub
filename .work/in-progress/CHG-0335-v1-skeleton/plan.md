# CHG-0335: v1 skeleton - shared packages, service descriptor, process mode, test base

Issue: #600 (M40.2). Milestone: M40 - v1 Service Split. Rules: docs/engineering.md
(rewritten in M40.1, #599, in parallel). Design source: the owner-approved
replan (architecture, M40 split, research), summarized here where it drives
code. Next: M40.3 spawn-controller.

## Goal

The frame every v1 service sits in: `shared/*`, a service descriptor with
profiles and the `trusted` installation flag, `hubctl run --mode process`,
and the test base. Delete the v0 scaffolding the frame replaces.

## Layout decision

`contracts/` lives inside the root module (no own go.mod; go.work only adds
the credential-broker module). `shared/*` follows the same pattern: plain
packages of `github.com/letya999/hermes-hub/shared/...`. A separate module
would need replace directives in every service module for no current gain
(simplicity rule 1); a service moving to its own module later can still
import them through the workspace.

## Slices (Work list order)

1. `shared/store` - open by URL (`sqlite://path`, `postgres://...`), embedded
   numbered SQL migrations (`NNNN_name.sql`) applied in one transaction under a
   migration lock (SQLite `BEGIN IMMEDIATE` + busy timeout; Postgres
   `pg_advisory_xact_lock`), `Store.Tx`, tenant-scoped handle that refuses SQL
   without the `{tenant}` predicate marker or with an incomplete tenant, and
   binds org/owner itself. `?` placeholders, rebound to `$n` for Postgres.
   The v0 `internal/sqlitedb` user_version opener moves in as
   `OpenVersioned`/`OpenVersionedRollback` for the four v0 stores until their
   services move (M40.3+); `internal/sqlitedb` is deleted.
2. `shared/fsm` - `Machine` (entity table, history table, edges, deadline per
   non-final state, validated), `Create`, `Transition(q, m, id, event, now)`
   (compare-and-swap state + history row in the caller's tx; forbidden edge
   writes nothing), `Sweep(tx, m, now)` (cross-tenant read of expired keys
   only, every write re-scoped to the row's tenant). No stored flags.
3. `shared/httpapi` - `contracts/internal/wire` moved unchanged.
4. `shared/auth` (signature, key, run-token and service-token verification
   moved from contracts/identity/v1; the contract keeps aliases and all
   issuing code), `shared/locale` (moved from internal/locale, parity test
   kept), `shared/log` (slog JSON with service attribute, used by process mode).
5. Service descriptor in `cmd/hubctl` (Go struct list): name, binary, port,
   env allowlist, data dir, zone, allowed edges, replica class, profiles.
   Profiles `minimal`, `family`, `enterprise`. Installation flag `trusted`
   (`--trusted` / `HUB_TRUSTED`, default false = fail closed).
6. `hubctl run --mode process --profile minimal|family` - starts binaries from
   `--bin` directly, data in `<data>/<svc>.db`, localhost ports, env limited to
   the descriptor allowlist plus computed wiring, waits for `/healthz`, stops
   all on first exit. Refuses `trusted=false` before touching disk.
7. Test base - tenant invariant runner (`shared/store/storetest`), run first by
   `just test`; contract conformance runner (`shared/testkit.Transports`) and
   `contracts/task/v1/fake`; Postgres store run when `HUB_TEST_POSTGRES_URL` is
   set plus a CI job; tool catalog snapshot; topology test; fake OpenAI SSE
   model server (`shared/testkit`).
8. Delete `cmd/hub` (+ `hub serve --zone`, release builds, locale key),
   `internal/devcheck/deps.go` + baseline + tests + justfile step,
   `contracts/toolsurface` (ToolHub keeps its catalog types locally). New
   devcheck rule `boundaries`: `shared/*` imports neither `services/*` nor
   `internal/*`.
9. `contracts/task/v1` and `contracts/identity/v1` on `shared/httpapi`.

## Tests (red first)

| Test | Package |
|---|---|
| TestStoreRefusesUnscopedQuery | shared/store |
| TestMigrationLockSerializesConcurrentStart | shared/store |
| TestTransitionRejectsForbiddenAndWritesNothing | shared/fsm |
| TestSweepMovesExpiredState | shared/fsm |
| TestProcessModeRefusesUntrusted | cmd/hubctl |
| TestMinimalProfileAnswersMessage | cmd/hubctl |
| TestToolCatalogSnapshotUnchanged | internal/devcheck |

## Rule -> enforcing test (for docs/engineering.md, owned by M40.1)

| Rule | Enforcing test or gate |
|---|---|
| Every row carries org and owner; a query without them is refused | `shared/store` TestStoreRefusesUnscopedQuery |
| Cross-tenant read is not_found, checked first and blocking | `storetest.TenantInvariants` via each package's TestTenantInvariants; `just test` runs `^TestTenantInvariants$` before the rest; CI job `tenant-invariants`; TestTenantRunnerExcludesUserSpaces |
| Migrations are numbered, embedded, applied once when N copies start | `shared/store` TestMigrationLockSerializesConcurrentStart, TestMigrationsRejectGapsAndNewerSchema |
| One DB per service, SQLite default, Postgres by the same URL, portable SQL | `shared/store` and `shared/fsm` suites run on SQLite always and on Postgres when `HUB_TEST_POSTGRES_URL` is set; CI job `postgres-store` |
| One Transition; history in the same tx; forbidden edge writes nothing | `shared/fsm` TestTransitionRejectsForbiddenAndWritesNothing, TestTransitionWritesStateAndHistory |
| Deadline + sweeper for every non-final state | `shared/fsm` TestMachineRequiresDeadlinePerNonFinalState, TestSweepMovesExpiredState |
| shared/* never imports services/* or internal/* | `devcheck boundaries` in `just check`; `internal/devcheck` TestSharedImportBoundary |
| Contract transports behave the same (in-proc and HTTP); a fake passes the same suite | `shared/testkit.Transports`; `contracts/task/v1` TestContractConformanceInProcAndHTTP; `contracts/task/v1/fake` TestFakePassesConformance |
| Every agent tool keeps name/schema/effect/right | `internal/devcheck` TestToolCatalogSnapshotUnchanged (snapshot rewrite refused without a catalog version bump) |
| Process mode only for trusted installations | `cmd/hubctl` TestProcessModeRefusesUntrusted |
| Minimal profile answers a message in process mode | `cmd/hubctl` TestMinimalProfileAnswersMessage (skips with a named gap until the core binaries exist, see state.yaml) |
| Service gets only its declared env plus wiring | `cmd/hubctl` TestProcessModeStartsProfileWithDeclaredEnv |
| Service descriptor is consistent (edges, zones, profiles) | `cmd/hubctl` TestServiceDescriptorIsConsistent |
| Every user-visible string localized in 4 catalogs | `shared/locale` TestCatalogParity |

## Out of scope

Moving any service (M40.3+). Docker/k8s descriptors (M40.3, M40.15).
Docs owned by M40.1: docs/engineering.md, docs/architecture.md,
docs/state-machines.md, ADR-0105..0109, SPEC-0100, services/README.template.md.

## Continuation verification (2026-11-20)

- c8d41b0e: pgx direct dependency, go mod tidy, ADR-0115, mandatory CI Postgres
  job, SQL scope bypass regressions. Real red: marker in literal/comment, UNION,
  negated guard and nested SELECT all returned data before the guard fix.
- 8b9b5c07: descriptors, profiles, trusted process launcher; trust refusal red
  before guard and green after; two external child processes stopped/joined on
  cancellation and first child exit. This is launcher verification only.
- 50fa92d7: testkit, task fake conformance, tool snapshot; drift detection tested
  independently of initial artifact creation. No catalog version or shape changed.
- 49c1ed25: retired multiplexer, gate/baseline and toolsurface removed; tenant
  invariants run first; service_enable without policy red (wrote state/restarted)
  then green (unauthorized, no state, no restart).
- Existing migration and FSM regressions have no supplied original behavioral
  red logs. Later sensitivity probes deliberately allowed duplicate migration,
  allowed forbidden transitions, or disabled sweep collection: the intended
  assertions failed. All probes restore production bytes before green checks.
- just security passed (govulncheck + npm audit). Postgres store/FSM suites passed
  in a uniquely named disposable postgres:17.12-alpine container, then stopped.
- Real minimal message topology remains unavailable until M40.3/M40.4. The named
  test skips with that reason; no fixture response is claimed as integration.

## Remaining acceptance

Owner review, minimal real topology, remote Postgres CI, publication and any live
smoke remain explicit acceptance actions. Keep this CHG in progress while that
acceptance is outstanding. No push, PR, live stack mutation or prune is performed.

Integration: e7b4a881 on feat/m40-v1-platform. Store connection diagnostic red/green
added in 0b3ac931. Native Windows credential-broker compilation is unavailable
(fs_linux.go only); root module build passed. actionlint on ci.yml passed.

TestTenantRunnerExcludesUserSpaces: real red when ./... ran the synthetic
spaces test; green with repository source directories only. The table-first
runner never implicitly selects real user spaces. TestProjectAndFormat fixture
now creates the contracts/shared directories required by the moved format gate.
