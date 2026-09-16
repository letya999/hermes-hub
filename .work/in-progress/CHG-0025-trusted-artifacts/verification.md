# Full objective verification — not complete

Evidence is scoped to the path actually executed. The legacy Telegram controller
is not generic runtime evidence, and the project Docker image gate is not artifact
pipeline evidence.

## Owner-requested public MCP checks, 2026-09-16

`go test -tags integration ./internal/toolhub -run '^TestArtifactRequestedPublicServers$' -v -count=1`
is an opt-in real GitHub import/recipe gate, not a runtime success test.

| Server | Frozen commit SHA | Evidence |
|---|---|---|
| Serena / Python | 704e8c3d1929bdd74bf0c8eee1596aeddf03fdb2 | Real automatic import: 1,070 recipe files; generated `/opt/venv/bin/serena-agent` command verified. Actual MCP launch requires its subcommand/arguments and private project data; not executed |
| Context7 / Node | b653c3a07d7936bdc4c23fc1c88903120e0ece77 | Real automatic import: 494 recipe files; pnpm workspace discovery selects `node /app/packages/mcp/dist/index.js`; recipe verification passed after npm-only and workspace-discovery fixes |
| mark3labs/mcp-filesystem-server / Go | ba3f07f22c309d932fa9b1cebe1eb7c55fcbb83b | Real import and generated recipe: 47 files, `/app/mcp-server`; verified |
| rust-mcp-stack/rust-mcp-filesystem / Rust | ef4797360ea03eec5375e03a6f10d092dfc6a5e0 | Real import and generated recipe: 121 files, `/app/target/release/rust-mcp-filesystem`; verified with anonymously resolved official Rust base digest |

No account keys were used. Context7's stdio CLI accepts an absent API key;
documentation calls still require egress and may be rate limited. Filesystem
servers must receive only synthetic files in an isolated named volume, not host
mounts. Serena must not expose its shell/edit tools without an approved effect
allowlist. These source facts do not establish successful `tools/call`.

The Serena gate now supplies the exact commit's documented CLI subcommand
`start-mcp-server --transport stdio`, disables dashboard/browser/GUI startup and
sets its tool timeout to 10 seconds. The prior default console-command recipe
was not an MCP launch recipe. Amended real exact-SHA gate session 33391 exited
zero in 39 seconds: 1,070 files, 9,207,808 context bytes, literal MCP argv retained
and recipe context verified. No build or runtime success is claimed. Fresh
`just check` after this change, session 4840, completed with exit zero at 85.59%
coverage. Docker session 33770 passed native approval, layered health, orphan
inventory, desired-state and supervisor checks and reached the actual five-minute
gateway idle window. This remains project-runtime evidence, not the generic MCP
builder/controller or the requested 95-plus-5 topology.

Docker gate session 33770 subsequently completed with exit zero for project image
`sha256:d01a38a05563441720ed6e738b6d029990c1a4e74c71d80add46bee29032f3b8`.
Actual five-minute automatic graceful shutdown, absence of idle compute and
original-session cold restore passed, followed by the pinned Hermes 0.21.0 API
contract result. STT fixture was unavailable (`hub-stt` exec failed), not passed.
This proves project-runtime compatibility only. No generic isolated artifact
builder, requested public MCP start, trusted registration or Hermes delivery was
executed by this gate. No verification process handles remain live.

The final repeated combined import run hit anonymous GitHub HTTP 403 after prior
successful source/recipe runs; do not represent that repeat as passing. Runtime
checks are still not run: there is no verified isolated builder or generic
ToolHive pre-start resource/rootfs enforcement path. No unsafe launch, trusted
catalog entry, or successful Hermes delivery was synthesized.
`just check` passed for the public-MCP source/recipe changes at 85.55% original
Go coverage (race, format, vet, staticcheck, docs, actionlint). No new Go/project
dependency was added. Security scanning is not green; the earlier project Docker
gate is not a build/start test of these four artifacts.

## Bounded stock ToolHive proof

