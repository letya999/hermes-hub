# #526 live acceptance, 2026-11-07

Source at the start of the local run: `3710949a0ba4742d2d4eaebb48281218c8ea4ee4` plus the #526 worktree diff. The diagnostic Docker image `hermes-hub:0.5.0-prod` was `sha256:098b90767f2a8f8dee812048c270a4c178e01076c89eeafc27d70255ed0a90ff`. It contained temporary checkpoint logging in supervisor; those lines were removed from the source after the diagnosis. Neither image nor run is remote CI acceptance of a clean commit.

The fixes and initial evidence were committed as `c079f375`. `hubctl build` then rebuilt the clean commit; `hermes-hub:0.5.0-prod` is `sha256:337228f880ffb7003aa952d4be7a8e427387823b7b4341bd6dc84524c2800ebe`. `hubctl up --no-build` recreated the synthetic Compose services. The supervisor container image ID matched its rebuilt `prod-control` tag, and both newly spawned managed contexts had image ID `sha256:337228f880ffb7003aa952d4be7a8e427387823b7b4341bd6dc84524c2800ebe` from Docker inspect. This is still local acceptance, not a remote CI run.

`hubctl build` used BuildKit. `hubctl space apply --confirm` published two synthetic users, `zzm32f` and `zzm32g`, from their authored passports. `hubctl up --no-build` started real Compose services. Their managed Hermes contexts started through the real supervisor and Docker daemon. The local model provider was an HTTP fixture; the real model relay processed its requests. This fixture required `allow_private: true` in ignored generated model registries so the relay could reach `host.docker.internal:18888`. An authored-only model provider acceptance remains open.

The first apply exposed a publish/order error: it attempted reconciliation before the generated Compose inputs existed. The fix publishes the first revision without actions; `hubctl up` starts services afterward. A shared `cliproxy` rebuild also used a nonexistent suffixed Compose service; service name resolution now uses the bare shared name. A removed synthetic sibling remained in the enrolled ToolHub bearer set; enrollment now replaces that set from current sibling spaces. These paths have regression tests.

The fixture initially omitted `toolhub-relay` and `egress-relay`, and the supervisor refused to start Hermes until those authored services were published and up. Two old synthetic contexts were left behind by the previous isolated fixture; the supervisor classified them as owned orphans and refused capacity. Only those four named synthetic runtime/control containers were removed, then supervisor restarted. No project-wide Docker prune was used.

Observed live results:

| Check | Result |
| --- | --- |
| Broker `/healthz` over synthetic TLS | HTTP 200 |
| ToolHub MCP projected catalog | 8 projected tools for each synthetic user |
| Unauthenticated MCP | HTTP 401 |
| Memory call without exact run | Denied by ToolHub run-context authority |
| Two concurrent private managed runs | Both streamed `running`, then `completed`; current generation matched |
| `memory_card remember` during each run | Succeeded for both users |
| `memory_search` for own marker | Succeeded for both users |
| Foreign user's bearer with the other run | Denied for both directions |
| Model relay via fixture | HTTP 200 after local fixture transport override |
| Clean-commit managed image | Both contexts used the exact rebuilt runtime image ID |
| Existing-card fact and truth proposal | Both users wrote a fact and persisted a proposal while their runs were active |
| Owner confirmation and replay | Both owner confirmations returned 200 and applied the exact text; both replays returned 403 |
| Foreign confirmation | 403 for both users before owner confirmation |
| Stale-base confirmation | After another fact changed the card, both confirmations returned 403 and both files were byte-identical before/after |
| Revoke before confirmation | Revoked synthetic `memory.remember` right with a current proposal base; owner confirmation returned 403 and file remained unchanged |

The `cliproxy` and `communication-hub` synthetic Compose services later entered restart loops because their fixture inputs were incomplete; their initial Compose health is not evidence of sustained service health. The full six-authored-inputs/no-effective-override requirement is not met by this model fixture. No external provider login, message send, or publication was performed.

Still open in #526: durable capture under lost acknowledgment/restart and bounded per-user work; group/private concurrency beyond the two private runs; routine/assignment/watch CRUD, claims, pause/revoke, gateway-down execution and deferred delivery; per-user local-day dreaming, recovery, revoke-before-commit and digest drafts. These require further real-container acceptance before #526 can be closed.

## Existing dev-space readiness audit, 2026-11-07

Read-only checks found that `artem_two` has an active generated six-passport bundle but no six authored `passports/*.yaml` directory. Its published bundle lacks the now-required `stack.identity.infra`, `config.memory.enabled` and `tools.binding`, and carries a retired `tools.select` grant. A scratch in-memory compile against the current catalog required 25 classified leaf changes, mostly tools/grants; it did not write a draft or alter the published pointer. `artem_main` likewise has no authored six-file directory, and its active bundle fails the current strict v2 decoder. Neither space can be declared cut over or used as acceptance evidence yet.
The current `stack.ReadEffective(artem_two, dev)` call also fails: the generated dev config view has no activation `revision` marker. This is a direct managed-read failure, separate from Docker availability.

The sibling infra resolver picked an unrelated synthetic M32 space because three sibling spaces claimed infra while neither `artem_main` nor `artem_two` did. Source now refuses alphabetical selection when claims are ambiguous and no longer falls back to another sibling's governance document. No Docker containers or volumes were touched during the owner's disk cleanup. Addressed removal of four old synthetic space directories was rejected by automatic execution policy; those directories remain.

## Continuation on 2026-11-07 (current working tree)

Reused the existing `zzm32f` infra and `zzm32g` managed space; no additional Compose project was created. A scoped `hubctl build ... toolhub communication-hub` built the changed control/core images, and `hubctl up --no-build ... toolhub communication-hub` recreated only those services. `just docker-clean` subsequently passed. This was a dirty-tree image, not final-SHA Docker acceptance.

The `zzm32g` test capability policy/profile initially admitted only routine tools. Publishing policy revision 3 and profile revision 7 added the existing assignment/watch selections and direct `core_tools` visibility. Public MCP `tools/list` then projected all five watch and six assignment tools. Without a managed runtime, calls returned ToolHub's uncertain-execution result because the agent executor channel was absent. During a real supervisor-managed run, ToolHub `routine_list`, `assignment_list`, `watch_list`, assignment create/read/remove, routine create/read/remove, and watch create/list/remove all succeeded through MCP. The watch request used a cron expression and `delivery=stream` with a bounded `min_interval_seconds`; two earlier malformed fixture requests were rejected with HTTP 400 as designed. The model run itself did not complete, so this proves the tool path during a running context, not a completed model conversation or due-work delivery.

The original synthetic passports cannot adopt authored `models.services.fixture.allow_private` through an ordinary update: their *previous published* grants omit `models.configure`. The publisher correctly rejects the change even after an operator issues a temporary live grant. Both temporary grants were revoked and the unpublished passport edits removed. The existing generated model registries do not allow the private local fixture address, so current model runs fail. The new source supports `allow_private` in a fresh authored models passport, but the six-authored-input/no-override criterion is not yet demonstrated on these already published fixtures. No access boundary was bypassed to make the test green.

Local validation on this diff: `just check` and affected `just test` packages passed. The full `just check-full` race/shuffle/coverage leg passed at 85.46% own-code coverage; staticcheck then found four unused remnants. After removing them, staticcheck, gosec, docs-check, config-check and affected package tests passed separately. The final `just go-check` race/shuffle/coverage leg passed at **85.48%** with Windows package concurrency increased from one to two and permission-only executable-launch retries. A transient Windows `compile.exe: Access is denied` interrupted an earlier lint attempt; its clean staticcheck retry passed. The final tree still needs exact-SHA Docker/CI acceptance and the remaining live #526 fault matrix. No GitHub issue was closed.

## Scoped image smoke from code commit `86c7f9c68f9a0996a477e8a8fe22c7f4d27a362d`

`hubctl build --dir .work/tmp/m32-real/spaces/zzm32f --env prod toolhub communication-hub supervisor model-relay` exited 0 after the duplicate Compose-stage build fix. `hubctl build --image runtime` exited 0. Core, control and runtime image IDs were respectively `sha256:50a71a84057dd86b92fae3afeab24e460db01fbb90ef42c4498d1a02152eaf49`, `sha256:f933ada23e64a606bad3966ff0204ca4054a52de10eb1f50337cfa1d2240f57e`, and `sha256:60103b1f96845c0c75bf167d7a6bd099a5fc2ab815a7282386fda06274fa6e63`. The source checkout was clean except for the preexisting untracked `hubctl.exe` when built.

Scoped `hubctl up --no-build` recreated the existing synthetic ToolHub, communication, supervisor and model-relay containers; `zzm32g` model-relay was recreated separately. Docker inspect showed the selected control/core containers using those exact image IDs. An existing ignored supervisor port override was reapplied without a build to permit host-side acceptance calls. Public MCP projected memory, routine, assignment and watch tools. A fresh managed context and its control sidecar both used the runtime image ID above; while the run was active, routine/assignment/watch list, create, read where applicable, and remove calls succeeded through ToolHub. The model run did not complete because its old generated registry still rejects the private local fixture. `just docker-clean` exited 0. This is scoped image/tool smoke, not the seven-image Docker gate, CI acceptance, or a pass for the remaining #526 fault cases. This evidence-only update comes after the image build and therefore is not itself inside that code SHA.

## Issue-by-issue acceptance audit, 2026-11-07

The GitHub source of truth was read for all 20 issues; #508–#527 are all open. Earlier CHG test results are implementation evidence, not acceptance of this integrated revision. The following gaps are against each issue's published acceptance checklist.

| Issue | Local evidence on the integration branch | Acceptance still missing |
| --- | --- | --- |
| #508 | Governed settings publisher and regression tests | Live six-authored edit/restart and retired-override proof |
| #509 | Scope enforcement tests | Live open-session narrowing and separate configure/use revocation |
| #510 | Selection/removal regression tests | Live open-session connector/raw-MCP removal and re-add |
| #511 | Publication pause/concurrency tests | Integrated live old/new snapshot observation at every step |
| #512 | Crash/recovery fault tests | Integrated live interrupted apply and concurrent revocation |
| #513 | Per-user vault resolver tests; two live private memory markers | Live wrong-environment/traversal and independent lock fault cases |
| #514 | Exact-run denial tests; live foreign-run denial | Concurrent private/group run admission matrix |
| #515 | ToolHub search tests; two live private own-marker searches | Live disable and revoke matrix on the final integrated revision |
| #516 | Temporary-vault HTTP lost-ack/restart tests | Real-container capture fault and outage recovery |
| #517 | ToolHub CORE/recall tests; no runtime vault mount observed | Concurrent live private/group prompt marker check |
| #518 | Writer/retry/revocation tests; live remember and own marker | Live crash/retry at writer commit barrier |
| #519 | Proposal/confirmation tests; live owner, foreign, stale, replay and revoke checks | Live crash between write and receipt |
| #520 | Retired OpenKnowledge code/tests; managed runtime mount inspected | Final image mount/direct-write proof in the complete Docker gate |
| #521 | Automation host/clock race tests; live CRUD through ToolHub | Integrated competing tick/reentrant recovery test |
| #522 | Durable host tests; live ToolHub CRUD | Real-container lost dispatch ack, restart and unavailable delivery |
| #523 | ToolHub owner code/tests; live routine/assignment/watch CRUD | Due work with communication down, claim/restart and deferred delivery |
| #524 | Protected day routine and local fixture tests | Two live users in different zones, DST and config-change matrix |
| #525 | Single-user fixture rollup/digest path | Two-user live crash/revoke/outage matrix |
| #526 | Two private managed runs, memory checks, scoped image/tool smoke | Full six-authored, group, capture, due, dreaming and fault matrix |
| #527 | Fast `just check`, affected tests, prior local coverage and scoped images | Final seven-image Docker gate, published CI, complete #526 and CHG closure |

The last synthetic managed job reached terminal `failed` after the model fixture rejected its private address. A cancellation through the supervisor returned HTTP 200 for the terminal job; no live run remains in progress. The warm managed context and control sidecar may remain until the configured idle timeout. Inspecting their mount destinations showed no `/vault` mount. The selected synthetic communication-hub, ToolHub, supervisor and model-relay containers were still up when inspected; cliproxy was absent from this scoped up. No personal space or broad Docker cleanup was touched.

## Current local quality gates

On the integrated code with the audit evidence above in the working tree, `just go-check` exited 0 with race/shuffle and **85.48% own Go statement coverage**. `just check` exited 0 after the audit edit; `just lint` (including staticcheck and gosec), `just docs-check` (including actionlint), and `just config-check` each exited 0. These are local source checks. No remote CI run exists for this unpublished branch, and the seven-image Docker acceptance gate was not run after the latest code edits. The scoped exact-code-commit image smoke described above is narrower.

## Existing dev owners, latest continuation on 2026-11-07

This section supersedes the earlier provider and existing-space readiness failures.
Source is `185892107fcc6056a41808fe22533a6c34c7940e` plus the current working tree;
the local overlays are not acceptance of a published source SHA.

