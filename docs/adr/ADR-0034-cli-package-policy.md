---
description: Explicit CLI package installation in private cells and atomic host catalog enablement.
last_verified: 2026-10-10
---
# ADR-0034: Private package installation and operator catalog binding

Status: accepted, 2026-10-10, explicit owner instruction to finish all six gaps.

Keep the existing networkless scratch terminal and CLI cell executor. Do not
mount Hermes home to support package managers. Grant a private executable
tmpfs home only for non-stateless private cells when both immutable workload
and operator registry settings request it. Registries expand the controller
ceiling, while every definition retains an exact CONNECT allowlist.

Trust shipped CLI definitions by complete compiled digest. Add one trusted
host command that commits catalog binding and managed profile permissions
atomically, with explicit ceiling extension and no new permissions in sibling
profiles. Model-facing controls cannot invoke this host operation.

The private home is disposable and bounded, so installation does not survive
cell release. Package managers still need compatible interpreters in their
reviewed image. Native terminal remains an explicit broader permission.