`internal/companion/toolhive_integration_test.go` is opt-in (`integration` tag).
Actual Docker run, session 39923, exited zero in 18.65 seconds. One non-root
container had read-only root, network none, capabilities dropped, NNP, CPU 1,
512 MiB memory/swap, PID 128 and bounded tmpfs. Tests inspected these settings
before starting any process; only an owned synthetic named volume was mounted.
No host mount or real Docker socket was provided.

Stock Linux ToolHive 0.48.0 release archive SHA-256:
`7ed4b9cbe7e3e052f3a9a2a3268d6e13b02ad492a740086d989720c7f30f4ea9`.
The test checks the archive hash before using its binary. Its private Unix shim
only answers Docker ping/empty container list; all tested create/start/image/
network mutations return 403, without forwarding to any real daemon. Native
Windows remote-workload startup without a runtime separately failed closed.

Existing companion launched an actual synthetic Go SDK stdio child. Stock
ToolHive remote workload discovery returned only the approved echo tool, and
tools/call succeeded. RunConfig export omitted the synthetic bearer literal;
stop/remove succeeded. ToolHive's environment secret provider supplied bridge
authorization only to ToolHive, not the MCP child. Incoming proxy authorization
was not configured in this loopback-only fixture and is not production evidence.
The owned verification container and volume were removed by test cleanup.

Subsequent actual Docker diagnostic, session 3163, exited zero in 10.88 seconds
and checked the exact structured echo response. Critically, a sibling subprocess
with the MCP's UID read the ToolHive forwarding-secret marker from
`/proc/<control-pid>/environ`. No value was logged. The diagnostic pass is
evidence of exposure, not successful secret isolation. The one-container
candidate is rejected for production; MCP/bridge and ToolHive control must use
separate bounded containers and private state. The initial diagnostic emitted no
evidence because test verbose mode was omitted; corrected repeat produced the
explicit exposure result. Actual verification containers/volumes were cleaned up.

This does not establish real Serena/Context7/Go/Rust builds, Hermes delivery,
trusted admission, generic runtime implementation or the 95-plus-5 topology.
`just check` session 49394 passed at 85.60% coverage. Project `just docker-check`
session 33770 remains running; it is not the isolated MCP artifact builder.
That session built project image d01a38a05563 and passed Docker smoke; the pinned
Hermes contract is now running. Fresh `just check` session 72750 completed with
exit zero after the sibling diagnostic change, at 85.59% original Go coverage;
race, formatting, vet, staticcheck, documentation and actionlint passed.

## Split MCP and ToolHive proof

Actual Docker session 22030 exited zero in 17.74 seconds. The stdio child and
existing companion run in one bounded container; stock ToolHive and bootstrap
shim run in another. Both use the same pinned cached image layers but distinct
named volumes and default private PID namespaces. MCP network is none; proxy
joins only the exact owned MCP container network namespace, verified by immutable
container ID before startup. Both profiles enforce non-root, read-only root,
capability drop, NNP and CPU/memory/PID limits before any user process starts.

During the actual tools/call, the MCP echo handler rejects access to the proxy's
control-state marker and readable ToolHive forwarding-secret markers in /proc.
The exact structured result passed these checks. tools/list, export without
synthetic secret literal, stop and remove also passed. Same-UID sibling exposure
inside the control container remains an intentional diagnostic, not a probe from
the isolated MCP. Both verification containers and volumes were removed.

Initial repeats failed closed on name-versus-ID Docker namespace comparison and
root-owned volume state-directory permissions; corrected without relaxing the
runtime profile. This does not prove generic admission, inbound proxy auth,
egress allowlisting, cross-user production isolation, 95-plus-5 capacity topology,
real public MCP builds or Hermes delivery. just check session 7078 completed with
exit zero at 85.59% coverage; final fixture binaries also compiled and actual
split-container integration passed. No dependency or upstream patch was added.

## Anonymous binding isolation regression

