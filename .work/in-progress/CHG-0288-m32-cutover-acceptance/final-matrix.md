# Finite M32 live acceptance matrix

## 2026-11-08: five technical MVP checks requested by the owner

The owner narrowed this local acceptance pass to five technical checks. The
older 26-bullet table below is historical and is **not** the denominator for
this pass. Result: **5/5 technical checks passed**. This does not close remote
CI or prove a human-origin Telegram update. The container product snapshot was
`7c08b9516b65ddf567ce89ef01b0cee8cfcad61a` (local, unpublished); runtime
image `sha256:e89cdc2b9941b78fd23668c8c33dc974eb59fac05a22f2f51eea8166a1b46f0f`,
core image `sha256:39a742643e20053ccc42d11f4eda8df0c09f05e38d1446fcae03a2a2a97cef11`.
Both images carry that source revision. The successful full lifecycle used a
Go overlay only for test-driver diagnostics and cancellation handling; product
code was identical to the images. `TestM32RealGroupMemoryIsolation` ran without
an overlay on the exact snapshot. Later edits to this document and the test
driver are not part of the image.

| Technical check | Status | Evidence and limit |
| --- | --- | --- |
| Two-owner private/group isolation and open-session rights | PASS | Exact-source container `TestM32RealGroupMemoryIsolation`; full lifecycle also ran two overlapping Hermes private runs, own/foreign memory checks and selection/revoke/ended-run fences. No private marker entered the group model prompt. |
| Capture lost acknowledgement, replay and ToolHub restart | PASS | Container `TestM32RealLifecycleMemoryIsolation` observed the actual producer pump, lost acknowledgement, restart, and exactly one turn/reply/provenance row per owner; native `TestM32CaptureLostAcknowledgementNative` passed. |
| Assignment memory and current rights | PASS | Earlier two managed model assignments called `memory_search` then `memory_read`, used each owner's fact in summary/finding and delivered to own mock chat. `TestAssignmentMemoryCardRememberAndRevoke` exercised actual ToolHub/vault remember and denial after right revocation under a verified active assignment; current lifecycle exercised real assignment model wake, result and own delivery. Model-selected remember was not observed in the same live assignment; the write path was checked deterministically. |
| Dreaming routine to saved memory and ToolHub reuse | PASS | `TestM32DreamingRelayIsolationAndRecovery` passed after the final host fixes: protected routine due/retry, two owners and timezones, controlled three local days × ten facts, revoke/disable before commit, restart, one applied result and digest recovery, followed by ToolHub search/read. Earlier real model smoke ran for both owners; it produced summaries but no promoted facts, so semantic promotion by a live model remains unproved. |
| Due work, publication and delivery routing | PASS | Full container lifecycle passed once with both due agent jobs while communication was unavailable, durable results, restored exact owner delivery, watcher and assignment wakes, restart deduplication, pause/revoke send fences. Native communication package passed topic binding, voice receipt and no-repeat delivery tests. Prior live outbound Telegram receipts were observed; no human-origin incoming Telegram update was simulated or counted. |

The full lifecycle initially observed two failed Hermes jobs with a model HTTP
error, then passed on unchanged product code. This is a remaining test-stack
stability risk; the model HTTP status and exact cause were not retained. The
isolated run's synthetic objects were removed by its fixture cleanup. Existing
`artem_main` service containers were restarted after port use; personal vaults
were untouched. `just check` and affected package suites passed locally.

## 2026-11-07 correction after system-routine, assignment and terminal fixes

The frozen rows below retain the original test plan and historical evidence. The
following observations supersede their old failure descriptions; a partial row
remains open until every action and expected result in that row is observed.

The owner-requested acceptance count uses the 26 explicit bullets in the seven
sections of the current instruction (3+4+5+5+4+3+2). A PASS requires the whole
bullet. Current count: **16 PASS / 9 FAIL / 1 BLOCKED = 62%**. The baseline
source is dirty at `185892107fcc6056a41808fe22533a6c34c7940e`; current
ToolHub image `sha256:fc7d11b30fd61b441655e36c6568966e37b6006a7b379551a3f2bf9f3ca246de`,
main runtime `sha256:1fe4ecf8d574a74440a54846dba647ae9922746b511b5b0db04fe996a0c58db0`,
two runtime `sha256:5aba34eb9abde84fa3a52d4b971494533ba7f3d568fe686a05e618dd2d09b02f`.
These overlays are not exact committed-source images.