Both existing dev owners select CLIProxy `devin/swe-2-flash` with `high` reasoning.
Both fresh managed runtime runs completed with a model-initiated `memory_search`
and a nonempty reply. Each owner has six nonempty authored passports and a valid
published revision; retired root configuration remains quarantined locally.

Live results through the actual ToolHub and supervisor:

| Check | Result |
| --- | --- |
| Settings edit, stale revision, restore | Passed with absent and warm runtimes; warm container actually replaced; stale edit leaves bytes/revision unchanged; original value restored |
| Real due script with communication stopped | One occurrence dispatched by ToolHub; no output emitted; communication restored in finally |
| Watch sample | Projected read source invoked, isolated filter returned `drop`, no source error |
| Autonomous stream watch | ToolHub clock polled the source, one event entered the durable seen set, no pending batch/source errors; paused and removed |
| Existing assignment/routine/watch CRUD | Earlier live create/read/edit/pause/resume/remove, foreign-owner and stale-revision checks survived ToolHub replacement |
| Probe cleanup | No probe schedules remain; CRUD manifest is empty |
| Proxy/control service state | Selected services running with zero restart counts |

Settings acceptance exposed two internal cycles: ToolHub held its registry fence
while the publisher wrote that registry, and publisher reconciliation used an
external PDP route while its publication journal intentionally denied admission.
The built-in settings publisher now runs after the outer registry fence and uses
the local manager for its runtime effect. Current edit rights/base are checked by
the publisher; external respawn still checks restart rights. Regression tests cover
JSON/SQLite registry writes, revoked profiles, publication journal timing and the
external restart gate.

Host-owned bind metadata also required a narrow filesystem authority. Supervisor
now starts through setpriv as UID 10001 with only FOWNER retained; bootstrap
identity capabilities are dropped before hubctl starts. Actual process UID and
effective/bounding capability sets were checked. An intermediate UID-0 trial was
removed, and its two affected private state files were returned to service ownership.
Other service identities and file modes are not relaxed.

Selected image identities: supervisor
`sha256:7c3db7de16a1c129691f50c96de85f3c6c2121ac3b12b221c1b5e22009875c60`,
ToolHub `sha256:319da5200f7accde9da77c37a66900ad98e258f16924ca83687f6f74c06afe3b`,
CLIProxy core `sha256:b0cc3355fbe2e3501ef597e24a14497adaff37d6508d54c9c379cbb19e7e1846`.
Only selected service overlays were rebuilt; no new Compose stack was created.

Final local gates: `just check` passed; all twelve affected package suites passed.
The first batch found old communication test clients missing the admission-epoch
contract. Those static fixtures now explicitly select the static job endpoint,
and the managed script fixture serves authenticated holds; the complete
communication package then passed in 97.815 seconds. `just config-check` passed.
The earlier coverage/race/lint record above predates these latest fixes; security
passed after the dependency updates, with no reachable Go or npm vulnerability.

Still open: live agent-driven due jobs/assignment execution and kept-event wakes,
deferred channel delivery, isolated group/capture lost-ack/revoke/crash acceptance,
different-timezone dreaming with real input, and published-SHA CI/image gates.
The two existing dreaming day records are complete in the same timezone; they do
not prove that fault matrix. No external test messages were sent.

## Real ToolHub canary continuation, 2026-11-07

The new integration build now uses production ToolHub startup and clock,
supervisor HTTP and durable records, actual managed Hermes containers and the
production model/ToolHub/egress relays. Only the model and external channel are
fixtures. Six authored passports are compiled/published, selections derived and
sibling runtime tokens enrolled using production functions. Startup tests exposed
and corrected missing final sibling enrollment and a fixture organization mismatch.

`TestM32ToolHubStartupAndRecovery` passed locally without Docker: two owner
schedule/assignment records, stale schedule revision denial, paused revision and
draft recovery after actual ToolHub service restart, foreign-owner read denial.
`TestM32Fixture*` passed, including retired-source poisoning and preservation of
an existing nonempty directory.

`TestM32RealLifecycleMemoryIsolation` is now wired into both CI image targets.
Its body checks concurrent real private runs, authenticated memory recall and
remember replay, foreign/wrong/missing/ended run denial, automation tool lists,
forbidden runtime mounts, and lost capture acknowledgements across real producer
and ToolHub restarts. Capture events are queued as fixture facts and verified
against real supervisor records; this does not separately test fact construction
from Telegram ingress. This container test has NOT run yet. No local Docker
stack, image build, deployment change or external message was made this turn.

Code inspection of that Linux path found `models.yaml` and its mounted directory
were owner-only although model-relay uses uid10001 plus the host group. Rendering
now grants that group read/traverse (`0640`/`0750`) without changing credential
auth/env modes. `TestModelRegistryReadableByRelayGroup` covers rerender permissions
on Unix; it is skipped on this Windows host, so Linux execution remains pending.

This is unpublished source after HEAD `185892107fcc6056a41808fe22533a6c34c7940e`;
there is no CI run URL. Group/revoke/crash scenarios, complete due work and later
delivery, protected dreaming faults and final-source gates still remain open.

Local verification after these edits: `just check` passed. Affected package
suites passed for `internal/stack`, `cmd/toolhub`, `internal/devcheck` and
`cmd/devcheck`. The first devcheck run caught an outdated nine-gate CI assertion;
after including M32 and both local exhaustive Docker recipes, the complete
devcheck package passed in 8.305s and its command package in 10.855s. Tagged
startup/recovery passed in 3.957s with a checked searchable private-card seed;
the bootstrap regressions also passed. The new container leg remains unrun.

## Clock, deferred delivery and send-time fences, 2026-11-07

`TestM32ReminderClockAndDeferredDelivery` passed in 30.279s. It runs production
ToolHub startup and clock, real per-owner schedules/outbox and gateway delivery
loops against a local Telegram API fixture. No supervisor execution or Docker
is needed for reminder-kind work, by contract. Two reminders materialized while
the communication HTTP surface returned 503. After ToolHub restart and restoring
communication, each owner received exactly one fixture send; another restart
did not repeat it. The history API retained exactly one occurrence per schedule,
including any compacted receipt.

A second batch materialized during another outage. Pausing the first owner's
schedule and revoking the second owner's issued tasks.schedule right before
recovery caused both queued deliveries to fail closed after ToolHub restart.
Their failed receipts retained the correct owner, and no fixture send occurred.
The first attempt correctly denied delivery because the fixture lacked authored
channels.send and the service bearer. Those inputs now use the production grant
and configuration paths; authorization was not relaxed.

The container leg uses this same outage/recovery scenario with agent routines
created through the real managed runtime's ToolHub tools, and verifies completed
background supervisor records before restoring communication. This agent branch
has not run; neither it nor assignment/watch execution is claimed accepted.
This continuation created no local Docker resources and sent no real message.

Final local gates for this continuation passed: `just check`, tagged vet for
ToolHub/devcheck, and `just test ./cmd/toolhub ./internal/devcheck` (1.066s and
9.251s). Tagged bootstrap/startup recovery passed in 3.339s/3.050s; the explicit
clock/outage/fence test above passed in 30.279s. The normal fast local gates do
not include that wall-clock integration scenario; its targeted command is now
documented in operations.md.

## #527 model-source cutover audit, 2026-11-07

Removed the root/shared/organization models.yaml loader and the fallback from
LoadModelRegistry when a published revision is absent. Model consumers now load
only a validated published models passport. Named regressions passed for missing,
pending and dangling publication, published registry selection despite retired
root files, model-wire validation and model-doctor behavior. Model-doctor fixtures
now publish real six-doc revisions instead of relying on the removed layering.
Its missing-model guidance uses passports/models.yaml + space apply in all four
locale catalogs.

The operator runbook previously still instructed edits to agent/workspace/scope
files. It now describes six-passport init, binding, publication and credential
separation, and corrects model timeout/reconnect guidance. Collector disable and
self-hosted GitLab host overrides have no current passport field; the runbook
states that limitation rather than recommending inactive legacy files.

A read-only helper compiled from this source successfully loaded the current
published model routes of artem_main and artem_two; both still select
devin/swe-2-flash with high reasoning. It printed only owner and validation
booleans, with no credentials or provider calls. No Docker resources or deployment
configuration were changed. This is reader compatibility evidence, not a new
model execution or final-SHA image gate.

The audit also identified replaySpaceDigest hashing retired root config files.
That follow-up and the broader retired-path audit remain open; #527 is not accepted.

The replay finding in the preceding paragraph was subsequently fixed in this
continuation: its digest now hashes only a complete, strictly decoded published
six-doc bundle. Header revision/space/environment/catalog coherence, current
pointer, generated-view revision and absence of an apply journal are checked.
Reads stay inside os.Root; symlink documents and directories are rejected before
digesting. Missing, pending, malformed and mixed revisions fail closed instead of
hashing an empty or legacy configuration. Unpublished authored and retired root
files do not affect the digest. The error is localized in en/ru/kk/es.

Named replay configuration/export/dispatch regressions passed in 1.370s; the
symlink regression also passed explicitly on Windows (not skipped). No old replay
run/channel behavior or bundle fields were changed. The broader source audit and
integrated #526/#527 acceptance remain open.

Final scoped gates for these #527 edits passed: `just check`; stack/locale
package suites in 19.047s/1.069s after model-source removal; hubctl/locale suites
in 48.711s/1.075s after replay cutover; named regressions and touched-package vet.
The current tree remains unpublished and has no final-source CI/image result.

## Distinct automation bearers and assignment execution, 2026-11-07

An assignment reconciliation scenario exposed a hidden fixture assumption:
gateway runtime and automation service bearers were identical. The fixture now
uses separate production roles. The owner-home endpoint incorrectly required
ControlAuth despite the supervised ToolHub using AutomationAuth. A read-only
probe of the existing shared dev gateway confirmed service owner-home requests
failed for both artem_main and artem_two before the fix. The handler now uses
the same automation service authority as delivery intake, retaining the direct
deployment fallback only when AutomationAuth is unset. Regression tests deny
owner runtime, wrong and missing bearers with the service bearer configured.

`TestM32AssignmentReconcileAndRecovery` passed against real gateway/ToolHub
handlers and durable stores: source provisioning and replay without duplication,
stale CAS denial, pause/restart/resume, foreign denial, invented specialist
progress denial, completion and removal of the owned source. The tagged M32
no-Docker suite passed (ToolHub 35.488s, devcheck 3.026s). Named owner-home tests
and touched-package/tagged vet passed. `just test ./internal/communication
./internal/devcheck ./cmd/toolhub` passed (97.447s/11.174s/2.057s).

Only `go run ./cmd/hubctl dev-rebuild --dir spaces/artem_main --env dev
communication-hub` was used for deployment. It reused the existing core base
and replaced one shared gateway container. The selected overlay is
`hermes-hub-dev-artem_main-communication-hub:local`, image
`sha256:e6a4784db4170a35fb5816b4b4561f98ae0e0731fadb00318a1cb148954c1464`.
The container became healthy and its restart count was zero. After replacement,
both owners' service home reads succeeded and runtime service access was denied.
Both owners also passed actual ToolHub settings_read with matching published
and effective revisions. Both published model routes still validate as
devin/swe-2-flash/high. These were
read-only checks with allowlisted booleans; no real channel message was sent.

The container canary now requires exact-HEAD runtime and core script/relay
images; CI pulls the selected environment's core role. Its new actual file
source -> isolated keep/drop filter -> watch/assignment agent wake scenario
checks owner delivery, assignment wake checkpoints and restart deduplication.
That container branch has compiled but has NOT run. Actual specialist progress,
competing claims, group/memory-revoke/crash and dreaming fault acceptance plus
published-source CI remain open; this is not milestone acceptance.
Final `just check` and whitespace validation passed for this continuation.

## #527 workspace executor cutover, 2026-11-07

Removed the executor startup downgrade that cleared a missing workspace
boundary and continued with the old env path list. Managed startup now requires
the strictly decoded published workspace document plus its matching config-view
revision. Every write checks it again; unknown fields, retired bodies, multiple
documents, mismatched/missing activation and symlink files are denied. Removed
the mtime/size cache so a same-size revision with preserved timestamps cannot
retain a wider old boundary. Compose no longer selects raw Hermes/SOUL config
mounts when the config view is absent. The existing standalone, non-managed
component executor remains usable without a space configuration.

Named activation/reload/OpenRoots regressions passed. The symlink regression
explicitly passed on Windows in 1.076s through the normal testexec retry helper;
the initial raw Go launch received Windows Access is denied before running it.
Affected suites passed: agenttools 5.110s, stack 18.747s, locale 1.065s, hubctl
45.267s. The permission audit then found that host publication used 0600 even
for runtime-mounted view files. Atomic view publication, tool-policy and manifest
now use 0640; private immutable files and current keep 0600. After that change,
passport and stack suites passed (1.268s/17.888s). Unix mode regressions were
added but do not execute on Windows; Linux CI must still prove them.

Only runtime dev overlays were built. The latest source was built under
hermes-hub-dev-artem_two-hermes-runtime:local and reused by tagging the identical
code image for artem_main. Both selected tags point to
sha256:eb3e4dc7d2a2b20861354e1007e46c72437e1536b1eda73b7643380460d77903.
No full stack build, new local stack or passport apply was used.

