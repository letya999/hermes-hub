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

Published compatibility release: https://github.com/letya999/hermes-hub/releases/tag/v0.2.1
Source promoted through PRs 66 (dev) and 67 (main), main a27532e555fb7daeeb73df236036e0f1108ed47f.
Downloaded published SHA256SUMS and Linux binaries verified against accepted Docker
image 7a3f67d90dba:

- hub-runtime: adc3ef1b99a952e0e2bc849928590be6833fc1b203ed3c599c44434f68c8d5be
- communication-hub: 87115df14d5be0b0f6b1268c0886746adcc9dc03e7c05b1da372fdb0f34b0954
- hubctl: bea6420a2185a2df57d867a542f4fc95b87657edf7ca45a1cc0f1c75f836fa54

Published source/assets passed archive secret scans. Linux CI checks passed but
container fixtures failed resolving host.docker.internal; the standalone fixture
now supplies host-gateway like the production supervisor already did. This failure
is recorded separately from the accepted real local Docker evidence.

v0.3.0 retirement removes one-shot execution and the native-mode toggle. Saved
legacy selections normalize to static native routing; published v0.2.1 artifacts
remain available for rollback. Final retirement gates and publication completed.
Full recurring routine CRUD/DST/native import remains issue 33; enabled native cron
refuses supervised migration and retains its static native clock. Verification uses
synthetic local Telegram/model fixtures; no real account login or send is claimed.

## Final retirement acceptance

Linux CI https://github.com/letya999/hermes-hub/actions/runs/34715743182
passed `just check` (85.06% own coverage), `just security` (zero findings), and
real `just docker-check` for dev and prod. Both contracts accepted native
sessions/runs/events/cancellation/approval/restart, supervisor recovery/races,
operator pin, orphan ownership and routine wake/sleep/preservation. Both full
production gateway paths accepted exact two-user homes, duplicate updates once,
warm session reuse and released terminal leases. Actual five-minute automatic
graceful removal and original-session cold restore passed at 20:14:46 UTC (prod)
and 20:14:56 UTC (dev). No integration acceptance relies solely on mocks.

Local retirement `just check` passed twice (92643/5751), own coverage 85.03%.
v0.3.0 published at main 1b1fe3413ba525309c0cc186079d6489d549e6f4 after
PRs 68/69. Source/archive scans and all candidate SHA256SUMS verified. Downloaded
published runtime/communication assets match sums and image c74d6046795a:

- hub-runtime: c07e72970accdf1dc0265134c6a53ebb9a5f4c448830d245b2429b5adb4eb8e1
- communication-hub: 8ba614bbaae08e34e07ba1e939532cf25a7dbb8dc9751b3148593d0e2553afc5
- hubctl: 6fe318f1ecbd2a2bc85a28f1e117a1afc94ed3d7b5e5364237759dc9d41cf126

Release: https://github.com/letya999/hermes-hub/releases/tag/v0.3.0
Issue19 closed after release acceptance. Milestone2 closed with 6/6 issues, zero open.
v0.2.1 remains published for artifact rollback; current native static selection
cannot restore the deleted one-shot executor. Full schedule import remains #33.

## Local fixture settlement correction

Retirement local combined gate 55151 passed standalone/native approval/restart,
recovery/race/orphan/routine contracts, then failed the immediate Alice mapping
assertion. The worker enqueues delivery before CompleteJob saves terminal mapping;
a final transport response is not proof of durable spool settlement. The shared
fixture reader now waits up to ten seconds for completed mappings, retaining all
identity assertions. Production execution binaries are unchanged. Local `just check`
36387 passed (85.03% own coverage); standalone real gateway repeat 21011 passed (exit 0): automatic graceful
shutdown after five actual minutes, no idle compute, original-session cold restore.
