---
description: hubctl — host-side control CLI: stack lifecycle, builds, spaces, grants, connector controllers.
last_verified: 2026-11-04
---

# hubctl

`cmd/hubctl` — the operator's control binary. It never runs inside the agent
trust boundary: every mutating path is host-side and most state changes require
a human confirmation link minted through the communication gateway.

## Command surface

`hubctl` (no args) prints the authoritative list:

- **Stack lifecycle**: `init`, `org-init`, `up`, `down`, `logs`, `doctor`,
  `render`, `space apply` (compile six authored passports, classified diff,
  publish + reconcile under `--confirm`), `apply`.
- **Models**: `models login` (per-service cliproxy OAuth enrollment +
  `model-grants.json` grant recording).
- **Images**: `build`, `dev-rebuild` (the only sanctioned local build paths —
  they force BuildKit, prune superseded hub images, cap build cache at 8 GB).
- **Spaces/selection**: `execution-audit`, `select-execution`.
- **Control plane**: `supervisor` (also the container entrypoint),
  `connector generic-controller` (workload admission endpoint, :8545),
  `connector local-controller` (fixed Telegram variant).
- **Operator state**: `catalog`, `artifact`, `tools`, `grant`, `governance`,
  `capability`, `context`, `memory`, `skill`, `routine`, `secret`.
- **Ops & delivery** (ADR-0077): `usage` (token/runtime-seconds report over
  `usage.jsonl`), `watch` (job/runtime transitions to terminal), `image`
  (smoke-gated rollout pins: `promote`/`revert`/`canary`/`status`),
  `replay` (sanitized incident bundle `export` + isolated mock-channel `run`),
  `storage` (owner budget `status` + digest-bound `cleanup` dry-run/apply).
  `apply -f` also converges a declarative `space:` manifest.
- **Interactive**: `chat`, `telegram-login`, `meet-auth`, `companion`.

Flags: `--dir spaces/<user>`, `--root .`, `--user`, `--org`, `--env prod|dev`.

## What it writes

`render`/`init`/`up` generate the space compose (`compose.dev.yaml`), config,
and materialized dirs under `spaces/<user>/` — see docs/spaces.md. Nothing in
`generated/` is hand-edited; re-render instead.

## Controller role

`hubctl connector generic-controller` is the workload-admission HTTP endpoint
ToolHub calls (`HUB_TOOLHIVE_ADMISSION_ENDPOINT=http://workload-controller:8545/
admit`). It validates a trusted artifact before a connector cell is created —
the only path that can mint cell workloads.

## Boundaries

- Never invoked by agent code or inside runtime containers.
- Build paths own Docker image hygiene; raw `docker compose build` bypasses
  BuildKit forcing and cache caps — forbidden by AGENTS.md.
- `secret`/`grant`/`capability` mutations write through confirmed flows; the
  CLI is an operator surface, not a self-service agent tool.

## Storage operator surface

`hubctl storage status --spool <dir> --toolhub-store <path>` reports each
storage owner's allowlisted aggregates — owner name, status word, logical
bytes, file count, byte/file limits and counter completeness. No paths,
object names or raw errors are emitted.

`hubctl storage cleanup --spool <dir> [--retention 72h]` defaults to dry-run:
it inspects the owned tree read-only and emits a plan bound by a `sha256:`
digest over the tree state — aggregates only, never file names.
Symlinks inside the owned tree are refused, not followed. `--apply` requires
`--digest`: the command takes the spool writer lock, recomputes the plan and
rejects a stale or foreign digest before the bounded terminal compaction
archives aged done/failed records. Live pending/running records, open
approvals and live receipts are never candidates. The offline apply uses the
same terminal-file, archive-byte and usage-entry limits as gateway maintenance.

`hubctl audit rollover --ledger <absolute audit.jsonl>` plans an offline
recovery of an oversized schema-1-only ToolHub audit. With ToolHub stopped,
`--apply --digest <sha256:...>` verifies a complete compressed archive before
starting a fresh bounded ledger. Output contains only safe counts, bytes and
digest; mixed or segmented ledgers are refused.

## Bounded trace

`hubctl trace <job_id> --spaces <root> --env dev|prod [--user <owner>]`
requires an existing supervisor authority record. Unknown/corrupt ownership
refuses discovery before any container or home read. The selected runtime mount
resolver supplies the Hermes home, including managed/<env>/hermes; no env/auth
files or credential dictionaries are read. Docker discovery uses exact project
and context/network selectors, never an unrestricted host inventory.

Reads have a 10-second deadline, finite source/byte/line/event/output limits,
exact structured correlation IDs and source deduplication. Known source loss,
unreadable files, tail/output truncation and deadlines produce diagnostic_gap.
Default output is lifecycle metadata; message bodies and unknown fields are
omitted before redaction. This is an operational view, not an action receipt.