The live file smoke has NOT passed: ToolHub returned status=uncertain for file
write and a subsequent read. Recording only that status and safe failure hints
showed an executor channel failure. No managed context container currently
exists (including stopped contexts). ManagedAgentContainer only derives its
name; it does not warm/acquire the runtime. This is the next cold-executor
lifecycle finding to resolve, including watch probes after idle. Earlier warm
runtime evidence does not cover it. Write retries were stopped after recognizing
the uncertain status. Do not infer file success or overall #526/#527 acceptance.
No smoke-prefixed files were found in either owner's managed or original
artifact directory. Final just check and whitespace validation passed.

## Cold managed executor lifecycle resolved, 2026-11-07

The preceding cold-executor finding was reproduced before this change with
status=uncertain and no managed context containers. ToolHub now acquires a
supervisor lifecycle lease from the enrolled identity and current published
user/group scope before agent-tools dispatch. The receipt must bind owner,
context, runtime, container and generation. Redirects are refused. Release uses
a separate bounded cleanup context, retaining the supervisor's durable expiry
if acknowledgement is lost. No token or backend payload enters diagnostics.

Cold startup runs after the first registry admission releases its mutex/disk
fence: startup may call ToolHub or materialize registry-dependent files. The
tool is fully re-authorized before actual execution, so a selection/right revoke
during startup is effective. Pre-dispatch startup failure reports before-effect;
post-dispatch channel failure still reports uncertain. Built-in settings and
ToolHub remote executors do not acquire an unnecessary agent runtime lease.

Named regressions passed (latest 1.198s), including the actual managed tools.use
ledger revoke during preflight, registry fence release, binding revoke,
preflight failure, post-dispatch uncertainty, cleanup after cancellation,
published user/group scope, foreign binding/generation and redirect denial.
Full internal/toolhub and cmd/toolhub suites passed in 96.798s/2.142s. Tagged
M32 startup and assignment recovery passed in 7.518s; tagged vet passed.

Only `hubctl dev-rebuild --dir spaces/artem_main --env dev toolhub` was used to
deploy the new code. The shared ToolHub selected image is
sha256:602266ca359c3c44a5c264086d4283265a1464d77727774052abf9726ff397c1,
healthy with zero restarts. After its replacement, actual MCP file_write/read/
remove succeeded for both artem_main and artem_two without a prior model job.
Each probe's exact text and revision round-tripped, and each probe was removed.
Both newly spawned contexts run runtime image
sha256:eb3e4dc7d2a2b20861354e1007e46c72437e1536b1eda73b7643380460d77903,
with zero restarts. A read-only supervisor HTTP status query confirmed both
owners idle with leases=0. Both actual settings_read calls also matched their
published/effective revision, and both model routes still select Flash/high.
No external channel message or new local Compose stack was used.

The isolated container leg now begins with owner-specific file probes before
any model job warms a runtime; it no longer mislabels the later overlapping
model runs as cold starts. That CI branch and the rest of the full fault matrix
remain unrun. Linux permission tests still require native execution; the local
control container's temporary mount is noexec and was not bypassed. The dirty
worktree remains unpublished, with no final-source CI/image acceptance.
Final just check and whitespace validation passed after the cold-executor fix;
both owners' smoke-prefixed artifact counts are zero.

## Native dreaming relay/PDP/vault acceptance, 2026-11-07

Source: HEAD `185892107fcc6056a41808fe22533a6c34c7940e` plus the unpublished
worktree diff. No new Docker stack, image build, personal data, credentials or
external channel send was used. The new tagged
`TestM32DreamingRelayIsolationAndRecovery` connects the production ToolHub
endpoint, automation startup factory, model relay constructor, live governance
store and real per-user Git vaults. Only upstream model/channel HTTP and clock
ticks are controlled. The relay constructor is exported within internal/runtime
so the same handler can run on loopback without adding a production listen flag.

The six authored test passports select UTC/New York and 02:30. Before New York's
spring gap, no local-day occurrence/model call is created there; the first
post-gap tick executes once. A missing issued dreaming right denies admission.
The first owner's model call is then held while the second completes its own
eligible owner-private entry. Neither request contains the other owner's marker.
Revoking the first owner's issued right at the model barrier leaves its Git HEAD,
fact path and night checkpoint unchanged. A new confirmed grant and a fresh
automation Host recover the pending day. Both owners then have one fact, one
draft-only assignment (no plan or provisioned source) and a completed local-day
receipt. Ordinary ToolHub automation control cannot pause the system routine.

With communication returning 503, both nights still complete and retain two
unsent digest intents. Another Host restart/same-day tick neither calls the
model again, changes either vault's Git HEAD, duplicates an intent nor mints a
delivery. Restoring communication drains both owner-bound digests through the
real gateway producer to the local channel fixture; another restart does not
replay acknowledged delivery. The final named regression passed in 7.47s;
six-passport fixture regressions passed in 2.09s. Tagged touched-package vet
passed. The initial compiler-wrapper launch hit transient Windows access denied;
retry with bounded Go package concurrency reached the test and passed.

This is component acceptance, not an actual upstream model, external delivery,
container run or process-kill test. Memory-disable/config-edit during a run,
autumn overlap, exact checkpoint crash points and the final published-SHA CI
matrix remain open. All twenty M32 issues were re-read through the GitHub guard
and remain open; no publication or issue mutation was performed.

### Dreaming mixed-activation finding resolved

The schedule reader previously verified enrolled identity and the six published
documents, but did not bind them to the active capability profile or reject an
incomplete apply. `TestNightScheduleRequiresActivatedPassport` reproduced three
false admissions before the fix: mismatched profile revision, absent profile
revision and a present apply journal. The active matching case passed.
`NightSchedule` now uses the existing Gateway activation fence, requires the
published revision to match its pin, and rechecks after loading the plan.
The existing per-write NightRun guard calls that reader too.

The model-response barrier regression now starts an apply after admission and
asserts refusal before even the night checkpoint is written. Removing the
interrupted marker allows recovery; a completed-day retry still skips the model.
Named schedule/right/apply regressions passed in 3.130s, tagged touched-package
vet passed, and the real native dreaming relay/PDP/vault scenario passed after
the production fix in 8.412s. Full no-Docker M32 tests had also passed together
(cmd/toolhub 42.927s, devcheck 2.530s); the container test explicitly skipped.
Earlier affected runtime/devcheck suites passed in 38.080s/10.806s.

### Memory admission lock order corrected

A second named regression reproduced the shared stall: a memory_card call
waiting on its owner's vault had already retained the registry admission fence,
so a queued registry writer could not progress. This also made dreaming's
commit-time registry checks susceptible to lock inversion with a waiting call.
Memory search/card now verify the exact private run before touching the vault,
wait outside the shared fence, then perform full admission again with the owner
lock held. A private context pin lets only that Gateway/envelope's handler reuse
the held vault lock. Backend authorization and commit checks remain intact.
The cataloged delegate also releases its outer fence before target admission.

Regression coverage proves a waiting owner does not admit the backend early,
block a registry writer or stop the other owner's real card write; the first
owner resumes successfully afterward. A wrong run denies without waiting for
the held vault. A right revoked during the wait, or memory disabled there,
denies before dispatch and leaves the card and vault write journal unchanged;
the central ToolHub audit retains the rejected job/run. The legacy tests now
assert this earlier refusal rather than requiring a vault denial mutation.
Named lock-order/revoke/disable, cataloged delegate and direct/control parity
checks passed in 5.710s. These unit tests use the existing fake lifecycle
authority seam and do not establish actual container/private-group acceptance.

After the last source edit, full internal/toolhub and cmd/toolhub suites passed
(98.685s/2.094s); real native dreaming passed again in 9.209s and tagged vet
passed. `just check` initially caught the service document at 501 lines; the
text was condensed to 498, then format/docs/repo vet all passed. No new
dependency or agent-tool shape/schema/catalog change was introduced.

Only `hubctl dev-rebuild --dir spaces/artem_main --env dev toolhub` deployed
these fixes. The resulting selected tag is
`hermes-hub-dev-artem_main-toolhub:local`, actual container image
sha256:51687f251812080e772ee1e90753d1c83137db6de6860ff89f676203f400d966.
The command's endpoint wait passed; Docker inspect reports running/restarts=0
(there is no Docker healthcheck on these containers). Communication and both
CLIProxy containers also remained running/restarts=0, on their preceding images.
Actual MCP settings_read passed for both owners and matched published/effective
revisions. The current published model reader confirmed Flash/high for both.
Both still have exactly six authored passports, protected dreaming registered,
and a complete 2026-11-07 day receipt. Both real owners use Europe/Moscow; the
different-timezone evidence comes from the native isolated test above.
No `space apply`, full image build, new Compose stack, vault probe mutation or
external channel send was used. Final source remains dirty/unpublished, with
no matching remote CI/image acceptance.

## Group/prompt/open-session matrix prepared, 2026-11-07

The isolated container driver now has a second scenario with one private home
and one explicitly published group home, each compiled/rendered from exactly
six authored passports. The group deliberately retains memory enablement and
issued rights: denial must come from the verified run kind, not an absent
selection. Concurrent real private/group model requests are held at the provider;
their actual message arrays must contain only the private owner's marker, with
no private marker in the group request. Public MCP calls deny group, borrowed
private and missing claims for search/card; the private caller remains usable.
Both runtime mount checks and completed-run denial remain required.

The two-private-user leg also inspects actual provider prompts and now keeps
MCP sessions open through issued tools.use/memory.search/memory.remember revoke
and confirmed regrant. The sibling must continue returning only its own marker
throughout. This is wired into the existing CI dev/prod M32 gate, not run locally.

Exact-source preflight now refuses modified, staged and untracked source before
trusting matching image labels. A temporary Git repository regression passed
for clean and all three dirty states. The prior status/mount/cleanup lookup
also omitted mandatory RuntimeID/UserID, so Manager normalization would reject
it and cleanup could miss the runtime. Both call sites now share a full enrolled
binding; cleanup reports lookup failure. The native real-Manager lookup test
passed for private and dedicated group homes without spawning Docker.

Latest named source/status regressions passed (cmd/toolhub 4.517s); the dedicated
group fixture passed (devcheck 1.730s), and tagged touched-package vet passed.
All new container paths compile; both actual container scenarios explicitly
skipped without HUB_M32_IMAGE. No local stack, deployment, image build, remote
publication or personal-data mutation was performed. Group/open-session matrix
execution and final published-source CI remain unproven.

After the final status-lookup edit, affected package suites passed
(cmd/toolhub 1.123s, devcheck 8.622s), tagged vet passed and final `just check`
passed. The live service code/images were unchanged during this driver-only
work; no redundant rebuild was needed.

## Confirmation and primary-control authority findings, 2026-11-07

Review of #519's actual channel path found gaps hidden by same-bearer unit
fixtures. Before the fix, a runtime bearer could submit a confirmation notice
(HTTP 200), while the distinct service bearer was rejected (401). A second
regression showed that the verified channel callback discarded the issued
profile/environment/generation/organization, so the memory writer could not
accept the returned managed proof. Callback HTTP also used the runtime primary
bearer against ToolHub's service-only confirmation endpoint.

Notice admission and confirmation return now use the same service-token selector
as owner-home/delivery, retaining the legacy ControlAuth fallback only when no
automation token exists. Generic runtime control authorization is unchanged
except for the primary-principal fence below. Notices still reject Origin,
wrong/missing/runtime service credentials and mismatched actors. The callback
keeps verified channel routing while preserving the managed authority from the
service-issued stored proof; ToolHub's current binding/base/revoke checks remain.
Named service/legacy callback, managed metadata, verified sender/chat/expiry and
restart tests passed; no real channel confirmation is claimed from these fixtures.

The primary supervised runtime bearer also previously accepted a foreign
X-Hub-Principal verbatim. A regression reproduced primary-owner impersonation
and acceptance without a bound primary owner. ControlPrincipal now comes from
the existing rendered HUB_USER_ID, pins that primary runtime bearer, and denies
if no valid owner is known. Enrolled siblings retain their token-bound principal;
explicit service peers retain service routing. No new authored input was added.
The native TestM32GatewayControlPrincipalIsolation passed (3.979s): two distinct
future routines in the real ToolHub host/storage stayed isolated through actual
gateway HTTP with forged headers for both primary and sibling runtime tokens.

The first full communication suite then exposed one old MCP-watch fixture with
a supervised primary token but no owner. Its real HTTP create correctly refused
401. The fixture now supplies its actual owner; the named MCP-watch/auth/
confirmation set passed (1.906s). Full-suite verification is in progress.
Final affected suites then passed (communication 100.389s, cmd/toolhub 1.136s),
as did tagged vet and final `just check` after the fixture correction.
The read-only ai-repo-safety prepush preparation gate also passed with no missing
tool warnings; no commit, push, CI dispatch or GHCR publication was performed.

