---
description: Issue #550 — replace subPath-based runtime Pod contract for Kata compatibility.
last_verified: 2026-11-17
---
# CHG-0321 — kata pod contract

Owner scope: #550. The generated Kubernetes space Pod must not rely on
`subPath`/`subPathExpr` anywhere so the identical spec reaches Ready under
runc, runsc and Kata (`kata-clh-runtime-rs`, `kata-qemu-runtime-rs`).

Design:

- The space PVC mounts once as `/persist` (rw) and `/persist-ro` (ro);
  every PVC-backed runtime target resolves through env-pointed paths instead
  of per-subtree mounts.
- Generated inputs project into the same Secret consumed by four volume
  groups: `inputs` at `/run/inputs`, `cfg` at `/config`,
  `model-config` at `/run/model-config` and `isolation` at
  `/run/hub-isolation`. No init copy of private inputs remains.
- `config.yaml`/`SOUL.md` inside the managed Hermes home become symlinks on
  the PVC pointing at the read-only projection; `managedPreflight` keeps
  asserting non-writability through the link.
- `/skills-effective-rw` (the only writable generated target) is copied by
  the init container into the whole-volume `private` memory emptyDir.
- Declared `/shared/*` mounts resolve through a writable `/shared` view
  directory populated with symlinks; workspace `read_only` subtrees and any
  non-env non-`/shared` target fail the plan explicitly.
- The relay Pod drops its own `subPath` mounts: model state rides
  `/persist-ro`, broker credentials mount the profile Secret directly, and
  the relay init container is removed.

Acceptance mirrors #550: zero subPath asserted in plan tests, identical
space Ready under runc/runsc/kata on the kind+KVM fixture, PVC content
verified inside the guest, inputs read-only and memory-staged, missing
RuntimeClass fails closed, `just check` green.
