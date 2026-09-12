# Communication rollout acceptance — 2026-09-12

## Accepted isolated runtime evidence

- Issues 16, 17 and 18 were closed after the original complete Docker gate passed
  (session 78075). Repository changes remain local; no publication was claimed.
- Latest `just check` passed (session 38613): own Go statement coverage 85.06%,
  race/shuffle, formatting, vet, staticcheck, documentation and actionlint.
- `just security` passed (session 52630): no Go or browser dependency vulnerabilities.
- Image `7a3f67d90dba` contains real upstream Hermes 0.21.0, pinned commit
  `869228cab4a8276d3b4c78da9d9939670c47bd0f`.
- Standalone production Telegram-adapter gateway lifecycle probe passed
  (session 73296). Telegram and model endpoints were local fixtures; the supervisor,
  HTTP job admission/resume, worker/spool and Hermes execution were real.
- Two verified users received exact separate homes/workspaces. A queued pre-rollout
  job executed once despite duplicate updates. Sessions/runs and runtime routing
  stayed separate; a warm Alice request retained generation/session with a new run.
- Both completed contexts reached Idle with zero leases. Observed UTC deadlines:
  Bob settled at 18:46:36, idle deadline 18:51:36; Alice settled at 18:46:46, deadline
  18:51:46. The automatic reaper stopped both, then Alice's next request started a
  replacement generation at 18:51:57; Bob stayed stopped. The replacement completed
  with the original Alice session and a new run. No manual clock advance was used.
- Apply preflight uses the maintained pinned native cron SDK in a disposable
  snapshot and verifies zero enabled jobs before supervised selection. The source
  Hermes home is mounted read-only. No guessed schedule importer was added.

## Final combined gate

`just docker-check` session 50105 passed on the final source (exit 0). Earlier new probes
exposed an invalid Docker CLI `--extra-host` flag (fixed to `--add-host`) and two
fixture defects: missing valid host ports and checking the idle deadline before
terminal stream settlement. Regression/fixture checks now cover these corrections.
Session 91958 exposed the same asynchronous terminal-settlement boundary before
the idle no-wake probe removed a container: the remaining stream lease correctly
caused recovery. The fixture now waits for Idle/zero leases at recovery, race and
routine handoff boundaries instead of assuming delivery means stream teardown ended.
The combined final probe additionally rejected shutdown before either real idle
deadline. Its observed UTC settlement/deadlines were Bob 19:10:05/19:15:05 and
Alice 19:10:14/19:15:14. Both automatically stopped; Alice's next request began cold
replacement at 19:15:24 and completed with the original session. The complete gate
passed standalone smoke, native API/session/run/SSE/cancellation/approval/restart,
host-supervisor recovery/races/pin/idle no-wake/orphans/routines/preservation and
the full two-user gateway lifecycle. No acceptance comes from mocks alone.

## Release boundary

Issue 19 explicitly requires one compatibility release before deleting the legacy
executor/static rollback. That release has not been published or accepted in this
work. A release name in a selection file does not satisfy this requirement. Full
recurring routine CRUD/DST/native import remains issue 33; contexts with enabled
native cron are refused supervised migration and retain their static scheduler.
No live Telegram/model account interaction, login, external send or publication was
authorized or exercised. Do not close the milestone as complete while its release
gate remains outstanding.
