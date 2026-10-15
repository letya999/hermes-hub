# M19 recovery verification — 2026-10-14

## Latest continuation: single Docker stack

- Later verification supersedes the earlier Windows registration failure: the
  existing interactive task was updated successfully, and killing its idle
  supervisor child restored service in 1.75 seconds
  (`.local/m19-scheduled-restart.json`). It is currently stopped. CHG-0097 replaces
  it with a Compose service after container acceptance, per the owner's request.
- The rebuilt base image and standalone STT image both passed WAV decode;
  the real STT command also produced a transcript. The rebuilt Hermes contract
  passed session, stream, stop, approval, cron, restart and STT checks
  (`.local/m19-final-hermes-contract.log`). Earlier STT-pending notes below are
  historical, not the current result.
- The corrected chaos run passed runtime mid-run/mid-delivery and communication
  admission/mid-run cells. The complete matrix and >5-minute heartbeat were
  interrupted and are still not accepted. No partial run proves the full grid.
- Idle gateway CPU remained above the 5% target while scanning the old terminal
  ledger. A nonterminal mapping index now updates after durable writes and is
  rebuilt on spool reopen; communication tests pass with the race detector
  (`.local/m19-active-index-resume.log`). Post-deploy CPU measurement is pending.
- Docker Desktop was restored by preserving its two socket-only directories
  under backup names after deletion was rejected. Engine 29.4.3 responds; no
  factory reset was used. Five stopped fixture containers and forty unused
  synthetic test volumes were removed with exact namespace/attachment checks.
- Container supervisor passed authenticated health, managed runtime wake and
  same-generation adoption after restart. Authenticated health recovered in
  2.854 seconds. Windows supervisor and legacy MCP-Hub tasks were removed with
  local XML backups. Runtime/control relay ports are not published on the host.
- Legacy wake exposed chmod of host-owned YAML; atomic replacement fixes this
  without elevating the controller. `just check` passes at 85.32% after the fix
  (`.local/m19-container-final-check.log`). Final image rollout is in progress.

## Owner-requested continuation with temporary TDD pause

- The owner explicitly requested pausing Probity. An ignored repository-local
  config disables only its TDD rule; the commit/push bypass prohibition remains.
  The global hook and other projects are unchanged.
- The Go supervisor watcher is wired into the CLI, including `-env` and `-image`.
  Real subprocess tests verify restart, cancellation, and CLI child launch.
  The Windows installer uses the supported one-minute Scheduler restart interval
  plus the one-second Go child restart, and now fails on registration errors.
- Windows denied task registration. The existing user-owned launcher was backed
  up and updated to the verified watcher binary. A direct launch succeeded.
  Killing the exact idle child restored the listener in 1.777 seconds; this does
  not establish active-run adoption or successful Scheduled Task startup.
  The durable ledger contains 835 jobs, 27 interrupted, and zero inconsistent
  interrupted responses after loading the new supervisor.
- Failure deliveries now include job ID and the controlled terminal outcome
  (`run.failed`, `run.interrupted`, or `run.cancelled`), without raw provider
  error text. Regression tests exercise actual stream receipt delivery.
- Both STT installation paths pin PyAV 18.1.0 with faster-whisper 1.2.1.
  The new offline WAV decoder test fails on PyAV 19 and passes on the rebuilt
  standalone STT image (`.local/m19-stt-sidecar.log`). CI also runs this contract.
  Base-image verification is pending the full build.
- The unsafe PowerShell chaos draft was replaced with a thin entry point for
  the isolated Go fixture. It uses synthetic accounts, exact container ownership
  checks, synchronized phase barriers, a synced append-only effect journal,
  and transport delivery receipts. Windows excluded ports required moving the
  fixture range to 32000–32999. The first full matrix still uses the old range;
  its failed cells must be rerun before acceptance.
- `just security` passes (`.local/m19-final-security.log`). The first fresh
  `just check` exposed race-instrumentation exit delay in a subprocess test;
  setting `GORACE=atexit_sleep_ms=0` only in that test passes ten repeats.
  The complete check is being rerun; earlier coverage is not final-source proof.

