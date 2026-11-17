---
description: Local sandbox validation evidence and remaining acceptance limits.
last_verified: 2026-11-17
---
# Local verification

The [spike](spike.md) and ADR-0103 were committed as e092c9c6 before code changes.
Implementation stays local; no remote source/image publication or CI acceptance.

Named regression coverage: runtime selection/availability and absence of fallback,
private relay alias validation/collision, actual supervisor launch arguments,
persisted selection refusing warm changes, missing/mismatched Kubernetes class,
script isolation, operator cell runtime configuration and real Helm rendering.
Test-critic self-review (retained authoring context) traced entrypoints, forbidden
effects and independent expected values; this is not independent review.

Local just check, stack/devcheck affected-package tests and scoped vet passed.
The first ToolHub package run failed while compiling its Linux bridge because
Windows denied compile.exe execution. Its named regression and subsequent full
package run passed (131.690 s). Supervisor final package passed (119.966 s).
Opengrep's bundled generic rules reported no findings/parse errors on the selected
production files; this limited rule pack does not replace CI's full Go/security
analysis. No Go dependency changes were needed.

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

Managed Hermes acceptance and cleanup are pending. QEMU guest-agent readiness
failed locally; Cloud Hypervisor cell PoC and its one external egress timeout
remain qualified exactly as recorded in the spike. Neither mocks nor names in
Docker/RuntimeClass metadata prove that a host administrator configured a real
sandbox. Full CI, race/shuffle/coverage and deployment acceptance remain remote gates.
