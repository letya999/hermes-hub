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
