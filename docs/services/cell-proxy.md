---
description: cellinit + cell cred-proxy + egress-relay — the sandboxed cell pid1, brokered-header forward proxy and managed-network egress path.
last_verified: 2026-10-20
---

# cell proxy (cellinit, cred-proxy, egress-relay)

Cells are disposable tool workloads created by toolhub under
workload-controller admission. Three small pieces cover their process and
network boundary.

## cellinit — cell pid1 and canary agent

`cmd/cellinit`: one static binary bind-mounted to `/cellinit` in every cell.
Keeping the contract in a tiny binary means cells need no shell and scratch
or distroless tool images work unchanged. Subcommands:

- `pause` — pid1: reap orphans, block until SIGTERM/SIGINT.
- `write <path> [content]` — stdin/argv → file (0600); canary seeding.
- `check <path> <token>` — exit 0 iff file content equals token.
- `clean <dir> <name> <token>` — exit 0 iff dir holds only `<name>` == token.
- `procs` — exit 0 iff no stray processes exist; lists them.
- `sweep` — SIGKILL every process except pid1 and self.
- `cred-proxy` — HTTP forward proxy injecting brokered headers.

## cred-proxy — brokered credentials without exposure

`cellinit cred-proxy` is the forward proxy inside a cell that injects
broker-materialized auth headers on egress. Tool code sees a plain HTTP
proxy; the credential value never enters cell env, argv or fs — consistent
with ADR-0049 (broker-only intake) and the network confinement in
`internal/netguard`.

## egress-relay — managed agent network egress

Managed-mode spaces put runtimes on a per-space agent network with **no
default route**. `hub-runtime egress-relay` (compose `egress-relay` service,
alias `egress-relay` on the agent net) is the reviewed allowlisted forward
proxy: `HUB_EGRESS_RELAY_HOSTS` is the complete host allowlist built by
`managedEgressHosts`; web/sibling reachability for the runtime goes through
it and nowhere else.

Siblings on the same pattern: `toolhub-relay` (agent-net alias `toolhub`
bridging to the real ToolHub) and `model-relay` (alias `model-relay-<user>`;
see services/model-relay.md).

## Boundary summary

- Cells: created only via controller admission, seeded/verified by cellinit
  canaries, swept on teardown.
- Credentials: injected at the proxy, never stored in the cell.
- Egress: allowlist-only in managed mode; unmanaged spaces keep the default
  network posture.
