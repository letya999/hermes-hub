# CHG-0321 validation

- `go test -short -run 'Kubernetes|Sandbox' ./internal/supervisor/` — pass
- `go test -short ./internal/supervisor/ ./internal/runtime/` — pass
- `TestKubernetesPodContractHasNoSubPath`: zero `subPath`/`subPathExpr` on
  runtime and relay Pods, no relay init container, whole-volume mount table
  (`/persist`, `/persist-ro`, `/private`, `/config`, `/run/model-config`,
  `/run/hub-isolation`, `/run/inputs`) and env redirects
  (`HUB_STATE`, `HERMES_HOME`, `HUB_WORKSPACE`, `HUB_MODEL_USAGE_FILE`).
- Kind+KVM live requalification under runc/runsc/kata: pending.
