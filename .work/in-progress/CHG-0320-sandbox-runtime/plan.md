---
description: M9 sandbox runtime spike and optional isolation selection.
last_verified: 2026-11-17
---
# CHG-0320 — sandbox runtime

Owner scope: #394 spike before #202 implementation; runc remains the default.

1. Evaluate runsc/systrap and Kata QEMU/Cloud Hypervisor against cells:
   cellinit, read-only rootfs/helper/skills, writable workspace, tmpfs, canaries,
   bounded resources and egress. Record comparable measurements and blockers.
2. Accept a decision memo before implementation. Describe each real trust boundary.
3. Reuse existing cell runtime selection; extend the space Compose service,
   Docker supervisor and Kubernetes space Pods. Reject missing runtimes/classes
   explicitly without fallback. Cover configuration, adoption and negative paths.
4. Verify real runsc locally in an isolated dev Docker daemon, plus managed
   runtime health/mount/network contracts. Kata live requires an accessible KVM
   node; otherwise verify selection/failure logic and report that limit.
5. Run named regressions during edits, then just check and affected package tests;
   record source SHA, evidence and unavailable remote gates. No remote publication.

Touch points: stack deployment configuration/render/rights classification,
supervisor Docker launch and ownership verification, Kubernetes Pod class,
Helm values/schema/env, existing cell selection/tests, operator and threat docs.
Persistent space data and upstream Hermes are not modified by the spike.
