# CHG-0065: Default-deny capabilities and isolated execution

Issue 122. Prepared 2026-10-02 in `feat/capability-profile-122`.
Owner adopted the design and instructed implementation on 2026-10-02.
Implementation is in progress. Deployment, account access and GitHub publication
remain outside the authorized implementation/verification work.

## Read in order

1. [Audit and community evidence](../../../docs/capability-boundary-audit.md).
2. [ADR-0031: policy and unified endpoint](../../../docs/adr/ADR-0031-default-deny-capability-policy.md).
3. [ADR-0032: execution isolation](../../../docs/adr/ADR-0032-capability-execution-isolation.md).
4. [SPEC-0041: capability contract](../../../specs/active/SPEC-0041-default-deny-capabilities.md).
5. [SPEC-0042: isolation and T01-T22 acceptance](../../../specs/active/SPEC-0042-capability-isolation-and-proof.md).

ADR-0031/0032 are accepted and SPEC-0041/0042 are frozen. Prior ADRs and SPEC-0040 remain
unchanged; successor documents identify their intended precedence. CHG-0064 remains
the record of the narrower matrix/name-reservation work, not a claim of completion
of this broader requirement.

## Implementation sequence and release conditions

Each phase should be a reviewable change with regression evidence. A later phase
must not grant access around a failing earlier gate. Keep existing APIs/records
where possible; introduce no framework/database/queue or second policy registry.
Data/command names below are design concepts unless delivery evidence says otherwise.

| Phase | Concrete work and existing owners | Exit condition | Tests |
|---|---|---|---|
| P0: establish the real Hermes boundary | Add a Go-driven pinned-image probe under `internal/devcheck`; inspect final tool schemas, invocation, plugin/hooks, API/CLI/channel/cron/child paths and optional registration. Use approved source metadata plus isolated discovery. Determine the supported native dispatch/refresh seam. | Reproducible zero-profile and unauthorized direct-call proof; choose supported upstream integration. If no seam exists, keep native paths off and record the exact upstream gap before proceeding with native enablement. | T01-T03, T19; compare against observed current API fallback |
| P1: authoritative policy | Extend `internal/toolhub` grants/definitions/bindings and `internal/identity`, plus `cmd/hubctl` human administration. Add typed complete-tuple evaluation, explicit ceiling/default/deny, reviewed group expansion, immutable implementation refs and durable audit/fencing. | Zero default, deny precedence, no default self-install, no agent grant administration; one evaluator used at every new managed dispatch. | T04-T09, T11-T12 |
| P2: isolate runtime and control | Change `internal/stack` render/config and `internal/runtime`/`internal/supervisor` mounts, startup and generation handling. Split control state from both infra-owner and secondary runtimes; protect executable extension roots. Limit service-to-service connectivity; explicit platform configs. | Generated topology and real containers deny access to control keys/state/sockets and sibling runtimes. No legacy fallback when managed readiness fails. | T01-T03, T06, T13, T19, T22 |
| P3: unify presentation and routing | Route existing `internal/agenttools` handlers and connector adapters behind the Go ToolHub endpoint using exact-scope private executors. Remove duplicate direct native/stdio MCP delivery for migrated capabilities. Generate allowed-only descriptions; cover resources/prompts/search/composites/control ops. | One logical implementation per capability; hidden tools cannot be reached by raw names, aliases or nested calls; approved positive flows still work. | T04-T07, T10-T11, T21 |
| P4: file and arbitrary-code grants | Reuse `os.Root` and revision checks across file/media/artifact operations. Extend existing workload supervision for scratch-only terminal/code, bounded inputs, leases, export and stop acknowledgement. Apply the same conditions to browser scripting/SSH/child code or keep them disabled. | Independent file effects/scopes, no live-write shell shortcut, tested hostile code containment and honest revocation states. | T09-T10, T14-T18, T22 |
| P5: profiles, replacements and migration | Add reviewed org default/personal overrides and a synthetic migration preview/apply path; coordinate web/STT/image/routine implementations with their existing changes. Remove HH from core/default semantics. Prepare a versioned connector recommendation manifest; actual admission remains opt-in. | Explicit old-to-new grant diff, isolated two-user/org acceptance, native duplicates absent, rollback cannot restore legacy permissions. Assets and historical evidence preserved. | T08, T20-T21 plus positive profile flows |
| P6: acceptance and current docs | Run the full relevant matrix on pinned artifacts. Update current architecture, operations and threat model only to describe behavior actually delivered. Record versioned evidence and remaining provider limits. | All applicable gates pass; unsupported capabilities remain disabled and explicitly reported. No claim of whole-boundary acceptance from unit mocks. | T01-T22, repository and Docker gates |