## Continuation after the interrupted run

- `just docker-check dev` completed with exit 0; log `.local/m19-resume-docker.log`.
  The LF repair works, but STT is still unavailable: faster-whisper 1.2.1 calls
  `av.open(metadata_errors=...)`, removed in the resolved PyAV 19.0.0.
  An isolated disposable container with `av==18.1.0` decoded a synthetic
  one-second WAV successfully; the same decode failed on the shipped image.
  Neither Dockerfile has been updated to pin the compatible dependency yet.
- `just check` completed with exit 0 and 85.32% coverage; log
  `.local/m19-resume-check.log`. No live deployment was performed.
- The Go chaos fixture's backend rejected Docker host requests with HTTP 403.
  Localhost protection is now disabled only on that isolated synthetic backend.
  A phase check refuses to kill mid-run without one completed tool effect.
  Runtime kill/start scenarios passed: admission (failed, zero effects, one send),
  mid-run (failed, one effect, one send), mid-delivery (completed, one effect,
  one send). Logs: `.local/m19-chaos-runtime.log` and
  `.local/m19-chaos-runtime-delivery.log`. Failure messages are still generic:
  these results prove no duplicates, not full cause-named A3.1 acceptance.
  Other target/phase combinations have not been added or executed.
- Windows Task Scheduler requires RestartInterval >= 1 minute, so the inherited
  installer cannot provide its claimed 10-second restart. A Go child watcher
  and real subprocess restart/cancellation test pass. The `-watch` and `-env`
  flags parse, but are NOT YET WIRED into run(); do not deploy this partial CLI.
  Installer/docs changes and the CLI launch integration test remain unwritten.
- Initial read-only inspection found no running host supervisor, no listener
  on 8876, and four stopped runtime records with zero leases. This is a snapshot,
  not continuing observation. Existing task HermesHubDevSupervisor was Ready.
- Probity rejected multi-case test writes and behavior beyond the observed red;
  work was split accordingly. It subsequently repeatedly rejected even a single
  CLI test write with `exceeded retry limit, last status: 429 Too Many Requests`.
  No alternate write path or hook bypass was used. Restore its service access
  before continuing source changes. Owner decisions on ADRs and live Telegram
  test sends were requested separately and have not been received.

Handoff branch: `feat/m19-execution-chain-reliability`, HEAD `1c23682`.
The inherited working tree also contains CHG-0090/0091; it was not reset or
committed. GitHub milestone number 20 is titled M19. The handoff's M29 label
does not create another milestone.

## Confirmed in this continuation

| Check | Result and scope |
|---|---|
| Standalone gateway lifecycle | PASS on pinned Hermes containers with local model/Telegram fixtures, including the full five-minute idle window, stop, cold restore, two principals and duplicate-update delivery. The warm-window predicate now checks and saves the same status snapshot. |
| Readmit before fix | Real supervisor contract failed with HTTP 500: metadata-only readmit reconstructed a different Hermes payload. Local log: `.local/m19-readmit-red.log`. |
| Readmit after fix | Real pinned-Hermes contract PASS: original completed run/session IDs returned, append-only tool counter stayed at one after replay. Local log: `.local/m19-docker-check.log`, `Real readmit passed`. |
| Receipt boundaries | Unit and race tests cover different input under one key, another job/principal, missing/corrupt/empty/expired/future receipts, concurrent conflicting creators and expiry compaction. These are not external-provider exactly-once evidence. |
| Artifact replay | Red then green: replay preserves the original recorded run-start window, returns its already-written deliverable, and excludes a file older than that run. |
| Run-budget boundary | Red then green at exact `UnconfirmedSince + RunBudget`: the reconciler expires at equality, without another observation attempt. |
| Expiry replay/durability | Red then green: interrupted record and replay response agree; failed persistence leaves the record uncertain for retry. |
| Legacy interrupted replay | Red then green: load repairs empty/uncertain responses on already-interrupted records, preserves identity/time, and persists the repair. |
| Release versus sweep | PASS, ten runs under `-race`: no count/map divergence and the other principal's live lease remains held. |
| Failed idle cleanup | Red then green: an expired idle runtime marked degraded after Docker removal failure is retried, with ownership and desired-work checks preserved. |
| Native Hermes / approvals / recovery | Pinned API contract, real approval/generation fencing, layered health, orphan ownership and crashed-turn wait passed during the full Docker run. |

