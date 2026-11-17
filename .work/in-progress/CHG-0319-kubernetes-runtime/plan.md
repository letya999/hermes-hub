# CHG-0319: Kubernetes runtime backend (M9 #58)

## Goal

Add Kubernetes beside Docker as an optional execution substrate for isolated
spaces; demonstrate real runtime spawn, suspend, respawn and message delivery.

## Read before coding

README.md, docs/index.md, AGENTS.md, supervisor lifecycle/ownership/phase code,
Helm chart, issue #58 and CHG-0035 audit. Required operator evaluation precedes
implementation: see operator-eval.md and ADR-0101 for the accepted boundary.

## Slices

1. Pin/render ToolHive Operator and CRDs; decide adopt/reject. Complete.
2. Add compute interface with existing Docker behavior as default; Kubernetes
   Pod/PVC/Secret/NetworkPolicy driver, stable scope identity and UID fencing.
3. Wire config/CLI/Helm RBAC and runtime class; map Pod observations to existing
   phase/conditions, document storage migration, network and recovery operations.
4. Add isolation, ownership, cancellation/hold, restart and bounded-input regression
   tests. Review tests before execution; run scoped vet and named regressions.
5. Run isolated kind acceptance with synthetic data and real Hermes runtime image;
   record exact source/images and message receipt. Clean test infrastructure.

## Red-team checklist

- No Kubernetes credentials in runtime Pods or diagnostic dumps.
- No cross-space host/PVC access, secret literals, or unverified object adoption.
- UID-preconditioned deletion; preserve PVC on suspend and uncertain failures.
- No Docker fallback or changed local default; no competing runtime controllers.
- Pod Ready alone is not Hermes readiness or successful user-message execution.

## Acceptance

Local implementation and acceptance are complete; see [evidence](validation.md).
The change stays in progress until review, remote CI and merge, per status hygiene.

The implemented slice is the managed gateway used by normal messages. Extra
runtime modes, browser/meet and Docker script jobs fail closed on Kubernetes.
Live testing also found a shared health-probe bug: managed MCP uses a loopback
proxy, while connector inventory belongs to the reviewed ToolHub relay. The
regression keeps inventory authorization and fixes the observation path.

just check, affected-package just test, dependency security gate, Helm contracts;
kind spawn/suspend/respawn with stable runtime identity, retained data, authorized
message delivery and negative scope checks. Full CI and real external provider
acceptance require published source/configured infrastructure, never mocks alone.
