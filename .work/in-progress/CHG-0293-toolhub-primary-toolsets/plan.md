# CHG-0293: Primary ToolHub toolsets

## Goal

New spaces and the two Artem spaces select `web_search`, `sessions`, `memory`,
and `media` through ToolHub. The web and session implementations are exercised
through the live artem_two owner binding, while native alternatives remain
available for future explicit selection.

## Evidence and scope

- The embedded default selected only `files`. artem_main omitted web and
  sessions and both authored passports selected legacy native flags.
- Catalog 23 already contained five web, seven session, four memory, and seven
  media tools. The handlers existed, but the local STT worker discarded segment
  timing and the web relay returned a failed provider result with nil Go error.
- Configured channel voice wired gateway URLs while Compose service generation
  still depended on the separate transcription flag.
- After publication, the artem_two binding still pinned definition 18.3.2 and
  the capability ceiling contained only older implementation digests. The
  profile alone therefore exposed remote tools but hid web and local media.

## Work

1. Update embedded default, both authored tools passports, and artem_main
   web/session grants. Keep storage configuration under `config.memory.enabled`.
2. Generate speech containers and auth from channel voice configuration,
   preserve local STT segment times/language, and make total web failure an
   MCP error. Add focused regressions and a family/member catalog parity check.
3. Bump the immutable agent-tools definition and platform catalog together;
   update prompt guidance, docs/tools.md, docs/routines.md, and affected service
   and space docs.
4. Run focused tests, vet, `just check`, affected package suites, then publish
   both authored passports, rebuild affected dev services/images, rebind, and
   respawn the managed artem_two runtime. Verify the actual projected tools and
   one owner's live outcomes; artem_main needs static compile and grants checks.
5. For an immutable catalog move, explicitly enable the reviewed definition
   for each selected principal and extend its policy ceiling. The operator
   catalog path now accepts remote tools with no capability-use path scope.

## Live acceptance corrections (2026-11-10)

The owner's Telegram trials exposed several contract errors beyond primary
toolset selection. Fix site slot discovery and terminal refusal before rate
limiting; align context status with preview's active session and project the
preview method without a false after-count; state the next-turn session
boundary; clarify recurring reminders and watch sample ordering without
restricting assignments. Persist a safe authorization category when an event
watch blocks. A failed site publication replay must remain an error instead of
returning `state=failed` as apparent success. Bump the immutable catalog,
rebind/respawn managed runtimes, and
verify the changed calls through the live artem_two ToolHub endpoint. Keep the
existing blocked watch paused until its owner binding is repaired.

## Live acceptance, 2026-11-10

- Both authored passports were applied in dev and the new ToolHub definition
  enabled for their owners. `artem_two` projects all 23 primary web, sessions,
  memory and media tool names through ToolHub. Web providers/search/fetch,
  multi-search and cache status returned results. Local STT returned a timed
  segment from a real WAV file.
- A disposable session completed create/list/search/read/switch/archive/
  restore/remove through ToolHub, with `effective_from=next_turn` and the
  original current pointer restored. Context preview returned a proposed
  method and null post-preview count while leaving status unchanged.
- `site_list` reported the allocated `m32test` slot; an unallocated name
  returned the exact slot refusal. A private static publish succeeded, but
  link publication failed because the configured `m32-sish-local` relay does
  not resolve inside the hosting network. A public URL cannot be claimed.
  Replaying that failed request incorrectly returned a success-shaped result;
  the new regression and catalog 26 close that error path.
- Direct `memory_search` without an exact current owner run was denied by its
  intended admission gate. This is not a positive memory execution test.
- Catalog 26 and agent-tools 18.3.5 were applied and enabled for both owners;
  the active profile/passport revisions match. The running artem_two context
  respawned onto its dev image; artem_main had no running context to respawn.
  Live replay of the failed link publish returned an MCP error, and the
  temporary private test publication was removed (slot again empty).
- The prior `watch-germi-live-check` is an event watch in `blocked` state.
  Its saved envelope policy differs from the current owner policy, so live
  `watch_sample` returns HTTP 403 at channel authorization before `file_read`.
  It was not resumed or rewritten during acceptance.
- Allowlisted ToolHub audit aggregates over the last three hours for artem_two:
  sessions 41 completed OK, web_search 21 OK and one backend error, media five
  OK and four backend errors, sites seven OK and three backend errors, watches
  four OK and three backend errors, context four OK. These are call outcomes,
  not proof of Telegram delivery or positive memory access.

## Local acceptance, 2026-11-09

- Applied both authored passports in dev, respawned artem_two with the current
  policy, registered and enabled hub-agent-tools 18.3.3, and extended the
  matching ceiling for both principals. artem_two's runtime ToolHub search now
  shows all 23 expected web/session/memory/media names; no native web or
  session-search surface is selected.
- artem_two live calls: web providers, two-result search and multi-search,
  220-byte fetched page, cache status/prune; disposable session search/read,
  switch, archive/restore/remove. The pointer was returned to the most
  recently active pre-existing task; its exact prior value was not captured
  before test creation. PNG inspect,
  PNG-to-JPEG conversion; local TTS HTTP response containing WAV audio; WAV
  transcription through ToolHub with two measured timestamp segments.
- Memory calls without an exact verified live run were rejected by the run
  gate. No personal memory was read or changed. `media_fetch` refused an
  unknown job; image generation/edit and Telegram voice delivery remain
  unverified because the provider route and external delivery were not
  authorized for this smoke.

## Acceptance boundaries

