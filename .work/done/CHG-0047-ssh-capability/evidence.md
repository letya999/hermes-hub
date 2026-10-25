---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0047 SSH capability (issue #42)

`internal/sshcap` tests run a real in-process `x/crypto/ssh` server with
pubkey auth: pinned host-key verification accepts the pinned key and rejects a
replaced one (dial fails closed), allowlisted exec returns bounded
stdout/stderr/exit codes, `sudo` maps to `sudo -n` only on passwordless hosts,
write commands and remote writes deny without `ssh_write`, SFTP reads/writes
enforce path allowlists and truncation, PTY shells echo through the ring
buffer, and managed tunnels carry TCP data to a real loopback echo target
without publishing a listener. Broker refs are exercised against a signed
fake broker: acquire -> materialize -> release, with release proven on failure
paths. Stack tests assert the read-only `/state/ssh` mount, grant env wiring
and fail-closed render; agenttools tests assert grant-gated registration.

A real OpenSSH server (`lscr.io/linuxserver/openssh-server` in Docker) was
also exercised through the shipped `hubctl tools` stdio MCP surface, not
mocks: `ssh_hosts`/`ssh_exec`/`ssh_read` returned live output, `ssh_write`
landed a real file, `ssh_shell_open` allocated a PTY, `ssh_tunnel_open`
bound a loopback forward, non-allowlisted commands and paths were denied
before dialing, and the owner audit ledger recorded bounded receipts for
both allowed and denied calls.

Not proven: production Credential Broker audiences for the runtime adapter
key (`broker:control` acquire + `broker:runtime` materialize on `ssh-<alias>`
bindings — provisioning concern), a live agent tool call on a real
deployment, and restart behaviour under the supervisor. Issue #42 acceptance
still needs a real host on a real stack; the Docker sshd smoke covers the
tool surface but not the full runtime environment.