P0 produces the compatibility facts needed to finalize dispatch wiring; it is not
permission to omit the requirement if upstream is inconvenient. Prefer documented
Hermes config/API/extension points. Any needed upstream change must be explicit,
reviewed and pinned; no monkeypatch in the image or replacement reasoning loop.

## Cross-cutting implementation details

- Extend existing stores and locking. A grant mutation, revision bump and durable
  audit admission must recover consistently after restart. Evaluate resource and
  action together; do not cache a session's allow forever. New schemas and group
  members never expand old grants automatically.
- Keep human consent separate from agent requests. The control path binds a
  confirmation to the exact change/digest and issuer identity. The agent-facing
  endpoint cannot use its own token to confirm a grant or edit raw effective YAML.
- Reuse broker injection after policy admission. File executors do not inherit
  central stores, and arbitrary-code workloads get neither executor nor runtime
  bearer/model secrets. Authentication is not derived from tool arguments.
- Revocation fences admissions first; projection updates are user experience, not
  the security boundary. Local stop acknowledgement and unknown remote effects
  are distinct outcomes. Do not replay uncertain mutations to obtain a receipt.
- Build new inventory from source/registry and actual surfaces; no hand-maintained
  parallel list of 19 tools. Keep a small reviewed effect/resource mapping because
  effects cannot be safely inferred from arbitrary names or annotations.
- A recommended connector bundle is inert reviewed metadata until assigned. Exact
  revisions, API contracts, egress, read/write credentials and broker delivery must
  be proven per connector. Do not implement all candidate providers in this change.

## Migration procedure to implement

1. Snapshot protected policy/configuration revisions and enumerate legacy native,
   hub, MCP, self-service and scheduled capabilities without exposing secrets.
2. Produce a human-reviewable mapping: old source/name, new capability and selected
   implementation, scope/effects, retained/removed/quarantined state, required grant.
3. Default to no grants. The operator selects the organization default and any
   user exceptions; existing credentials and installed extensions are not consent.
4. Prepare immutable config/extensions and isolated volumes; validate owned roots,
   permissions, environment separation and executor reachability. Do not mount live
   `spaces` into a development image or use real data in tests.
5. Fence admissions, drain or explicitly stop old generations and revoke old
   execution leases. Atomically publish a coherent policy/inventory/configuration
   revision; restart the exact managed context and verify projection.
6. Verify an authorized positive call and forbidden negative calls before enabling
   normal traffic. On failure, stop or restore the previous proven managed revision.
   No automatic fallback to legacy broad access, no deletion of user history.

## Requirements traceability and deferred work

| Owner requirement | Contract | Primary phase |
|---|---|---|
| Every native/managed capability detected; nothing implicitly enabled | CP-01/02, IS-05 | P0-P2 |
| Organization default plus individual permissions/restrictions | CP-01/05 | P1/P5 |
| Config, plugins, skills and MCP install denied by default | CP-05, IS-02/05 | P1/P2 |
| Native replacement and consistent names/groups | CP-03/04/07 | P3/P5 |
| One connection/management philosophy for hub, MCP and skills | ADR-0031, CP-04/05 | P3 |
| File scope and read/create/edit/delete separation | IS-03 | P4 |
| Terminal/code isolation without alternative bypass | IS-02/04/05 | P2/P4 |
| No self-grants; trace who granted/revoked; old sessions obey | CP-05/08/09 | P1/P3/P4 |
| Trusted organizational connector bundle, broker-only credentials | CP-06/07 | P5 |
| HH out of core; web/STT/vision/image/routines routed to selected services | CP-07 | P3/P5 |
| Todo provider/channel choices, memory providers, mixed CLIProxy/fallback | CP-07; existing ideas 183/184/185 | Later provider projects under this policy |

