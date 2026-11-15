# CHG-0234: Parallel session lanes per context

Chain: `docs/adr/ADR-0090-conversation-parallel-lanes.md` D1-D6 ->
`specs/active/SPEC-0089-conversation-parallel-lanes.md` R1-R9 -> this plan ->
`state.yaml`.

## Goal

One user runs several conversations (forum topics, DM tasks) inside one
space at once. Each session keeps strict FIFO within itself; contexts stay
fair against each other; the upstream Hermes concurrency cap is never hit.
No subagents, no new queues — the in-flight key gains the conversation
dimension.

## Read before coding

1. `internal/communication/spool.go` — `inFlight` (76), `rebuildMappings`
   (296-328), claim scan + sort (1019-1160)
2. `internal/communication/job_mapping.go` — `jobContextKey` (245-250),
   `mappingHoldsContext` (234-243), release path (220-225),
   `releaseInFlightLocked` (254-260)
3. `internal/communication/config.go` — `HUB_COMMUNICATION_WORKERS` (427);
   `internal/communication/budget.go` `limitsFromEnv` (108)
4. `internal/supervisor/lease.go` `Acquire` — concurrent job leases on one
   runtime already allowed
5. `internal/runtime/server_observe.go` — `sessionIDFor`,
   `admissionPayload`; `internal/supervisor/execute.go` `beginJob`
6. `docs/adr/ADR-0025`, `specs/active/SPEC-0012` (rule 4), ADR-0090, SPEC-0089
7. Upstream evidence (pin `869228cab`):
   `gateway/platforms/api_server_runs.py` (executor thread per run,
   contextvars, 429 cap), `gateway/wake.py` line 24 ("no per-session lock"),
   `hermes_cli/config_defaults.py` (`max_concurrent_runs` default 10)

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work (tree carries
  unrelated M23/M24 edits).
- Red test first, then fix. Go stdlib only.
- One changeset: code + SPEC-0089 freeze + SPEC-0012 status/index notes +
  docs/operations.md + docs/channels/telegram.md + index rows —
  `devcheck docs` enforces index sync.
- `just check` before handoff. Gateway/supervisor code changed: prefer
  `just docker-pull` + `just docker-check-prebuilt`, plus `dev-rebuild`
  live acceptance on the dev stack.

## Slices

### S1 Lane key gains the conversation (R1, R2, R7)

Anchors: `job_mapping.go` `jobContextKey`; `spool.go` `rebuildMappings`,
claim loop.

Change:
1. `jobContextKey(principal, contextID, conversationID)`; empty
   conversation falls back to the old two-part key.
2. Update call sites: `spool.go:323` (rebuild), `spool.go:1111-1113`
   (busy skip), `spool.go:1152` (claim hold), `job_mapping.go:221`
   (release).
3. Split helpers: keep a two-part `jobFairKey` for the sort/cursor sites
   (`spool.go:1071`, `spool.go:1116`) — fairness per context (R4).

Tests (red first):
- `TestClaimParallelConversationsSameContext`: two pending jobs, different
  `ConversationID`, same context -> both claimed.
- `TestClaimSerialWithinConversation`: second job in the same conversation
  skipped until the first is terminal.
- `TestRebuildInFlightLaneKey`: restart rebuild rebuilds per-conversation
  holders.
- `TestUncertainNoAdmissionReleasesLane`: uncertain mapping without
  run/session fields does not hold the lane (ADR-0025 invariant).
- `TestEmptyConversationFallsBackToContextKey`: job without
  `ConversationID` still serializes per context.

### S2 Per-context admitted cap (R3)

Anchors: `spool.go` claim loop; `config.go` env parsing.

Change:
1. Count held lanes per `(principal, context)` while scanning candidates;
   skip a context at `HUB_CONTEXT_MAX_PARALLEL` (default 8) like a busy
   lane — job stays pending.
2. Doc comment ties the default below upstream `max_concurrent_runs` (10).

Tests (red first):
- `TestContextParallelCapSkips`: cap=2, three conversations -> third stays
  pending until a lane frees; foreign context still claims.

### S3 Worker pool default (R5)

Anchors: `config.go:427`.

Change: default 4 -> 32; comment ties it to per-context cap and stream
cost. Existing env-override tests adjusted.

### S4 Docs, freeze, status flips (R8, devcheck)

- SPEC-0089 `status:` -> frozen on owner acceptance; SPEC-0012 frontmatter
  `status:` notes rule 4 superseded by SPEC-0089/ADR-0090; `specs/index.md`
  SPEC-0012 row gets the same note.
- `docs/operations.md` (job phases/queue section) and
  `docs/channels/telegram.md` (topics concurrency + cap env) updated.
- `ponytail:` comments at the shared-workspace, browser and wedge ceilings
  (D5/R8).

### S5 Live acceptance (A1-A6)

- Mock legs: `/v1/mock/message` into 3 topics of one user -> 3 parallel
  admitted runs; two messages into one topic -> strict FIFO; cap leg.
- Dev stack, real forum group: 3 topics, intersecting run windows,
  per-topic delivery, clean status messages.

## Red-team checklist

- 1000-message burst in one topic: strict FIFO, no interleaved transcript.
- Burst across N topics above the cap: pendings wait, zero 429-failed.
- 12-topic user vs another user: the other context still claims (R4).
- Legacy job with empty `ConversationID`: context-only serialization,
  no panic, no lane leak.
- Uncertain admitted run blocks only its own lane; gateway restart
  rebuilds lanes from mappings.
- Two parallel runs writing `MEMORY.md`: LWW tolerated and documented;
  Hermes-owned files stay consistent (upstream handles its own state).
- Shared browser sidecar under two sessions: contention degrades
  gracefully (documented ceiling, not corruption).

## Acceptance

A1-A6 live procedure: mock-stack legs, then dev stack forum group.
Evidence: trace overlap of `run_id`s, per-topic delivery, `just check`,
coverage.

## Out of scope

- Per-session containers, workspaces or runtimes.
- Subagent / delegated-run lanes (separate feature; lands on the same key
  model later).
- Intra-session concurrent runs (upstream transcript is LWW — forbidden).
- Upstream run-queue bump; durable requeue of 429 failures.
- Browser multiplexing or per-session browser profiles.

## Done checklist

- [ ] Tests red then green; `just check`; docker gates as per rules.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] SPEC-0089 frozen; SPEC-0012 status/index notes; docs updated.
- [ ] Issue updated and closed; state done; folder moved to `.work/done/`.
