---
id: CHG-0047-ssh-capability
issue: 42
---
# SSH as a standard opt-in Hermes capability (issue #42)

## Decision

SSH is a hub-owned capability on the existing `hub` MCP server (`hubctl tools`),
not a ToolHub workload or upstream connector: the tools process already runs
inside the owning user's runtime as a provisioned Credential Broker runtime
adapter (`HUB_CREDENTIAL_BROKER_RUNTIME_*` + `broker-secrets-runtime` mount).
Host/account/port are bound in `spaces/<user>/connections/ssh/config.yaml`,
mounted read-only at `/state/ssh`; the model only names an alias.

## Features

- `ssh` (host-managed): `ssh_hosts`, `ssh_exec` (allowlisted read commands),
  `ssh_read` (allowlisted paths, bounded bytes).
- `ssh_write`: `ssh_exec` matches `write_commands`, `ssh_write` file upload
  (atomic temp+rename, bounded text). Requires `ssh`.
- `ssh_shell`: `ssh_shell_open/send/read/close`, PTY shell, ring buffer,
  idle + lifetime bounds. Requires `ssh`.
- `ssh_tunnel`: `ssh_tunnel_open/list/close` over pre-approved forwards;
  loopback listener only, nothing published. Requires `ssh`.

## Hosts config (per owner, read-only mount)

Per alias: `host`, `port`, `user`, `host_keys` (pinned public keys or
`sha256:` fingerprints — mandatory, no TOFU), `key_ref` (`file:` under the
config dir or `broker:<grant-id>`), optional `certificate_ref`, `commands`
(read allowlist), `write_commands`, `paths`, `write_paths`, `sudo`
(`never` default / `passwordless`), `timeout`, `max_output`, `max_read`,
`tunnels` (approved remote endpoints). Defaults bound sessions, output,
dial/exec duration, tunnel/shell lifetimes and idle reaping.

## Credentials

`broker:` refs go acquire -> materialize -> parse signer in memory -> release
(same signing key, `broker:control` + `broker:runtime` audiences; grant
matches principal/context/runtime/policy + `ssh` binding). `file:` refs are an
operator fallback confined to the mounted config dir. Keys never enter chat,
env files or the shared image. Revoke = broker revoke or file/host removal;
every dial re-resolves, so revocation takes effect on the next call.

## Bounds and cleanup

Per-call dial (no cached client): reconnect is the next call. Command/path
wildcard allowlists, output truncation, per-command timeout, dial timeout,
global semaphore. Shell/tunnel state dies with the tools process and is
reaped on idle/lifetime/`Tools.Close`. Exec honors MCP cancellation.
Audit ledger `/state/audit/ssh.jsonl` records allowed and denied calls
(alias, effect, outcome, bounded receipt — no key material, no argv beyond
a truncated command).

## Verification

`internal/sshcap`: config validation, matcher, sudo/allowlist/cross-alias
denial, `file:` confinement, broker flow vs signed fake server, exec/sftp/
shell/tunnel over an in-process x/crypto/ssh test server. `internal/stack`:
feature deps, mounts, env wiring, render/doctor validation.
`internal/agenttools`: grant-gated tool registration.