`TestAnonymousPerBindingWorkloadIdentity` creates 100 credential-free per-user
bindings with distinct runtime IDs. Resolve and NewWorkloadInstance now agree on
100 unique workload IDs; real temporary filesystem state directories are unique
and stable on restart; a different principal cannot resolve any binding. Missing
anonymous binding/principal identity fails closed when creating state.

This fixes the previous anonymous path's definition-only workload collision and
context/definition-only state directory. No credential-bearing legacy paths were
changed and no previous user state was moved/deleted. This is a model/filesystem
regression, not actual Docker process/volume isolation, the 95-distinct-plus-5-
shared credential-reference scenario, or tools/call through Hermes.
`just check` passed after fixing staticcheck's unused path initializer, at 85.57%
original Go coverage; session 78562 completed with exit zero. No dependencies were
changed and Docker artifact/runtime integration was not run in this step.

`TestCredentialBindingIsolationNinetyFivePlusFive` now covers the requested
credential-reference shape at the identity/filesystem layer: 95 distinct
synthetic references plus five uses of one shared synthetic reference produce
100 workload IDs and 100 state paths. This is not yet the required Docker
topology or a Store authorization proof; the current catalog ownership model
does not permit one connection credential to be owned by multiple principals.

The following matrix is the original gap snapshot. The current implementation
and evidence update is recorded below under **Generic controller adapter —
2026-09-16**.

| Requirement | Current evidence / remaining gate |
|---|---|
| Exact Git commit SHA; reject mutable refs | `artifact_source.go`, negative tests and real `TestArtifactSourceGitHubRead`; `hubctl artifact import` now runs the exact-SHA source→recipe→BuildKit→quarantine path |
| Read only declared public source files | Explicit selection and automatic safe regular-file selection implemented; sensitive filenames excluded before content requests. Automatic exact-SHA raw reads verify Git blob IDs and use two API requests. Drift/size/link/submodule/redirect/duplicate negatives passed. Content secret scanning remains required |
| Explicit Dockerfile/Python/Node/Go/Rust recipe | Dockerfile-v1 validation and automatic conventional four-language recipe generation implemented; deterministic contexts, literal argv and pinned base required. Four-language positive/negative tests and requested public source/recipe fixtures passed |
| Isolated BuildKit/docker-container build | Not implemented; installed buildx has only Docker drivers. Upstream buildx 0.33.0 docker-container driver sets Privileged=true; official rootless Docker examples relax security profiles. Neither posture was silently accepted |
| Build credentials excluded | Source helper inherits no Git auth/cookies/proxy; build workers and build context secret scan still unverified |
| Immutable digest, provenance, SBOM, scan result | OCI integrity/presence verifier plus write-once private quarantine persistence implemented. Import materializes the verified archive into the local Docker image store by immutable image name; exact archive SHA-256 remains separate from OCI index/manifest/config digests |
| Exact tools, effects, transport, env manifest | Existing definition schema only; bounded discovery and capability review not implemented |
| Re-review changed commit/artifact/tool set | Existing definition immutability only; artifact/review hash and live drift rejection missing |
| Shared trusted/preflighted catalog entry | `RegisterTrustedArtifact` requires unchanged review/evidence/entrypoint digests before catalog registration |
| ToolHive Workload Manager / RunConfig / stdio proxy | Actual bounded synthetic stdio/companion/stock remote-workload discovery, call, export, stop and remove passed without real Docker socket. Production generic controller not implemented |
| Permission profiles, isolation, controlled DNS/egress | Upstream primitives exist; generic policy mapping and actual allow/deny probes missing |
| Local Docker only; reject remote daemon | Generic native and fallback paths require a local Linux Docker context; remote/Windows daemon and non-Linux runtime fail closed |
| Per-binding state/static credentials; explicit stateless sharing | Fallback uses one workload ID per binding, named state volumes and owner `credentials.env`; shared stateful/credential-bearing plans fail closed. Full Docker 95-plus-5 run remains pending |
| Deny-by-default concrete host/port egress | Fallback creates an internal network and a pinned egress proxy with explicit host/port ACLs; live escape probe remains pending |
| No privileged, host network/PID/devices/socket/mounts/foreign spaces | Generic pre-start Docker create flags and inspect checks enforce this for artifact, proxy and relay; live public-artifact escape probe remains pending |
| Read-only root, non-root, cap drop, NNP, seccomp/AppArmor | Docker fallback enforces read-only root, non-root, cap-drop ALL, NNP and seccomp before start; native ToolHive path fails closed when flags are absent |
| CPU/memory/PID/timeout/output/request/log limits | Definition bounds alone are not enforcement; generic runtime and helper limits missing |
| Docker VM / ECI / Hyper-V defense in depth | Docker Desktop local daemon observed; paid ECI not required; authorization still mandatory |
| Image reuse without shared processes/volumes/secrets | Named workload/bridge/proxy/relay volumes and process IDs are unique; full image-reuse E2E remains pending |
| Real Python and Node builds | Not run |
| Real stdio through ToolHive proxy, tools/list and tools/call | Not run for generic artifacts |
| Reject shell/host mount/privileged/unlimited runtime | Recipe parser and command validator reject shell/batch and unsafe fields; generic fallback rejects host mounts and missing bounds before Docker create |
| 100 synthetic bindings: 95 distinct and 5 equal credential references | Identity/filesystem regression covers 100 IDs and paths; Docker 95-plus-5 capacity run remains pending |
| Active workload budget and queue, helpers accounted | Not implemented |
| Idle cleanup, health failure, stop/start/restart/remove | FIFO budget, queue admission and idle cleanup are implemented; explicit lifecycle/health E2E remains pending |
| Cross-user denial / network escape / secret leakage | Source negative tests only; actual generic workload probes missing |
| just check, >=85% own Go coverage | Immutable-storage snapshot passed at 85.50%, including race, format, vet, staticcheck, docs and actionlint. Sixteen-writer CAS and actual Windows foreign symlink tests passed. Inherited base-CMD reset regression passed for all four languages. Real automatic GitHub Python context and generated recipe passed at c9460f8ded6e2457bd70ebabfad840b58d23645d without manual file selection |
| just security / dependency scan | No dependencies changed; earlier safety scan failed, npm unavailable; not a green security gate |
| just docker-check | Earlier four-language project image cd5c838b8e3b passed smoke, pinned Hermes API contract, real five-minute idle shutdown and cold restore (session 89565 exit zero); STT executable probe unavailable. This predates automatic-context/OCI-storage changes and is not generic artifact integration evidence |

