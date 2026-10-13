# CHG-0093: Tool surface integrity and context hygiene (M19 stage 1)

GitHub: issue [#262](https://github.com/letya999/hermes-hub/issues/262), milestone [M19](https://github.com/letya999/hermes-hub/milestone/20).

Chain: [ADR-0036](../../../docs/adr/ADR-0036-execution-chain-reliability.md) D5, D6, D7, D12c
-> [SPEC-0053](../../../specs/active/SPEC-0053-execution-chain-reliability.md) R1
-> this plan -> [state](state.yaml). Evidence: [research](../CHG-0092-chain-stop-the-bleeding/research.md).
Previous stage: [CHG-0092](../CHG-0092-chain-stop-the-bleeding/plan.md). Next: [CHG-0094](../CHG-0094-chain-latency/plan.md).

## Goal

The model always sees every tool the owner enabled, and when something is missing
the owner is told exactly what and why. Prompts stop carrying unrelated history.

## Read before coding

1. ADR-0031 and SPEC-0041 (default-deny; nothing in this change may widen a grant).
2. ADR-0036 D5-D7, SPEC-0053 R1.
3. CHG-0091 P0 notes in its state.yaml (hub child env work already done; reuse its canary).
4. Anchors per slice; list every caller of `PolicyVersion`/`policyVersion` first.

## Slices

### S1.1 Rights-only policy hash (R1.1)

Anchors: `internal/stack/render.go` `policyVersion` (hashes the whole `Config(s)`),
callers of `PolicyVersion` (render, communication users file, supervisor
`authorizeCurrentPolicy`, toolhub bindings, `cmd/hubctl/capability.go`).

Change: build a `rightsProjection(Settings)` struct containing only rights-bearing
fields: features, `tools:` entries and their modes, organization ceiling/denies,
`mcp:` definitions (command/url/tools include lists, not timeouts), org actions,
mounts and ingress. Hash that. Write a table test listing every `Settings` field and
whether it is rights-bearing; the test must fail when a new field is added to
`Settings` without being classified (reflect over struct fields).

Tests:
- `TestPolicyVersionIgnoresNonRightsFields` (model, model_url, ports, browser_port, style, execution mode).
- `TestPolicyVersionChangesOnRightsFields` (each rights field flips the hash).
- `TestEverySettingsFieldClassified` (reflection guard).

### S1.2 Atomic binding migration on render/up (R1.2)

Anchors: `cmd/hubctl/capability.go` (`--kind agent-tools` re-registers built-in
catalog and stamps a binding), `internal/toolhub/toolhub.go` `ToolBinding`
(`PolicyVersion`, `Status`), store persist/lock (`Store.persist`, `store.json.lock`).

Change: add `toolhub.MigrateBindings(store, principal, context, runtime, from, to)`:
under the existing store lock and stale-writer fence, for each binding with
`Status == active` and `PolicyVersion == from` write a copy with the new version
and a new revision; never touch disabled/revoked; append an audit record per
binding; idempotent when `to` already present. Call it from `hubctl up`/`render`
after computing the new rights hash, only for the selected user/context.

Tests:
- `TestMigrateBindingsMovesOnlyActive`, `TestMigrateBindingsIdempotent`,
  `TestMigrateBindingsConcurrentWriterRetries` (two goroutines + file lock),
  `TestMigrateBindingsAudited`, `TestUpMigratesAfterRightsChange` (hubctl integration with temp spaces).

### S1.3 No silent projection drops (R1.3)

Anchor: `internal/toolhub/projection.go` `ListProjectedTools` (`continue` on
Unauthorized/NotFound/Stale/Revoked/Degraded).

Change: classify the error into a reason enum and log once per
`(principal, binding, projection revision, reason)` with a bounded in-memory set.
Expose the excluded list through the existing ToolHub diagnostics/status surface
(principal-scoped, no other principals' data).

Tests: `TestProjectionLogsExcludedBindingOnce`, `TestProjectionExclusionReasonIsPrincipalScoped`.

### S1.4 Tool-surface manifest and capability readiness (R1.4, R1.5, R1.6)

Anchors: `internal/stack/render.go` (generated files), `internal/runtime/health.go`
`RuntimeHealth`, supervisor `runtimeHealth` strict validator in `internal/supervisor/reconcile.go`,
`/status` command in `internal/communication/task_commands.go` (or wherever `/status` is handled; grep `"/status"`).

Change:
1. Render writes `generated/tool-manifest.<env>.json`: `{schema, context, policy_version, projected:[names], direct:[names]}`.
   Mount it read-only into the runtime next to the effective config.
2. Runtime adds `ToolSurface string` (`ok|degraded|unknown`), `Missing []string` (max 64,
   names only), `ModelStatus` (`ok|model_not_found|unknown`) to `RuntimeHealth`.
   Obtain actual tools from Hermes (check pinned API: `/v1/capabilities` or tool
   listing endpoint; if absent, list via the hub/toolhub MCP sessions the runtime already opens).
3. Hub child self-test at runtime start: reuse the CHG-0091 pinned-loader canary logic
   in-process (spawn via Hermes config loader is already proven); record result.
4. Model check: reuse `cmd/hubctl/model_check.go` logic (move shared code into an
   internal package if needed) against the configured `model_url` `/models`.
5. Supervisor validator accepts the new fields additively; `degraded` tool surface
   or model never triggers remediation (extend `TestDegradedRuntimeDoesNotRemediateUncertainJobs`).
6. `/status` renders: "Недоступно: <tool> — <reason>" lines, model status.

Tests: `TestManifestRendered`, `TestHealthReportsMissingTools`, `TestHealthModelNotFound`,
`TestSupervisorAcceptsExtendedHealth`, `TestToolSurfaceDegradedNeverRestarts`,
`TestStatusShowsMissingTools`, docker-gated `TestHubChildSelfTestInImage`.

### S1.5 Tool-level errors are not transport errors (R1.7, D12c)

Anchors: `internal/agenttools` server handlers; pinned Hermes `tools/mcp_tool.py`
(`_CIRCUIT_BREAKER_THRESHOLD`, `_bump_server_error`).

Change:
1. Ensure hub tools return MCP results with `IsError: true` and a message for
   expected failures (bad path, unsupported media, provider 4xx), and reserve Go
   `error` returns for transport/internal faults. Check go-sdk behavior and add tests.
2. Evaluate a Hermes pin raise containing PR 67340 and, if merged, PR 68455 (find
   the first release tag containing them; verify merge state on GitHub; record commit). Run `just docker-check` including
   `hermes-contract`. If accepted, update `docker/Dockerfile` pin and `docs/integrations.md`.
   If the raise is not acceptable yet, keep the current pin (it has no
   `breaker_threshold`), rely on item 1 and document the residual risk in state.yaml.

Tests: `TestHubToolExpectedFailureIsToolError`, docker contract
`TestHermesBreakerIgnoresToolErrors` (three tool-level errors, fourth call still reaches the server).

### S1.6 Context hygiene (R1.8, D7)

Anchors: `internal/communication/runtime.go` `RunOutcome` (long static `instructions`),
`internal/runtime/server.go` `sessionIDFor`/`sessionRevision` (SOUL hash is part of the session ID),
`config/SOUL.md`.

Change:
1. Move the static paragraphs (artifacts, MEDIA, listing guidance, reminder guidance,
   VOICE convention) into a rendered, versioned prompt section in the effective SOUL
   or Hermes system prompt config; keep only style/voice flags per run.
   Note: SOUL hash participates in `sessionRevision`; changing it starts new sessions
   once. That is acceptable and should be announced in the release notes.
2. Verify Hermes compression settings for API-server sessions; set an explicit
   context budget if the pinned version supports it.
3. Add a measurement script/test that reads `state.db` `sessions` (input_tokens /
   api_call_count) for acceptance S8.

Tests: `TestRunInstructionsArePerRunOnly`, `TestStablePromptRendered`, session isolation from CHG-0092 S0.6 remains green.

## Red-team checklist

- Policy narrowing: after S1.1/S1.2 attempt to gain a tool by editing a non-rights
  field (model, port) and a rights field without operator `up` -> no new binding.
- Revoked binding must never be resurrected by migration (including after crash mid-migration).
- Cross-principal: principal A's `/status` must not reveal principal B's missing tools or bindings.
- Manifest tampering: the runtime cannot write the manifest (read-only mount); a forged
  manifest cannot grant tools (manifest is reporting only, ToolHub authorizes).
- Log injection through tool names/reasons (newline, ANSI) is escaped.
- Breaker: a malicious tool result containing `{"error":...}` cannot disable other servers after the pin raise.

## Acceptance (SPEC-0053 A1.1-A1.6)

Live on dev stack: change `model` in `agent.yaml`, `hubctl up`, verify all manifest tools
visible (A1.1); seed an old-policy binding copy in a scratch space and migrate (A1.2);
remove a required child env var in a scratch space and read `/status` (A1.3); call
`image_inspect` on three invalid paths then a valid one (A1.4); compare session row
counts after routines (A1.5); measure S8 over 30 turns (A1.6).

## Done checklist

- [ ] Tests red then green; `just check`; `just docker-check` (image/runtime code changed).
- [ ] Red-team recorded; live acceptance recorded in state.yaml.
- [ ] docs/architecture.md (policy hash, readiness), docs/operations.md (status meaning), docs/integrations.md (pin) updated.
- [ ] GitHub issue updated; state done; folder moved to `.work/done/`.
