---
description: Local sandbox validation evidence and remaining acceptance limits.
last_verified: 2026-11-17
---
# Local verification

The [spike](spike.md) and ADR-0103 were committed as e092c9c6 before code changes.
Remote acceptance on the draft PR is recorded at the end of this document.

Named regression coverage: runtime selection/availability and absence of fallback,
private relay alias validation/collision, actual supervisor launch arguments,
persisted selection refusing warm changes, missing/mismatched Kubernetes class,
script isolation, operator cell runtime configuration and real Helm rendering.
Test-critic self-review (retained authoring context) traced entrypoints, forbidden
effects and independent expected values; this is not independent review.

Earlier local `just check`, stack/devcheck affected-package tests and scoped
vet passed. The first ToolHub package run failed while compiling its Linux
bridge because Windows denied compile.exe execution. Its named regression and
subsequent full package run passed (131.690 s). Supervisor final package passed
(119.966 s). Opengrep's bundled generic rules reported no findings/parse errors
on the selected production files; this limited rule pack does not replace CI's
full Go/security analysis. No Go dependency changes were needed.

After the managed-live additions, `go test -short ./internal/devcheck`,
`just test ./internal/devcheck` and `go vet -p=2 ./internal/devcheck` passed.
An early `just check` reached the docs gate and failed on pre-existing stale
`.work/index.md` links to dozens of absent `in-progress` CHG plans; those rows
were repointed to the `done/` plan locations and the gate now passes.

## Additional real Docker observations

The runsc readonly toolbox image mount passed on the fixture daemon. A 128 MiB,
0.5 CPU script profile with 16 host PIDs failed to start even a shell under runsc;
64 PIDs passed. The script path therefore uses a bounded 64 host PIDs only for
runsc, retaining 16 for runc. Cells keep requested policy limits unchanged.

Real supervisor admission was exercised with runsc removed from the isolated
daemon registry: it rejected the spawn with the specific missing-runtime error
before any runtime/control-relay start. Restoring registration enabled subsequent
positive testing. A Go fixture executable repeatedly overwritten on a Windows
bind mount produced corrupt executable views; copying the completed binary into
the daemon's Linux filesystem fixed the fixture. No production workaround added.

## Managed live acceptance

`internal/devcheck/managed_supervisor_integration.go` is parameterized by
`HUB_CONTAINER_RUNTIME`; it asserts the spawned Docker `HostConfig.Runtime`,
keeps primary, secondary and respawned control relays on `runc`, rejects
host-gateway shortcuts and requires the reviewed
`toolhub`/`model-relay`/`egress-relay` aliases.

The canary's first gate is now a real mock-channel run: it starts a production
`communication.Gateway` (durable spool, dispatch workers, delivery loop) in
supervised mode against the in-process supervisor handler with a dead Telegram
endpoint, posts `/v1/mock/message` with a stable `client_id`, and the admitted
`mock-…` job itself cold-spawns the managed runtime through supervisor
`/v1/jobs`. The durable `mock-sent` receipt and `mappings/<job>.json` must show
`completed` with nonempty run/session identities and matching
principal/task/channel/chat; re-submitting the same `client_id` must return the
same job/task and produce exactly one delivery file. The Hermes reply runs
through the model-relay SSE fixture. Lifecycle assertions then reuse the
spawned runtime: workspace marker, second-space alias/network parity, marker
persistence and selected-runtime retention after reap/respawn, plus teardown.

On the isolated Docker daemon with exact image revision
`590d352e7cdf63dfb509ecd1795206a3a7184f69`, the strengthened full managed
canary — mock-channel gate included — passed for `runc`, `runsc` and `kata`
(`mock-a012…`→`run_1e08…`, `mock-3bcf6…`→`run_f16a…`, `mock-d001…`→`run_c2a8…`;
all `completed`). The Kata result is Cloud Hypervisor on this KVM fixture, not
QEMU. Selecting `kata` on the main daemon, where that runtime is not
registered, failed closed through the same real channel: supervisor `/v1/jobs`
answered `503 runtime unavailable` with `X-Hub-Admission: not-dispatched` on
every attempt, the gateway retried within the four-minute cold-start budget and
the job ended `failed` (named outcome, not uncertain). The canary exited with
`mock channel admission: mock channel job terminated without delivery: failed`;
no runtime or relay container was spawned and no `runc` fallback was attempted.

On the isolated kind fixture, `substrate-runtimes` scheduled the managed runtime
Pod with `runtimeClassName=runsc` while the relay Pod stayed unclassified. The
runtime passed `/readyz` and completed a real `/v1/execute` marker request with
`kubernetes runtime received message`. A reap/respawn produced `gen-4`, again
with `runtimeClassName=runsc`; relay remained on the default runtime.

The purpose-built publisher twice saw `HTTP 200` followed by
`uncertain/run.unknown` after stream loss. Production fail-closed behavior kept
the runtime busy and refused teardown while the business outcome was
unconfirmed; observing the exact run through runtime control returned
`completed`. `ExpireUncertain` then settled `gen-3`/`gen-4` through the durable
cancel/observe path, `Reap` removed compute Pods and generation Secrets, and the
publisher's `suspend` stage passed. PVCs and NetworkPolicies remained only as
the persistent space substrate by design; fixture cleanup then deleted those
synthetic objects, leaving `substrate-runtimes` empty.

