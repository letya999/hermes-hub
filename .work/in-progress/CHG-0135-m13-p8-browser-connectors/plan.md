# CHG-0135 — M13 P8: browser workloads as connectors

Issue: https://github.com/letya999/hermes-hub/issues/289
Branch: feat/m14-toolhub-connector-platform
Depends on: CHG-0128, CHG-0130, CHG-0131, CHG-0132, CHG-0133

## Scope (contract-testable slices)

### S8.1 Egress class `web`
- `ExecutionPolicy.EgressClass = "web"` — arbitrary outbound HTTPS through
  the per-workload proxy instead of a ≤32 host allowlist; valid only for
  container-mcp workloads.
- `WebProxyConfig` renders a deny-first Squid ACL: built-in deny of
  private/loopback/link-local/CGNAT ranges, cloud metadata
  (169.254.169.254), and hub-internal hosts; deny entries evaluate before
  the allow-all tail; `dst` ACLs resolve per request so a rebound name is
  still denied.
- With class `web` the definition's `Egress` list carries reviewed extra
  deny entries, never allow entries.

### S8.2 Per-connection egress refinement
- `Connection.Metadata` keys `egress_allow` / `egress_deny` carry
  comma-separated host:port or CIDR refinements (bounded, validated);
  deny always wins over allow; renders into the web proxy config at spawn.

### S8.3 Owner-bound viewer link
- `Store.MintViewerLink(auth, connectionID)` arms a one-time, short-TTL
  nonce on the owner-scoped connection; a second mint replaces the nonce.
- `GET /connect/<connection>?t=<nonce>` serves the viewer only to the live
  nonce inside the TTL; a spent or expired nonce answers 410 gone, a wrong
  nonce 403 — the link is never a published port without authentication.

### S8.4 T0 browser recipes
- `agent-browser` and `playwright-mcp` prepared entries: tier t0,
  digest-pinned CI image, `stateful` browser profile on `state_target`
  (connection state volume), read-profile toolset projected by default,
  interaction tools gated behind `connector_allow_writes`, `eval` behind
  its own toolset, egress_class `web`, optional `file` storage-state
  credential through the reviewed `browser-session` Broker contract.

### S8.5 Budgets
- `WorkloadPolicy.ShmMiB` adds `--shm-size` to the workload run when set —
  Chromium needs shared memory beyond the default 64 MiB; bounded at
  validation. CPU/memory/PID/timeout/output caps come from the existing
  execution policy; profile volume and downloads ride the existing
  finalizer ladder.

### S8.6 Docs + closeout
- `docs/prepared-connectors.md` browser section; ADR "Browser workloads as
  connectors" appended; SPEC-0023 delta; state.yaml.

## Out of scope (live gates)
- Two real principals with separate browser workloads on one deployed
  stack (live two-user acceptance).
- noVNC/CDP viewer payload rendering — the contract covers the link
  lifecycle only.
- Live site login through the viewer.

## Done checklist
- [ ] Tests red then green per slice; `just check` green.
- [ ] ADR appended; docs updated.
- [ ] Live acceptance deferred honestly.
