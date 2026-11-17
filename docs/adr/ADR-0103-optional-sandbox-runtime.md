---
description: Optional OCI sandbox selection without downgrade.
last_verified: 2026-11-17
---
# ADR-0103: Optional sandbox runtime

Status: Accepted 2026-11-17, before #202 implementation.

The [cell spike](../../.work/in-progress/CHG-0320-sandbox-runtime/spike.md) supports
optional runsc/systrap. Kata/Cloud Hypervisor has a local cell PoC; QEMU and full
Kata network acceptance are unqualified. Keep runc default. Missing selected
runtimes/classes must fail without fallback. RuntimeClass handler/node mapping
is operator-owned; a sandbox selection never becomes an omitted Pod class.

Keep all managed mount, admission and network boundaries. Pin only private
space relay aliases for supervised Docker sandboxes to avoid embedded DNS;
missing/ambiguous aliases are fatal. Do not use host networking. Unmanaged and
browser/meet Docker sandbox spawns remain unqualified. Control plane and relays
are separate trusted components.

Persist selection and require stopped compute/settled jobs before a runtime or
class change. Verify before adoption to prevent warm reuse of weaker compute.
Host administrators, runtime binaries, VMMs, network enforcement and exposed
storage remain trusted. Sandboxes do not hide data from the host administrator
or replace application authorization.

Update 2026-11-17: kind+KVM qualification corrected two spike limitations. The
DinD QEMU vsock failure was fixture-specific — both shipped VMMs booted guest
kernel `6.18.56` under containerd, and a runsc managed Pod reported a
`4.19.0-gvisor` kernel, so all three backends now carry kernel-level isolation
evidence rather than class-name metadata. A new Kubernetes limitation replaces
the old one: the memory-backed EmptyDir + `subPath` input contract is invisible
inside a Kata guest, so managed kata Pods fail closed at startup; runsc passes
the identical spec. Kata-in-Kubernetes admission stays an accepted option whose
end-to-end readiness waits on the volume-contract rework, never a silent
downgrade.

Update 2026-11-17 (#550): the `subPath` limitation is removed. The generated
Pod contract now mounts the space PVC once at `/persist` plus a read-only
alias at `/persist-ro` and resolves every PVC-backed runtime root through
env-pointed paths (`HUB_STATE`, `HERMES_HOME`, `HOME`, `XDG_CACHE_HOME`,
`HUB_WORKSPACE`, connection configs, `HUB_MODEL_USAGE_FILE`, skill roots).
Generated inputs project directly from the Secret through dedicated volume
groups (`/config`, `/run/model-config`, `/run/hub-isolation`, `/run/inputs`);
the init container no longer copies private inputs onto a shared volume —
its remaining duties are PVC directory creation, writable-skill staging into
the memory-backed `/private`, read-only workspace-subtree snapshots into
dedicated memory volumes, and symlink layout (`/shared/*`, hermes
`config.yaml`/`SOUL.md`). Relay Pods drop their own `subPath` mounts
(model-state via `/persist`, broker Secret mounted directly) and no longer
carry an init container. Security posture is preserved or improved:
plaintext inputs stay on memory-backed volumes, `readOnlyRootFilesystem`
and non-root/drop-ALL are unchanged, read-only workspace subtrees keep
kernel enforcement, and any mount shape outside the reviewed contract fails
the plan explicitly rather than degrading. The identical spec is expected
to reach Ready under runc, runsc and Kata; live kind+KVM requalification is
the remaining gate before `containerRuntime=kata` is called ready.
