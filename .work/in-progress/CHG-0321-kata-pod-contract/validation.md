# CHG-0321 validation

- `go test -short -run 'Kubernetes|Sandbox' ./internal/supervisor/` — pass
- `go test -short ./internal/supervisor/ ./internal/runtime/` — pass
- `TestKubernetesPodContractHasNoSubPath`: zero `subPath`/`subPathExpr` on
  runtime and relay Pods, no relay init container, whole-volume mount table
  (`/persist`, `/persist-ro`, `/private`, `/config`, `/run/model-config`,
  `/run/hub-isolation`, `/run/inputs`) and env redirects
  (`HUB_STATE`, `HERMES_HOME`, `HUB_WORKSPACE`, `HUB_MODEL_USAGE_FILE`).
- Kind+KVM live requalification: passed (details below and in CHG-0320).

## Kind+KVM live requalification (2026-11-17)

Fixture: kind `hermes-sandbox` on WSL2 nested KVM (`/dev/kvm` present);
supervisor runs inside the kind node (`docker exec`) with
`HUB_EXECUTION_BACKEND=kubernetes`, `HUB_KUBERNETES_NAMESPACE=substrate-runtimes`,
`HUB_KUBERNETES_CONTROL_NAMESPACE=substrate-control`,
`HUB_KUBERNETES_CONTROL_RELEASE=local` and the control-plane service URLs.
Synthetic space `checker` (copy of the published `artem_two` space renamed so
the passport identity matches the fixture ToolHub's decide contract).

One real defect found and fixed in this rework: the roview init step ran
`chmod -R a-w` on the read-only EmptyDir mountpoint itself, which is
root-owned while the init container runs as uid 10001 — EPERM, init failed,
runtime never started. Fix (2f68e845): chmod only descendants via
`find -mindepth 1`; the mountpoint stays read-only through
`VolumeMount.ReadOnly`. Every earlier failed kata/runsc init attempt traced
to this, not to the mount model.

runsc (`HUB_CONTAINER_RUNTIME=runsc`):
- Pod `runtimeClassName=runsc`, runtime 1/1 Ready, relay 3/3 Ready.
- `/readyz` 200 with runtime bearer, 401 without; `/v1/execute` 401 without.
- Real `/v1/execute` → `status:completed`,
  `text:"kubernetes runtime received message"`
  (job `fx-1794940337554934263`, run `run_dd3377c8cc9a4d9d8ad5e8ab1b165047`,
  gen-4).
- PVC in guest: file written inside the guest at
  `/persist/managed/dev/workspace/` landed in the local-path PVC backing dir
  `/var/local-path-provisioner/pvc-b608d942-…_space-1e3e03b3…` on the node.

kata-clh (`HUB_CONTAINER_RUNTIME=kata`,
`HUB_RUNTIME_CLASS_NAME=kata-clh`):
- Pod `runtimeClassName=kata-clh`; guest `uname -r` = `6.18.56` vs node
  `6.6.87.2-microsoft-standard-WSL2`; `cloud-hypervisor` process visible on
  the node (`/opt/kata/bin/cloud-hypervisor`) — real VM boundary.
- `/readyz` 200 auth / 401 noauth; `/v1/execute` → `completed` with the
  provider's marker text (run `run_f12566313cb14effb64c3bbf3ed01290`).
- PVC persistence across runtime-class switch: the marker written during the
  runsc run was still readable inside the kata guest; a new kata guest write
  landed on the same PVC backing dir.

kata default class (`HUB_RUNTIME_CLASS_NAME=kata`, handler
`io.containerd.kata.v2` — also Cloud Hypervisor in this fixture):
- `/v1/execute` → `completed` (run `run_1369033bd58e4692b1a3cc158ff3d292`).

Negative tests:
- `HUB_RUNTIME_CLASS_NAME=kata-nonexistent` → `503 runtime unavailable`,
  `runtime Pod admission failed; verify selected RuntimeClass and namespace
  policy`, zero pods created, no runc fallback attempted.
- Forged `policy_version` → `503 runtime unavailable`; forged `principal_id`
  → `409 user scope mismatch`; forged `identity_schema: 2` →
  `409 unsupported identity schema`.
- Unsupported `browser` capability in the space passport → fail-closed at
  admission (observed earlier in this fixture).

Fixture-specific observations (not product defects):
- First execute after each cold respawn ended `uncertain/run.unknown` once
  while the embedded Hermes gateway finished its first-run warmup; the
  identical retry completed. Same transient previously recorded for runsc
  in CHG-0320.
- The fixture has no credential broker: the published `models.yaml`
  `proxy_access` blocks were removed and service endpoints repointed to the
  provider fixture service so providers resolve through `key_env`
  (`OPENAI_API_KEY`). The ToolHub decide stub authorizes only principal
  `checker` with the synthetic control token, so the supervisor ran with
  `HUB_SUPERVISOR_AUTH=synthetic-kubernetes-control-0123456789abcdef` and the
  space identity was renamed accordingly. Production wiring is unchanged:
  `HUB_TOOLHUB_CONTROL` still carries `RuntimeAuth` and broker-key
  `proxy_access` still materializes `broker_grants`.
