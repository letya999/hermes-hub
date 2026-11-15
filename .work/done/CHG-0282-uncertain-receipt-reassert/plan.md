# CHG-0282 — stale-receipt wedge + managed media admission (telegram-837845960 incident)

## Goal

Repair the live Telegram pipeline wedge observed on `telegram-837845960`
(PDF inbound, uncertain terminal receipt, lane deadlock) and land the two
minimal code fixes the incident exposed, each with a regression test.

## Incident recap (2026-11-04)

1. Job `telegram-837845960` (PDF upload) hit the runtime during a restart
   window; `/v1/execute` answered `uncertain`/`run.unknown` and the
   terminal receipt `handoff:uncertain` was written.
2. The reconciler re-admitted the job: the mapping regressed to `running`
   while the finished receipt stayed authoritative at the same position.
3. The next identical uncertain handoff hit `applyStreamReceiptLocked`'s
   `receipt.Position <= mapping.EventPosition` early exit; the terminal
   finish then compared `running` vs `uncertain` and returned
   `errStaleReceipt` — the job stranded in `running/` forever, the
   `inFlight` conversation lane stayed held, and `telegram-837845961`
   ("ну?") starved in `pending/`.
4. Separately, the ToolHub projection for `artem_two` was locked
   (envelope `policy-7e4d…` vs profile `policy-6adf…` after governance
   rotations) — `503 projection unavailable` → parked MCP → zero hub
   tools/connectors.

## Code changes

- `internal/communication/stream.go` — same-position terminal receipt
  re-asserts a regressed nonterminal mapping instead of returning the
  stale-receipt path; preserves the first `UncertainSince` anchor.
- `internal/communication/terminal_handoff_test.go` —
  `TestUncertainReclaimReassertsFinishedReceipt`: claim → uncertain →
  requeue → reclaim → queue next conversation job → repeat identical
  uncertain → mapping stays uncertain at the original anchor, job lands
  in `failed/`, next job claims.
- `internal/stack/config.go` — managed `image_gen` admission accepts the
  canonical r5 `media` toolset (`toolhubCapabilityID` already maps
  `image_gen`→`media`); the retired literal `image_gen` entry stays
  accepted. Without this, a migrated managed space fails
  `ReadEffectiveSpace` → supervisor `Ensure` → no runtime can spawn.
- `internal/stack/media_profile_test.go` —
  `TestManagedImageGenAcceptsMediaToolset`.

## Live repair executed (dev stack, spaces/local + spaces/artem_two)

- `space-migrate --apply` on `spaces/artem_two`: revision
  `r-5a915b571999243c`; profile `artem_two-default` republished rev 44,
  `policy-246cf8812d42e4e4`; runtime token enrollment refreshed
  (`toolhub-tokens.json` envelope `246cf`).
- ToolHub container restarted (boot replays + double full-state digest
  marshal take ~2-4 min on the host mount — looks wedged, is just slow).
- Verified live: `POST /mcp` initialize 200, `tools/list` = **94 tools**
  for `artem_two`.
- `dev-rebuild communication-hub` + `dev-rebuild supervisor` (the old
  supervisor binary rejected migrated `media` settings on every
  `Ensure` → `context settings unavailable` → zero spawns).
- Job 960 finalized `failed/` + uncertain notice delivered
  (`outbox/done/job-telegram-837845960-uncertain`).
- Job 961 executed for real (`run_c12e69ad30dc…`, gen-520) and its
  answer delivered via `job-telegram-837845961-response`.

## Residual / follow-up observations

- Supervisor `beginJob` replays an in-memory `uncertain` record forever
  while `/v1/resume` 409s on admission-unknown records: if the record is
  never persisted (`durable=false` or a lost `persistLocked` error), a
  restart strands the gateway in a permanent resume-409 loop until the
  run-budget notice parks the job. Live fix was a supported `/v1/jobs`
  resubmit under the original idempotency key. Worth a design note:
  admission-unknown records may need a bounded resubmit path rather than
  infinite resume cycling.
- ToolHub cold start on the Windows bind mount takes minutes before it
  listens; restarts should expect that delay.
