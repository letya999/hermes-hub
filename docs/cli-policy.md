---
description: CLI catalog operations, isolated terminal and opt-in package registries.
last_verified: 2026-10-10
---
# CLI policy and operator catalog

`tools.terminal: toolhub` selects the existing `code_exec` scratch executor.
It removes native `terminal` and native `code_execution` on every channel,
including Telegram and unmanaged supervisor spaces. Scratch execution has
network none, a read-only root and no Hermes home or control state. It may
invoke binaries already in its image. Explicit `terminal: native` still grants
Hermes its native shell; do not select it when internet installation must be
blocked. `terminal: toolhub` with `code_execution: native` is rejected.

CLI cells mount only the helper, their declared workspace, scratch tmpfs and
optional pinned toolbox layers. `/state/home` is never mounted. The normal
home is `/tmp`, which is noexec and whose drift the canary enforces. Package
installation requires both operator registry configuration and an immutable
definition with `install_packages: true`. That grants a cell-private
executable tmpfs `/cellhome` (sized from the memory ceiling, 256 MiB..1 GiB,
non-root, mode 0700) plus a `/usertmp` tmpfs (256 MiB) wired as `TMPDIR` so
package managers do not dirty `/tmp`, with `.local/bin`, `.cargo/bin` and
`go/bin` on PATH. Binding lifecycle retains installations until the cell
expires or is released; ephemeral lifecycle discards them after each call.
Stateless pools and toolbox sharing cannot request this home. It does not
install missing interpreters into a release image: the selected image must
contain the package manager and its required runtime.

## Registry permission

Set `cli_registries: [pypi, npm]` in operator-owned `agent.yaml` (or legacy
`settings.yaml`) and re-render/restart the workload controller. Supported
values are `pypi`, `npm`, `cargo`, `go`; unknown names and duplicates fail
validation. Workspace wishes and MCP arguments cannot change this ceiling.
Each definition must still list its own exact `egress` hosts; selecting a
registry does not grant network to other CLI definitions or to the terminal.

| Registry | Allowed hosts |
|---|---|
| PyPI | pypi.org, files.pythonhosted.org |
| npm | registry.npmjs.org |
| Cargo | index.crates.io, static.crates.io, crates.io |
| Go | proxy.golang.org, sum.golang.org, storage.googleapis.com |

Registry contracts: [PyPI index API](https://docs.pypi.org/api/index-api/),
[npm registry](https://docs.npmjs.com/using-npm/registry.html),
[Cargo registry index](https://doc.rust-lang.org/cargo/reference/registry-index.html),
[Go module protocol](https://go.dev/ref/mod).
Git dependencies, alternate registries and Python runtime downloads need
separate reviewed egress; a denied host must not be replaced with `*`.

## Runtime tier and per-principal uid

`cli_runtime` selects the OCI runtime for bounded-cli cells: `runc`
(default), `runsc` or `kata`. The Docker daemon must list the runtime; an
unavailable one fails closed at admission — a cell never silently falls back
to a weaker tier. `cli_per_principal_uid: true` maps each principal to a
stable uid in the 20000+ range so files a read-write cell leaves in a
workspace carry a per-principal owner instead of the shared cli uid. Both
are operator-owned `agent.yaml` settings; self-service overlays reject them.

## Lifecycle ladder and pool bounds

`cli.lifecycle` selects the cell tier: `ephemeral` (default) | `task` |
`binding` | `toolbox` | `shared-pool`. `task` keys the cell by the call's
job id and dies at job end — the communication hub posts
`POST /v1/cli-jobs/release {"job_id": ...}` to ToolHub on every terminal
outcome, which translates it to the controller `/cli-release`; callers may
only release their own principal's jobs. `binding` keys by
principal+binding, `toolbox` by principal+toolset digest (member images
mount read-only at `/tools/<i>`, configured via `cli_toolboxes`), and
`shared-pool` admits `stateless: true` workspace-free definitions only.
Toolbox cells always run the configured tools image as their base — the
member artifact supplies only its files under `/tools/<i>`, and an ELF
interpreter resolves against the cell rootfs, so member images may be
minimal even when the tool binary is dynamically linked. Warm-reuse keys
include the resolved cell image ID, so a rebuilt artifact or tools image
never lands a call on a stale cell.
Disable/revoke of a binding also releases its warm cells through the same
channel. The pool cap (`pool_size`, default 2) bounds live cells — idle
plus claimed — so a burst can never exceed it; excess callers wait up to
`pool_claim_wait_seconds` (default 30, max 300) and then fail closed.
`POST /cli-metrics` on the controller surfaces creates, reuses, canary
failures, rotations, pool hits/misses/claims/exhaustions and claim wait
time (the handler is POST-authenticated like the other control routes).

## Onboarding and catalog binding

`prepare_source` exposes a structured `cli` object. For a release use
`cli.source: github-release:owner/repo@tag`, `cli.asset` and an absolute
`cli.binary`; fixed argv goes in `cli.args`, typed inputs in
`cli.tools[].arguments`. A release URL in top-level `source` produces an
actionable error. This reduces malformed requests; it cannot guarantee a
model chooses the correct upstream asset.

Registry packages install as `cli.source: pypi:name@version` or
`npm:name@version` (`uvx:`/`npx:` are accepted aliases). `@latest` resolves
against the registry at import and is recorded as the resolved exact
version; ranges (`^`, `>=`, `*`) are rejected — the pin must be exact. The
build runs `pip install --no-cache-dir` / `npm install --global --omit=dev`
inside the egress-allowlisted restricted builder on the digest-pinned
python/node toolchain images and records registry+name+version in the
definition's source provenance next to the image manifest digest. The
toolchain image must provide the binary's runtime; the package's own
dependencies ride along in the built layer.

Only exact compiled CLI catalog definition bytes are marked hub-owned and
reviewed. An arbitrary definition with the same id or bounded-cli transport
remains external. Retire any old blanket `transport=bounded-cli` allow rule
after migrating its intended user definitions to explicit scoped approvals.
Use `hubctl governance --kind rule` with the same scope/match/effect,
`--status disabled`, the next `--revision`, an audit `--reason` and `--confirm`.

On the trusted host, register the shipped definitions and enable one exact
version for a principal:

```powershell
hubctl connector catalog-cli --toolhub-store C:/private/store.json
hubctl connector catalog-enable --toolhub-store C:/private/store.json --definition cli-rg-search --version 1.0.2 --principal alice --context alice --runtime alice --policy-version policy-1
```

For managed principals add `--profile <profile-id> --env dev --generation 1`.
The operation atomically creates the binding and adds exact implementation
selections and profile allows. It refuses authority outside the policy
ceiling unless the trusted operator adds `--extend-ceiling`. A shared policy
extension re-pins sibling profiles without granting them the new tool.
Existing deny rules remain in force. Credential-bearing catalog definitions
use the existing protected credential onboarding instead. Disabled, revoked
or stale bindings require explicit lifecycle handling; enabling cannot reset
them. Concurrent store writers fail on a stale disk revision without leaving
partial bindings or permissions. This command is not exposed over model MCP.