Only communication-hub was rebuilt/recreated through the scoped dev-rebuild
path. Its actual selected image is
sha256:bb54a65d7c8a8f9d610807a4aca6fe221d503d70ada248d6d3cfe34e4ef90a56,
running/restarts=0. ToolHub remains on image 51687f251812, and both CLIProxy
containers remain running/restarts=0 on their preceding shared core image.
Read-only actual owner-home checks passed for artem_main and artem_two using
service authority and denied runtime service access. Both current published
routes still validate as Flash/high. No new stack, full build, space apply or
external test channel message was initiated. Live channel-confirmed memory
mutation and the full container fault matrix remain unverified.

The owner was asked asynchronously for the separate commit/push/full-CI/GHCR
authorization required by AGENTS.md and ai-repo-safety; that request remains
unanswered. The unpublished worktree is not final-source remote acceptance.

## Channel-confirmed memory and Windows policy replacement, 2026-11-07

`TestM32MemoryChannelConfirmationRoundTrip` now connects actual ToolHub startup,
public MCP, current issued rights, the communication gateway, its durable spool
and the sole vault writer. Both users come from the six-authored-passport fixture.
The Telegram HTTP transport and supervisor live-run authority are fixtures; this
is native component integration, not a live Telegram or real-container pass.

For each owner, MCP seeds a card and persists a truth proposal without changing
it. Model self-apply and a foreign sender cannot confirm. The opaque button
survives gateway reconstruction, reaches only the matching chat and applies the
exact owner text once. Replay preserves the card and Git HEAD. A newer fact
invalidates the old base, and revoking all current memory.remember grants blocks
the pending confirmation. Confirmed recovery grants restore subsequent access.

A real Git index lock then interrupts an owner-confirmed edit after content lands
but before commit/receipt. The test verifies used/applying state and unchanged
HEAD, removes the isolated lock, and restarts production ToolHub. Startup finishes
the same edit, marks it complete and commits it without duplicating content.
Gateway reconstruction and a repeated callback leave both content and HEAD fixed.
The shared helper is also wired into the private managed-container leg, still
unrun. This is controlled storage failure plus service reconstruction, not an
abrupt process-kill proof. All mutations use temporary fixture data.

The new path reproduced Windows `Access is denied` replacing governance.json
while another reader held it without FILE_SHARE_DELETE. SaveGovernanceLocked now
retries only Windows sharing/access errors for at most 475 ms. Existing flock,
same-directory synced temporary file and atomic replacement remain; the old file
is never deleted to force replacement. The real-handle regression failed before
the fix; eventual replacement and bounded exhaustion preserving the prior whole
revision/no leaked temporary files passed afterward. Unix keeps plain rename.
This does not establish the cause of compiler executable-launch failures.

The Telegram fixture was corrected to retain updates until offset acknowledgement,
so cancellation during gateway reconstruction cannot destructively consume one.
The final named channel test passed in 11.653s. The combined native M32 matrix
then passed: cmd/toolhub 61.525s and internal/devcheck 4.825s, including reminder
outage/restart/deferred delivery, assignment recovery, dreaming relay/PDP/vault,
primary control isolation and the new channel recovery path. Both managed
container scenarios explicitly skipped without HUB_M32_IMAGE. Final affected
package/check results follow below; no new local stack or image build was needed.

Final affected package suites passed (`just test ./internal/toolhub/api
./cmd/toolhub`): API 40.249s and cmd 2.101s. Integration-tagged touched-package
`go vet -p=2` and final `just check` passed, exit 0. Historical coverage was not
rerun; full published-SHA CI, image gates and the remaining #526 matrix stay open.

## Spawned runtime and script group mismatch, 2026-11-07

Reviewing the unrun container leg found a production contract mismatch: Compose
uses UID 10001 with the controller/host group, but supervisor runtime spawns and
isolated watch scripts hardcoded 10001:10001. Published config-view files are
0640 and watch directories are 0750, so a host/controller group different from
10001 can make those read-only mounts inaccessible on Linux. Windows bind
semantics and mode-only tests do not prove the intended Linux access.

`TestSpawnedRuntimePreservesComposeUser` failed before the fix: actual spawn
10001:10001 versus rendered 10001:0 on this Windows host. The existing script
HTTP contract also failed after asserting the controller group. Both runtime
spawn paths and script helpers now use UID 10001 with the current controller
group, matching the existing Compose and prepare contract. Root UID, new
capabilities, supplemental groups or wider file modes were not introduced.
The control relay has no such filesystem mounts and keeps its existing user.

Named spawn/network/script-isolation/watch-mount regressions then passed, as
did integration-tagged touched-package vet. Final affected suites passed:
supervisor 76.617s, devcheck 11.348s and cmd/toolhub 1.198s.

The actual managed M32 leg now checks the running container user and executes
readability tests for all six /config passports, revision, executor policy and
manifest. The existing Docker script gate now includes a group-readable watch
file and traversal denial, alongside network denial/OOM/cleanup. M32 invokes it
with the same exact-source core image in CI and both local Docker recipes. These
container paths compile but have not run. Scoped supervisor deployment and final
just check are pending at this record boundary.

## Existing two-owner dev stack, programmatic channel and ToolHub, 2026-11-07

Current worktree base is `185892107fcc6056a41808fe22533a6c34c7940e`
plus unpublished changes. No synthetic Compose space or full image build was
created for these checks. Scoped `dev-rebuild` overlays changed ToolHub,
ToolHub relay, supervisor and each managed runtime. The inspected shared
ToolHub image is `sha256:cc91c4dc39c4aba77dce9303cd506bfb1e2f3e413e6346745e98ea71033ef366`;
ToolHub relay is `sha256:bbcf5f268a91e11013579575b399b26cc870203f30da95edbfda2827239652d9`.
Both services and the shared supervisor/communication hub were running with
zero Docker restarts after the addressed ToolHub restart.

Hermes' static MCP headers omitted the active job/run. The managed runtime now
binds its local MCP proxy to the admitted pair, and ToolHub's managed run route
uses a stateless MCP handler so a persistent Hermes client cannot retain the
previous run's correlation. The common correlation parser also accepts the
69-character mock job ID. Before the stateless route, a new file operation was
audited against an earlier completed job; after it, all four file CRUD calls
were audited as successful under the current job. Auth, spoofed correlation,
persistent-client and run-route regressions passed locally.

Programmatic `POST /v1/mock/message` and `GET /v1/mock/outbox` exercised the
existing communication queue, supervisor, real Flash/high model, Hermes,
ToolHub and durable mock delivery for both owners. Each model wrote a synthetic
memory card and later used `memory_search` to answer the stored calibration
color (`amber`); owner vault search found the marker only under its owner.
After an addressed ToolHub restart, both models recalled the same fact again.
The mock channel is an in-process acceptance transport, not Telegram delivery.

The agent-tool routine contract currently binds origin destinations to Telegram
jobs, so a mock job cannot be the `origin_job_id` for `routine_create`. For
local delivery acceptance, both one-shot reminders were created through the
communication HTTP API with explicit `channel=mock`; that API delegates to the
same ToolHub automation host. Each exact marker appeared once in the correct
owner outbox. After the ToolHub restart, the count remained one. Both schedules
were then removed by their exact IDs with HTTP 204. Repeat schedules, live
assignment/watch execution and agent-tool-origin routing remain open.

All 48 synthetic mock outbox records and six exact chat links created by these
checks were removed and the owner-filtered outboxes were rechecked. Synthetic
memory cards and daily entries remain in the governed vault: memory deletion
requires verified owner confirmation, which the mock channel does not provide.
The two daily files also have other content, so they must not be removed
wholesale. Full container fault,
group, three-day dreaming, channel-confirmed memory mutation, exact-source
Docker/CI acceptance and external Telegram transport remain unverified.

A read-only passport/system-routine audit found six authored passports and a
readable published/effective revision for each owner. Both protected dreaming
routines are registered, and one local-day record per owner is complete for
2026-11-07. This does not satisfy the requested three dated days with ten
synthetic facts per day or prove absence of every retired reader in live code.

Final local `just check` passed. The affected package suite passed for
`cmd/hubctl`, `internal/runtime`, `internal/toolhub`, `internal/toolhub/api`
and `internal/communication`; `internal/stack` initially found a stale expected
managed MCP URL, then passed its named regression and full package after the
test was corrected. No current-source Docker image/CI gate was run; images
are scoped dev overlays of an unpublished worktree.

The current integration-tagged native M32 set also passed: reminder clock with
deferred delivery, assignment reconcile/recovery, two-owner memory channel
confirmation, and dreaming relay/isolation/recovery (`go test -tags integration
-run '^TestM32(ReminderClockAndDeferredDelivery|AssignmentReconcileAndRecovery|MemoryChannelConfirmationRoundTrip|DreamingRelayIsolationAndRecovery)$' ./cmd/toolhub`, exit 0). These use production components
with isolated temporary state and fixture transport/model boundaries. They do
not cover live Telegram or the unrun Docker group/fault leg.

## Live repeated routines and assignment event execution, 2026-11-07

Both owners' existing mock chats passed a recurring reminder every minute:
two exact deliveries, a future next due, interval edit to two minutes, persisted
pause, and no additional delivery after the edited due plus ten seconds. Both
schedules were removed. The six new mock output records (command and two
reminders per owner) and reclaimed test chat links were removed by exact IDs.

The real assignment path exposed three defects that were reproduced before
fixing them:

- Event and watcher wake instructions named retired `assignment_get` and
  `assignment_progress` instead of the current projected `assignment_read`
  and `assignment_edit action=progress`. Existing catalog shape was unchanged;
  the generated instructions now follow that catalog.
- Event-sink agent jobs skipped assignment wake reservations entirely: live
  model progress existed while `wakes` and `model_calls` both remained zero.
  Admission now reserves before enqueue, checks an existing durable mapping
  on retry, and settles the matching reservation after durable admission.
- Hook envelopes were hashed as single raw events, including `{"events":[]}`;
  an idle tick therefore woke the model. Hook extraction now preserves source
  IDs and ignores empty event envelopes.

Named regressions failed for the relevant behavior, then passed:
`TestAssignmentWatcherWakeUsesCurrentToolNames`,
`TestAssignmentWakeBundleCarriesOwnerContractAndLanguage`,
`TestAssignmentEventWakeBudgetSurvivesLostAdmissionAck`, and
`TestHookEventsPreserveIDsAndIgnoreIdlePayloads`. The budget regression includes
a lost acknowledgement after queue persistence, host reconstruction, one
charge for the original job and a second pending event held by the daily cap.
The existing event admission-failure replay regression also passed. Touched
package vet and `just test ./internal/routines ./internal/toolhub/automation`
passed after the final code edit.

Only ToolHub was rebuilt/recreated. Its inspected image is
`sha256:f5005e4bbe447670ba3caeb4561187d011a56963d46a83b64a58bce45b7a3832`,
running with zero restarts. Source remains unpublished worktree changes on
`185892107fcc6056a41808fe22533a6c34c7940e`.

Fresh assignments for both owners applied an owned authenticated hook source
and an isolated keep filter. Each source event caused one real Flash/high
model run, successful ToolHub file listing and saved progress/finding. Empty
polls and a repeated source ID did not cause another wake. A CAS plan update to
revision 2 retained progress; a new event produced a distinct second job and
finding, with wake/model counters exactly 2/2 for each owner. Both final replies
reached the owners' actual Telegram chats: exact SQLite outbox records were
read through an ephemeral networkless, read-only helper and verified `done`,
matching job/chat/marker and a positive Telegram message ID. Configured API is
the default/official Telegram API. The helper initially looked for retired
JSON spool files; only the driver was corrected to query SQLite, without
copying the database or changing the product.

The owner completed each assignment; another hook event returned HTTP 404.
Both assignment records and their source schedules were removed. The two
failed first-probe assignments had already been cancelled/removed. Exactly
seven synthetic bot messages across the failed and successful probes were
deleted through Telegram's existing delete contract after matching their
stored synthetic marker, job and owner chat. No owner message was selected.

The requested successful assignment→memory chain is concretely BLOCKED by the
current contract: `RunContext.Kind` classifies cron/assignment as background,
and SPEC-0098 M2's model-facing memory gate admits only a live interactive
private run. Both live agents' memory attempts were denied before execution;
their file and progress calls succeeded. Authorization was not widened to
make the acceptance chain pass. The initial synthetic private-memory cards
and daily entries still require the supported owner-confirmation path.

Final `just check` passed after these code and documentation changes. This
result does not include exact-commit Docker gates or remote CI publication.

## Managed catalog, applied revisions and retired dispatch, 2026-11-07

The preceding goal turn made progress: direct managed-runtime CRUD/isolation and
six-passport revision evidence completed concrete acceptance probes. This turn
rechecked the full catalog, then reproduced and fixed the observed retry tail.

Both actual managed containers called their run-bound local MCP proxy using the
existing runtime identity. Each exposed six pinned platform tools and 51 admitted
first-party tools from immutable catalog 16.0.0. Names, descriptions, effects and
input schemas match the current contract. The `skill.name` property deliberately
projects eligible names and replaces its static description; the driver initially
treated that documented projection as schema drift. Only the ignored checker was
corrected. Both final catalog-only probes passed. Each owner's file create/read/
edit/remove passed; the second owner's traversal toward main's unique synthetic
file returned the intended opaque not-found refusal. Main then read its own blue
value unchanged and removed it. All catalog probe files were removed.