Those later provider projects may add implementations, not bypass the policy.
Channel transport permission does not grant personal-account data access. A native
implementation is an option only after the same proof, not a grandfathered exception.

## Validation and delivery evidence

For the completed preparation: verify document links, IDs, proposal status, requirements/test
traceability and preservation of earlier accepted ADRs/frozen specs/runtime code. Run
`just check` and, with Docker available, `just docker-check`; record their actual
outcome in state.yaml. Dependencies are unchanged, so `just security` is not needed
for this documentation change. Existing baseline gates do not prove future T01-T22.

For implementation: meaningful regression tests for each phase, `just check`
(own Go coverage >=85%), `just security` on dependency changes, broker gates if
touched, `just docker-check`, then the new isolated acceptance matrix. Build through
repository wrappers/BuildKit, observe existing bounded cleanup policy, and never
prune unrelated host volumes. Live login/provider writes/deployment/publication
remain separate actions requiring the user's actual instruction.

Preparation was completed and adopted. Track implementation and proof separately
in state.yaml. Do not mark issue 122 or the enforcement milestone
complete on the strength of this design package.

### Implementation evidence, 2026-10-02

- P0 partial: `just capability-check hermes-hub:0.3.0-dev` imports pinned Hermes in
  an isolated disposable container. It records 95 registered tools, 90 declared
  leaves, 59 toolsets, source hashes for 249 tool modules, registry schemas,
  aliases and conditional/dynamic markers. These counts are observations, not a
  manually maintained allowlist. Twenty-seven platform resolver configurations
  have an empty surface; a real API-created agent and the native HTTP `/v1/runs`
  endpoint reject forged terminal,
  file-write, skill-install, generic-call and newly registered tool calls. Both
  exact rejection feedback and an unchanged synthetic canary are asserted.
- Probe image: `sha256:96dedefde9baba381816e6ea5e02026fff8fbb1a0f4dadf1aaccd9efbf5314f1`;
  upstream `869228cab4a8276d3b4c78da9d9939670c47bd0f`;
  inventory `fbbec23d3a93c1c572b795c3b9dfeca1072aead123fd644d13f149d70561045c`.
- This covers parts of T01/T03, not child/channel execution,
  extension/restart, mutable-grant revocation or OS isolation. Native capabilities
  are not approved for managed enablement on this evidence alone.
- P1 partial: explicit self-install/catalog grants, matching-deny precedence,
  monotonic grant revisions, store reload on admission and atomic durable grant
  publication. Regression coverage includes missing grants, unrelated principals,
  conflicting grants, stale-store writes and unavailable storage. These initial
  principal-only grants are outside the managed-profile evaluator added below;
  they are not complete T07/T08/T12 acceptance.
- Targeted `go test ./internal/toolhub ./cmd/hubctl` passed. Current full gates
  and subsequent phase state are recorded in state.yaml.
- Control operations now require separate exact operation grants for projection
  and dispatch, including `invoke`. An open HTTP MCP session test proves zero
  initial tools, exact granted visibility, cross-user isolation and immediate
  refusal after revocation without changing the session ID. Host `hubctl grant`
  supports `--operation`, `--status` and explicit monotonic `--revision`.
- Existing lifecycle fixtures now obtain explicit control grants so their
  readiness/credential/isolation checks still run. Migration does not invent a
  catalog grant. The principal-only scope of these initial grants and the old
  model-visible confirmation flow still require P1's full policy/control work.
