# SPEC-0069: Ops, admin & delivery plane

Frozen: 2026-10-22. Decision: ADR-0072. Delivery: CHG-0148, CHG-0149,
CHG-0150, CHG-0151, CHG-0152, CHG-0153.

Stage 1 — usage export (issue #195):

- R1.1 `hubctl usage` reads the spool `usage.jsonl` read-only and aggregates
  journaled records per scope and per actor over an operator window
  (`--since`, `--user`, `--model`).
- R1.2 The reader never opens or mutates the spool, tolerates the torn tail
  the writer quarantines, and fails closed on a corrupt completed record.

Stage 2 — declarative space manifest (issue #197, apply half):

- R2.1 `hubctl apply -f` accepts a versioned manifest with an optional `space:`
  block whose `agent:` and `workspace:` subtrees are complete desired
  documents parsed by the same strict validators as the on-disk files.
- R2.2 Every section of the manifest is validated before the first mutation;
  a rejected manifest changes nothing on disk or in the spool.
- R2.3 Re-applying a byte-equal manifest reports each target unchanged and the
  space as already converged.
- R2.4 Secret material is rejected in manifests; `secrets.<env>.env` remains
  the only credential path.

Stage 3 — live job watch (issue #197, watch half):

- R3.1 `hubctl watch` prints phase/status transitions for one job id or one
  scope, sampled from the supervisor control API and/or the local spool at
  least once per second.
- R3.2 Watch never mutates state and exits once a job reaches a terminal
  status.

Stage 4 — agent-image rollout (issue #198):

- R4.1 A per-space `generated/image-pins.json` persists `{current, previous,
  canary}` per environment; spawn resolves dev overlay, then pin, then the
  flag default.
- R4.2 `hubctl image promote` moves the pin only after a recorded smoke pass
  on the exact candidate image and preserves the previous verified image.
- R4.3 `hubctl image revert` restores the previous verified pin in one command
  and re-verifies it with a post-revert smoke.
- R4.4 `hubctl image canary` overrides exactly one context's spawn image; a
  canary failure is confined to that context and surfaced in job status.

Stage 5 — admin diagnostics principal (issue #141):

- R5.1 The enrolled-tokens contract accepts an optional `scopes` list;
  `scopes: ["admin"]` marks the `hermes-admin` bearer. Entries without scopes
  keep working unchanged.
- R5.2 `/v1/admin/*` answers only admin-scoped bearers: cross-principal
  onboarding reads, `diagnose` (phase, error, error code, source ref, pinned
  commit) and `inspect-source` (file list and grep hits over the pinned
  upstream commit).
- R5.3 The admin surface is serialized and rate-limited, every call is
  audit-tagged with the admin principal and the target onboarding, and it can
  neither mutate provider state nor return secret values.
- R5.4 Recipe proposals are response payloads; nothing applies them.

Stage 6 — incident replay (issue #206):

- R6.1 `hubctl replay export` writes a bundle with the durable envelope, the
  prompt when durable or operator-supplied, recorded versions and a redacted
  config digest — never a secret value.
- R6.2 `hubctl replay run` materializes a scratch space, forces the mock
  channel, strips all secrets, and re-admits the job through the real
  admission path against a stub or an operator-supplied replay supervisor.
- R6.3 The replay output diffs the recorded outcome against the replayed one
  (status, reply, tool activity) so the runs are comparable.

Acceptance:

- A1 Re-applying an unchanged manifest is a no-op with an explicit "already
  converged" report; an invalid manifest mutates nothing.
- A2 Watch reflects real transitions within seconds; a terminal job exits the
  watcher.
- A3 The pin never moves to an unsmoked image; revert is one command; a canary
  failure touches only its context.
- A4 A non-admin bearer gets 401/403 from every `/v1/admin/*` endpoint; every
  admin call leaves an audit entry naming the admin principal and the target.
- A5 A replay bundle contains no secret material and a replay cannot deliver
  to a real channel or provider by construction.
- A6 Usage export matches the journaled totals for the window; `just check`
  stays green.
