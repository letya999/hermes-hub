# CHG-0309: Per-space OpenKnowledge editor restored as human UI

Owner instruction: the per-user OpenKnowledge editor must work over that
user's own workspace (memory vault) and open in a browser.

ADR-0095 D2 retired the upstream editor because it cannot act as a
governed-API client. The 2026-11-15 ADR-0095 amendment carves the scoped
exception: the editor is a *human* surface — owner edits land as
working-tree changes and the existing `vault.CommitExternalEdits` sweep
(the nightly rollup path, `internal/memory/night/night.go`) commits them as
`hermes-owner` before rollup writes, the same provenance a host-side owner
edit already took. The agent never gains a route: the
`mcp_servers.open_knowledge` render stays retired on both render paths and
`knowledge_ui_write` stays deferred under #422.

## Goal

`knowledge_ui` (requires `memory_vault`) renders one pinned `open-knowledge`
replica per space mounting `spaces/<id>/vault` at `/data` read-write; the
owner reaches it in a browser through the `ui_port` loopback publish
(`ssh -L` tunnel from a remote machine), `hubctl doctor` reports the edge.

## Slices

### S1 Render (internal/stack/compose.go)

Restores the M20.8 shape minus the runtime route: pinned role image
(`hermes-hub:<ver>-<env>-knowledge`, BuildKit `imageBuild`), `replicas: 1`,
`ok init --no-mcp --no-skills` first boot, vault bind → `/data`,
`OK_ALLOW_EXTERNAL=1`/`OK_BIND=0.0.0.0`/`PORT=8080`/`HOME=/tmp`, tmpfs
`/state/home` for the passwd-home registry, `ui_port` → exactly one
loopback publish with `OK_EXTERNAL_URL` = the loopback browser origin.
Networks: `default` on unmanaged (the shared-hub-net alias existed only for
the retired MCP dial), the internal agent net on managed.

Not restored (stays dead by design): `mcp_servers.open_knowledge` on
unmanaged/managed renders, `HUB_MANAGED_KNOWLEDGE_UI`,
`MaterializeOptions.KnowledgeUI` — all existed only for the route.

### S2 Status + docs

`KnowledgeDoctor` reports the edge again (network-only hint, loopback bind
refusal, listening probe). Feature description, `operations.md` runbook,
`tools.md`, `spaces.md`, E2E-249, governance_io comment, ADR-0086 D8 +
ADR-0095 amendments, SPEC-0085 R8 note.

## Tests (red first)

`TestOKRenderSingleReplica`, `TestOKMountVaultRoot` (rw `/data`),
`TestOKNoPublishedPorts`, `TestOKWritablePasswdHome`, `TestOKPrivateNetOnly`
(default-only unmanaged, agent-net managed, never the shared net),
`TestOKPathConfined`, `TestUIPortLoopbackOnly` (single publish +
loopback `OK_EXTERNAL_URL`, `[::1]` bind), `TestKnowledgeUINoRuntimeRoute`
(both renders), `TestOKDeclaredButUnrendered` (passport declaration without
the feature fails the check), `TestWriteDenyReceipt`
(`knowledge_ui_write` refused), `TestDoctorUIPortChecks` +
`TestDoctorUIPortReachable`, doc-gate tests.

## Acceptance

Feature on → service renders per-space with the rw vault mount; `ui_port`
publishes loopback-only; off → zero effects. Passport spaces keep the
declared-services contract (`open-knowledge: {mode: per-space}` in the
stack doc). No agent route, no attestation vocabulary. Scoped suites green.

## Out of scope

Agent MCP access to OK (#422), the M18 named edge (`ui_expose` still
fails).

## Delivery notes (2026-11-15)

Live acceptance on the artem_two dev stack (published r-4df7e1ab9705a5d4)
surfaced two fixes beyond the original restore:

- managed `ui_port` publish never bound — the agent net is `internal:
  true` and drops published ports; the service now also joins a dedicated
  `knowledge-publish` network (the `browser-publish` pattern).
- git refused repo ops on the host-uid vault ("dubious ownership"),
  blanking OK's history/sync views — the command now sets
  `git config --global --add safe.directory /data` (git config lives on
  the /tmp tmpfs).

Operator note: `hubctl space apply` on a non-Linux host needs
`HUB_RUNTIME_SUPERVISOR_URL=http://127.0.0.1:8876` — the recorded
`supervisor_url` is the in-network name.