The read-only revision audit compared all six authored compiled bodies to the
published bundle, then compared the six byte hashes inside each managed runtime,
spawn revision, channel policy and ToolHub `settings_read` published/effective
revision. Main is `r-8b565a4eca56a1d8`; two is `r-bf3385d48344dc52`. All matched.
Native runtime memory is disabled and neither runtime mounts the vault. Operational
channel enrollment remains distinct from authored agent configuration; its binding
matched the applied policy. The final legacy-reader audit remains open.

One dispatch from the cancelled initial assignment probe remained unacknowledged
and accepted after its source's permanent retirement. Every reconcile pass scanned
it again. `TestRetiredScheduleStopsRetriesWithoutInventingRunOutcome` failed with
one pending dispatch after deletion and host reconstruction. The fix persists a
separate `retry_stopped` flag only after validating an owned, newer, disabled/paused
deletion record. It does not assign an unproven terminal execution result: admission
before a lost acknowledgement may have succeeded. Pause retains pending work;
corrupt or foreign deletion records do not discard it, and an actual late terminal
result remains writable. Those checks and existing lost-ack recovery passed.

Only ToolHub was rebuilt/recreated through `dev-rebuild`; inspected running image:
`sha256:4c44f0a78ea87139a5c31246df1930c301674a05159546d1acf2e4a4416db480`.
It is healthy with zero restarts. The exact old artem_two dispatch now has
`retry_stopped=true`, `acked=false`, mapping status `accepted`. It will no longer
be resubmitted, while its unknown execution outcome remains honest. No vault,
owner content, full Compose stack or passport publication was changed by this fix.

Final gates for this fix passed: named regression and lost-ack recovery, touched
package `go vet -p=2`, `just test ./internal/routines ./internal/toolhub/automation`
(2.310s / 8.181s), `just check`, and whitespace diff validation. The documentation
gate first rejected the expanded routines document above its 500-line ceiling;
the paragraph was shortened and the final gate passed. ToolHub, communication-hub
and both CLIProxy containers were running with zero restarts. The post-deployment
revision audit again passed for both owners, including managed `settings_read`.
This is dirty local source on `185892107fcc6056a41808fe22533a6c34c7940e`, not a
published-SHA CI or full milestone acceptance claim.

## Settings/render recovery and file watches, 2026-11-07

The previous goal turn made progress by fixing the retired dispatch retry and
completing the managed catalog/revision probes. This turn added only the missing
file-watch/combined-restart scenario on the same two existing spaces.

Both deterministic command probes read unique synthetic files through the real
isolated script containers. Red established a silent baseline; red→blue produced
one owner-specific mock-channel notification; another successful poll produced
no duplicate. No native direct handler tick was used. The mock transport remains
distinct from the earlier verified actual Telegram assignment deliveries.

Settings preparation exposed actual render failures. A manual authored draft
was inert; `settings_edit` correctly refused that dirty base because it requires
`space apply` for pre-existing drafts. The driver now restores the inert draft
before asking the governed tool to author/publish the same leaf. A driver-only
receipt race was also removed by running state-mutating phases sequentially.
These are checker corrections, not product fixes.

The failed apply retained a prepared journal and admission refusal. The normal
`space apply` preview recovered it and reported no authored diff. Restricted
render checks exposed three permission/noop problems:

- Immutable revision directories/files were owner-only, preventing a different
  restricted controller UID from reading them. New revisions now use 0750/0440
  and current 0640 for the trusted controller group, with no group write on
  immutable documents or other-user access. Only the exact existing two-owner
  test revision's permissions were repaired, without recursive permission changes.
- Legacy skill migration eagerly read every private draft even when an entry
  was a byte-identical platform copy or already migrated. Draft inventory is now
  loaded only for an actual remaining candidate. An unrelated invalid draft no
  longer blocks a noop; actual migration still fails closed on that inventory.
- `generated/model-registry/<env>` was reset to 0750 immediately before atomic
  replacement. A controller sharing the group but not the owner UID could not
  create the temporary file. This mutable projection directory now uses 0770;
  the model file remains 0640 and credentials retain private modes.

During diagnosis `/proc/1` was initially mistaken for the service process; it is
root `docker-init`. `docker top` confirmed the actual hubctl process is UID 10001.
No UID or capability relaxation was applied. Final render ran successfully with
the exact setpriv UID/GID/FOWNER context. Only supervisor was overlaid, finally
running image `sha256:7d81dbb99cdd32dce054f0c2f4d62f8711c00a36eac4c395424360eb5d3cea41`.

Linux container regressions passed for published controller/runtime readability
and model-registry permissions. The bundled-noop regression failed before the
fix and passed afterwards, including the negative candidate-migration assertion.
Touched vet, `just test` for passport (2.577s), skillstore (3.998s), stack (25.767s)
and hubctl (55.923s), and final `just check` passed. The governed setting then
published successfully to `r-fa6de3ad265cf210` and actually replaced the warm two
runtime. The temporary setting is retained solely for the combined restart check
and still needs restoration when that scenario completes.

The combined scenario has now completed. Both owners' watchers remained active
with the blue baseline. Each owner had a one-shot reminder due in 90 seconds.
With communication stopped, the normal ToolHub clock durably recorded both
results as unsent; no direct handler tick was invoked. After a supervisor
inventory confirmed both primary runtimes idle with zero leases, the existing
ToolHub, supervisor, both primary/control runtimes, both proxies, ToolHub relay
and model relay were stopped and started. Broker and unrelated services were
not changed. The same image IDs were retained. Both queued results reached the
proper mock chat exactly once. A successful watch poll after a timestamp taken
after the restart retained blue and produced no duplicate change notice.

The temporary setting and its published/effective revision survived this common
restart. Two new real Flash/high model jobs then called memory.search successfully,
with current job/run-bound ToolHub audit completion, and both delivered replies
used the previously saved synthetic color amber. These are live model/runtime/
ToolHub/vault checks with mock inbound/outbound transport; earlier Telegram receipts
remain separate external-transport evidence. The mock poll driver initially waited
for old entries whose output had already been cleaned; it now observes the latest
job per owner. Actual new jobs were already complete, and were not re-enqueued.

Both watchers were paused, their files changed to green, and no poll or new notice
occurred past the saved next due plus eight seconds. Both watchers, both reminders
and their two exact source files were removed. The original compression.enabled
absence was restored through governed settings_edit; two now uses
`r-4543490ac923f7c7`, main remains `r-8b565a4eca56a1d8`. After restoration, six authored/
published/runtime hashes, spawn/channel policy and managed settings_read matched
again. The completed ignored receipt confirms no setting/draft restoration pending.
Ten exact new mock outputs were removed (including transient probe-error notices),
and known test links reclaimed; no owner message was selected. Original synthetic
vault memory still needs the supported owner-confirmation cleanup.

Final inspected components are running with zero restarts: ToolHub image
`4c44f0a78ea87139a5c31246df1930c301674a05159546d1acf2e4a4416db480`, supervisor
`7d81dbb99cdd32dce054f0c2f4d62f8711c00a36eac4c395424360eb5d3cea41`, communication
`bb54a65d7c8a8f9d610807a4aca6fe221d503d70ada248d6d3cfe34e4ef90a56`, both proxies
`b0cc3355fbe2e3501ef597e24a14497adaff37d6508d54c9c379cbb19e7e1846`. No full image
build, new full stack, commit or publication was performed. Dreaming-result recall
cannot be claimed from this restart because the three-day live input is still
unavailable; full watcher variants and the security/fault matrix remain open.

## Reader audit, live selection/grant fences and model-created routine, 2026-11-07

The preceding turn made progress: render defects were fixed and the controlled
restart/file-watch scenario completed. This turn completed these finite legs:

- C1 source inventory covered hubctl authored publication/settings writes;
  stack ReadEffective/ReadEnvironment aliases; supervisor resolution and both
  spawn paths; managed config materialization/attestation; ToolHub published
  settings; communication managed routing. Root user scope/settings/model files
  are not effective config sources. Organization settings are a distinct
  protected host ceiling and organization-init writer, while communication users
  are operational verified channel enrollment. Feature/env fields there feed
  diagnostics or the unmanaged runner, not managed tool authority. The existing
  retired-file poisoning/strict-reader regressions passed in the final stack
  suite; the previous common live restart used the corresponding published
  bundles. Stale user scope/feature/unpublished-reader documentation and CLI
  recommendations to edit inert root files were corrected against code.
- C3 used one open MCP session and two unique workspace files. A member path
  rule allowed one file and refused the sibling. Removing file_read from the
  selection blocked even that allowed path. Published grants did not change.
  Governed apply restored the exact original tools.yaml bytes; the same session
  read both original contents again. Both files were removed. No image build
  was used; only normal scope publication/rebind/runtime reconciliation.
- A real Flash/high model called routine_list then routine_create through
  ToolHub for its own default Telegram task. The normal clock's 90-second
  one-shot reminder reached the verified main owner chat. An exact read-only
  communication SQLite receipt matched occurrence/chat/marker, done state and
  positive Telegram message ID. The routine and only that verified synthetic
  bot message were removed.
- A separate real model run successfully read its own test file before the
  protected workspace.read grant was revoked. Supervisor confirmed that exact
  job was running. The already open MCP session was denied, and model-job/run
  audit denials strictly after the governance revoke timestamp recorded
  execution=not_started. Restoration used a revision CAS on the same grant ID,
  same scope/expiry/issuer/operation, advancing revision 1→2 revoked→3 active.
  An actual ToolHub read succeeded after restoration. The unique file was
  removed. This did not republish grants or widen them.

The first grant driver's relative governance path was rejected by SafeStorePath
before mutation; it was corrected to absolute. Its sleep helper also exposed
the real terminal gap below. The failed attempt's model job was confirmed
completed before a new read-only authorization scenario was admitted. The final
model job also completed and its correct-job/chat reply was delivered with a
semantic denial term, but without the artificial exact marker/READ_DENIED text.
The checker's 150-second marker wait therefore failed; it was not treated as a
terminal run or retried. A direct supervisor recheck established completion and
the actual delivery was inspected using only allowlisted booleans. Authorization
proof comes from the post-revoke exact-run audit, not that response spelling.
The prior grant was restored as soon as the run-bound denial had been verified.

I2 has a newly proven failure beyond catalogue shape: terminal_exec is selected
and exec.run is issued, but its ordinary managed executor reaches
internal/agenttools/call.go's explicit unavailable branch. terminal_job shares
the same branch. A direct documented sleep-1 call and the model's admitted
terminal backend-error reproduce it. The earlier catalogue/schema/CRUD/foreign
proof remains valid, but cannot establish a fully callable catalogue. I2 is now
FAIL pending a proper bounded executor/job implementation. Native shell was not
enabled, and no special-case command or foreground-only substitute was added.
Current docs now disclose the unavailable terminal backend and the absence of
six-passport controls for legacy CLI registry/runtime/UID knobs.

After restoration, all six authored/published/runtime hashes, spawn/channel policy
and managed settings_read agreed again: main r-f5fc67dc3c4791b8, two
r-4543490ac923f7c7. Existing ToolHub/supervisor/communication containers run with
zero restarts on their prior scoped images. Sixty-four exact new main mock output
records (including progress output) were removed; no two-owner output was removed
because none was new. Known synthetic chat links were reclaimed. No setting,
scope, issued right, file, routine or actual bot-message cleanup remains from
these probes; the older synthetic vault memory still requires owner confirmation.

No production Go behavior changed in this turn. Final just check after the doc
corrections passed; prior final affected package suites remain applicable.
Whitespace diff validation passed. Source is still unpublished dirty worktree
changes on 185892107fcc6056a41808fe22533a6c34c7940e; no remote write or full image
build was made. Positive HTTP probing still needs an owned mutable public source
under PublicHTTPClient's unchanged boundary. The background-memory contract
question remains pending; no authorization relaxation was made.

## System-routine, assignment-memory and terminal correction, 2026-11-07

This section supersedes the preceding terminal-stub and assignment-memory-denial
statements. Baseline source SHA is `185892107fcc6056a41808fe22533a6c34c7940e`;
the working tree is unpublished and dirty, so these overlays have no exact source
SHA acceptance. The currently inspected ToolHub image is
`sha256:3cf503108884ec660c25015cc02c0ef359c0524a77eb78caf1c95192684e7b4f`;
owner runtime images are main
`sha256:1fe4ecf8d574a74440a54846dba647ae9922746b511b5b0db04fe996a0c58db0`
and two
`sha256:5aba34eb9abde84fa3a52d4b971494533ba7f3d568fe686a05e618dd2d09b02f`.
All three inspected containers are running with zero restarts. No full Compose
stack or image set was built for this correction.

- Dreaming projects a protected `system-dreaming` schedule into the existing
  engine. `TickSchedules` admits its occurrence and the memory processor runs as
  that occurrence's operation. Named restart/retry, disable, per-local-day,
  autumn repeat, spring gap, revoke and atomic-write tests passed. The isolated
  supported calendar test appended ten facts on each of three local days for
  each of two owners and passed with controlled `Now`. Real model relay calls
  completed one synthetic day per owner in isolated vaults, persisted summaries,
  and promoted zero accepted facts. The full live routine→ToolHub recall chain
  and three-day model acceptance are still open.