| Criterion | Status | Present evidence | Still required |
| --- | --- | --- | --- |
| Config: six authored inputs and revisions | PASS | Both published/applied revisions and six hashes matched | Exact-source image gate |
| Config: legacy paths inert | PASS | Reader audit, poisoning tests, restart | None for this bullet |
| Config: current catalog projection | PASS | Catalog 18.0.0, 52+6 tools in both managed runtimes after rebind/respawn; descriptions, effects and schemas match | None for this bullet |
| Rights: two owners and parallel private/group | FAIL | Two concurrent private runs | Group container run |
| Rights: private memory absent from group/other owner | FAIL | Other-owner denial and native group fixture | Live group prompt/tool assertion |
| Rights: selection/scopes/revoke in open session | PASS | Live narrow/remove/revoke/restore same session | None for this bullet |
| Rights: finished or forged run denied | PASS | Exact-run deterministic tests and live foreign denial | None for this bullet |
| Memory: lost-ack/retry/restart capture once | FAIL | Native and older partial live capture | Current container fault |
| Memory: real remember/search/read | PASS | Prior live remember; two real managed model jobs each called `memory_search` then `memory_read` (exact-job audit: completed/succeeded) and answered with their own synthetic color; catalog 18 projection | None for this bullet |
| Memory: proposal→owner confirmation→exact apply | PASS | Prior live owner confirmation, current native writer regressions | None for this bullet |
| Memory: self/foreign/stale/replay/revoke denial | PASS | Prior live denial and current native regressions | None for this bullet |
| Memory: interrupted write recovery | FAIL | Native journal recovery | Container/process interruption |
| Dreaming: routine execution for both | FAIL | Protected routine engine tests and schedules registered | Live due operation both owners |
| Dreaming: process/save/ToolHub reuse | FAIL | Real relay one day each, summaries, zero promoted facts | Complete ToolHub recall chain |
| Dreaming: repeat/restart/disable/revoke | FAIL | Deterministic checkpoint/right tests | Container fault and live retry |
| Dreaming: timezone/local date transition | PASS | Two-zone/DST deterministic tests | None for this bullet |
| Dreaming: controlled 3×10 and real model both | PASS | Isolated two-owner 3 local days × 10 facts per day with controlled time and no personal-vault edits; one actual model day per owner persisted its summary | None for this bullet; full live routine path and ToolHub reuse remain separate failing rows |
| Automation: assignment memory/current rights | FAIL | Two real direct-mock assignments used each owner's bound mock envelope; both model jobs called `memory_search` then `memory_read` successfully, persisted `amber` in summary and finding, and delivered to exact own mock chat; native rights tests | Live assignment remember and revoke before write in isolated data |
| Automation: pause/update/finish/recovery | PASS | Two-owner continuation, plan revision, finish refusal and native recovery | None for this bullet |
| Automation: due during communication outage/recovery | PASS | Two live durable due results delivered once after restart | None for this bullet |
| Automation: positive HTTP watcher/limits | PASS | Live public UUID watcher fetched changed responses and durably sent five exact own Telegram notices; native limit tests | None for this bullet |
| Terminal: short command through agent | PASS | Real artem_main model job selected `terminal_exec`; exact-job audit shows `terminal.exec` admitted and completed/succeeded, and its reply included the synthetic command marker. Direct managed MCP short commands passed both owners | artem_two model replied without invoking the tool; two-owner direct MCP remains separately proven |
| Terminal: long/output/input/kill/timeout | PASS | Live MCP job send/poll/kill/timeout and worker checks | None for this bullet |
| Terminal: foreign job/path denied | PASS | Live foreign ID and directory escape; named regression | None for this bullet |
| Channels: incoming Telegram round trip | BLOCKED | Outbound own Telegram receipts exist | Human-origin incoming message/access |
| Channels: group privacy | FAIL | Native fixture only | Real group run and private-marker assertion |

