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

## Slices (detailed when CHG-0336 S7 is green)

- S1 move: `internal/runtime` without relays, `internal/agenttools` executor
  side (`call.go`, `boundary.go`, `tools.go`, `toolsexec`),
  `cmd/hubctl/toolsexec.go`, `internal/compaction`, `internal/companion`,
  `internal/systemguide` into the service; old paths deleted.
- S2 session slots: N concurrent sessions (default 5, configurable), session
  table, run identity from the table. Tests: `TestParallelSessionsIsolated`,
  `TestRunIdentityNotTakenFromRequest`.
- S3 config isolation and rights: spec-only config, workspace config ignored,
  narrower rights -> separate runtime, per-call `tool-policy.json` re-read
  deleted (`agenttools/call.go:280-313`). Tests: `TestWorkspaceConfigIgnored`,
  `TestNarrowerRightsNeedSeparateRuntime`.
- S4 env and topology: README env table, unknown `HUB_*` refusal, minimal
  profile answers with native tools, tool preservation for runtime families.
- S5 docs and acceptance.

## Out of scope

Model gateway, egress gateway, run tokens.