The runtime now writes the original Hermes input/session/instructions to
private `/state/admissions/` before POST. The supervisor stays metadata-only.
Legacy requests without that receipt cannot safely readmit. Replay is refused
at 23 hours, inside upstream's documented 24-hour retention; new admissions
compact receipts older than 24 hours. Idle state can retain old receipts until
the next admission. This adds private content to runtime state, never to Git
or diagnostic output. See [operations](../../../docs/operations.md).

## Live drain observations

- Read-only aggregate: 835 job records, zero nonterminal records, zero leases.
- `local` reached idle with zero leases. After the deadline, Docker showed its
  exact runtime container exited. Supervisor remained degraded after cleanup
  failed, exposing the retry defect fixed above.
- The first full Docker gate then removed that exited residue through its
  scoped `docker-clean`; the unchanged supervisor observed absence and reached
  `stopped`, zero leases. This completes the drain, not deployment of the fix.
- 26 interrupted records retain stale responses in the live ledger (21 uncertain,
  five empty). The load repair is tested but is **not deployed**.
- Automatic approval review rejected replacing PID 381080 with the verified
  host binary (`blocked by policy`, no more specific reason). The old process
  remains running; no live state rewrite or restart was performed here.
- Prepared binary: `.local/bin/hub-supervisor-m19-verified.exe`. Preserve the
  existing launch configuration/authentication and use a single writer when
  applying it. The existing process is not running this binary.

## Gates and limitations

Final-source `just check` passed with 85.32% statement coverage, including
race/shuffle, formatting, vet, staticcheck, docs, actionlint and deployment
configuration contracts. Log: `.local/m19-delivery-check.log`. Docker results
are recorded in `state.yaml` after completion. Logs remain local under `.local/`.

The first full Docker recipe exited 0, including lifecycle, stdio reminder,
capability, managed-network/supervisor, scratch and quarantine checks. It
exposed CRLF in the executable Python shebangs
`docker/hub-stt` / `docker/hub-tts`. STT was reported unavailable, not passed.
Source line endings and Git attributes are corrected; a rebuilt-image check
is required before claiming STT success. No Hermes upstream code was patched.

Repository safety scan: no gitleaks/trufflehog secrets and no reachable
govulncheck finding. The aggregate scan was not green: Windows cache-link
traversal, generated-script lint and gosec/Go-version diagnostics prevented a
clean result. No dependency was changed in this continuation.

## Chaos grid — not yet acceptance evidence

| Kill target | Admission | Mid-run | Mid-delivery |
|---|---|---|---|
| runtime | Not run | Not run | Not run |
| ToolHub | Not run | Not run | Not run |
| communication-hub | Not run | Not run | Not run |
| supervisor | Not run | Not run | Not run |

The inherited PowerShell draft is unsafe and was not executed. It selects all
runtime names globally, chooses the first supervisor-like process, restores
components after rather than during settlement, counts files instead of tool
effects, and cannot prove it hit the delivery phase. No row is promoted from a
mock/unit test or a different restart scenario. Replacement needs isolated
synthetic principals, exact owned process/container IDs, explicit phase
barriers, an append-only counting tool, immediate recovery, and exact delivery
receipt checks. Owner Telegram canary remains disabled.

Still open: full 12-cell grid, owner-notice/photo trace acceptance, 24-hour
observation and measured live latency/CPU SLOs. ADR-0037/38/39 are proposed;
owner decisions have not been received. CHG-0092 through CHG-0096 stay in
progress; moving them to done or closing their issues would overstate evidence.