Runtime path outstanding: an evidence-backed unmodified ToolHive enforcement
path. Owner explicitly rejected patching ToolHive. Do not silently patch upstream,
introduce a second supervisor/proxy, or use post-start docker update as protection.

Build decision also outstanding: a verified restricted rootless BuildKit profile
that meets the requested policy, not privileged or unconfined defaults. Primary
implementation references: https://github.com/docker/buildx/blob/v0.33.0/driver/docker-container/driver.go
and https://github.com/moby/buildkit/blob/master/docs/rootless.md.

Additional builder source audit: RootlessKit's current parent setup always calls
newuidmap/newgidmap; Linux no_new_privs prevents setuid/file capabilities granting
new privileges on exec. BuildKit v0.29.0 executor GenerateSpec explicitly appends
oci.WithNewPrivileges. Therefore stock rootless bootstrap must not be presented
as compatible with an unchanged drop-all/NNP runtime profile. This is source
evidence, not an actual restricted-builder integration failure or proof that all
possible implementations are impossible. Clarify owner scope for trusted builder
bootstrap before accepting a distinct security profile; no exception was applied.
Primary references:
https://raw.githubusercontent.com/rootless-containers/rootlesskit/master/pkg/parent/parent.go
https://docs.kernel.org/userspace-api/no_new_privs.html
https://raw.githubusercontent.com/moby/buildkit/v0.29.0/executor/oci/spec.go

## Restricted rootless BuildKit MVP probe