- P1 managed primitive: reviewed definition contracts, exact implementation
  digests/selections, org ceiling/defaults and personal allows/denies now evaluate
  complete action/resource/connection tuples plus logical path and execution limits.
  Authenticated profile/environment/generation fields select this path; absent or
  stale authority fails closed without inheriting legacy bindings/grants.
- Managed projection/catalog and actual argument dispatch use that evaluator.
  Tests assert denied aliases, scope spoofing and cross-grant combination reach
  neither injection nor backend; two identities and open MCP revoke are covered.
  This is Go/MCP evidence for parts of T04-T08/T11, not file/executor isolation.
- Host `hubctl capability` publishes strict bounded reviewed policy/profile JSON.
  Ordered policy/profile history is the atomic persisted authority; current maps
  are reconstructed on reload. Failed publication leaves neither a grant nor a
  successful history event. Same-revision retries preserve history, and stale
  writers cannot report successful publication. Call admission requires durable
  audit and preserves profile/revision/digest/environment/generation metadata.
- A shared file fence rechecks the admitted snapshot against cross-process writes.
  Regression tests cover concurrent lock exclusion, changed snapshots, disk errors,
  restart/history ordering and failed-write rollback. This addresses parts of T12;
  the lock currently spans the bounded call. P4 must still implement execution
  leases, prompt revocation and confirmed stop/quarantine outcomes.
- Remaining P1 work includes reviewed group expansion, separate human approval,
  all binding/control mutation paths and complete lifecycle audit. P2-P6 still
  own immutable runtime configuration/extensions, private executors, unified local
  tools, safe file/scratch operations and migration. No managed runtime provisioning
  or full boundary acceptance is implied by the new policy primitive.
- The pinned inventory now generates the Go-managed zero config. The real pinned
  image accepted this generated config with all 27 platform surfaces empty and
  62 registered tool groups disabled, while native/API forged calls remained
  rejected. Managed materialization refuses any edited native, direct MCP,
  plugin, skill, hook, memory or install settings and exposes only ToolHub.
  Render, runtime startup and supervisor launch explicitly refuse managed mode
  until P2 separates control state and protects executable roots. This is an
  intentional fail-closed checkpoint, not a deployable managed profile.
- The managed Compose candidate now assigns Hermes distinct state/home/cache and
  a narrow model env file, mounts effective config read-only and places empty
  read-only tmpfs over executable extension roots, with a private scratch workspace. It omits the live
  workspace/archive, control-store path, broker runtime key volume, dev source
  mounts and host-gateway alias. This is a topology preparation only: current
  private executor boundary and supervised network lifecycle still fail IS-02,
  so the managed launch guard remains in place.
- `just docker-check` on the current source passed both image builds, standalone
  smoke, real Hermes API/supervisor/gateway lifecycle and the Go-generated
  zero-capability probe (27 platforms, 62 disabled groups, inventory digest
  `fbbec23d3a93c1c572b795c3b9dfeca1072aead123fd644d13f149d70561045c`).
  A real supervisor stop-vs-natural-exit race surfaced on the first run; the
  reaper now accepts a failed stop only after inspecting the same immutable
  container ID and confirming `Running=false`. The full wrapper passed on rerun.
  This validates the existing lifecycle and zero surface, not managed P2 Docker
  containment or T01-T22 as a whole.
- The next managed Compose candidate gives each user/environment an internal
  agent network. Infra-owner ToolHub and model adapter join that network, while
  broker/controller and other runtimes do not. The secondary runtime service
  renders the same private-network attachment. A disposable Docker canary on
  `hermes-hub:test` reached a dual-homed ToolHub fixture but could not resolve
  broker or sibling fixtures, and left no test containers/networks. This proves
  the Docker network mechanism only; supervisor network creation/attachment,
  exact service routing and end-to-end managed T13/T22 remain unimplemented.
