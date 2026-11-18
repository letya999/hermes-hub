# CHG-0101: Control-plane tool calls and mcp_tool probes (M12 stage 3)

Chain: `docs/adr/ADR-0041-proactive-automation.md` D4 -> `specs/active/SPEC-0054-proactive-automation.md` R3
-> this plan -> `state.yaml`. Evidence: `../CHG-0099-followup-hardening/research.md`.
Previous stage: `CHG-0100`. Next stage: `CHG-0102`. GitHub issues: #257 (foundation), #248 (connector sources).

## Goal

The communication hub can invoke an owner's read-only ToolHub tool through
one bounded control endpoint — turning any projected connector read into a
deterministic probe source for watchers, checks and (stage 5) event
subscriptions. Credentials never leave the ToolHub workload.

## Read before coding

1. SPEC-0054 R3, ADR-0041 D4.
2. `internal/toolhub/gateway.go` `connectorAuth`, `serveConnectors`,
   `serveCLIJobRelease`, `CallAuthorized`, `filterProjectedTools`;
   `internal/communication/communication.go` `toolHubConnectorsHTTP`,
   `toolHubCredential`, `fetchWatch`; `watchers.go` `probeOne`.

## Slices

### S3.1 `POST /v1/tool-call` endpoint (R3.1)

Anchors: `internal/toolhub/gateway.go` `Handler` mux, `connectorAuth`,
`g.call`.

Change:
1. Mux gains `/v1/tool-call` (POST only, Origin header forbidden, body
   <= 64 KiB, `DisallowUnknownFields`).
2. Request `{tool, arguments}`: `connectorAuth` resolves the envelope;
   `filterProjectedTools` output must contain the projected name; the
   tool's `Effect` must be read — else 403 before invocation.
3. Invoke via `g.call(ctx, auth, name, arguments)` (same audit/deny fences);
   respond `{text, is_error}` — text flattened from content, capped
   64 KiB; `IsError` propagates.

Tests:
- `TestToolCallRejectsWriteEffect`, `TestToolCallRejectsForeignHeader`,
- `TestToolCallReturnsText` (fake read tool), `TestToolCallUnknownTool`.

### S3.2 `probe.kind=mcp_tool` (R3.2)

Anchors: `watchers.go` `probeOne`, `fetchWatch` signature;
`schedules.go` `validateScheduleDraft` probe-kind check;
`communication.go` `toolHubCredential`.

Change:
1. `WatchProbe` gains `Tool string`, `Args map[string]any`,
   `RequeueTool string` (requeue used by stage 5; validated now, <= 128
   chars, `spoolIDPattern`-ish safe charset, args <= 8 KiB JSON).
2. `validateScheduleDraft`: `mcp_tool` requires `tool`, forbids `url`
   scheme rules (url must be empty for mcp_tool); allowed on watcher,
   check, event kinds.
3. The probe fetch becomes `probeFetch(ctx, schedule, principal)`:
   http/github_issue -> existing GET; `task:` -> stage-1 resolver;
   `mcp_tool` -> POST `config.ToolHubURL + /v1/tool-call` with
   `toolHubCredential(principal)` bearer + `X-Hub-Principal` when set,
   15 s timeout, returns text or error (`is_error` = error).
   Missing ToolHubURL/credential = probe error (unknown, not empty).

Tests:
- `TestMCPToolProbeFeedsWatcher` (httptest tool-call stub);
- `TestMCPToolProbeErrorKeepsBaseline` (`is_error` -> failed, baseline
  untouched);
- `TestMCPToolProbeRejectsValidation` (write tool names never reach HTTP).

## Red-team checklist

- `X-Hub-Principal` honored ONLY with the control-plane bearer — an owner
  token + foreign principal header cannot cross (connectorAuth already
  pins; assert in test).
- Arguments are forwarded verbatim to `tools/call`, never templated — no
  injection surface beyond the audited call path.
- A write-effect tool name in `probe.tool` fails closed at the endpoint and
  at create-time validation cannot verify effects (deferred to fire time —
  probe error, not bypass).

## Acceptance

SPEC-0054 A5. Live: bind a read-only connector on the dev stack, create a
`mcp_tool` watcher, change the source, observe one notification.

## Out of scope

- Event extraction semantics (stage 5 consumes `mcp_tool` results).
- Connector workload lifecycle changes.

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] Red-team recorded.
- [ ] Issue #257 commented with the foundation note.