After explicit owner authorization, the official `moby/buildkit:v0.29.0-rootless`
multi-platform image was resolved and pulled by immutable index digest
`sha256:93efd7f1c17f16cea080c34f68e19863b9fe541db550bf607221ce425c0ab9ef`
with an empty project-local Docker client configuration.

The actual pre-start-inspected container was non-root 1000:1000, privileged false,
network none, read-only root, cap-drop ALL with only SETUID/SETGID, private PID and
cgroup namespaces, no devices or bind mounts, 2 CPU, 2 GiB memory and swap, 512
PIDs, bounded tmpfs and one owned state volume. Default seccomp failed closed at
RootlessKit namespace creation. An unconfined diagnostic built no source and was
not accepted. The pinned Moby seccomp v0.2.1 profile (upstream SHA-256
`536529b665dd0972c37bfb569f5d4ac8a53592e7b00752bc39ff063ca9864c74`)
was extended only for clone/user-namespace and mount-family bootstrap calls;
final profile SHA-256 is
`1ae93b56f7ec0ed35e62ad2183177c8d6521a7655ae6a9493c2abd92c134a147`.
The current repository profile additionally permits only `keyctl`, `pivot_root`
and `sethostname`, required by the pinned BuildKit Syft scanner's nested rootless
container. Its current pinned SHA-256 is
`bd1e45460cc93f0cb19393e58c48c8f33b2e410f537d94ac16ba2283bfd982ef`;
unconfined seccomp was not used.
With native snapshotter, a linux/amd64 worker became ready.

A real network-none scratch build copied one synthetic payload and exported OCI
archive SHA-256
`195dbb4ce04a19f3995956e0c4841a8da75c3ca653d6dcc12aa7184a3edd2399`.
No credentials or public MCP source were used. Provenance and SBOM were not
generated, so the archive is quarantine evidence, not trusted catalog material.
The exact labelled container and volume were verified and removed. This does not
yet prove Python/Node dependency builds, controlled registry egress, builder
timeout/output enforcement, provenance/SBOM or production controller wiring.

Subsequent real Docker integration uses an internal network with no direct
route and pinned ToolHive egress-proxy image
`sha256:72a43857af69e602bdc1285bbb074e829ae967c1ede4144377ddeb048bacb1be`.
CONNECT to the explicit package registry allowlist on port 443 passed while
`example.com:443` returned 403. A pinned Python base image was then pulled and
built by rootless BuildKit, with SLSA provenance and SPDX SBOM generated by the
pinned scanner digest
`sha256:ae4f3b554449e7e25548e7d8ccc029d17357348e30c6e3df01b92bc93654d6a9`.
The exported OCI archive passed `VerifyOCIArtifact`, including the real legacy
BuildKit index-attestation binding. All temporary containers, networks and
volumes were removed. This proves the restricted build substrate and attested
OCI export, but production importer/controller wiring and real MCP builds remain.
`just check` session 43653 passed at 85.59% Go statement coverage.

The verified profile is now repository-owned at
`docker/seccomp-buildkit-rootless.json` and content-pinned by
`restrictedBuildKitCreateArgs`. The generated Docker create plan fixes the image
digest, builder identity, network-none mode, non-root/read-only/capability policy,
CPU/memory/PID/tmpfs bounds, private state volume and native snapshotter. Drifted
profiles and unsafe names fail closed. Focused tests and `just check` session
88581 passed; project Go statement coverage was 85.58%. This is policy wiring,
not yet a production build operation or trusted artifact.
## Automatic public MCP builds and MVP control plane — 2026-09-16

The production `BuildRestrictedOCI` path now uses the pinned rootless builder,
an internal-only network and the pinned ToolHive egress proxy. Proxy build args
are declared as `ARG` (never `ENV`) and the build sandbox receives the proxy's
owned internal IPv4 address, avoiding resolver leakage while preserving the
deny-by-default host allowlist. Automatic discovery excludes `docs/` because
it is not build input; explicitly requested files remain subject to the normal
path and content gates.

