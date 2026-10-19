---
description: Run the supervisor in the existing control image, retaining the Docker CLI and explicit host bind mapping.
last_verified: 2026-10-14
---
# ADR-0040: Single-stack supervisor

Status: accepted direction, 2026-10-14; deployment acceptance pending.

The owner requested one main Docker stack, on-demand user runtimes, and removal
of unnecessary Windows services. This selects a smaller implementation than the
proposal in [ADR-0037](ADR-0037-supervisor-in-linux-container.md); it does not
claim that proposal's HTTP socket adapter or proxy allowlist is implemented.

When execution selection uses `http://supervisor:8876`, the infrastructure stack
renders one supervisor using the existing control image and Docker CLI. The
trusted controller mounts the Docker socket and spaces; user runtimes retain
their existing per-context mounts and credentials. No additional image,
database, daemon, or provider integration is introduced.

Supervisor-to-runtime transport uses Docker DNS. Managed runtimes remain behind
their control relay on the internal control network. Spawned runtime and relay
ports are not published on the host in container mode. Bind sources must stay
under the configured spaces root; Docker Desktop paths are translated at the
Docker command boundary, including ownership verification.

The socket grants host-equivalent authority to this trusted controller. This
change does not introduce a socket proxy or make that authority sandboxed.
Host mode remains supported for debugging and rollback.

Migration requires draining jobs and leases, stopping the previous writer,
backing up state, and removing its verified runtime containers before starting
the container writer: ownership IDs include the state path, which changes
between Windows and Linux. Never run both writers. Remove the Windows scheduled
task only after a container canary and restart pass. Rollback stops the container
writer before restoring host selection and its task.

Implementation and live acceptance are tracked in
[CHG-0097](../../.work/done/CHG-0097-single-stack-supervisor/plan.md).