- The canary is now reproducible as `go run -tags integration ./cmd/devcheck
  managed-network-canary hermes-hub:test` and included in `just docker-check`.
  It checks ToolHub reachability and both DNS and direct-IP denial for broker
  and sibling fixtures. The managed candidate now binds `model_url` to
  `http://model-relay:8318/v1`. A per-user Go relay forwards only
  `POST /v1/chat/completions` to CLIProxy on the shared control network;
  CLIProxy itself no longer joins the agent network. Other methods, paths,
  queries and upstream redirects fail. The Docker canary now checks the
  allowed model call plus denied management routes and direct CLIProxy
  DNS/IP access. These checks do not imply that the supervisor dynamically
  provisions the per-user network or that redirects/metadata egress satisfy T22.
  The full Docker wrapper passed with this relay canary and cleaned its exact
  fixtures. The relay does not yet make managed launch or arbitrary executor
  egress safe; those remain blocked by the P2 guard.
  The relay also requires exactly one top-level JSON `model` matching the
  reviewed managed model ID; missing, changed, duplicate and differently cased
  model keys fail before forwarding. Request bodies are bounded at 32 MiB.
  The Docker canary checks the selected model and rejects alternate/duplicate IDs.
  The complete `just docker-check` wrapper passed with this integrated canary,
  the real Hermes lifecycle contract and the pinned zero-capability probe;
  `just check` passed with 85.19% own Go statement coverage. Managed launch
  remains refused until the missing runtime and executor boundaries are proven.
- Supervised runtime argument construction now rejects managed settings before
  SOUL healing or sibling token enrollment. An incomplete managed profile cannot
  take the legacy shared-network/mount path or mutate its context during a
  failed spawn. A regression covers the early refusal; this does not enable
  managed supervised execution.
- P1 policy/profile records now accept fixed `default_groups` and
  `allow_groups` snapshots. Each embeds its group ID, revision and explicit
  complete allow tuples. The existing evaluator checks them at projection and
  invocation; defaults/personal groups must fit the organization ceiling.
  Changed membership with a reused group revision is refused against ordered
  history, including after restart. A policy update still invalidates old
  profiles until republished. This supplies reviewed group expansion without
  a live name lookup that could grant newly discovered tools. Human consent,
  remaining mutation gates and lifecycle audit are still P1 work.
- The pinned-image capability probe now creates a synthetic on-disk hook and
  verifies that Hermes imports its Python handler even when YAML says
  `hooks: {}`. The initial host-bind preparation guard was replaced by empty
  read-only tmpfs roots, eliminating host extension sources while managed
  launch remains refused. P2 still needs actual runtime lifecycle proof.
- Managed runtime startup now refuses self-managed environment loading before
  legacy state cleanup or service activation. Its authenticated self-env HTTP
  endpoint also refuses writes in managed mode. Regression tests preserve the
  original file and process environment; managed launch remains refused.
- The model relay now uses a direct Go transport with proxy-env lookup disabled,
  so deployment `HTTP_PROXY` cannot redirect its reviewed CLIProxy call. A unit
  regression uses a synthetic proxy trap; the Docker network canary also asks
  the upstream to redirect to metadata and requires the relay to return 502
  without a `Location` header. This proves the relay route, not all egress.
- Candidate managed Compose now places a per-user ToolHub MCP relay on the
  agent/control networks, with only `/mcp` reachable under the `toolhub` alias.
  The authoritative ToolHub stays on control networks under a distinct
  `toolhub-control` alias. This gives infra-owner and secondary users the same
  agent route without exposing ToolHub credential/OAuth routes or Docker-socket
  service directly on an agent network. It remains candidate-only while managed
  render and supervisor launch are refused.
- Candidate managed Hermes no longer mounts organization documents at `/org`
  for org-scoped users and no longer publishes the legacy OAuth callback port.
  Org reads must use a separately authorized file route; managed connector
  callbacks belong to the control plane. Both infra-owner and secondary runtime
  shapes are covered by the mount regression.
