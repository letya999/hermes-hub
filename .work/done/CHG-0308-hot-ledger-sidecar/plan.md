# CHG-0308: Hot ledger moves off the registry commit channel (cursor sidecar + persist-on-transition)

Incident: `/v1/rights/decide` starved 8–20s behind `Store.mu` writers →
model-relay context-canceled → fail-closed `403 right denied` → agent runs
aborted. Same class as CHG-0305 (restart backlog storm), one incident earlier.

Root cause (FSM audit + cross-repo comparison): two lifecycles share one commit
channel. Durable registry (bindings, onboardings, profiles — one commit per
human/agent transition) and hot bookkeeping (broker cursors ~1/s, credential/
onboarding heartbeat timestamps ~1/30s) both pay the whole-snapshot
`saveDBLocked` — double full-file hash + FULL-sync tx + dir sync on a Docker
Desktop 9p bind mount. A pending writer blocks every reader (Go RWMutex), so
each ~10–30s save parks the PDP. Every neighbouring agent harness surveyed
(codex, iva, osinara, openclaw, nanoclaw, goclaw×2, ironclaw, open-duet,
open-dot, paperclip, eve) keeps cost-of-write proportional to the delta —
row-level SQL or per-domain files; none commits a whole registry snapshot for a
counter.

## Goal

Hot at-least-once bookkeeping gets its own cheap durable home; the registry
commits only on real transitions. `/v1/rights/decide` returns to millisecond
latency under live broker event load.

## Slices

### S1.1 Cursor sidecar

Anchors: `internal/toolhub/cursor_store.go`, `internal/toolhub/onboarding_events.go`,
`internal/toolhub/store_io.go`, `internal/toolhub/control.go`.

1. New `CursorStore`: `map[string]uint64` + own mutex + dirty flag + JSON
   temp-write/rename + `cursorFlushInterval` = 60s. CAS `CompareAndAdvance`
   preserves the registry map's lose-deterministically semantics.
2. `ReconcileBrokerEvents` reads/advances the sidecar instead of
   `s.brokerCursors` + `saveLocked`. Lazy `cursorStore()` binds the file
   (`cursors.json` next to `store.db`) and seeds from persisted
   `brokerCursors` — one-time migration.
3. `BrokerCursors` leaves `buildSnapshotLocked`; `Load` still reads persisted
   rows for the seed. Old `broker_cursor` kvmap rows self-delete on the next
   registry save.

### S1.2 Heartbeats persist on transition only

Anchors: `internal/toolhub/store.go` `observeCredentialCheckIfCurrent`,
`internal/toolhub/onboarding_worker.go` `recordOnboardingObservation`.

1. `CredentialCheckedAt`/`CredentialRetry*` and `ObservedAt` are memory-truth;
   the durable commit rides `CredentialVisibility`/`HealthCause` and
   `ObservationCode` transitions only. Losing them on restart just means the
   reconciler re-checks sooner.

### S1.3 Tick cadence

Anchors: `cmd/toolhub/main.go`. Lifecycle predicates are >=30s-grained;
tick 1s → 15s.

### S1.4 Read-path convoy (found in live acceptance)

After S1.1–S1.3 stopped registry writes entirely (content hash stable),
decide still stalled 5–46s. Goroutine dumps showed the second half of the
mechanism: `ProjectionRevisions` (5s restart-notify ticker) holds
`s.mu.RLock` while `semanticRestartDigestLocked` evaluates O(profiles ×
managed tools) and each `decideCoreLocked` re-reads `governance.json` —
tens of seconds of 9p file I/O under the read lock. Any queued writer
(`MaterializePendingSelections`, onboarding commits) then parks every new
reader behind it (RWMutex writer preference).

1. `cursorStore()` double-checked: `s.mu.Lock()` only at first bind —
   previously every reconcile was a waiting writer parking all readers.
2. `loadRightsLedgerLocked` consults a memo that `ProjectionRevisions`
   populates for the duration of its pass — one governance read per pass
   instead of N×M. The decide path keeps its per-call read.

## Explicitly rejected

- Gate `saveDBLocked`'s fileHash fence on `data_version`: the fence exists
  because data_version is unobservable for foreign commits on this mount —
  gating it on the signal it compensates for removes the protection.
- WAL/`synchronous=NORMAL` on the registry: ADR-0038 contract; unsafe on 9p.
- Actor/single-writer + snapshot readers: ceiling, not now — see ponytail note
  in cursor_store.go.
- Fingerprint-caching `ProjectionRevisions` on diskDigest+revisions: tried and
  reverted — the digest contract tracks in-memory mutations (credential/grant
  revocation tests mutate maps under s.mu without a save), so no cheap
  fingerprint covers its inputs. Removing the per-tool file I/O is the fix,
  not skipping the pass.

## Tests (red first)

- `TestCursorStoreAdvanceFlushReload` — sidecar persists + reloads.
- `TestCursorStoreCASLosesToConcurrentAdvance` — stale expected loses.
- `TestCursorStoreFileWinsOverSeed` — file authoritative, seed covers
  unmigrated keys.
- `TestCursorStoreFlushHonoursInterval` — batching window.
- `TestCursorStoreSeedsFromRegistryWhenFileMissing` — migration seed.
- `TestBrokerEventGapKeepsCursorUntilOwnedStateCommits` — updated: cursor
  durability asserted through the sidecar, not the registry.

## Acceptance

`go test -short ./internal/toolhub/` green; `just check`; live: rebuild
toolhub, decide latency returns to ms, comm-hub capture outbox drains.

## Out of scope

Named volume for /state (parallel ops task); actor/single-writer rewrite;
relay timeouts (fail-closed is correct); unrelated pending ops/hygiene.
