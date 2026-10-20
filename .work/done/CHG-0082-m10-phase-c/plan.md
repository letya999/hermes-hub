# CHG-0082: M10 Phase C — operability

Worktree `hermes-hub-m10a`, branch `feat/m10-phase-a` (continues from 1d871ca).
Scope from the M10 audit, phase C: mock channel, declarative apply, image
build discipline.

## Mock channel

A fake chat that pushes real messages through the same gateway pipeline —
inbound staging, job queue, routine occurrences, outbox delivery — with zero
external APIs. Purpose: end-to-end acceptance of phases A/B features before
they hit Telegram.

- Channel name `mock`. Envelope `mock-<chat_id>` via identity.MockEnvelope.
- `POST /v1/mock/message {chat_id, text}` — control-auth; injects a job for
  the caller's user through the same enqueue path (ResolveTask +
  enqueueChannelJob). Idempotent per caller-supplied id.
- deliverOne case `mock`: captures the delivery (text, buttons, artifact
  refs + blobs) to `spool/mock-sent/` durably instead of calling Telegram.
- `GET /v1/mock/outbox` — lists captured deliveries for the caller's
  principal only.
- Routine/occurrence/schedule validation extended to channel `mock` so
  reminders/watchers/checks/digests/scripts are exercisable end-to-end.

## apply -f

`hubctl apply -f config.yaml --spool <dir> --user <id>` — declarative
desired state on the local spool (same direct-spool access as `hubctl
routine`; no HTTP needed since hubctl is an operator tool). v1 scope:
routines (phase-B kinds with probe/expect; kind=check refused — it needs
the live baseline probe only the hub API performs) + staged knowledge
files under spool/knowledge/<user>/.
Idempotent: same file applied twice reports `unchanged`; a divergent spec
for an existing id is a loud conflict, never silent mutation. Deletion of
resources not listed is out of scope for v1 (apply adds/updates only).

## Image discipline

Verifiable, not documentary: `devcheck.BuildKitEnv` is the single source
of forced-BuildKit env shared by `devcheck.streamDocker` and
`hubctl.dockerCLIEnv` — previously two drifting literals. Tests pin the
exact variable set, the build-arg shape, cache-flag forwarding, the 8 GB
cache cap, keep-set and residue-only cleanup (the latter two already
covered by docker_clean tests).