Progress updates published under the handoff instruction:
[#261](https://github.com/letya999/hermes-hub/issues/261#issuecomment-6162062700),
[#262](https://github.com/letya999/hermes-hub/issues/262#issuecomment-6162063838),
[#263](https://github.com/letya999/hermes-hub/issues/263#issuecomment-6162064764),
[#264](https://github.com/letya999/hermes-hub/issues/264#issuecomment-6162065878),
[#265](https://github.com/letya999/hermes-hub/issues/265#issuecomment-6162066866).

## Restored Docker acceptance, 2026-10-14

All twelve fault-injection combinations passed sequentially on the restored
Docker engine: runtime, ToolHub, communication hub and supervisor, each at
admission, mid-run and mid-delivery. Runtime admission ended in a named failure
with zero effects; runtime mid-run failed after exactly one effect. Other
combinations completed with exactly one effect. Every case delivered exactly
one final response. The strict 305-second heartbeat case also passed (382.12s total fixture duration). The complete thirteen-case run passed in 1318.208s.
Log: `.local/m19-chaos-restored-docker.log`. The fixture compiles current Go
services; the runtime uses the existing dev image whose runtime code was
unchanged by the subsequent atomic supervisor-config and build-transport fixes.

The owner now authorizes real Telegram acceptance sends and explicitly waives
the 24-hour wait in favor of bounded accelerated acceptance. Actual observation
duration will be reported; this is not a 24-hour stability claim.

The isolated live gateway exercised the configured owner with a real model,
runtime, and Telegram outbound API. Thirty synthetic inbound text updates each
completed with one final send. Observed liveness p50/p95 was 257/515 ms
(maximum 554 ms); warm answer p50/p95 was 6297/7941 ms (maximum 11361 ms).
The first answer took 11.475 s after runtime prewarm. A real Telegram photo
upload then yielded the correct red/blue description in 28.796 s, with one
final send and 495 ms liveness. Evidence:
`.local/m19-text-acceptance-results.json`,
`.local/m19-photo-acceptance-results.json`, and their live acceptance logs.
Inbound Telegram updates were synthetic; outbound sends, model calls and photo
fetches used the real integrations.
The isolated usage journal has 30 known input-token deltas for those 30
tool-free turns (one model call each), with p50 11,988, p95 12,827 and
maximum 12,861 tokens. No turn had unknown usage. This meets S8's p50
15,000-token target. Source:
`.local/m19-live-acceptance/spool-2470661162/usage.jsonl`.

The canary's first live round exposed that ordinary tool events carry an empty
display progress string. The runtime now projects the upstream tool name as a
separate event field; the canary reads that field and accounts for
Hermes' tool discovery/invocation wrapper while rejecting unrelated tool names.
The earlier `unexpected_tool` owner notice was a false positive. The live
baseline and fault/recovery exercise remain pending the rebuilt image.

The first 30-sample Docker idle CPU measurement on the pre-throttle gateway
ran 15:24:07–15:29:38 Moscow time: mean 10.79%, median 10.28%, p95 15.60%.
This fails S9's 5% target. The spool contained 153 failed records, 134
schedules and 269 occurrences; four workers rescanned failed records each
second and the proactive loop rescanned schedules/occurrences each second.
Both background scans now run at most every five seconds; the one-second
interactive worker fallback remains. Repeat the same live sample on the final
image before claiming S9. Raw observations:
`.local/m19-gateway-idle-cpu.csv`.

The first final-runtime canary exercise produced three healthy rounds before
the model proxy was stopped. The round starting at 12:52:16 UTC failed at
12:53:18 UTC, sent one real owner notice naming `run_failed`, and met S10's
five-minute detection limit. A second round exposed an acceptance-only short
interval bug: `canaryLoop` reused the tick's stale timestamp after an overlong
round, classifying the next run as `timeout` and sending a second notice for
that different class. Model restoration produced healthy rounds and one
recovery notice. The loop now starts each run's deadline at its actual start;
`TestCanaryTimeoutStartsWhenRoundActuallyRuns` covers intervals shorter than
the run duration. This fix awaits the final rebuilt image and one repeat of
the fault/dedupe scenario. Evidence: `.local/m19-live-canary-final.log`.

The composed Docker contract on the new core image passed pinned Hermes,
native approval, two-user gateway isolation, warm session reuse, released
leases, five-minute idle shutdown, and cold restore. STT decode on the same
image passed. Evidence: `.local/m19-final-hermes-contract-after-canary.log`
and `.local/m19-final-image-stt-after-canary.log`.

The repeated live canary exercise used the timer-corrected Go binary in an
isolated gateway container. Two healthy rounds preceded the model outage.
Two consecutive outage rounds both classified `run_failed`; exactly one real
owner failure notice was sent. Restoring CLIProxy produced healthy rounds and
exactly one recovery notice. The isolated gateway exited and the ordinary
gateway was restored healthy. Evidence: `.local/m19-live-canary-final2.log`.

The repeated five-minute idle sample contains 30 `docker stats` observations:
mean 1.47% of one core, p95 5.18%, maximum 5.53%. S9's mean <=5% target now
passes on the throttled gateway; the p95 remains slightly above 5% and is
reported without rounding it away. Raw data:
`.local/m19-gateway-idle-cpu-after-cadence.csv`.

Managed-user wake and model invocation passed with a private agent network,
no host ports and a released lease. Its ToolHub agent-tools binding was the
only active binding migrated from the old to the rendered current policy;
disabled/revoked and unrelated integration bindings were untouched. A fresh
MCP session through the managed relay then listed 35 tools, including
`file_list`, where the stale binding had listed 11 bootstrap tools. The
managed runtime's readiness probe additionally found the relay's read-only
connector and model-catalog routes were closed despite successful MCP and
chat calls. The relays now expose only caller-scoped `GET /v1/connectors` and
a selected-model-filtered `GET /v1/models`, respectively, alongside their
existing MCP/chat routes. On the rebuilt image, the model catalog probe returned
`ok`. The remaining `browser` manifest entry had no ToolHub definition or
binding, so the ignored managed workspace request was set to `off` after a
private backup; this removes no served tool. Render changed the rights-policy
version. The one active agent-tools binding migrated to that version, and the
operator capability profile was renewed at revision 5 with unchanged selections
and grants. With the protected registry reloaded, live `/v1/health` reported
`hermes_readiness=ready`, `tool_surface=ok`, no missing tools and
`model_status=ok`; a fresh MCP session listed 35 tools. The user runtime had
no host port mappings. This first readiness check preceded the final
idle-recovery supervisor image; its source regression and `just check` passed.

The missing-idle case was reproduced live after the managed runtime container
and its control relay were stopped and removed with zero leases: the previous
supervisor briefly kept the runtime `idle` and returned a stale wake without a
container. `ReconcileHealth` now durably changes a confirmed missing idle
runtime to `stopped` immediately, releasing its capacity; the next wake is the
only action that creates replacement compute. The focused regression passed.
On the final rebuilt core/control images, the supervisor marked the missing
runtime `stopped`; a real wake created generation `gen-292` on the final image
with no host port bindings. It settled to `idle`, zero leases, and reported
`ready`/`tool_surface=ok`/`model_status=ok` with no missing tools. The two
temporary runtime containers were then removed; the supervisor returned to
`stopped` and the main stack remained healthy. The final full `just check`
passed at 85.33% Go statement coverage.

The final core and control images were built through `hubctl build` and all
seven selected main services were recreated healthy on their final tags. The
build command's automatic tail for the unchanged optional CLI cell images was
stopped after core/control export to avoid duplicate downloads; existing
on-demand CLI images remain. `just docker-clean` passed, and three unreferenced
`:test` tags were removed without force. Final inventory: six project image
tags and twelve running containers (nine main-stack services, three relays for
the managed user; no user compute container). The heavy Docker image/containers
CI gate was not rerun on this final source revision; the earlier pinned-Hermes,
two-user, idle-reap, STT and chaos contracts passed before the isolated
missing-idle supervisor fix. The live gen-292 wake verified that fix on the
final image.
