---
description: Accepted separation of control state, Hermes state, scoped file execution and arbitrary code workloads.
last_verified: 2026-10-02
---
# ADR-0032: Separate capability authorization from execution privileges

Status: accepted, 2026-10-02. Depends on ADR-0031; owner instructed implementation
according to the ADRs, specifications and plan. Execution isolation remains to be proven.

## Context

The current infra-owning runtime shares a writable state root with control services.
Native terminal runs locally inside Hermes. A read-only config file and dropped
Linux capabilities do not isolate an agent from files readable by that same user,
nor from writable extension directories. A ToolHub authorization check cannot
restrict code that reaches the underlying files or services directly.

## Decision

Separate four trust domains, using existing Go services and container supervision:

| Domain | May access | Must not access |
|---|---|---|
| Control services | Own grant/binding/audit state; controller scheduling; broker-specific keys | Unnecessary user workspace trees or unrelated services' keys |
| Trusted Hermes harness | Its private session state, selected model route, scoped ToolHub identity, immutable approved configuration/extensions | Control stores, operator identity, broker signing/admin keys, Docker socket, other users' state |
| Scoped file/media executor | The exact authorized roots and bounded operation inputs | Parent `spaces`, control stores, session/plugin/config trees, unrelated roots |
| Arbitrary code/terminal workload | Disposable scratch and approved input copies; explicitly granted network destinations | Hermes home, runtime/model/broker credentials, host sockets, sibling workspaces, control network |

1. One agent-facing ToolHub endpoint authorizes and dispatches, but file access and
   arbitrary code do not execute with the control service's filesystem authority.
   Reuse `hubctl tools`/existing handlers as scoped executors rather than mounting
   every workspace into the central gateway. Authenticate the private executor
   channel and bind it to an exact user/context/environment and operation.
2. Split writable volumes and credentials by service role, including the
   infra-owning user. Read-only mounts do not provide confidentiality. The network
   permits only required directed connections; another runtime, controller, broker
   administration and Docker host are not reachable merely because a tool can use
   the network. Do not rely on a shared bridge network as an isolation control.
3. Protect all executable extension sources, not only `config.yaml`: plugin/hook
   directories, skills manifests/scripts, provider/context-engine modules, startup
   files and config-driven command hooks. Writable data cannot become imported
   code after restart. Installation publishes immutable reviewed revisions through
   the human control path. Credentials do not implicitly enable tools or providers.
4. Use the existing `os.Root` approach for managed file operations, with trusted
   root selection and separate read/create/edit/delete checks. Apply those checks
   to document conversion, image output and artifact operations too. Reject unsafe
   mounts and imported links; path handling alone cannot police bind mounts.
5. For the initial terminal grant, use an isolated workload with no network unless
   separately granted, no live workspace write mount, bounded resources and a
   finite lease. Exporting a result is a new authorized file operation with a
   revision check. Arbitrary code that can write a live directory can also delete
   or replace its contents; do not promise write-without-delete for such a shell.
   A broader live-workspace shell mode is excluded from the initial contract.
6. Persist revocation before admitting further work; cancel affected jobs and
   withdraw leases. Report completion only after the relevant stop acknowledgements.
   If stopping cannot be confirmed, quarantine the executor and report pending or
   unknown outcome. Already-dispatched remote effects cannot be retroactively undone.

See [SPEC-0042](../../specs/active/SPEC-0042-capability-isolation-and-proof.md)
for layout, threat cases and release gates.

## Alternatives and limits

- An in-process hook or filtered environment alone is insufficient for hostile
  shell/code: those mechanisms cannot revoke filesystem or process privileges.
- Giving the central gateway every workspace would consolidate protocol handling
  while expanding its exposure; keep execution scoped instead.
- Docker is the initial supported isolation mechanism on the trusted host. A
  separate VM/host is required if the deployment threat model includes escaping
  the container kernel boundary. This decision does not add a VM orchestration
  platform or claim that Docker proves hostile-host isolation.
- Model/provider credentials needed by the trusted harness remain separate from
  connector credentials. Sandboxed code receives neither by inheritance. A
  credential-bearing connector remains trusted with its own granted credential;
  egress restrictions and upstream scopes limit, but cannot erase, that trust.

## Precedence

Preserve ADR-0026's single shared control plane and scoped identities, but replace
its shared-runtime-network assumption for the managed capability profile. Replace
the broad native workspace/terminal assumptions in SPEC-0040. Preserve the existing
broker and workload-controller ownership, resource limits and opt-in integration
rules. New service-specific storage does not create another user namespace:
`spaces/<user>` remains canonical, with dev/prod state kept separate.

## Evidence

- [Pinned Hermes hook loading](https://github.com/NousResearch/hermes-agent/blob/869228cab4a8276d3b4c78da9d9939670c47bd0f/gateway/hooks.py): writable hook placement can become imported code.
- [Go traversal-resistant APIs](https://go.dev/blog/osroot): `os.Root` protects path resolution, with explicit mount-related limitations.
- [Audit and current limitations](../capability-boundary-audit.md).
