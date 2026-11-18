# CHG-0317: portable control-plane Helm chart (M9 #352)

## Goal

Package communication-hub, supervisor, ToolHub, credential-broker and cliproxy
without deployment-specific registry, storage, ingress or secret-store assumptions.
Owner narrowed #352 to these five services. Keep the runtime Pod backend (#58)
separate; no Docker socket, privileged Pod or hostPath compatibility shortcut.

## Read before coding

README.md, docs/index.md, docs/architecture.md, docs/operations.md,
docs/services/supervisor.md; issue #352 and milestone 7; current Compose and
service entrypoints. Owner authorized local integration of the M9 topic branches
before continuing. The original checkout's dirty IAM/provisioning work is untouched.

## Slices

1. Integrate current remote dev with local dev/M30, egress, observability and IAM;
   retain both sides of concurrent gateway/supervisor changes.
2. Add deploy/helm/hermes-hub: five single-writer Deployments, namespaced Services,
   retained/existing PVCs, private Secret materialization, opt-in ExternalSecrets,
   restricted ingress, per-service NetworkPolicy and authenticated ServiceMonitors.
3. Add TestHelmChartContract in internal/devcheck for rendered security, isolation,
   wiring, opt-ins and invalid values. Add explicit just helm-check; operator runbook.
4. Attempt isolated kind acceptance with synthetic data and source-built images;
   authenticate /v1/status and /v1/metrics, verify persistence and public API egress.

## Red-team checklist

- No host mounts, privilege escalation, service-account API token or secret literals.
- No unauthenticated metrics; ingress cannot select internal control routes.
- Single writer/Recreate, release-local selectors, PVC retention, independent broker recovery.
- Network policy has no unrestricted all-port/world egress; DNS and dependencies explicit.
- An empty runtimeClassName omits the field; a selected class reaches every Pod.

## Acceptance

helm lint/template, Go chart regression and affected merged packages, just check,
just security (merged dependency updates), then kind Helm install and authenticated
status/metrics. A reachable listener is not an end-to-end channel/runtime proof.
Record exact source SHA, commands and limits in state.yaml. No remote publication
or account login is authorized. Do not close #352 without its live evidence.

## Out of scope

Native spawned runtime Pods (#58), per-space model relays, workload-controller and
media releases, controller/CRD installation, real account credentials, HA/replicas,
provider mutations and publishing images or Git branches.
