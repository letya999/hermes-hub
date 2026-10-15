# CHG-0096: Control-plane simplification proposals (M19 stage 4)

GitHub: issue [#265](https://github.com/letya999/hermes-hub/issues/265), milestone [M19](https://github.com/letya999/hermes-hub/milestone/20).

Chain: [ADR-0036](../../../docs/adr/ADR-0036-execution-chain-reliability.md) D12
-> [SPEC-0053](../../../specs/active/SPEC-0053-execution-chain-reliability.md) R4
-> this plan -> [state](state.yaml). Evidence: [research](../CHG-0092-chain-stop-the-bleeding/research.md).
Previous: [CHG-0095](../CHG-0095-recovery-observability/plan.md).

## Goal

Decide, with measurements from stages 0-3, whether to (a) move the supervisor into
a Linux container, (b) replace file stores with an embedded single-writer store,
(c) raise the Hermes pin if CHG-0093 deferred it. Each accepted proposal becomes its
own ADR and its own implementation CHG. This change produces decisions, not code.

## Deliverables

For each proposal write `docs/adr/ADR-00NN-<name>.md` with status `proposed`,
containing: problem with measured evidence (SLO table values after stage 3),
design, isolation impact (mounts, sockets, credentials), migration and rollback,
test and chaos plan, and a go/no-go criterion. Do not implement before the owner
accepts the ADR.

### P4a Supervisor in a Linux container

Questions to answer:
- Docker socket access: reuse the Docker socket proxy already mounted in ToolHub
  (`/run/host-services/docker.proxy.sock`); restrict to the API calls the supervisor
  uses (`containers/create|start|stop|inspect|logs`, `networks`), via an allowlisting proxy.
- Replace Docker CLI calls (`Manager.command`) with stdlib `net/http` over the unix
  socket; measure per-call latency before/after.
- Space materialization runs in Linux: paths become container paths; the Windows
  path bug class disappears; verify bind-mount sources still resolve on Docker Desktop.
- Gateway reaches the supervisor on the control network, removing `host.docker.internal`.
- Prior art: NanoClaw host process + per-session containers; OpenClaw gateway daemon.

### P4b Embedded store

Only if stage 2 metrics show spool/supervisor persistence still dominates latency
or correctness incidents remain. Candidate: pure-Go SQLite, one writer per file,
`journal_mode=DELETE` for files crossing VirtioFS/bind mounts (NanoClaw finding),
or keep files. Requires `just security` and an explicit dependency decision.

### P4c Hermes pin

If CHG-0093 S1.5 deferred the raise, decide with the upstream release containing
the MCP breaker fixes; requires the Hermes contract gate (`just docker-check`).

## Acceptance

Each ADR has measured evidence, isolation analysis, rollback, and an owner decision
recorded in state.yaml. Implementation CHGs are created only for accepted ADRs.

## Done checklist

- [ ] ADR drafts for P4a/P4b/P4c (or a recorded "not needed" with metrics).
- [ ] Owner decisions recorded; follow-up milestone/issues created via the milestone playbook.