| Row | Current observation | Remaining check |
| --- | --- | --- |
| I2 | Catalog 18.0.0 projected to both existing managed owners (52 first-party plus six platform tools). Direct MCP calls in both real managed runtimes executed short terminal commands in agent-image containers. Background send/poll/kill, timeout, foreign job ID and directory escape were exercised; fixed killed-job result state. A subsequent artem_main real model job selected `terminal_exec`; exact-job audit shows authorized successful execution and the reply contains the synthetic marker. | The artem_two model did not choose terminal; exact committed-source Docker gate remains open. |
| A3 | Two earlier real owner assignments searched memory and delivered own Telegram receipts. A new direct-mock test bound the actual mock envelope for each owner, produced a routine occurrence and one model wake each; exact-job audit shows successful `memory_search` and `memory_read`, persisted summary/finding both used `amber`, and exact own mock deliveries passed. Both assignments and their exact mock links/deliveries were removed. The earlier failed mock probe used a Telegram caller envelope for a mock destination; stage trace confirmed no product clock defect. | Live assignment `memory_card remember` and post-revoke denial in isolated data; deterministic rights and finish tests passed. |
| D1/D2 | Dreaming now uses protected `system-dreaming` schedule and routine occurrence, with per-day processing checkpoints. Isolated temporary vaults passed two owners × three local days × ten facts using controlled `Now`. A real model call for each owner completed one synthetic day and persisted daily summary, but promoted zero facts. This satisfies the controlled-calendar plus two-owner model-smoke bullet. | Full live routine→ToolHub recall, duplicate/restart/revoke container fault and downstream use. Do not count the fixture-only calendar as live routine acceptance. |
| M1/M2/F1 | Earlier live capture and confirmation evidence remains. Current named authorization, journal recovery and isolated-vault tests pass. `memory_read` resolves an exact own indexed hit through ToolHub under the live-run memory gate; catalog 18 is projected in both managed runtimes. Two managed model jobs each called `memory_search` and `memory_read` successfully by exact-job audit and each eventually delivered its own synthetic color. The main reply arrived after the five-minute poll deadline; both were verified in the outbox. Exact mock deliveries and links were removed. | Lost-ack/restart container capture, full current-source confirmation/fault matrix, and private/group isolation in live containers. |
| A2 | Existing file/hook watch paths passed; native HTTP probes and limits passed. A live public UUID watcher fetched changing values, sent five own Telegram notices and was deleted; all five exact bot messages were removed. | None for this row. |
| I1 | Both owners' outgoing mock and Telegram delivery paths passed. | Actual incoming human Telegram round trip and group privacy path. Do not synthesize human input. |
| G1 | No new full Compose stack or full image rebuild; ToolHub and runtime overlays were scoped. Affected package suites and `just check` passed after stale-test, format and doc repairs. Source remains dirty at baseline SHA `185892107fcc6056a41808fe22533a6c34c7940e`. | Exact-source image gate and remote CI. Remote workflow/publication require owner instruction. |

Frozen 2026-11-07 for the existing `artem_main` and `artem_two` dev spaces. A PASS
requires evidence on the current source and deployed image; earlier fixture/native
results are supporting evidence only. Real defects may be fixed within these rows;
new optional cases go into a later change. Remote CI/publication is a separate gate.

