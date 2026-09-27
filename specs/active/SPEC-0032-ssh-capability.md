---
status: active
title: Opt-in user-scoped SSH capability
---
# SSH capability

Scope is GitHub issue #42: SSH as a standard opt-in Hermes capability while
keys, destinations, commands, files, tunnels and effects stay user-scoped.
Broad log access is tracked separately and SSH is not a generic logs API.

The capability is four stacked features: `ssh` (allowlisted read commands and
remote file reads), `ssh_write` (allowlisted write commands and bounded remote
file writes), `ssh_shell` (bounded interactive PTY sessions) and `ssh_tunnel`
(managed loopback forwards). Each privileged feature requires `ssh` and is
validated by settings dependencies. The tools are hub MCP tools, never embedded
upstream servers; host, account, port, host keys, credential references,
allowlists and bounds come only from `spaces/<user>/connections/ssh/config.yaml`,
mounted read-only at `/state/ssh` inside the runtime. The model selects a host
alias; it cannot supply hostnames, ports, users or keys.

Every dial authenticates against pinned host keys (authorized-key lines or
SHA256 fingerprints). Unknown aliases, unpinned keys and malformed configs fail
closed; render refuses a space that enables `ssh` without a valid config.
`key_ref`/`certificate_ref` resolve either `file:` below the read-only config
directory or `broker:` grant IDs materialized through Credential Broker with
the runtime adapter identity (`ssh-<alias>` binding, short-lived lease,
release after materialization). Plaintext keys never touch disk outside the
config dir, env, chat or the audit ledger.

Commands match an anchored `*` allowlist after a single-line/4 KiB bound; sudo
is `never` by default and `passwordless` maps to `sudo -n` on the effective
command only. Output is capped per host (default 64 KiB), command duration is
bounded by per-host timeout and context cancellation, and one semaphore bounds
global concurrency. File reads/writes use SFTP against path allowlists; writes
are atomic temp+rename, UTF-8 and <=2 MiB. Shells run a fixed 256 KiB ring
buffer with offset reads, idle and absolute lifetime expiry. Tunnels are named
endpoints from config only; listeners bind to 127.0.0.1 and never publish.

Every operation appends an `ssh` audit event (kind/alias/outcome/bounded
receipt without command output or secrets) to the owner ledger. Shell and
tunnel state lives only in the runtime process; runtime restart closes them and
the reaper sweeps expiry. Cross-user isolation is fail-closed: each runtime
mounts only its own `connections/ssh`.

Non-goals: unrestricted production shell, host-wide SSH configuration, and log
shipping. Revoking a Broker grant or removing `connections/ssh` entries
invalidates the capability at the next dial; nothing is cached.