Real isolated builds passed with provenance/SBOM and `VerifyOCIArtifact`:

| Server | Commit SHA | Archive digest | Image manifest digest |
|---|---|---|---|
| Serena / Python | `704e8c3d1929bdd74bf0c8eee1596aeddf03fdb2` | `sha256:477caacd116c60b3be7fd67bff20d995e1041d1d1f20ec490c2a6d88df8236ce` | `sha256:cfd1862eaab8438d2a8de3df608b88db7d675742fd1fdcdcb1d9005915b5a04a` |
| Context7 / Node | `b653c3a07d7936bdc4c23fc1c88903120e0ece77` | `sha256:7656bf84b90330f86441c6bc8cf7b2557f8b95f832dfd1782db355ae272498f5` | `sha256:634cfbb7993d51803fa6118f009a9326983d641ac64a793e1e12bde24d3623f6` |
| mark3labs filesystem / Go | `ba3f07f22c309d932fa9b1cebe1eb7c55fcbb83b` | `sha256:ff385e206964c5549c8c39044c8900a1a5bb25dc129db8ac7848fc9ae5072502` | `sha256:637e7747ce8d5f9c022bdf89f553588485cfeb47b7774af9d1807dde33188b72` |
| rust-mcp-filesystem / Rust | `ef4797360ea03eec5375e03a6f10d092dfc6a5e0` | `sha256:929c115174c80160638516db702a5bed4a30b168897f836acf9a2348e3b601ff` | `sha256:04765c7dc3e0ae5c899e744f924fdeca324babe928482fbcd5c9911103571939` |

`hubctl artifact import` is the automatic entry point. It emits a review packet;
`hubctl artifact approve` is the only catalog publication step. The packet
stores no source bytes or credential values. `DefinitionSource` carries exact
source/artifact/recipe/provenance/SBOM/review digests, and registration
recomputes the review handle so any changed commit, artifact or declared
contract requires a new approval. `LoadStoredOCIArtifact` performs local Linux
Docker-only materialization from a verified CAS object.

`WorkloadBudget` supplies FIFO admission, cancellation, touch and idle reclaim
for active workload accounting. `RuntimeSecurityProfile` is a small pre-start
fail-closed contract rejecting host network/PID, privileged mode, host mounts,
added capabilities, writable rootfs, missing NNP/seccomp and unbounded
CPU/memory/PID/timeout/output values. The generic controller adapter is now
implemented and tested at its contract boundary; its live MCP path intentionally
fails closed when stock ToolHive does not advertise the required create-time
controls. The existing split-container synthetic ToolHive proof remains evidence
for that boundary and is not upgraded to public-MCP runtime evidence.

Latest `just check` passed at 85.06% total Go statement coverage, with
race tests, format, vet, staticcheck, docs and actionlint green. The four public
build subtests were run separately because anonymous GitHub API rate limits
produced intermittent HTTP 403s in combined repeats. `just security` still
passed (govulncheck and npm audit); the broader repository safety scan remains
a separate non-green gate. No credentials, login or publication were
performed.

## Generic controller adapter — 2026-09-16

Added `internal/toolhub/generic_controller.go` and the operator entry point
`hubctl connector generic-controller`. The adapter accepts only a trusted
definition and an exact immutable controller plan, allocates a per-workload
internal network and egress proxy volume, passes credential references through
ToolHive's environment secret provider, and returns a loopback endpoint in the
admission receipt. It has FIFO budget/idle cleanup, rejects generic host binds
and shared credential-bearing workloads, and removes partially-created Docker
resources on failure. Docker inspection checks image reference, non-root user,
read-only root, NNP, cap-drop, seccomp, limits, private network and named proxy
volume before admission. The gateway now accepts this receipt endpoint for
credential-free container bindings.

The adapter checks `thv run --help` for create-time security controls before
starting any MCP. Stock ToolHive v0.48.0 does not advertise the complete set,
so the production path fails closed before creating a workload; the successful
unit fixture only verifies the adapter contract. Stateful generic mounts
likewise remain intentionally disabled until a named-volume handoff is
implemented.