| ID | Mechanism | Action | Expected result | Evidence and starting state |
| --- | --- | --- | --- | --- |
| C1 | Six passports and legacy retirement | Audit hubctl/supervisor/runtime/ToolHub/communication readers and writers; compare authored/published/applied for both owners; poison retired inputs safely | Six sole authored inputs; coherent revision, retired files inert | PASS: audited hubctl publisher, stack.ReadEffective aliases, supervisor resolution/spawn, managed runtime materialization/attestation, ToolHub settings and communication enrollment. No user config fallback. Organization policy and channel enrollment are distinct operational/ceiling state. Current six hashes/spawn/channel/settings agree (main r-f5fc67dc3c4791b8, two r-4543490ac923f7c7); native poisoning regressions passed and common live restart succeeded. Stale docs corrected |
| C2 | Settings and activation | Edit inert draft, publish/apply, restart, restore; probe stale/unauthorized and concurrent recovery | No prepublication effect, coherent activation and durable rollback/recovery | PASS: draft inert; dirty-base refusal; governed publication r-fa6de3ad265cf210 actually respawned two, value/revision survived combined restart, original absent leaf restored through settings_edit (r-4543490ac923f7c7). Prepared journal recovered through ordinary preview. Final hubctl suite includes stale/unauthorized/concurrent regressions |
| C3 | Tool scopes and grants | Check selected vs usable tools, narrowed path, removed selection, revoked rights in open run | No scope/selection bypass or resurrected right | PASS: same open MCP session enforced a published file_read rule, refused sibling and removed selection, then accepted original restored selection; published grants stayed identical. An exact real running model job then observed workspace.read revocation: post-revocation run-bound audit denied before execution. Original grant ID/scope/expiry restored by protected CAS (revision 3), live read succeeded again, probe removed. Exact model reply marker assertion failed, but correct-job delivery and a semantic denial term were verified |
| I1 | Communication and model | For each owner send normal, continued and ToolHub-using messages on Flash/high; inspect owner run and delivery | Correct own chat, nonempty reply, no cross-owner context | Both existing owner stacks passed programmatic mock-channel model round trips and outbox delivery; external Telegram round trip pending |
| I2 | ToolHub runtime | List catalog, file CRUD and foreign denial from managed runtime | Contract complete; own effects, foreign refusal | FAIL for full callable catalog: shape/parity of 51 tools plus six platform tools, managed own CRUD and foreign refusal passed, but terminal_exec has a selected/granted public contract and reaches an explicit no-terminal-executor stub (terminal_job shares it). Direct sleep-1 and real-run backend-error reproduced; no native fallback or fake success. Mandatory file/memory/automation evidence remains valid; terminal implementation gap needs resolution |
| M1 | Memory capture and isolation | Capture real turn, inspect saved record, replay lost acknowledgement/restart, check private/group context and other owner | Durable once, correct provenance, no foreign disclosure | Both model turns captured and recalled owner-only synthetic facts after ToolHub restart; lost-ack and group container faults pending |
| M2 | Memory tools and confirmation | Remember/search/read/edit/delete with owner confirmation, stale/replay/revoke fences; use fact in later reply | Exact guarded mutation, current rights, persistence, no direct runtime vault | Native real-vault/channel test passed; live full cycle pending |
| D1 | Dreaming input and schedule | Prepare 3 local days × 10+ synthetic facts per owner through supported import; schedule near-term due tick | Correct source days and actual scheduled runs for both zones | Both protected system routines registered and one local day is complete per owner; safe three-day fixture remains unavailable through current import path |
| D2 | Dreaming effects | Inspect 3 summaries/long-term updates per owner, ToolHub recall, model use, progress and duplicate tick | Correct meaning/provenance, no duplicate or mixed memory | Native relay/PDP/vault fault test passed; live effects pending |
| A1 | Routines | One-shot and repeat due, edit/pause/cancel; inspect execution, next due, result and owner delivery | Single owner clock, durable result and correct delivery | PASS for frozen row: both owners' once/repeat/edit/pause/no-later-wake mock legs passed. A real Flash/high model invoked routine_list and routine_create through ToolHub, then normal clock delivered its 90s reminder to main's verified own Telegram chat; exact SQLite done/occurrence/chat/marker/positive Telegram-ID receipt verified. Routine and that exact bot message removed |
| A2 | Watchers | Own source unchanged/changed/unchanged, update and stop; supported action/filter variants | One event per change, no duplicate, durable action | Both hook assignments passed filter/agent, idle/dedup/update/stop. Both command/file probes passed silent red baseline, blue change/one mock notification, unchanged poll/no duplicate, retained baseline after combined restart, pause then green with no poll past due; exact sources/schedules removed. HTTP and remaining substantially different variants remain open |
| A3 | Assignments | Source event, work, continuation, progress/status, update/finish; ToolHub and memory in one chain | Durable correct run/result/delivery, no later wake after finish | Both owners passed two distinct real model runs, ToolHub file calls, progress/findings/counters, plan revision 2, real Telegram receipts, completion and later hook refusal; objects/messages removed. Successful memory chain BLOCKED: SPEC-0098 M2 admits model memory only for interactive private runs, not background assignments |
| R1 | Restart and outage | One controlled restart after active work; keep memory/settings/automation and pending delivery | Stable images, no crash loop, retained state and exactly one due wake | Core live PASS: zero leases before stop; two actual due reminders durable while communication stopped; ToolHub/supervisor/runtimes/proxies/relays restarted, both pending results delivered once; changed setting and watcher state survived, a fresh post-restart poll did not duplicate. Both real Flash/high model runs then succeeded through memory.search and used the old fact. Dreaming-result recall remains dependent on unpassed D1/D2 |
| F1 | Security and fault gates | Concurrent private/group, disable/revoke, claimed run denial, capture/edit crash, scheduler competition and DST | Fail closed, once-only commits, no cross-user access | Named native tests passed; actual container fault matrix pending |
| G1 | Final gates and cleanup | Check exact image/source IDs; run final just check and affected tests once; clean only test objects; CI if authorized | Reproducible final source, no test residue, honest issue trace | Final `just check` and six affected package suites passed after one stale-test repair; 48 mock deliveries/six links and two schedules removed. Vault test memory remains; unpublished source has no exact-commit Docker/CI proof |