- Assignment events now carry their assignment ID and untrusted/probe flags
  through ToolHub and the supervisor. The automation client consumes the
  supervisor's run stream, which publishes the run ID while the job is active.
  ToolHub waits briefly only for that same verified run and rechecks rights at
  the vault. Two live model assignments called `memory_search`, used the own
  synthetic `amber` fact in summary and finding, and produced own completed
  Telegram receipts. Deterministic active/foreign/finished/revoked and writer
  tests passed. Live assignment remember and revoke-after-search remain open.
- `terminal_exec` and `terminal_job` now execute in the intended agent-image
  container with bounded scratch and process control. Direct MCP calls in both
  live managed runtimes passed stdin/output; long job send/poll/kill, timeout,
  foreign job ID and cwd escape were checked. A killed-job state bug was fixed
  after observing the live result. Real model prompts did not invoke `exec.run`,
  so model-selected terminal use is not claimed. The old runtime-local stub is
  still a fallback, while ToolHub's catalog executor handles the selected tool.
- Immutable hub-agent-tools catalog 17.0.0 was registered and rebound to both
  existing owners; managed runtimes were respawned. Catalog discovery showed 51
  first-party tools and six platform tools for each. Published revisions remain
  main `r-f5fc67dc3c4791b8`, two `r-4543490ac923f7c7`.

The current corrections have named regression, touched-package vet, final
affected-package suites and `just check` passes. The initial full affected suite
found one stale terminal use-count assertion; its named retest and the complete
agenttools package passed after repair. `just check` first identified gofmt and a
501-line routines document; both were repaired and the final run passed. Full
Docker gate, remote CI and the remaining live/fault matrix remain open.

## M32 issue audit, 2026-11-07

Read-only GitHub inspection found all 20 milestone issues #508–#527 OPEN and all
acceptance checkboxes unchecked. The statuses below are local evidence judgments,
not GitHub closures. All rows use dirty baseline source SHA
`185892107fcc6056a41808fe22533a6c34c7940e` and the ToolHub/runtime image
identities recorded in the correction above; no exact-source image/CI proof exists.

| Issue | Local status | Existing proof | Remaining criterion |
| --- | --- | --- | --- |
| #508 | PASS | Six-authored publication, draft inertness, retired override audit | GitHub checkbox/CI status untouched |
| #509 | PASS | Live narrow scope/open session and independent-right regressions | GitHub checkbox/CI status untouched |
| #510 | PASS | Live selection remove/re-add, unchanged grants and idempotent tests | GitHub checkbox/CI status untouched |
| #511 | FAIL | Native publication and live complete revision | Controlled pause at every activation step |
| #512 | FAIL | Native rollback/recovery tests | Integrated fault at every step and concurrent revoke |
| #513 | PASS | Two live private vault markers, resolver/lock isolation tests | GitHub checkbox/CI status untouched |
| #514 | FAIL | Exact-run tests, live foreign denial | Concurrent private/group admission |
| #515 | PASS | Two-owner live ToolHub search, switch/right tests, no runtime vault mount | GitHub checkbox/CI status untouched |
| #516 | FAIL | Native capture recovery | Live lost-ack/restart/foreign fault matrix |
| #517 | FAIL | Owner recall and no legacy runtime vault access | Concurrent private/group prompt check |
| #518 | PASS | Live remember, retry/revoke/disable writer tests | GitHub checkbox/CI status untouched |
| #519 | FAIL | Live confirmed edit/denials, native journal recovery | Container crash between write and receipt |
| #520 | PASS | Current runtime mounts omit `/vault`; confirmed writer and direct-write refusal | GitHub checkbox/CI status untouched |
| #521 | PASS | Real temporary storage, reentrant/concurrent tests, one clock | GitHub checkbox/CI status untouched |
| #522 | FAIL | Native restart, idempotency and outbox tests | Integrated lost-dispatch-ack fault |
| #523 | PASS | Live ToolHub routing, due work during communication outage, same pending delivery | GitHub checkbox/CI status untouched |
| #524 | FAIL | Protected schedule, two-zone/DST/restart tests | Two-zone live routine and config-edit matrix |
| #525 | FAIL | Native guarded commit and digest tests | Live crash/revoke/outage matrix |
| #526 | FAIL | Partial two-owner live matrix, positive HTTP watcher and scoped images | Group/capture/dreaming/fault completion and exact SHA |
| #527 | FAIL | Catalog 18 projection, docs and local gates | #526, exact-source Docker/CI, CHG closure |

## Positive public HTTP watcher, 2026-11-07

One `artem_main` watcher `m32-http-1794074606` was created through the existing
ToolHub routine API in the existing dev stack. Its GET source was the public
`https://httpbingo.org/uuid` endpoint; successive polls recorded different
value hashes, no watch error or blocked state, and five durable ToolHub outbox
records marked sent to the owner's Telegram chat. The watcher was deleted before
another poll. Each of the five bot messages was matched to its exact watch
occurrence, owner chat, marker and positive Telegram message ID in the
communication SQLite receipt, then deleted through Telegram `deleteMessage`.
No incoming human message or other user's message was touched. Native tests
cover private-address refusal, response size, retry/rate limit and deduplication.

## Exact memory read and scoped catalog overlay, 2026-11-07

`memory_read` resolves an indexed hit ID to the full private text and evidence
through ToolHub under the current `memory.search` right, live run, selection and
memory switch. A foreign, unknown or forgotten ID is opaque; own forgotten
history requires `history:true`. The new immutable `platform/catalogs/18` and
`hub-agent-tools@18.0.0` were rebound for both owners and their managed runtimes
respawned. Each now exposes 52 first-party tools plus six platform tools, with
matching descriptions, effects and schemas. The first catalog-corrected ToolHub image was
`sha256:07ada38dd08ba6103a8c2ce28ebdf942c7eeddd5a2af374fdc2b1101bc057298`;
main runtime is `sha256:1fe4ecf8d574a74440a54846dba647ae9922746b511b5b0db04fe996a0c58db0`
and two runtime is `sha256:5aba34eb9abde84fa3a52d4b971494533ba7f3d568fe686a05e618dd2d09b02f`.
These are scoped dev overlays on dirty source baseline
`185892107fcc6056a41808fe22533a6c34c7940e`, not exact committed-source images.

The first scoped rebuild replaced the binary but left catalog 17 from the base
image; the supervisor rights check then failed closed with 403. `dev-rebuild`
now stages only the five pinned catalog files into the ToolHub overlay. A
second scoped ToolHub rebuild restored rights; both managed catalogs were
verified without a full stack rebuild. Named exact-read and catalog-stage tests
and touched `go vet` passed. Both managed real-model jobs subsequently called
`memory_search` and `memory_read` with admitted/completed/succeeded audit events
for their exact job IDs. Each eventually replied with its own `amber` marker;
the main reply arrived after the five-minute polling deadline but was verified
in the owner outbox. Two exact mock deliveries and four temporary links per
owner were removed. The model-read path therefore passes for both owners.

The affected-package gate initially failed in `internal/e2e` because its
unbound-vault fixture received a 502 after the new vault-lock admission and
because `memory_read` lacked a registry case. Read-only search/read now return
honest `unavailable` after current admission when no binding resolves; writer
calls still fail closed. Registry case E2E-251 selects the exact-ID regression.
The two named e2e regressions and touched vet passed after this repair.
`just check` passed after the final source edit. The final touched `just test
./internal/toolhub ./internal/e2e` rerun passed (131.522s and 12.888s);
`internal/catalog`, `internal/memory/search`, `internal/agenttools`,
`internal/stack` and `cmd/hubctl` had passed in the first affected-package
batch. The final ToolHub-only scoped dev rebuild/recreate used image
`sha256:98ae283903637f8bfe8f46fe0a636459ff1eb569b74d50d9945619c7fb913a10`;
the service became healthy without a full stack rebuild. After removing the
temporary trace, a cached scoped rebuild/recreate returned the same application
config digest and produced final manifest-list image identity
`sha256:fc7d11b30fd61b441655e36c6568966e37b6006a7b379551a3f2bf9f3ca246de`.

An additional two-owner assignment search→read smoke used mock chats only.
Both sources remained at zero occurrences and wakes because this local driver
used the service CRUD endpoint, which supplies the enrolled Telegram caller
envelope, while its plan specified a mock destination. The resulting channel
authorization correctly skipped execution. A temporary allowlist stage trace
confirmed both owner clock passes completed every stage; it was removed from
source afterward. Both invalid test assignments were cancelled and removed;
one exact mock delivery and five temporary links per owner were removed.
This probe is inconclusive for assignment read/remember/revoke acceptance and
does not establish a product clock defect.

A later targeted real-model terminal prompt used the two existing managed
owners and local mock channels. The `artem_main` job selected `terminal_exec`:
its exact-job ToolHub audit has `terminal.exec` admitted and completed with
successful execution, and the model reply contained the synthetic command
marker. The `artem_two` model replied without invoking the terminal; its direct
managed MCP terminal execution had already passed. The one-agent short-command
bullet now passes. Exact mock deliveries and links were removed after the run.
The current 26-bullet count is 16 PASS, 9 FAIL, 1 BLOCKED (62%).

The assignment read path was then retested with the correct mock channel
envelope through ToolHub's existing service control operation, with no new
Compose stack or Telegram send. For each existing owner, a synthetic hook
created one routine occurrence and one real model wake. The exact assignment
job audit shows `memory.search` and `memory.read` admitted and completed with
successful execution. Both durable assignment summaries and findings contain
the owner's synthetic `amber` fact, and each exact job delivered once to its
own mock chat. Both assignments were cancelled and removed; exact synthetic
mock deliveries and links were removed. Assignment remember and post-revoke
write denial remain unproven live, so the whole automation bullet stays FAIL.

## 2026-11-08 owner-scoped five-check technical acceptance

The historical 26-bullet status above predates this narrower technical pass.
The current five-row result and limits are in `final-matrix.md`: 5/5 local
technical checks passed, with human-origin Telegram incoming and remote CI
still outside this pass. Container source snapshot
`7c08b9516b65ddf567ce89ef01b0cee8cfcad61a` was clean and unpublished;
the runtime/core image identities are recorded in the matrix.

`TestM32RealGroupMemoryIsolation` passed against those exact images without an
overlay. `TestM32RealLifecycleMemoryIsolation` passed using the same product
images with a test-only Go overlay. It covered overlapping real Hermes runs,
ToolHub memory isolation, capture lost acknowledgement across ToolHub restart
with one turn/reply/provenance per owner, due agent work during communication
outage, durable delivery recovery, file watcher filter, assignment wakes,
restart deduplication and pause/revoke delivery fences. Before the successful
run, two attempts exposed a transient failed Hermes job categorized as a model
HTTP error; neither source nor images changed between those outcomes. The
exact upstream HTTP status was not retained, so the intermittent failure is
not claimed fixed.

The named native `TestM32DreamingRelayIsolationAndRecovery`,
`TestM32CaptureLostAcknowledgementNative`,
`TestM32MemoryChannelConfirmationRoundTrip` and
`TestM32AssignmentReconcileAndRecovery` passed together. The dreaming test
used controlled local time and isolated vaults for two owners × three days ×
ten facts, then verified protected routine execution, revoke/restart recovery
and ToolHub search/read of its result. `TestAssignmentMemoryCardRememberAndRevoke`
passed under a verified exact assignment run with current grants and actual
vault write; earlier managed model assignments independently proved
memory search/read and fact use. This does not claim a single live assignment
model chose `remember`.

The final broad local `just test` run passed every affected package except one
stale supervisor test expectation. That test had expected a direct ToolHub MCP
URL in a managed runtime, while the current contract uses the local MCP proxy.
The expectation and unsafe full-config failure output were corrected;
`TestRunArgsMountsExactContextAndLimits`, `just check` and
`just test ./internal/supervisor` then passed. No remote workflow was started.

## 2026-11-08 direct agent tool-family smoke

Against existing `artem_two` dev via the mock channel's real queue/model/ToolHub
path, source HEAD `185892107fcc6056a41808fe22533a6c34c7940e` plus dirty local
changes, ToolHub image `sha256:11651e9fd2e5c31bede9a25466f37974b8e02cb350bb73dfed416f0103643e59`:

| Family | Exact-job evidence | Result |
| --- | --- | --- |
| Files | `mock-e29085e8…`: write/read; `mock-e29fa6fb…`: read/edit/read/remove; `mock-0d4f77d8…`: remove file and directory | PASS; synthetic path absent |
| Projects | `mock-278434ed…`: create/list/remove/list | PASS; synthetic project absent |
| Documents | `mock-5ed6255b…`: create/read/edit/read/convert/read/remove ×2/list | PASS; artifacts absent |
| Terminal | `mock-6b590176…`: exec ×2 and job list/send/poll/kill/poll, reply contained `got:beta` | PASS |
| Skills | `mock-253e0115…`: search and create draft | PARTIAL; draft rejected and absent; live publish/change/delete requires owner approval |
| Routines | `mock-23803038…`: list/create/read/edit/pause/remove | PASS; one initial edit failed before corrected retry |
| Assignments | `mock-af646026…`: draft create/list/read/edit/read/remove/list | PASS; no work queued |
| Watches | `mock-5f0d6785…`: create/list/sample/pause; `mock-8419d8df…`: remove/list | PARTIAL; resume failed through agent, direct owner-scoped service call succeeded |
| Media | `mock-131ad619…`: local PNG→WebP/list/remove ×2 | PARTIAL; `media_inspect` in `mock-45068ebc…` returned backend error and the run failed |
| Settings | `mock-b61d9d92…`: settings_read | PARTIAL; no personal passport mutation |
| Connectors | `mock-9656dd82…`: list; `mock-b544ed00…`: search | PARTIAL; no connector was installed and provider lifecycle was not changed |
| Memory | `mock-0aa8691a…`: search/read an existing synthetic fact | PARTIAL; no new personal memory or owner-confirmed edit/delete |

The first live inventory had only eight first-party tools after catalog
`18.0.1`: existing policy/profile rules still used old implementation digests.
Both owners' original 48 capability tuples were re-pinned to the new digest
without widening actions/scopes; a copied-store dry run showed 52 selected
tools for each owner, and the live `artem_two` tool search showed 52. Future
catalog rebinds still need a durable migration/validation fix.

Connector calls initially returned `uncertain`. The ToolHub facade was called
under its registry admission lock, and the managed facade required a legacy
connector bundle even when the exact connector tool was selected and allowed.
`TestRemoteExecutorDispatch` now covers the lock boundary and selected-only
profile. A scoped ToolHub rebuild/recreate and managed runtime respawn preceded
the successful connector list/search jobs; no full Compose stack was built.
After the final argument-scope and `connector_edit` name repair, image
`sha256:62fa21346c887c03b5f7507a1687b594f8abfee7e3e99c113df785f82a3f0ac1`
admitted and completed a further `connector_list` in `mock-487e877e…`.
That model run later ended `run.failed` before a final reply; the exact tool
call passed, while the whole round trip did not. Its failure cause remains
unresolved. The earlier successful list/search runs used the preceding scoped
ToolHub image.
The skill draft and synthetic workspace files were removed. Mock channel
records remain as local audit evidence and are subject to ordinary spool
retention; no external channel message was sent.

Final connector compatibility guard: the selected `connector_edit` path now
checks its exact managed selection, while the existing connector bundle still
authorizes its mapped lifecycle verb. `TestConnectorEditLegacyBundle` and
`TestRemoteExecutorDispatch` passed, as did `go vet -p=2 ./internal/toolhub`,
`just test ./internal/toolhub`, and `just check`. The first `just check` attempt
hit intermittent Windows `Access is denied` launching Go's compiler; the
immediate retry passed. Only ToolHub was rebuilt/recreated. Its current local
image identity is
`sha256:4f956d12c516b5bcea85c18119d9afb8c77f59fe13d1a24f73bdf949797bec74`
(running). This final image has package-test coverage but no further live
connector model round trip; the successful list/search agent jobs above used
the prior scoped image. The source remains dirty at HEAD
`185892107fcc6056a41808fe22533a6c34c7940e`.

## Memory boundary follow-up, 2026-11-08

The owner's ordinary remember → search → read → fact-use cycle had already
passed for both dev owners. This follow-up used only synthetic `m32acc-*`
markers and mock chats in the existing dev stack. Source HEAD was
`b35a97c2dacd5e65e535d2036646d80088bc827c`; the running ToolHub image
was `sha256:bc33d338874c1b5b11525bb8f05a87f270bce2183b318425ffabde66d21af2eb`.
The two managed runtime image IDs were `sha256:2091994b243c0edaff7a987af4e26c871abb277b17dd91ebf2fc901d86ec30d8`
and `sha256:2fc5be0977785411f9219783273a40bbec06498f90142c0fbd5911252225872b`.
These dev overlays have no exact HEAD label; this is local live evidence, not
published-image or remote CI acceptance.

| Requested case | Result | Observed reason |
| --- | --- | --- |
| Two simultaneous writes | **FAIL for strict overlap** | Two mock jobs were submitted concurrently and each model invoked ToolHub `memory_card`; both owner vaults had one card, one `ok` write-journal entry and one admitted/completed `memory.write` audit pair. The actual writer calls were about 39 seconds apart, so this does not prove simultaneous writer contention. `TestMemoryCardTwoUserIsolation` passed on temporary vaults. |
| Right revoked before write | **PASS** | `artem_main`'s current `memory.remember` grant was revoked before one live mock job. ToolHub audit recorded two `decision/deny/not_started` events; the named card and successful write journal entry were absent. The original grant was restored by same-record revision CAS and verified active. `TestMemoryCardRevokedRightBlocksCommit` passed on temporary storage. |
| Memory in active assignment | **FAIL live** | A verified owner mock assignment reached `active`, recorded one wake and one model-call attempt, but its dispatch intent remained `acked=false`; supervisor returned 404 for that exact event job, no model progress or memory card appeared. A ToolHub restart did not recover it. The assignment was cancelled and removed. `TestAssignmentMemoryCardRememberAndRevoke` passed in-process, but it does not replace this missing live execution. |
| Foreign or finished run | **PASS** | Direct ToolHub read probes with each owner's bearer and the other owner's job/run, then each completed own job/run, returned HTTP 502 in all four cases. The segmented ToolHub audit recorded four `decision/preflight-error/not_started` entries per owner across the direct MCP and HTTP probes; no extra vault mutation appeared. `TestAssignmentMemoryWaitsForSameLiveRunID` passed on temporary storage. |
| No duplicate after restart | **PASS for event replay** | Only the existing ToolHub container was restarted. Repeating both exact mock messages with their original client IDs returned the same job IDs, and each card still contained one marker before cleanup. The temporary-vault `TestMemoryCardIntentAndCommitRecovery` and `TestAuditRetryAfterJournalFailureDoesNotDuplicate` passed; a live crash at the writer commit barrier was not performed. |

The successful writes are in the segmented ToolHub audit
`runtime/toolhub/audit.jsonl.v1-000001` (the older `audit.jsonl` prefix is
unchanged) and in each vault's `runtime/memory/writes.jsonl`. Cleanup removed
the two exact test cards with path-scoped vault commits, their two request
intents, the active assignment, four mock-chat links and their exact mock
deliveries. The revoked-write and assignment markers never created cards.
Both card paths are absent and clean in their vault Git worktrees. Durable
audit/journal and dispatch receipts remain as append-only evidence. No existing
personal fact was edited or deleted; the managed runs used the owners' ordinary
pre-turn context. No full Compose stack was started, and no remote CI
or external message was sent.

## Memory boundary follow-up correction, 2026-11-09

The two FAIL rows above describe the first probe, not the corrected result.
The first "concurrent" probe submitted two mock jobs together but let the model
schedule the actual writes 39 seconds apart. A new isolated integration probe
(`TestM32LocalConcurrentWriters`) held two real managed Hermes runs open, then
released two direct `memory_card remember` MCP calls against one production
ToolHub process through a start barrier. The final run passed in 64.91 seconds:
both ToolHub call intervals and both `memory.write` audit admitted→completed
windows overlapped. Each synthetic owner vault had exactly one card containing
its own marker and exactly one `remember/ok` journal row tied to its job/run.
The first diagnostic run failed before writing because it used a cli-tools image
without `hub-runtime`; rerunning with the correct existing image passed. That
failed start left one named fixture container, which was removed, and the
fixture cleanup was corrected to register each container before starting it.
The successful run removed its own containers and networks. The two existing
dev services were stopped only while the fixture held their ports, then restarted with their
previous scoped dev images. No full Compose stack or remote CI was run.

The live assignment failure had a separate cause. `artem_main`'s generated
automation routing JSON still stamped policy `policy-301cac0b1edc0491`
after the published owner policy had become `policy-dcac72efa593cc60`.
Supervisor rejected that exact assignment dispatch with HTTP 409, so its
unregistered job naturally returned 404 and the durable dispatch intent stayed
unacknowledged. `hubctl render` regenerated the local dev routing input, and
`hubctl dev-rebuild ... communication-hub` recreated only that service.
Sibling `space apply` now refreshes the shared generated routing immediately
after runtime token enrollment; `TestRefreshSharedRuntimeTokensAfterSiblingApply`
reproduces a stale JSON policy and verifies the current stamp after refresh.
A manual edit to `communication.users.yaml` still requires render and a scoped
communication-hub recreate so both channel maps agree. A new
isolated `artem_two` assignment then reached active, registered a supervisor
run, invoked `memory_card remember`, recorded progress and completed with one
mock delivery. Its exact job has one `remember/ok` journal row and one
`memory.write` admitted/completed audit pair. After restarting ToolHub, the
journal count was still one. The test assignment, its card, exact request intent,
mock links and deliveries were removed; the append-only journal, audit and
dispatch receipts remain. The other M32 boundary results above remain PASS.

This corrects the five requested cases to **5 PASS / 0 FAIL** in local dev
acceptance. Source HEAD was `b35a97c2dacd5e65e535d2036646d80088bc827c`
plus dirty local changes; no exact committed-source image or remote CI acceptance
is claimed. True parallel managed sessions within one owner's runtime remain a
separate ADR-0090 limitation; this probe proves overlapping ToolHub writes
across two owner runs.

## Five simultaneous sessions of one owner, 2026-11-09

`TestM32FiveManagedSessionsExecuteConcurrently` passed in 140.86 seconds on
dirty local source. Five distinct conversations of one synthetic owner each
reached the held model provider before its gate opened. Supervisor reported
five distinct live generations with matching current generations. While all
five runs were active, production ToolHub accepted one `memory_card remember`
per job/run; the isolated vault journal had exactly one successful row for
each, and its audit had exactly one admitted and completed `memory.write` pair
per job. A sixth conversation did not reach the provider while the five
slots were occupied, then all six completed after release. The fixture removed
all ten runtime/control containers and its networks; both preexisting scoped
dev services were restarted. No personal vault or full Compose stack was used.

The first cold-spawn probe failed when concurrent preparation raced on the
shared generated skills directory. The named regression reproduced its
`directory is not empty` failure; per-owner spawn preparation now remains
serialized until Docker has created the container. A second probe showed
that counting raw provider requests was insufficient to establish five
distinct sessions; the final test identifies all five conversations from their
synthetic prompts. This is live local integration, not deployed Telegram
acceptance or an exact committed-image/remote CI gate.

Named supervisor regressions additionally cover five-slot allocation, a
waiting sixth slot, FIFO when two jobs from one conversation are admitted
before either claims a slot, separate `/state` and shared `/state/hermes`
mounts, concurrent cold-spawn preparation, and skill-ref lease routing to the
admitted job's lane. The gateway cap test rejects values above five or zero.

After scoped dev rebuilds, the same test passed again in 88.90 seconds using
the new runtime image, this time sending each memory write through its own
runtime `/mcp` proxy with no caller-supplied job/run headers. ToolHub still
attributed each write to the exact active job/run and the isolated journal
and audit each showed one successful write per lane. The fixture again removed
all runtime/control containers; the two port-sharing dev services restarted.
The running dev ToolHub, supervisor and communication-hub images match their
new scoped tags (`e9b4f0ab38de`, `7dfbb389ec35`, `a2f5ed7012da`); the
selected runtime image for the next spawn is `755187c6f32b`. Unauthenticated
ToolHub and communication endpoints returned 401, confirming the listeners
were up without reading credentials. This is local dirty-source acceptance at
base SHA `b35a97c2dacd5e65e535d2036646d80088bc827c`; no real Telegram
five-session run or remote CI is claimed.

## Single-process comparison baseline, 2026-11-09

Before changing production logic, the isolated five-slot fixture was run twice
against the existing dev runtime image. The first run failed in 189.54 s and
the second in 97.42 s: supervisor returned 503 while cold-spawning additional
runtime/control containers, before five distinct model calls could overlap.
Neither is a successful five-run timing result. The prior successful fixture
result was 88.90 s for the whole test and ten runtime/control containers.

An ignored `.work/tmp/m32-container-baseline.csv` sampled `docker stats` in
the first failed run. The largest observed aggregate was 372.83 MiB across
two runtime and four control containers at one timestamp; an individual
runtime reached 187.00 MiB, a relay 10.91 MiB. The sampler did not capture a
simultaneous ten-container peak and was stopped before retrying without it.
All fixture containers and networks were removed by test cleanup. No personal
facts, full Compose or remote CI participated.

The original fixture also bound its ToolHub to fixed host port 8090. A live
ToolHub already owned that port in later attempts: synthetic model relays saw
HTTP 401 from the other ToolHub and Hermes returned HTTP 403 / permission
denied before reaching the held model. This was a fixture isolation defect,
not evidence that the single-process path failed. The fixture now chooses a
free host port and sends both relays to that port; it never changes the
existing ToolHub.

## Single-process comparison result, 2026-11-09

