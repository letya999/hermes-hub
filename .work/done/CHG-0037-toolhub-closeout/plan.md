# ToolHub closeout

Goal: close the six remaining acceptance areas from the repository-driven ToolHub review without treating mocked tests or green CI as live provider proof.

1. Broker and status: validate every required runtime delivery, prove mounted-credential admission before projection, checkpoint state only after workload stop, and distinguish MCP readiness from provider access. Complete the protected OAuth return path and rotation/revoke/restart checks (#105, #106).
2. Two-owner isolation: run one immutable artifact for two principals with separate Broker credentials, grants, state and projections; exercise cross-owner denial, concurrent calls, rotation and revoke.
3. Registry discovery: search without a prepared match, bind selection to owner/policy/TTL and an exact source/artifact, rank and deduplicate at most five results, and complete one unprepared registry selection through the generic lifecycle (#110).
4. Hermes reconnect: test supported upstream `tools/list_changed` or MCP-only reload against a pinned upgrade; preserve the in-flight turn and PID. Keep #74 open if upstream cannot meet the contract.
5. Provider and bundle scope: verify remaining GitHub/GitLab authorization and account/project lifecycle, audit #37/#126 closure, and complete each exact-source entry required by #115 with contract, runbook, handoff and live smoke (#118, #40, #115).
6. Production: fresh Telegram owner journey, safe provider reads, second-owner isolation, restart/rotation/revoke/remove/rollback evidence and issue closeout (#116).

Gates: `just check` with own Go coverage >=85%; `just security` for dependency changes; `just docker-check` with Docker. Live provider and production checks are separate, opt-in evidence. No secrets, provider payloads or personal data enter tracked files.