The adapter now has an explicit `docker_fallback` mode for the local MVP. When
stock ToolHive lacks those flags, a reviewed artifact is created directly with
Docker's pre-start profile, a private companion bridge, and an
internal-network-only MCP container. A separate fixed-target relay container
publishes a loopback port (Docker Desktop does not publish ports from
`--internal` networks) and forwards only `/mcp`; ToolHive remains the remote
proxy and lifecycle API. Per-binding credentials are validated from the owner's
`credentials.env` and handed to Docker with `--env-file`; state uses
per-workload named volumes. Shared stateful or credential-bearing definitions
remain denied. The fallback uses an isolated ToolHive state directory, a
generated bridge token, and the immutable image entrypoint only as literal argv.
Unit coverage proves the complete admission/cleanup command sequence and deny
paths.

A live Docker Desktop acceptance probe then admitted the bounded artifact,
completed MCP `tools/list` and `tools/call` through ToolHive v0.49.0, and returned
the expected read-only catalog result. The probe used a synthetic, credential-free
`hermes-hub` artifact; it is runtime/control-plane evidence, not public Serena or
Context7 artifact evidence. The relay and all temporary containers, volumes,
networks, ToolHive state and processes were removed after the run.

## Serena through ToolHub into Hermes — 2026-09-16

This is a separate compatibility proof for the already-running Serena process;
it does not bypass the generic ToolHive admission gate above. Serena 1.5.3 was
started without credentials on a temporary private project over streamable HTTP.
An authenticated ToolHub endpoint projected all 28 Serena tools. The Hermes
0.21.0 container then ran `hermes mcp test toolhub` against that endpoint and
reported `Connected` and `Tools discovered: 28`. A second request used the same
ToolHub endpoint to call `hub-serena-get-current-config-94c53b8d`; the response
contained Serena's configuration and version `1.5.3`. The backend transport
pins MCP protocol `2025-11-25`, which is required for this Serena release.

The synthetic ToolHub token, temporary Hermes volume and both local processes
were removed after the run; no test files were written to the repository. This proves Hermes visibility
and an end-to-end read-only call through ToolHub; it is not evidence that stock
ToolHive 0.48.0 can yet enforce the requested generic container policy.

## Product-gap closure — 2026-09-16 evening

Shipped code now fail-closes three remaining product gaps. These rows are
implementation and unit/integration fact, not a mock-as-passed upgrade of the
public MCP Hermes chain.