- A returned tool result, a successful MCP status, and actual provider or
  delivery success are distinct evidence. Failed image provider authorization
  remains an explicit external blocker if no working configured route exists.
- Telegram delivery requires a settled outbox receipt. An enqueue result alone
  is not delivery evidence. Session mutation uses a disposable owned test task
  and restores the original active pointer.
- No existing native implementation is globally banned. No credentials,
  transcripts, or personal memory content are recorded in this change.

## 2026-11-12 focused recheck

- From the existing `artem_two` managed runtime, direct MCP initialization and
  owner-scoped reads succeeded for platform, connector status, sites, watches,
  models and settings. The GitHub onboarding is still `awaiting_confirm`; this
  is not provider-read acceptance.
- `research_list` returned a tool error. `research_create` returned exactly
  `research_create is unavailable: the deep-research flow backend is not enabled`.
  This is an advertised but unimplemented async backend, not a transient test
  failure. The bundled research skill also used obsolete managed tool names;
  its guidance now selects `web_fetch`, `routine_edit action=pause` and
  `browser_open profile=guest` in managed spaces, with native Hermes names
  retained. Named lint/regression, `go vet`, `just check` and the affected
  package test passed. The skill edit has not been deployed into a managed
  runtime during this recheck.
- `media_generate` returned `media service HTTP 502`. The current image model
  is `gemini-3.1-flash-image` through CLIProxy; its recent service log had two
  HTTP 403 entries for the image route. No image bytes or derived edit exist.
- Web providers reported 10 catalog entries, while `web_providers` took 51 s;
  `web_cache status` took 4 s. A later `web_search` returned a success-shaped
  payload after 41 s and `web_fetch` returned one result after 6 s. The helper
  did not record the search hit count or fetch content, so those remain
  unaccepted. During `web_search_multi`, Docker Desktop's Linux Engine started
  returning HTTP 500; Ubuntu's Docker Unix socket `_ping` timed out, and its
  mounted CLI failed with I/O error. The request was interrupted. Timing alone
  does not establish that web_search_multi caused the engine outage.
- The Docker outage prevents reliable live tests of link publication, browser
  cleanup, Telegram delivery and owner-bound state. Other projects have live
  containers on this engine, so no engine restart was performed without owner
  approval. No credential values, personal messages or raw service logs were
  saved in the tracked record.

## 2026-11-12 Antigravity image route

- A direct owner-scoped `gemini-3.1-flash-image` provider probe returned HTTP
  200 and decoded image bytes; the shared infra provider still returned 403.
  The `artem_two` passport now routes the logical `image` model through its
  own `cliproxy-google` and private `hub-media` to its own model relay. No
  provider credential was copied into the runtime or media sidecar.
- Publication `r-d91831ed6d8e5530` completed with no remaining apply journal.
  A 403 during recovery exposed the journal's 0600 mode and the supervisor's
  use of `LoadEffective` while `current=pending`; group-readable 0640 journal
  mode and journal-pinned revision loading now have regression coverage.
- The first ToolHub call reached `hub-media` but returned 404 because the
  running owner egress relay still held the old host allowlist. Recreating
  only that relay loaded the new private hostname. The planner now includes
  this recreate and a runtime respawn when media service mode changes.
- A live ToolHub `media_generate` returned
  `artifacts/images/red-tram-live-check.jpg`. The file is JPEG 1024×1024,
  1,034,276 bytes, SHA-256
  `18ca4f726ea3a01c145086a4f9316f25b43b7391153c179a004e59e09ba61888`.
  ToolHub `media_inspect` independently confirmed the same dimensions, format
  and byte count. The test covered generation and inspection, not editing or
  async video fetch.

## 2026-11-12 research backend and live acceptance

- The advertised `flow:deep_research` tools had no executor. Added durable
  owner-scoped research records, asynchronous Supervisor execution with a
  stable job/idempotency ID, restart replay, and grant/projection checks before
  each run. The Supervisor submission reads the current operational epoch;
  an active administrative hold blocks submission.
- Published immutable catalog Release 35 (`hub-agent-tools@18.3.17`), rebound
  both managed owners and rendered their prompt contracts. Rebuilt and
  recreated only ToolHub in the live dev stack.
- Owner-scoped MCP calls created `research-513c29348f1397fd429598ab` with
  `status=queued`. `research_read` reached `status=completed` and returned a
  5,356-byte report with source and limitations sections. It cited primary
  Go documentation at `go.dev/doc/tutorial/add-a-test`,
  `go.dev/doc/security/fuzz/`, `pkg.go.dev/testing`, and
  `go.dev/doc/tutorial/fuzz`. ToolHub audit recorded successful live
  `web_search` and `web_fetch` calls during the model job.
- A separate live `research_list` call returned the same ID as completed and
  omitted its report body. The completed report is evidence for the research
  lifecycle; it does not establish that the model invoked the bundled skill.
- Local `just check`, `just test ./internal/toolhub`, named Supervisor/rights
  regressions and `go vet -p=2 ./internal/toolhub` passed. Remote CI and
  publication were not run for this dirty local tree.
- A Windows-side `dev-rebuild` briefly recreated ToolHub with Windows state
  mounts, hiding the earlier WSL research record. The container was recreated
  with the original WSL Compose manifest and the already built dev image.
  The unrelated user relays affected by an intermediate Compose command were
  restored and became healthy. Final ToolHub has zero Windows checkout mounts;
  `research_list` again sees the completed ID and `research_read` still returns
  the 5,356-byte report after restart. Use the WSL Compose manifest for this
  deployment; the Windows checkout is the source tree, not its state mount.
