# CHG-0292: Five-prompt live tools repair

## Goal

Repair the actual terminal, media and speech failures reported by the owner on
2026-11-09; verify file revision editing, PDF extraction, browser and web calls
through the same admitted MCP surface.

## Scope

- Reject malformed agent-tool arguments before backend admission and effects.
- Wire scoped audio transcription to hub-stt and voice synthesis to the existing
  owner-bound communication outbox, preserving uncertain delivery semantics.
- Publish an immutable agent-tools version, prompt guidance and documentation.
- Correct authored deployment passports, start shared media/speech services,
  rebind and respawn the existing dev runtime, then collect live evidence.

## Acceptance

- A malformed terminal call cannot execute or create an uncertain operation;
  a corrected call computes the real fixture hash and line count.
- STT cannot escape its admitted filesystem scope; timestamps survive the API.
- Voice delivery uses the live run's recorded destination and is idempotent per
  tool operation; a sibling bearer cannot select another owner's destination.
- Real image generation/edit/inspection and Russian voice synthesis work, with
  queued versus sent outcomes reported separately; no mock is provider proof.
- Existing file/PDF/web/browser tools remain functional. Run just check and the
  affected package tests once after the final edit; no publication is requested.

## Final live evidence

- Managed passport applied without rollback; catalog 18.3.2 selected and enabled for both spaces.
- Terminal SHA-256 matches file revision; five lines. PDF extraction, browser open/read/capture/close, vision, web fetch/search/cache and STT passed through MCP.
- Speech delivered through media_speak: durable Telegram receipt voice_sent=true, outbox/done.
- Shared gateway user policy and user egress relay reconciled to the published configuration.
- Image generation/edit remain blocked by upstream authorization. Current Devin catalog and image endpoint probes do not establish a working image-generation route; do not mark this change done until actual image bytes and edits succeed.
- just check, all affected package suites, security gate, Docker breaker check and scoped Docker cleanup passed. Remote CI was not run for unpublished changes.

## 2026-11-11 document-chain follow-up

- A real model run through `/v1/mock/message` completed `documents.create`,
  `documents.edit`, `documents.convert`, `documents.read`, and
  `documents.remove` with successful ToolHub admission/completion pairs
  (job `mock-ac246ef18cd9254793652e2fbf6bde3d60513c219e56701201845425f602986a`).
- The resulting one-page PDF retained the corrected second point and marker;
  independent text extraction and rendered-page inspection passed. The PDF
  remained in the managed workspace, while the intermediate Markdown did not.
- Fixed two issues exposed by the live run: embedded PDF glyph advances were
  absent, causing visible overlap, and root-run `dev-rebuild` wrote the runtime
  image pin unreadable by the supervisor's UID. Generated plugin views also
  need group write access so supervisor can rematerialize them at spawn.
- Local acceptance is limited to this document path and the relevant dev stack;
  the image-provider blocker above remains open.

## 2026-11-11 media mock follow-up

- The exact media prompt was sent through `/v1/mock/message` for `artem_two`.
  `media_generate` reached hub-media, but its upstream image endpoint returned
  HTTP 403 (hub-media surfaced 502); no image bytes exist to fetch, inspect,
  convert or edit. This remains a provider authorization blocker.
- The absolute STT path `/workspace/work/tools-audit-voice.wav` was rejected by
  ToolHub's relative-path capability policy. Repeating with
  `work/tools-audit-voice.wav` succeeded through hub-stt with a measured segment
  at 0–4 seconds. Release 27 / agent-tools 18.3.6 now normalizes exactly the
  `/workspace/` alias before admission, leaving traversal and foreign absolute
  paths rejected; managed rebind and a fresh live absolute-path probe remain.
- Two live `media_speak` calls initially returned HTTP 403 after a 20-second
  ToolHub rights timeout. The ToolHub admission lock was held across the
  communication callback; the callback reentered ToolHub for `send_voice`.
  The remote channel callback now runs after releasing and rechecking the
  admission authority. Mock speech also captures actual TTS audio under the
  owned chat, with `voice_sent=true` only after synthesis and durable capture.
- After scoped dev rebuilds of communication-hub and ToolHub, a model call
  completed `media_speak` in about 204 ms. ToolHub audit recorded admitted and
  completed/ok, the communication outbox reached done, and the mock outbox
  contains a 9,098-byte OGG voice file. `status=queued` remained only an enqueue
  acknowledgment in the model response.
- Release 27 rebind initially failed because the catalog operator appended a
  second selection for the same capability. Catalog upgrades now replace the
  previous version's selection, preserve its path scopes, and confirm a new
  profile revision; a regression covers the upgrade and rejection boundaries.