The scoped runtime image was built with `hubctl build --image runtime` from
the edited source. The isolated five-session live test passed four times;
measured runs took 66.00 s, 66.55 s, 65.15 s and 63.60 s for the full fixture.
In the final exact-image run all five model requests and their ToolHub writes
were ready after 24.377 s. All five had the same runtime generation and one
runtime/control container pair. A sixth session remained queued until a slot
freed. Each of the five wrote one synthetic memory card with its own exact
job/run audit attribution. After release, Hermes itself called ToolHub
`memory_search` once per run; the audit recorded one completed
`memory.search` for each exact job. The test checked the memory write journal
and audit, and its cleanup removed only its generated containers and networks.

At the held-five point in the final run, `docker stats --no-stream` reported
219.2 MiB for the runtime and 8.996 MiB for its control relay, 228.196 MiB
combined. The old
failed sample was 372.83 MiB across only two runtimes and four relays, not a
ten-container peak. The prior successful old whole-test time was 88.90 s,
but the runs were on different local conditions, so the 63.60 s versus
88.90 s difference is indicative rather than a controlled benchmark. The
container count reduction (ten to two per owner for five active sessions),
exact run attribution and successful overlap are decisive for selecting the
single-process design. Its shared process remains a larger failure domain.

## Browser through ToolHub and the existing sidecar, 2026-11-09

The existing `artem_two` dev Playwright sidecar was resumed; no Playwright
package or second sidecar was installed. A host-served static page at
`http://host.docker.internal:18432/` contained only an M32 heading, one local
button and a result label. Direct calls to the installed sidecar established
the actual `browser_wait_for` contract: waiting for the heading succeeded,
while waiting for an absent marker timed out. Each direct probe made and
closed one new tab.

The pre-fix ToolHub run opened a new user-profile session on that page.
`browser_read` returned the heading. `browser_act` with `action=wait,
value="7"` returned `browser operation failed`; the same call with the
heading as value succeeded. The adapter had unconditionally passed `value`
as Playwright's `text`, so the numeric call waited for visible digit 7
instead of seven seconds. The scoped runtime fix maps a finite numeric value
in (0, 30] to Playwright `time` and keeps other values as `text`; out-of-range
numbers fail locally. The post-fix live ToolHub call with `value="7"` passed,
as did a text wait. The browser contract/argument guidance and immutable
`hub-agent-tools@18.3.1` definition were updated. The old 18.2.0 binding
was temporarily retained during live behavior checks because the selected
profile still pinned that version; the new binding must be projected with a
clean-source apply and managed respawn.

During rollout, the protected store refused to re-register `18.3.0` as an
immutable definition. A digest comparison isolated the difference to the
`browser_act` record: that provisional version had been registered before
the final wait argument contract. The completed contract is therefore
versioned `18.3.1`; the provisional record was not overwritten.

The same post-fix user-profile session passed `browser_act click` on the local
button, and the next `browser_read` contained the page's `M32 Browser Clicked`
marker. `browser_capture` completed through ToolHub. The first probe showed
that its returned `browser/<session>.png` path was false: Playwright wrote a
suggested filename at workspace root, despite `--output-dir`. Passing the
`browser/` prefix in the suggested filename fixed that mismatch; the final
probe verified a nonempty PNG at exactly the returned path. An invented
session ID was rejected; an `artem_main` ToolHub call carrying the open
`artem_two` session ID was also rejected. The owner-bound lookup is covered
separately by `TestBrowserSidecarSessionOwnerGrantAndOperations`; the live
other-owner call also met `artem_main`'s narrower tool selection.

While that session remained open, the one active `artem_two` `browser.read`
right was revoked through the protected governance store. Its next ToolHub
`browser_read` was refused before backend execution. Regranting the same
right with a higher revision restored the next read in the same session.
The final governance document has one active `artem_two` `browser.read`
grant and zero revoked rows for that right. All probe sessions returned
successful `browser_act close` results. The audited window contains 17
`browser.act` admitted rows, 16 completed/ok and one pre-fix backend error;
four `browser.capture` admitted/completed-ok pairs; six `browser.open`
admitted/completed-ok pairs; and three other-owner decision denials. These
counts include diagnostic reruns, not one single acceptance run.

Cleanup removed exactly four probe PNGs and 20 sidecar snapshots created
during this test window. An older personal-profile PNG remained in place.
The temporary test page and supervisor proxy were stopped after catalog
rollout; the existing browser sidecar is left to its normal idle lifecycle. No personal
tab was navigated or closed: `browser_open` now creates a new tab and
`browser_act close` closes that tab. No personal memory, full Compose stack or
remote CI was used.

### Clean-source catalog rollout and final repeat

Source `f08d2ff8` contains the final `hub-agent-tools@18.3.1` definition.
`just check` and all eight affected package suites passed before the first
commit; the catalog patch then passed named Browser tests, `just check`, and
the agenttools/stack package suites. Both owners' immutable definition bindings
were registered. `space apply --confirm` published the new first-party
selections without authored passport changes or service actions. The first
two-space projection attempt exposed a separate approval pin: policy ceiling
and profile allow rules still carried the `18.2.0` implementation digest,
so ToolHub correctly hid the new selections. Reviewed policy/profile updates
changed only that digest on 254 main and 258 two existing first-party rules;
their right counts and selected tool counts stayed unchanged. The resulting
policy/profile revisions are main 9/26 and two 9/52. Both owners have one
active `18.3.1` binding. The transient `space apply` recovery journal was
recovered by the next preview/apply; no journal remains. Scoped main ToolHub
and both runtime images were rebuilt. Main respawn returned 200 (no idle
container on the final call); two respawn returned 200 and `applied=true`.

The final `artem_two` ToolHub run on profile revision 52 repeated the whole
user-profile case. Open/read returned the test heading; numeric seven-second
and text waits succeeded; click changed the label and the next read showed
`M32 Browser Clicked`; capture returned a nonempty PNG at its exact path.
The invented ID returned an error and the other-owner call was denied. In the
same open session, right revocation refused read before backend admission;
restoration resumed read, and close succeeded. Audit in the final window has
one open, one capture, four acts and four reads admitted, each with a
completion: 9 ok and one expected backend-error for the invented ID. It also
has one decision denial for each owner (revoke and foreign selection). Every
final-window browser event carries profile revision 52. Governance ends with
one active `artem_two` `browser.read` grant at revision 5. Cleanup removed
the final test PNG and four test-page snapshots; no final test artifact
remains. The pre-existing personal PNG and snapshots were retained.
## M32 web search and fetch live acceptance, 2026-11-09

Pinned upstream is Hermes `869228cab4a8276d3b4c78da9d9939670c47bd0f`
from `docker/Dockerfile`; the running managed runtime's provider registry
contains `brave-free, ddgs, exa, firecrawl, keenable, parallel, perplexity,
searxng, tavily, xai`. The upstream registry and `plugins/web/keyless_mcp.py`
were read in the running image. The ToolHub request used the existing
`artem_two` runtime identity and only `Example Domain IANA` / `https://example.com/`.
No credential values, raw environment, profiles, personal memory or response
body were printed.

Before repair, ToolHub `web_providers` returned `default_search_backend=auto`,
`default_extract_backend=auto`, `multi_search_providers=[]`; `exa`,
`firecrawl`, `keenable`, `parallel` were locally `keyless`, while `ddgs` was
`unavailable`. All checked provider credential presence flags were false in
the authored and materialized dev secrets. The live runtime's web section was
empty; its egress relay admitted only `communication-hub,hub-media`.
`web_search` returned `success=false,error=provider_unavailable` because
`internal/agenttools/web.go` maps every upstream `success=false` to that code.
The pinned Exa keyless diagnostic behind the same relay classified HTTP 403
at the proxy. `web_search_multi` returned `no search providers configured`.
The prior Firecrawl 403 was also consistent with the missing egress host.

Published `web.search_backend=exa`, `extract_backend=exa`, and
`search_providers=[exa,parallel,firecrawl,keenable]`. One first publication
attempt failed before activation on an existing GitHub binding repin with
`owned storage I/O unavailable`; a no-change publication restored ToolHub.
A subsequent apply lacked a host-reachable Supervisor, left the guarded
`pending` pointer, and was recovered through a temporary local Supervisor
proxy. The successful publish `r-007256aeb6fb16f3` respawned runtime but
did not recreate egress relay. The relay was recreated alone from the
generated Compose file; its active host allowlist then included
`mcp.exa.ai`, `search.parallel.ai`, `api.firecrawl.dev`, and
`api.keenable.ai`. The fix in `internal/reconcile` now plans relay recreate
before runtime respawn for web routing changes. Actual follow-up applies to
`r-21dc09f485dc4a19` and final `r-ca97162f3014e9c7` both reported
`recreate egress-relay: applied` then `respawn hermes-runtime: applied`.
The Firecrawl keyless doctor warning was removed because the pinned upstream
has an anonymous client path; this does not assert remote scrape entitlement.

ToolHub receipts after egress repair:

| Call | Safe observed fields |
| --- | --- |
| `web_providers` | default search/extract `exa`; multi allowlist `exa,parallel,firecrawl,keenable` |
| `web_search` | final `success=true`, `search_hits=2` |
| `web_search_multi` `all` | final `success=true`, `providers_used=[exa,parallel,firecrawl,keenable]`, `results_count=5`, `partial_failure=false` |
| `web_fetch` | final `results_count=1`, `result_has_error=false`, content present |
| `web_search_multi` `ddgs` | rejected as not configured; no provider request made |
| `web_search_multi` `all`, earlier call | `success=true`, `partial_failure=true`, four hits from `exa,firecrawl,keenable`; `parallel=search failed` |
| `web_fetch` with Firecrawl temporarily selected | one result, `error_http403=true`, no content |

For the Firecrawl-specific failure, the running relay already allowed
`api.firecrawl.dev`; pinned `_KeylessFirecrawlClient` posts to `/v2/scrape`
without an Authorization header. A direct classification of that pinned
client in the same runtime returned HTTP 403, with neither `host not allowed`
nor proxy-tunnel failure. Firecrawl extraction is **BLOCKED** until a valid
`FIRECRAWL_API_KEY` or authorized managed gateway route is provided. Exa
extraction is the working published default. The final config has no pending
apply journal. No test browser session or owned data object was created.

## Goals, context, and GitHub connector follow-up — 2026-11-09

The goal check used only two synthetic `artem_two` mock chats
(`800000903209217400`, `800000903209265300`). The second confirmed run
`mock-c7a594696b02689bf74db9ab15c2eda33210e9d754d504501eb5c7b74a7f8e14`
had an exact running Hermes run and generation. ToolHub returned, in order:
`goal_status` absent, `goal_edit set` active, `goal_status` active,
`goal_edit pause` paused, `goal_edit resume` active, `goal_remove` absent,
`goal_status` absent. A foreign `artem_main` token was refused while the run
was active; the owning token was refused after it became terminal. The first
diagnostic goal was cleared, the second removed, and a later exact status
check found neither goal. The two mock deliveries and links were removed;
ordinary job and audit history was retained. No personal goal was read.
The durable ToolHub audit segments for the exact second mock job contain
eight admits, seven completed `ok` results, one completed preflight error
for the terminal call, and one authorization denial for the foreign call.
The current communication goal status reports `found=false` for both exact
synthetic conversations.

The context check used `TestM32ContextPreviewApplyAndContinue` with disposable
owners, a local model fixture, real managed Hermes containers, and ToolHub.
On the same active session, `context_status` established a baseline,
`context_edit preview=true` returned `applied=false`, and a second status
reported the same compaction count. `context_edit queue=true` returned
`failure_class=queued` while the run held the lease. After the run completed,
the exact session journal contained one successful `manual` event; a new run
on the same session saw `compactions=1` through ToolHub and completed. The
fixture cleanup removed its containers and temporary state.

GitHub's active connector binding was on the current policy, but its Broker
grant reference was on an older policy epoch. `POST /v1/connectors:install`
for `github` returned HTTP 200, `awaiting_confirm`, `link_sent=true`. The
ToolHub log showed the first notice failed DNS while communication was
restarting. Its startup failure was one historical non-test run receipt with
a Telegram delivery target inconsistent with its chat ID; a read-only scan
found one invalid delivery among 416 receipts with deliveries. Recovery now
skips that unsafe replay while retaining its receipt; the scoped communication
rebuild started healthy with restart count zero. A second install call on the
same operation renewed the expired confirmation nonce, but the notice reached
communication with HTTP 400: this local HTTP install had no initiating
Telegram job and the channel selected an earlier mock origin, where a human
confirmation is forbidden. The earlier `link_sent` flag described a minted
link, not successful Telegram delivery. The owner reported receiving no
message; no confirmation, grant replacement, or `get_me` success is claimed.
This connector is outside the accepted local M32 MVP denominator.

Verification: `just check` passed; the named communication recovery and
connector retry regressions passed; the isolated Docker context integration
passed twice, including unchanged preview tokens and last event. One combined
`just test` run passed ToolHub, devcheck and cmd/toolhub but failed in
communication with an overlong, truncated test log; a scoped rerun of
`just test ./internal/communication` passed. The first failure's test name
was not recoverable, so this is a local flaky-gate risk, not a clean claim
for that combined run. Staged diff secret scan and whitespace check passed.