- A Docker canary now recreates the candidate writable Hermes state parent with
  nested read-only config and empty tmpfs skills, hooks and plugins mounts. Even as root with
  dropped capabilities, the container can write ordinary state but cannot
  write inside, symlink into, or rename the executable roots or config mount.
  The canary cleans exact synthetic mountpoint placeholders. This demonstrates
  the container mount mechanism without host extension sources. The pinned
  Hermes `HookRegistry` then runs in two fresh Python processes and imports
  no hook from an unapproved writable-state location. The canary removes its
  exact disposable state root, including files Hermes creates while loading.
  Actual managed gateway lifecycle and other extension sources still need
  separate proof before launch. The fixture also exposed Hermes' malformed-YAML
  fallback to defaults. The managed Go wrapper now re-attests its mounted
  effective YAML before state mutation on every attempted launch or restart,
  comparing the full profile, model identity and one ToolHub endpoint. The
  Docker canary confirms the built `hub-runtime` rejects malformed YAML before
  Hermes and reaches the isolation guard with valid YAML. That guard still
  refuses launch, so the real managed lifecycle remains unproven.
- The disposable agent-network canary now checks the actual Linux IPv4 and
  IPv6 route tables: the internal agent network has no default route. It also
  retains direct-IP denials for broker, CLIProxy, ToolHub control and sibling
  fixtures. This is network-topology evidence, not a complete DNS/redirect or
  future executor egress proof for T22.
- A pinned-source audit found additional `HERMES_HOME` ingress: `skill-bundles`
  becomes slash-command instructions; `scripts` can feed webhook/cron execution;
  `bin`, `node` and `lsp` hold installed executables. The managed candidate now
  overlays empty read-only tmpfs on all five, in addition to skills/hooks/plugins.
  Its environment pins bundle discovery and disables project plugins; the Go
  preflight rejects redirect attempts. The Docker canary exercises Hermes'
  hook, bundle and script scanners. Other alternate import/launch paths still
  require the broader T19 lifecycle proof.
- The supervisor managed launch refusal is replaced by real topology
  provisioning. `capability_mode: managed` now requires a supervisor execution
  selection; settings are validated before SOUL healing or sibling-token
  enrollment, and unreadable/malformed settings fail closed instead of taking
  the legacy path. Spawned managed runtimes join only the internal per-user
  agent network (`hermes-hub-agent-<user>-<env>`) with no published port and no
  /scope, /org, archive, broker, skills-source or host-gateway authority. A
  labelled `<container>-ctl` control-relay sidecar dual-homes onto the agent
  network and a supervisor-owned `hermes-hub-control` bridge, publishing only
  `127.0.0.1:<port>:8091` so readiness and control traffic reach the runtime
  through a fixed-origin reverse proxy that denies redirects and rewrite
  headers. Compose no longer ships a static managed runtime service; it owns
  only control plane, relays and the internal agent network. Managed state
  lives under `managed/<env>/{runtime,hermes,home,cache}`; effective config is
  materialized host-side and mounted read-only; all eight extension roots are
  sealed tmpfs. `hub-runtime serve` runs `managedPreflight`, which re-attests
  pinned discovery env, the complete effective YAML and in-container isolation
  (identity pins, empty non-writable extension roots, immutable config, no
  default routes, denied control hosts, reachable relays) before Hermes starts.
  Orphan inventory skips `control-relay` sidecars and reaps stale ones with
  their runtime via recorded container names. Unit coverage includes run-arg
  construction, separate state layout, selection gating, relay dual-home and
  failure cleanup, non-internal network refusal and all verifier failure modes.
  `just check` passed at 85.08% and `just docker-check` passed the updated
  preflight canary (profile-unset refusal, relay-unreachable gate) plus the
  existing network/extension/hook canaries.