- After the operator rebind and managed `artem_two` runtime respawn, a live
  model request using `/workspace/work/tools-audit-voice.wav` completed with
  the real 0–4-second STT segment. A full repeat of the owner's media prompt
  then produced ToolHub admitted/completed records for generate (backend
  failure), transcribe (ok) and speak (ok). The mock outbox's voice receipt is
  a 9,098-byte OGG with `voice_sent=true`; image generation still receives
  HTTP 403 from CLIProxy and surfaces HTTP 502 through hub-media.

## 2026-11-11 local skill draft follow-up

- The unmodified owner prompt completed through the mock model channel after
  `hub-agent-tools@18.3.7` / Release 28 rebind and managed runtime respawn.
  ToolHub recorded successful `skills.search`, `skills.invoke`, `files.write`
  and `skills.edit`; the model staged `/workspace/work/germi-test-skill/SKILL.md`
  and imported it as a draft without publishing it.
- `install_local` now returns the actual draft diff, lint and scan, matching
  its advertised contract. The prompt and docs explain workspace-relative
  paths; the guarded contract avoids naming a file tool when that family is
  disabled. The local approval card responded HTTP 200 from Windows, and the
  skill remains absent from the effective `/skills` view pending owner approval.
- `just check` and affected `just test` passed. Remote CI acceptance remains
  unavailable for these unpublished changes.
- Two catalog operator calls briefly returned `owned storage I/O unavailable`
  while the live ToolHub store was changing; immediate retries succeeded.
  Storage reconciliation now tolerates a child disappearing during its scan,
  while still rejecting a missing root and permission errors. The later
  reminder follow-up captured the separate SQLite snapshot conflict below.

## 2026-11-11 one-time reminder follow-up

- A live mock model run exposed that `routine_create` defaulted to Telegram for a mock origin. Communication now verifies the origin mapping and claimed chat before routing that reminder to the mock channel; the foreign-owner regression rejects reuse.
- The first model run removed the reminder before its due time. The later two-turn run created `germi-reminder-live2`, read and paused/resumed it, captured the exact reminder in the mock outbox, observed `last_delivery_state=delivered`, then retired only that ID. Release 29 / `hub-agent-tools@18.3.8` adds post-delivery cleanup guidance to the tool and prompt contract.
- ToolHub's nightly relay caller file was mode 0600 and unreadable by its service user. Rendering now grants the host file group read access and Compose adds that group to ToolHub. Both mounted caller files became readable after the scoped recreate, and the 2026-11-11 dreaming records completed for both owners.
- After Release 29 rebind and managed respawn, the model left `germi-reminder-contract-29` active at revision 3 until its 03:11:56 UTC deadline. The mock outbox and communication done receipt recorded the exact reminder at 03:11:58 UTC. A follow-up model turn read `last_delivery_state=delivered` and retired only that ID at revision 4.
- The intermittent operator error `owned storage I/O unavailable` was reproduced with concurrent live-store writes. Its wrapped cause is an optimistic SQLite snapshot conflict, not a disk failure. Catalog enable and agent-tools registration now reload and retry only this conflict, at most three attempts; deterministic stale-snapshot regressions cover both commands.
- The event-watch mock run read the blocked existing watch but failed before its answer: `platform_status` re-entered Store.Reload while a remote tool call still held the registry admission read lock. A concurrent store change made Reload wait for the write lock, stalling rights decisions; model-relay then failed closed with `403 right denied` despite an active `models.use` grant. Remaining remote agent-tool handlers now dispatch after releasing the admission lock and reverify the binding. A database-backed regression forces the store change during admission.
- The updated ToolHub image was activated in the existing WSL dev stack. A direct rights decision returned HTTP 200 with `allow=true`, and a second exact-prompt mock job (`mock-82306bb0d7d273a67b27372ff78c48f6972088f1cfd1c1b7afad73384b5b2a82`) reached `run.completed`. The model reported `watch-germi-live-check` blocked by `owner authorization failed`, checked candidate reads, and correctly declined to create a test watch because none returned stable event IDs. One candidate returned a backend error; no watch state was changed.

## 2026-11-11 assignment follow-up

