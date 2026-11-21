# CHG-0337: Isolate harness-runtime (M40.4)

Issue: #602. Milestone: M40 - v1 Service Split. Rules: docs/engineering.md.
Previous stage: CHG-0336 (M40.3, #601) - blocked until its S7 lands, because
the runtime gets its spec and policy slice from spawn-controller.

## Goal

`services/harness-runtime`: Hermes in an isolated container (or process when
trusted) with parallel session slots, only its own workspace, workspace tools
via tools-daemon, and no policy re-evaluation.

## Decisions

1. Hermes-specific code in one package `services/harness-runtime/internal/hermes`;
   no generic harness abstraction (S1). Other code under
   `internal/{session,tools,api}` only if a second file set needs it.
2. Model relay and egress relay (`internal/runtime/model_relay*.go`,
   `egress_relay.go`) do not move; they stay in v0 until M40.12/M40.13.
3. Run identity comes only from the runtime's own session table; request body
   and Hermes-set headers are ignored for identity.
4. One runtime = one (space, agent, policy digest). A narrower-rights subagent
   asks spawn-controller (contracts/spawn/v1) for a separate runtime; Hermes
   native subagents are disabled when they would inherit wider rights.
5. Config comes only from the rendered spec delivered by spawn-controller;
   workspace project config and workspace MCP configs are never loaded.
6. Env is exactly the README table; an unknown `HUB_*` variable fails startup
   with a localized message (V9).

## Slices (detailed 2026-11-21, CHG-0336 green)

Recon facts that shaped the cut:

- `internal/runtime` is one package: ~118 external files import it, but the
  whole external surface is ~30 symbols - the /v1 wire DTOs
  (`ExecuteRequest/Response`, `RunControl`, `RunReference`, `RunAuthorization`,
  `SessionUsage`, `RuntimeStatus`, `RuntimeHealth`, `GoalRequest/Result`,
  `CompactRequest`, `UsageRequest`, `ArtifactRef/Request`, `ScriptRequest`,
  `SelfEnvRequest`, `SessionReadRequest`, `SkillApplyRequest`,
  `SkillCredsRequest`), header/content constants (`RunDeliveryHeader`,
  `RunAuthorizationPath`, `RunStreamContentType`) and the stream/budget
  helpers (`ReadRunStream`, `IdleReader`, `StreamIdleDeadline`, `RunBudget`,
  `ValidSessionReadRequest`, `ModelRelayHandlerFromEnv`).
- Relay-only files: `model_relay*`, `model_run_proxy`, `model_meter`,
  `egress_relay`, `relay_run_authority`, `toolhub_relay`, `control_relay`,
  `cliproxy_materialize`, `toolhub_config`. Everything else is workload.
- `companion` (exposure, hubctl) and `systemguide` (stack, toolhub) are not
  workload code - nothing in the moving set imports them; they stay (plan
  deviation recorded).
- `internal/compaction` stays too: `hubruntime.SessionUsage` embeds
  `*compaction.Event` (wire DTO read by communication) and a cmd/toolhub
  integration test reads the journal via `compaction.Open` - a move would
  duplicate wire types at both seams. Its real home is the future
  `contracts/runtime/v1` (plan deviation recorded).
- `internal/agenttools` splits on the catalog seam: `capability.go` +
  `arg_docs.go` (`HubToolsDefinition`, T1 snapshot source) move to
  `internal/catalog`; the executor (all other files) moves wholesale.
  `agenttools.HubToolsDefinition*` call sites (~8) switch to `catalog.`.
- `internal/toolhub/api` docker-execs `hubctl tools-daemon|tools-exec|exec-pack`
  inside the runtime container (agent.go x2, scratch.go, terminal.go); the
  subcommands move to `harness-runtime` modes and argv changes with them.

Slices:

- S1b split `internal/runtime`: contract surface stays as `contract.go` +
  `idle.go` + relay files + `relays.go` (Run dispatch for relay modes only);
  every other file moves to `services/harness-runtime/internal/runtime`
  (`package runtime`) plus `alias.go` re-exporting the contract surface
  (`type X = hubruntime.X`, const/var bridges) so moved code and ~118
  callers compile unchanged. `contracts/runtime/v1` is deliberately not
  created - the issue does not name it and the alias file records the seam.
- S1c move `internal/agenttools` executor wholesale ->
  `services/harness-runtime/internal/agenttools`; `capability.go` +
  `arg_docs.go` + catalog-only tests -> `internal/catalog`.
- S1d `tools`, `tools-exec`, `tools-daemon`, `exec-pack` leave `cmd/hubctl`
  for `harness-runtime` argv modes; `internal/toolhub/api` docker-exec argv
  switches to `harness-runtime`.
- S1e wiring: Dockerfile builds `/harness-runtime`, runtime image entrypoint
  `harness-runtime` (`Command ["serve"]` spec unchanged), compose
  healthcheck `harness-runtime health`, `hub-runtime` keeps relay modes
  only; workload argv modes deleted from `internal/runtime`.
- S2 session slots: `HUB_SESSION_SLOTS` (default 5); per-session FIFO +
  N-wide admission gate in `runtimeHTTP`; session table binds
  (session,run,job) at admission only; `/v1/observe` and `/v1/control`
  resolve only table hits. Tests: `TestParallelSessionsIsolated`,
  `TestRunIdentityNotTakenFromRequest`.
- S3 config isolation and rights: spec-only config, workspace config ignored,
  narrower rights -> separate runtime (claim key already carries
  IdentityRevision - verified, covered by test), per-call
  `tool-policy.json` re-read deleted (`agenttools/call.go` toolPolicyDeny).
  Tests: `TestWorkspaceConfigIgnored`, `TestNarrowerRightsNeedSeparateRuntime`.
- S4 env and topology: README env table, unknown `HUB_*` refusal, minimal
  profile answers with native tools, tool preservation for runtime families.
- S5 docs and acceptance.

## Out of scope

Model gateway, egress gateway, run tokens, `contracts/runtime/v1`
(DTOs stay in `internal/runtime` behind the alias seam; the contract
package is a later-milestone move), `internal/companion`,
`internal/systemguide`, `internal/compaction` (shared by v0 owners or
embedded in the contract surface, not workload-internal code).