- The managed supervisor path is now proven end to end on real Docker.
  `managed-supervisor-canary` constructs a managed context, spawns the actual
  supervised runtime on a fresh internal agent network, reaches readiness only
  through the labelled control relay on `127.0.0.1:<port>` and confirms the
  in-container `verify-managed` attestation passes against the live topology.
  Reap now removes the `<container>-ctl` relay after verified runtime removal:
  sidecar lookup inspects labels instead of guessing from `ps`, unverifiable
  names are left untouched, and a stale same-context relay is reclaimed when a
  respawn hits the name. `just check` passed at 85.06% and the full
  `just docker-check` wrapper passed including this canary.
- Loopback ports are no longer hash-guessed: `Config.PortRange` (default
  10000) bounds a tracked allocator that probes linearly from the context's
  deterministic hash offset, so hundreds of contexts can never collide on
  `127.0.0.1`. Exhaustion fails closed before any docker call; a recovered
  running container is reused through its actually published port
  (`docker port`), not the newly allocated one.
- Generation rollover is repaired end to end: a dead or stale-generation
  same-name container (runtime or `<name>-ctl` relay) is reclaimed only when
  its labels prove same-owner same-context older-generation ownership;
  foreign and same-generation containers are never touched, and relay spawn
  retries once after reclaiming a stale name.
- The extended canary now proves mount immutability across a real container
  restart (normalized mount signature and config SHA unchanged, attestations
  and sealed-write denials re-pass), secondary-runtime parity (a second
  managed context on its own internal network with a distinct loopback port
  and no sibling name resolution), secret isolation (no broker material,
  no /scope, no control-plane secret env), unreachable link-local metadata,
  and respawn-after-reap with a fresh generation. `just check` passed at
  85.10% with the new port/reclaim regressions.
- P0 completion pass over the remaining launch surfaces: the probe now proves
  hostile persisted cron jobs (`enabled_toolsets` per job), the `cron` platform
  fallback and the `enabled=None` full-default fallback all resolve to zero
  tools under the managed denylist, and delegate children of a zero parent
  inherit it (upstream's orchestrator role deliberately re-adds only
  `delegate_task`, unreachable while the parent lacks it). Channel adapters and
  alternate entrypoints stay resolver-only by construction: the supervisor
  spawns a fixed `hermes gateway run` argv. Startup coverage beyond hooks
  closed two remaining writable-state inputs — the verifier refuses launch when
  `$HERMES_HOME/.env`/`.op.env` (loaded upstream with `override=True` at start
  and per turn; the probe demonstrates the re-pointing) or the upstream managed
  scope (`HERMES_MANAGED_DIR`/`/etc/hermes`, an in-process config/env overlay)
  is present. Revocation seam analysis: upstream provides only the
  confirm-gated `/reload-mcp` slash command (MCP reconnect, unreachable over
  `/v1/runs`) and the CLI MCP file watch; there is no native grant-revocation
  API, so managed revocation remains admission fencing plus generation restart.
- Adapter re-audit on pinned source 869228c found three upstream bypasses:
  forged `hosted_room_dispatch` plus a self-signed `_room_execution_policy`
  (unsigned sha256 digest) arms an api_server agent past the entire denylist
  because `disabled_toolsets` is never passed on that path; literal-toolset
  sub-agents (memory hygiene `["memory"]`, curator `["skills"]`) bypass
  platform resolution because `AIAgent` never reads the denylist config; and
  blueprints accepts caller-chosen toolset lists. Fixes: the control relay
  strips caller-supplied room-dispatch policy keys, the managed render
  disables `compression.enabled` and `curator`, and the pinned probe gained a
  wire-level forged POST plus config-gate assertions proving the arm pre-fix
  and denial post-fix.
- P3 unified agent-tools entry landed: the `AgentTools` transport carries no
  DefinitionSource, CLI flags or MCP schema and is only reachable through
  ToolHub admission. `CapabilityScope` {resource, path_argument, path_prefix}
  binds at dispatch and travels verbatim on `EffectiveBinding`; the private
  `tools-exec` channel (docker exec into the owning runtime) carries only
  tool+args+scopes+timeout. `hubctl capability --kind agent-tools` registers
  the compiled-in definition plus an operator binding; projected descriptions
  name the admitted scope.
