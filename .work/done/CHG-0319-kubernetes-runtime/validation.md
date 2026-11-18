# CHG-0319: local verification, 2026-11-17

Code source: `6b7ad876e10d0563016f43d4c1af665a3c193167`, branch
`feat/m9-k8s-runtime`. The checkout was clean when the final runtime image was built.
This is local acceptance; no remote CI run URL exists and nothing was published.

## Required spike

[Operator evaluation](operator-eval.md) preceded implementation in `139a4dcd`.
ToolHive Operator/CRDs 0.66.0 were fetched and rendered, and the pinned MCPServer
API was inspected. Rejected for Hermes runtime spawning because its MCP lifecycle
does not implement the authenticated runtime/generation contract. The operator was
not installed or benchmarked; this does not reject it for connector scheduling.

## Environment and artifacts

- Isolated `hermes-substrate` kind 0.30.0 cluster, Kubernetes 1.34.0, separate
  kubeconfig, local Docker Engine 29.4.3. Default kind CNI disabled.
- Calico 3.33.0, [pinned manifest](https://raw.githubusercontent.com/projectcalico/calico/v3.33.0/manifests/calico.yaml),
  SHA256 `2de8f47595fb9c41b3f47d7b767a1f8e72ecf84057af834738ff12689a234da5`.
  Node and Calico rollout became ready before application acceptance.
- Dedicated restricted control/runtime namespaces. The supervisor used the chart's
  namespaced Role/RoleBinding and a projected rotating service-account identity.
  Runtime and relay Pods received no Kubernetes identity.
- Final image built through scoped `hubctl build --env dev --image runtime`,
  OCI index `sha256:51591b519652b8228d228bfcbf8472f57494252d8f3d83f37521ab50e1157b27`,
  platform manifest `sha256:ebaf7a19f67c48bede91a9a3c90f2f2c7a1130f7ab66ca1e0ca67a9dc6dfeaaf`,
  image config/CRI image ID `sha256:1f5c3e020e90f43e2c3ab337ef685366e585cf8b677fc9e84346522522474059`.
  Both final runtime and relay containers ran this config ID. Existing local
  `hubctl` dev builds leave the OCI revision label `unknown`; the source above is
  recorded from the clean build checkout. This image cannot satisfy the formal
  CI-labelled exact-HEAD image gate. Build ID: `qsb7dpvbhqbpmq34248ecu78b`.
- Real pinned Hermes `869228cab4a8276d3b4c78da9d9939670c47bd0f`, existing Go runtime,
  supervisor and relays. A separately compiled Go acceptance harness exercised the
  production manager/HTTP handlers inside the supervisor-labelled Pod. Final
  harness SHA256 `1beb5163f2b0757b9faac1605e467052b64246f9a151b2926917d13f44ea50df`.
- Synthetic published passports, no real users or provider credentials. Local
  OpenAI-compatible model and authenticated PDP/connector-inventory fixtures stood
  in for external systems. The private model endpoint was explicitly approved by
  its authored `allow_private` field. No authorization or transport guard was disabled.

## Live results

1. Spawned separate real gateway/relay Pods, immutable generation Secrets,
   per-space policies and a dynamically bound RWO PVC. Published/private inputs
   used non-root init copying into an owned memory subdirectory; no hostPath/socket.
2. A synthetic user request passed through the real supervisor HTTP admission,
   gateway, pinned Hermes and model relay. The model fixture checked message
   presence, and the actual run returned its expected completion with a run ID.
3. Suspend physically terminated both Pods and deleted their generation Secrets.
   PVC `space-1e3e03b30335814ac1227e79631ae4af` retained UID
   `7dd606f1-18ff-4b94-b5ff-c4f742658e64`. A workspace marker survived respawn.
4. Logical runtime ID stayed `checker`; verified generations included 4, 5 and 6.
   Generation 4 Pod UID was `3b9fdc76-d6a1-41d6-9fa6-b76518cec931`; final generation
   6 UID was `0cd8e03d-9c23-4d5e-93c5-57728c0f4c85`. No data adoption across scopes.
5. Final gateway used installed `RuntimeClass acceptance-runc` with handler `runc`;
   relays used the default class. This proves class propagation, not a sandbox
   implementation. Earlier empty-class spawns omitted the field.
6. The final observer initially exceeded its five-minute harness context while the
   admitted run remained alive. Supervisor preserved `uncertain`. `/v1/resume`
   observed that same admitted run to completion; no replacement execution was
   submitted. Final phase was `running`, Hermes readiness `ready`, model status
   `ok`, tool surface `ok`, with true Pod/relay readiness conditions.
7. Real supervisor `/v1/status` and `/v1/metrics` returned authenticated 200 and
   unauthenticated 401. Runtime unauthenticated readiness returned 401.
8. On the final image, TCP connections from the gateway to public HTTPS, direct
   core model/ToolHub endpoints, the Kubernetes API and an unrelated reachable Pod
   were denied. Supervisor could reach the unrelated listener as a positive
   control; that Pod could not reach gateway port 8080. Runtime had no API token.

Development DNS forwarded `host.docker.internal`; the unchanged managed preflight
correctly refused it. Only this isolated cluster's CoreDNS was configured to answer
NXDOMAIN for that zone. CNI denies were tested independently of DNS and receipts.
The managed inventory probe also needed a shared fix: its loopback MCP proxy has
no inventory route, so it now observes through the existing authenticated relay.

## Gates and limits

- `just check`: passed, including repository vet and docs/format checks.
- `just test ./internal/supervisor`: passed in 122.853s. An initial concurrent
  four-package invocation exhausted the supervisor's aggregate five-minute Windows
  budget; the isolated package passed without changing its timeout.
- Affected runtime, stack and devcheck package gates passed. Runtime was rerun
  after the final health fix and passed in 42.749s. Helm contracts use real Helm.
- Named Kubernetes isolation/UID/concurrency/hold/restart/backend-switch/node-loss
  regressions and the authenticated inventory regression passed; scoped vet passed.
  These fake-client tests are contract evidence, not the live evidence above.
- `just security`: govulncheck reported no vulnerabilities; browser dependency
  audit reported zero. Staged diff Gitleaks reported no leaks.
- Local SAST is incomplete: Opengrep's seven configured rules found no matches,
  with partial parsing on eight Helm/template targets. Installed gosec cannot
  decode Go 1.27 export data (version 5 versus supported 4). Its output and the
  broad safety profile's baseline/ignored-fixture findings are not acceptance.
  CI owns full current-toolchain SAST, race/shuffle, coverage and image gates.
- Windows' intermittent freshly-linked executable refusal was retried through the
  repository's existing testexec wrapper. Probity/hooks stayed enabled.

Real channel/provider accounts, broker delivery into runtime relays, alternate
sandbox handlers, multi-host capacity and live node partition/fencing were not
validated. Unknown termination is covered by negative unit tests; it never releases
capacity or advances generation. Kubernetes scope is the ordinary managed gateway;
extra modes, browser/meet, Docker script jobs and external host mounts fail closed.

Final suspend again left only the retained synthetic PVC and unrelated test Pod.
Cluster teardown and image/cache cleanup are recorded in `state.yaml`. All raw
diagnostics, fixtures and temporary credentials remain under ignored `.work/tmp/`;
none are part of the changeset.