- The exact assignment prompt reached the live model via mock job `mock-a3ba8b5dadb17a520b16e86380668bbdc2fbc5f5e002e3af5921f8438da61bf3`. The model passed an ID rather than a source plan to `assignment_plan`, then tried `apply` despite its failed verdict. It created `asg-d4b38e725272de349212ab246c1c69a0` as a draft; no result was recorded.
- A recovery mock on a clean chat fetched the official Go page through ToolHub but tried standalone `watch_create`, and `resume` promoted the draft to `active` with no plan. Direct owner-scoped reads confirmed zero sources and progress. The stray `go-tutorial-watch` was deleted and read back as disabled/paused; the assignment was retained.
- A guarded owner-scoped dry-run of a one-time HTTP check returned `go-tutorial: ok`. The same assignment was updated and applied with one source. The backend defaults the first updated plan revision like create/dry-run and keeps resumed unapplied drafts in draft state. A one-shot probe now settles after schedule retirement, and the completing trigger's confirmed result survives the send-time fence. `hub-agent-tools@18.3.10` on Release 29 clarifies plan shape, explicit plan-first requests, update-before-apply and real trigger-job provenance for progress.
- The retained assignment reached revision 12 and `completed` after a second one-time watcher captured the official page's `func TestHelloName(t *testing.T)` example with `t.Errorf`; the specialist recorded two source-backed findings. Its model answer initially failed the send-time fence because completion had archived the occurrence. The narrow archived-occurrence authorization fix was locally tested, then the original answer was recovered through the service delivery endpoint and appeared once in mock chat 4711923 as `recovery2-routine-296359b1e03a03ac2d11ee2758ce17deb0fbccfa592c5ebd9195872ccdf87fbc`. This recovery proves the live delivery path but is not a fresh automatic model run after the fix. The assignment remains for owner-approved deletion.
- `hub-agent-tools@18.3.10` was registered and enabled for both existing dev spaces, the effective guides were rendered, and managed runtimes were respawned or marked fresh on next spawn. A read-only post-respawn model run completed; the live store projected 71 tools for `artem_main` and 124 for `artem_two`, including `assignment_plan` and `assignment_edit` in both.

## 2026-11-11 goal, context and settings follow-up

- The full prompt sent to mock chat 4711924 initially ended without an assistant reply. A shorter goal retry reached `goal_edit` but returned `Goal unavailable: goal returned HTTP 403`. Supervisor logs classified two `agents.monitor_self` rights-decision timeouts; direct owner-scoped rights and goal-control calls succeeded. The in-runtime `handler:goals` call held ToolHub's registry admission lock while communication and supervisor called back into ToolHub. The same reentry risk applied to `handler:context` when compaction uses model relay. Both now dispatch after releasing admission and reverify the binding before execution; a deterministic lock-reentry regression failed before the change and passed after it.
- The scoped ToolHub dev image was deployed in the existing WSL stack. A new live mock model job `mock-d3d54f56dd5fc0d1d371a5a000486aaebbcca7f9a08c317c5e17b275156d70c0` admitted and completed goal set, status, pause, status, resume, status, remove and final status. Direct owner-scoped goal status confirmed the test record absent afterward.
- The same mock chat's context job `mock-6bd6c395d703f99884a2cdb85ea1a20a3631b8b964b1fa787bdd3669b7721779` returned `applied=false`, `would_method=summary`, unchanged compaction count and last event. The live token estimate rose as tool messages were added; no actual compact call ran. Settings job `mock-890f579701d51e3233357406642e95d8b501e519a6506014923231aa45f31bcd` read the published revision and key allowlist; because `compression.protect_last_n` had no published baseline, it stopped before `settings_edit` as the owner requested. The audit contains only `settings.read` for that job. Post-deploy logs showed no rights timeout, HTTP 403, panic or failed run for these checks.
- A full combined mock on fresh chat 4711925 ran into `settings_edit` and left a publication journal at phase `prepared`, with `current=pending`. `space apply` recovered the previous revision `r-58907eb0e4e07b0e`; a subsequent dry run showed zero authored changes, and a live `settings_read` returned that revision. The settings publisher now retries recovery under the apply lock after a failed publication and recovers an interrupted journal before a subsequent edit. A regression exercises the interrupted-journal path.
- `hub-agent-tools@18.3.11` states that a settings edit may respawn the caller's runtime and interrupt the turn. The prompt contract and docs direct the agent to stop after reading when the owner requires same-turn restoration. Both dev spaces were registered and rebound to the new catalog version, their prompt views rerendered, and the scoped ToolHub and supervisor dev images recreated healthy. No managed runtime was active at rebind, so the next spawn used the fresh prompt. The new-version settings mock `mock-768af75db62f53e2f787b708f116eea2b092fb2c23067ea76a52f4dc333ac4d9` completed with three `settings.read` admissions and no edit.
- Two further full combined prompts reached no audited capability call: the first called `tool_search`, then the model continuation returned an upstream interruption; the second returned a generic unprocessed-request message. This is separate from the successful split goal, context and settings runs; combined-prompt model reliability remains unverified.
- A third combined mock `mock-8b17a37dd7f021d9482c96f36bc17ea2d5519c961c3765fe876ed96d97d630cb` reached goal and context effects, then the model called `settings_edit` despite the same-turn restore condition. The edit returned backend-error and again left `current=pending` at journal phase `prepared`. The first manual `space apply` recovery returned `owned storage I/O unavailable`; the immediate retry restored `r-58907eb0e4e07b0e` and removed the journal. This matches the previously isolated optimistic SQLite snapshot-conflict class during rights restoration. Rollback and journal recovery now retry only that class, at most three attempts; a deterministic regression checks the bounded retry and non-retryable refusal. The exact session's compaction journal has zero events, and the matching test goal left by the failed run was cleared and verified absent. The model's combined-prompt instruction compliance remains unresolved.