| Requirement | Evidence | Status |
|---|---|---|
| Trusted registration needs a confirmed tool contract | `AttachConfirmedToolContract` + `hubctl artifact approve --contract`; claimed `ArtifactImportConfig` tools cannot register; mismatch fails | implemented; unit + CLI |
| Windows `hubctl.exe` as Linux bridge | `ValidateLinuxBridgeBinary` rejects PE/`.exe`; `hubctl artifact linux-bridge` produced a 18 964 705-byte ELF (`7F 45 4C 46`); current Windows executable rejected | implemented; live Windows build |
| BuildKit policy | Named `rootless-buildkit-bootstrap-v1`; SETUID/SETGID/`systempaths=unconfined` builder-only; MCP `RuntimeSecurityProfile` rejects unconfined seccomp and SETUID | implemented; unit |
| MCP vs relay forwarding secret | Fallback MCP create has no `HERMES_BRIDGE_TOKEN`; relay holds it and authenticates; inspect fails if MCP env contains the token | implemented; unit |
| 95+5 unique workloads | Shipped `startDockerRemoteFallback` ran 100 plans: 95 distinct env-files + 5 shared owner env-file; unique names; values never in CLI args | command-hook of shipped start, not 100 live MCP stacks |
| Real Docker isolation | `TestRealDockerFallbackNamesAndRuntimeProfile` PASS 51.10s on Docker 29.4.3: 100 unique named volumes, two live non-root/ro/NNP/cap-drop containers, cross-volume denial, privileged+host-net+host-pid+docker.sock rejected; restart onto the same volume kept `/state`; removing one workload left the other marker | live Docker |
| Sequential 100 Docker bindings | `TestRealDockerSequentialHundredBindings` PASS 304.21s: 100 unique networks/volumes/containers created one at a time; 95 distinct `credentials.env` + 5 shared; TOKEN values never in Docker argv; first workload restarted onto the same volume | **passed** sequential unique names; not 100 concurrent MCP+ToolHive stacks |
| FIFO/idle cleanup | Shipped budget blocks a second acquire at max_active=1; idle cleanup removes the workload | unit of shipped controller |
| Serena/Context7/Go/Rust SHA→fallback→Hermes | 2026-09-16 live `hubctl connector generic-controller` `docker_fallback: true` + stock ToolHive 0.49.0; inspect uid 10001, ro-root, cap-drop ALL, NNP, 1 CPU / 512MiB / 64 PID; Hermes 0.21.0 in `hermes-hub:0.3.0-runtime` `mcp add` + `-z` `tool_search`/`tool_call` | **passed** (logs under implementer scratch `runtime-*.log` / `hermes-*-call.log`) |
| Live tools/list and tools/call for those four | Serena 29 tools, `mcp__serena__initial_instructions` body “You have semantic coding tools”; Context7 2 tools, `mcp__context7__resolve_library_id` returned `/reactjs/react.dev`; Go 14 tools, `mcp__go_filesystem__list_allowed_directories` `/state`; Rust 24 tools, `mcp__rust_filesystem__list_allowed_directories` `/state` | **passed** |
| Companion `server/discover` hang on Rust stdio | Go SDK v1.7 discover RPC; rust-mcp-filesystem ignores it. Shipped companion synthesizes JSON-RPC -32601. `TestDiscoverFallbackUnblocksLegacyStdio` + live Rust Hermes call | **passed** |
| 100 live MCP+ToolHive stacks | Not run concurrently; Docker Desktop IPAM cannot hold 100 internal networks here | **unverified concurrent stacks**; sequential 100 unique Docker networks/volumes/containers passed |
| Same-UID control/proxy `/proc` secret | Diagnostic session 3163: sibling with MCP UID read ToolHive forwarding-secret marker from `/proc/<control-pid>/environ`. Production fallback keeps the token off the MCP container (relay authenticates). | **failed** for same-UID sibling; MCP-only miss is not enough |
| `just check` after CLI approve/import coverage | 2026-09-16: race, format, vet, staticcheck, docs, actionlint; own Go statement coverage **85.03%** | **passed** |

The four-public-MCP Hermes chain is live for list+one safe call. It is not a mock. Caller catalog (`spaces/<user>` files/skills) is still not mounted; tracked as GitHub issue #103 vs catalog-default #81.

## Stage closeout — 2026-09-16 night

Owner accepted the scale model: 100 users means 100 registered bindings and a
bounded number of live slots (`max_active` + FIFO), not 100 concurrent
MCP+ToolHive stacks on Docker Desktop. Sequential 100 unique networks remains
the capacity proof on this PC.

`TestRealDockerCloseoutImageReuseSecretAndRevoke` PASS 11.20s:

- two MCP containers reused one `python:3.12-slim` image id with distinct
  container ids, volumes and `TOKEN` env-files
- relay held `HERMES_BRIDGE_TOKEN`; MCP env, logs and in-container `/proc`
  walk reported `mcp-secret-inaccessible`
- shipped `Gateway.call` reached the Docker HTTP backend once; after
  `SetBindingStatus(Revoked)` the next call was denied and the backend was
  not invoked again

Same-UID sibling inside a ToolHive PID namespace remains a rejected
one-container diagnostic, not the production fallback. Caller catalog grant
is #103 (M5.2+). VPS remains deferred. No new Go/project dependencies.

CHG-0025 is **done**. Next: M5.2 user binding journey (bind / project / revoke).