- P4 file grants (T14/T15): per-call `scopedRoots` narrows the executor's
  os.Root workspace/archive/organization roots to the admitted `path_prefix`
  via `ScopedSub` (file scopes narrow to the containing directory and keep
  the target name; directory scopes narrow to themselves). Duplicate
  read+write scopes deduplicate before path-argument rewriting, symlinks and
  swapped scope directories deny, and file operations carry distinct
  read/write/create/delete capability tuples so conversion needs create
  authority on its O_EXCL destination.
- P4 scratch execution (T16-T18): `ToolSpec.Sandboxed` marks `code_exec` and
  restricts it to the AgentTools transport, dispatching to `DockerScratchExec`
  instead of the in-runtime executor (unconfigured fails closed ErrIsolation).
  The lease is: `exec-pack` inside the owning runtime packs only the exact
  admitted scope through an os.Root into a bounded regular-files-only tar; a
  disposable container runs `exec-scratch` with `--network none --read-only
  --cap-drop ALL --security-opt no-new-privileges --user 10001:10001`, tmpfs
  /scratch /outputs /tmp, `--memory 256m --cpus 0.5 --pids-limit 64
  --stop-timeout 2`; stop is confirmed by inspect before success; unconfirmed
  stop quarantines that binding only until inspection proves the sandbox is
  gone; exports are bounded, create-only, under the admitted scope, and gated
  by a `Store.ReverifyEffective` fence so revocation or policy/profile bumps
  stop the write. A real-image runner check proved uid 10001, read-only
  rootfs, absent workspace/state/socket/network and a working export channel;
  the multi-container topology remains for the next CI docker gate. A
  serialization bug where `CapabilityScope` fields were `json:"-"` (dropping
  the boundary on the private wire) was found and fixed.
- `just check` after P4 passed all stages with race/shuffle tests at exactly
  85.00 percent own Go statement coverage. P5 (profile preview->apply, HH
  connector manifest, rollback without legacy) and P6 (full T01-T22 matrix)
  remain.
- P5 preview->apply and connectors: `Store.PreviewCapabilityProfile` diffs a
  candidate against the stored profile with the same `managedSelectionLocked`
  evaluator and the same admission checks `PutCapabilityProfile` runs
  (extracted as `validateProfileAdmissionLocked`), so preview cannot diverge
  from apply. The old side evaluates under its pinned policy revision replayed
  from capability history, so a policy bump reports `changed` scopes rather
  than an empty set; denied selections stay visible with their reason instead
  of dropping silently (CP-10 quarantine of unknown entries). Drafts need only
  `validateStructure` — confirmation remains the apply gate and preview never
  writes the store. `hubctl capability --kind preview` prints the diff;
  `--kind connectors` prints the versioned `ConnectorManifest` where every
  family is `opt_in` (hh implemented via the agenttools `hh` capability behind
  an explicit grant, org bundle candidates recommended or deferred,
  txttsql/dbhub marked mutually exclusive). Rollback is republishing earlier
  content at a new monotonic revision — stale revisions conflict and only
  reviewed selections return; `TestPreviewMatchesApply` proves an applied
  draft admits exactly the previewed surface.
- P6 traceability: `.work/in-progress/CHG-0065-capability-enforcement/
  acceptance-matrix.md` maps every T01-T22 row to its existing evidence and
  layer (unit, fake-docker contract, real-Docker canary, pinned-image probe)
  and marks open obligations honestly — T16-T18 full multi-container leases,
  T13/T22 canaries on the latest image and live-provider T09/T21 still need
  the CI docker gate or explicit provider instructions.
- `just check` after P5 passed all stages at 85.02 percent own Go statement
  coverage. No release claim: `implementation_status` remains
  `partial_not_release_ready`.