## 2026-11-11 web and browser transcript follow-up

- The owner-provided 15:02 web transcript matches ToolHub audit outcomes: providers, search, multi-search, fetch and all three cache calls were admitted and completed successfully. A fresh live-model mock also completed those calls, including a non-destructive cache prune. The audit proves call outcomes, not the result counts stated by the model.
- The 15:08 browser task used a site without an IANA link; its failed selector was expected. The model then closed its guest tab before capture, so that original task was incomplete. The 15:14 footer retry did reach capture, but all four PNG calls failed while PDF succeeded. A fresh model mock reproduced two PNG backend errors and a successful PDF.
- A direct Playwright MCP probe on the actual guest sidecar returned a raw `TimeoutError` at the upstream 5-second action budget while waiting for footer stability. A direct viewport screenshot succeeded and created a real 1042×769 PNG. The sidecar now sets a 15-second action budget for both profiles; the focused command-tree regression checks both argv sets. A scoped `hubctl build` and `hubctl up --no-build` recreated only `browser-artem_two`; both running Playwright processes show the new budget.
- Post-recreate live-model mock `mock-518ffecf80e5e0d053e379087b04149a4cd615c402ce523e2dcaf15bb1cec2f5` completed open/read/scroll/capture/close; the first PNG capture was `completed/ok` in ToolHub audit. Its 71,336-byte PNG exists in the managed WSL workspace and visibly contains the footer. The model incorrectly claimed that this file was absent after its separate terminal search; the returned relative path maps to the managed workspace, not the terminal's independent filesystem view. The screenshot tool itself succeeded, while the model's path explanation still needs correction.
- A later focused web mock exposed a separate live mount failure: upstream `web_search` and `web_fetch` succeeded, but `web_providers`, `web_search_multi` and `web_cache` returned `web provider unavailable`. The generated host `hub-web` directory had inode 1601921 with `tools.py`; the running runtime's bind mount held old, empty inode 1602010. `MaterializeHubPlugins` had removed and recreated the mounted directory during a scoped `hubctl up`. It now keeps enabled plugin directory inodes, atomically replaces changed files and prunes stale files; regressions cover stable mounted inode, update/prune and symlink rejection.
- A scoped supervisor build/recreate and managed runtime respawn restored the plugin. Host and runtime both then reported inode 1601921 and the plugin file present. Live-model mock `mock-85db61b608442e412ae59fa6d499be0303221fcfdff25820266b1c0389557662` completed providers, search, fetch, multi-search and all three cache calls. Multi-search returned six results with `parallel` reporting a provider error; a separate direct owner-scoped ToolHub retry of `parallel` succeeded with five results, `partial_failure=false`. A subsequent `hubctl render` preserved inode 1601921 and the live plugin file. Another focused model turn incorrectly claimed the web tools were not projected, while a direct ToolHub inventory found all 23 expected extended names; this is a model discovery/reporting failure, not a missing catalog entry.

## 2026-11-11 site, model, session and memory transcript follow-up

- ToolHub audit confirmed the private site publish/remove, model route switch/restore, and session create/search/read/switch/archive/restore/remove all completed successfully. Three `access=link` publishes returned backend errors because the configured local sish relay `m32-sish-local` was absent; the test domain `hub.local.test` is not an external publishing route. The test memory card exists once and its forget proposal remains unconfirmed.
- The first memory prompt reached no `memory.*` call. Instead, a `browser.open` call ran for about 68 seconds. While it held ToolHub's local agent-tool admission read lock, other requests including `memory-capture` and model rights decisions waited up to about 70 seconds. The model relay's `models.use` check timed out and the upstream run failed with provider `403`/`PermissionDeniedError`. The generic local agent-tool path now releases the admission lock after runtime acquisition and reloads/rechecks binding and rights immediately before dispatch; the concurrency regression verifies a blocked local tool does not block an independent rights decision.
- After the scoped ToolHub rebuild, a mock model job admitted `browser.open` for 77.5 seconds, followed by `browser.act` wait and close. During that open, 60 direct owner-scoped `models.use` decisions all returned HTTP 200/allow; median 8 ms, maximum 1.56 s. The previous 70-second lock convoy did not recur.
- A separate read probe found that `/v1/tool-call` flattened only MCP text content. A structured-only `site_list` response therefore returned HTTP 200 with `is_error=false` and empty `text`. The endpoint now JSON-serializes structured content when there is no text; the HTTP contract regression covers the case.
