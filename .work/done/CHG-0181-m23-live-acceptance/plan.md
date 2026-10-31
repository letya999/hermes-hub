# CHG-0181: M23 live acceptance (stage 4)

Chain: `docs/adr/ADR-0082-standing-assignments.md` ->
`specs/active/SPEC-0080` R3, `specs/active/SPEC-0081` R3 -> this plan ->
`state.yaml`.
Previous stages: `CHG-0178`, `CHG-0179`, `CHG-0180`.

## Goal

Prove on the real dev stack (not mocks) that standing assignments keep
working over time: multi-source acquisition, deterministic filtering,
restart/cancel/pause correctness, privacy boundaries and measurable cheap
polling.

## Read before coding

1. `docs/operations.md` (dev-rebuild, hubctl up/down, stack layout),
   `docs/index.md`, `deploy/` compose files for the dev stack.
2. CHG-0174 live-acceptance evidence style (`.work/done/CHG-0174*/`).
3. `internal/communication/gateway.go` config envs; `just docker-pull` /
   `docker-check-prebuilt` gates.

## Preconditions and rules

- Live evidence = durable receipts (spool files, mappings, counters,
  docker events); never raw user content; artifacts under `.work/tmp/`.
- Report unavailable legs honestly (e.g., no Slack creds ⇒ say so).
- No base-image additions for optional integrations.

## Slices

### S4.1 Dev-stack wiring (R3.1 setup)

Change:
1. `dev-rebuild` the communication service into the dev stack for
   `spaces/<test user>`; verify the container runs the local image and
   the control endpoint answers.
2. Local fixture service (opt-in profile container or `python -m
   http.server` variant emitting a counter page) + pick 2 stable public
   endpoints + Prometheus (`prom/prometheus` sidecar profile OR an
   operator-provided endpoint) + file source under `spaces/<user>/watch/`
   + MCP source if a connector is configured.

Tests: manual runbook recorded in state.yaml.

### S4.2 Assignment lifecycle live run (SPEC-0080 R3.1)

Scenario script (each step records evidence):
1. `assignment_plan` → `assignment_create apply:true` with 4+ sources.
2. Kill gateway mid-apply (docker kill) → restart → re-apply ⇒ receipts
   show `kept`, zero duplicates.
3. Update plan (change one `expect`), `expected_revision` CAS ⇒ replaced
   artifact receipt.
4. Fire change on fixture (counter increments) ⇒ watcher delivers;
   urgent-path event ⇒ immediate + `[urgent]`.
5. Pause ⇒ source quiets; resume ⇒ catches up.
6. Cancel while an occurrence is `probing` ⇒ no delivery, artifacts gone,
   tombstone card readable.

### S4.3 Pipeline evidence (SPEC-0081 R3.x)

1. Two subscriptions on one URL ⇒ `fetches:1, shared_hits≥1`.
2. ETag-capable fixture ⇒ 304s ⇒ `no_change` ticks.
3. Cursor-expiry fault injection ⇒ `gap_since` + single owner notice.
4. Hook endpoint: curl wrong token ⇒ 404; valid ⇒ event lands once;
   replayed body ⇒ deduped.
5. Idle-load sample: docker stats + tick counters at 1/10/50 subs — record
   numbers, no assertion.

### S4.4 Slack slice (SPEC-0080 R3.2)

Attempt order: (a) real Slack connector if creds exist in env; else
(b) signed-intake harness — craft HMAC-signed `/v1/slack/events` payloads
(bot_message ⇒ dropped, unmapped sender ⇒ refused, IM ⇒ job), verify
urgent route lands only on owner's private target, and PostMessage seam
receipts. State precisely which leg ran.

## Red-team checklist

- Cross-user read of assignment ⇒ denied (live curl with foreign
  principal).
- Restart during `probing` ⇒ occurrence resumes or fences honestly; no
  double delivery (outbox listing counted).
- Kill during `sending` ⇒ delivery finishes or stays `sending→failed`
  honest, never duplicated.

## Acceptance

SPEC-0080 A3.1 + SPEC-0081 A3.1 evidence in `state.yaml`; docs updated
(`docs/architecture.md` or `docs/routines*.md`, `docs/operations.md` if
operator-facing), indexes synced, `just check` green; issues #324/#332
commented with receipts and closed on verified acceptance.

## Out of scope

- New provider APIs, production deployment, memory-layer work (M20).

## Done checklist

- [ ] Live evidence recorded (redacted).
- [ ] Docs + indexes; `just check`; state done → `.work/done/`.