A separate `missing` context with `container_runtime=kata` and no `kata`
RuntimeClass returned `runtime Pod admission failed; verify selected
RuntimeClass and namespace policy`. The supervisor state stayed stopped and no
runtime/relay Pod or Secret survived. Its durable PVC/NetworkPolicy substrate
was intentionally retained by the manager and removed during fixture cleanup.

The provider fixture only accepts requests containing
`k8s-user-message-acceptance`; auxiliary model calls without that marker return
HTTP 400 and surface as model-relay `model_route_unavailable`. That fixture
constraint explains transient auxiliary failures and short-lived uncertainty;
it is not evidence of a runsc isolation failure.

QEMU guest-agent readiness failed locally; Cloud Hypervisor cell and managed
Docker results remain qualified exactly as recorded in the spike. Full CI,
race/shuffle/coverage and deployment acceptance remain remote gates.

## Kubernetes sandbox-runtime live qualification (kind + KVM)

The earlier limitation — RuntimeClass names in metadata do not prove a real
sandbox — is closed on this fixture. The WSL2 host exposes nested KVM, so the
kind node received the full Kata install from the DinD image: containerd
handlers `kata` (QEMU via the default configuration) and `kata-clh` (Cloud
Hypervisor via `configuration-clh.toml`), plus RuntimeClasses `kata` and
`kata-clh` alongside the existing `runsc` class.

Probe Pods under both VMMs reached `Running` and reported guest kernel
`6.18.56` while the node kernel is `6.6.87.2-microsoft-standard-WSL2`; a guest
`uname` that differs from the host kernel proves a real VM boundary, not a
relabeled runc. The runsc managed Pod reported `4.19.0-gvisor` — gVisor's
synthetic kernel — so all three backends now have kernel-level isolation
evidence, not just `runtimeClassName` assignment.

A published managed space admitted through the supervisor with
`ContainerRuntime: runsc` produced a runtime Pod `Running`/ready serving real
HTTP on the runtime port, relay Pod unclassified as designed. The identical
space with `ContainerRuntime: kata` was admitted to a Pod with
`runtimeClassName=kata` — VM booted, `private-inputs` init ran, the runtime
container executed inside the guest — then exited `1` with
`managed runtime passport activation mismatch`. The mounted `/config/revision`
input is delivered through a `Medium: Memory` EmptyDir populated by the init
container and mounted via `subPath`; under Kata that write happens inside the
guest and the host-resolved `subPath` bind observes an empty directory, so the
activation file is absent in the runtime container. The identical spec passes
under runsc, so this is a real Kata substrate incompatibility in the current
volume contract — surfaced fail-closed (Pod `Failed`, never ready, no runc
fallback), not silent.

Consequence: `kata` in Kubernetes is now proven to reach real VM isolation but
is blocked end-to-end by the Memory-EmptyDir subPath input contract. Remediation
is a concrete follow-up — drop `Medium: Memory` on the `private`/`tmp` volumes
or replace `subPath` mounts with projected volumes for Kata-class backends —
tracked below rather than silently worked around.

## Remote acceptance (draft PR #549)

Draft PR <https://github.com/letya999/hermes-hub/pull/549> on branch
`test/m9-sandbox-mock-channel` was validated without merging into `dev`. The
first run exposed gate debt accumulated by the unpublished M9 wave: stale
`.work/index.md` links (fixed by repointing rows to `done/` plans), staticcheck
findings across merged M9 files, five gosec findings (fixed with a relay-file
base-name guard, a RuntimePort ≤65535 bound and per-site `#nosec`
justifications), and merged coverage of 84.48% against the 85% floor (restored
with regression tests for fail-closed, authorization, lifecycle, persistence
and malformed-input paths; merged local estimate 85.09%).

Full workflow run <https://github.com/letya999/hermes-hub/actions/runs/44075592994>
on source SHA `b78d6bcf` completed `success`: lint (staticcheck + gosec),
coverage floor, all four checks shards, docs-config, security, gitleaks,
dependency-review, credential-broker, every image build and all twenty
container-gates (dev/prod × gateway/smoke/network/scratch/native/capabilities/
m32/supervisor/bench/managed). `main-source` is skipped on non-main branches
by design. The PR remains an open draft; no merge into `dev` was performed.

Follow-up run <https://github.com/letya999/hermes-hub/actions/runs/44079004907>
on head `a920b3f3` (this evidence commit) also completed `success`. Two
container-gate flakes were retried to green: `prod, smoke` failed once on a
ghcr.io blob pull (`connection reset by peer`), and `prod, m32` needed two
retries for timing-sensitive m32 integration tests
(`TestM32RealLifecycleMemoryIsolation`, then
`TestM32FiveManagedSessionsExecuteConcurrently` — a run making zero attributed
memory searches inside the observation window). Neither failure traces to
this changeset; the m32 suite's prod timing sensitivity is pre-existing.
