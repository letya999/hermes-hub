# CHG-0153: incident replay harness (M17 stage 6)

Chain: `docs/adr/ADR-0077-ops-admin-delivery.md` D6 -> `specs/active/SPEC-0076-ops-admin-delivery.md` R6
-> this plan -> `state.yaml`.

## Goal

Re-run an admitted job against an isolated clone of the space to answer "why
did the agent do that" — the durable envelope preserved, real channels,
providers and credentials unreachable by construction.

## Read before coding

1. `internal/commodel/delivery.go` `Job`; `internal/communication/spool.go`
   job persistence (sensitive prompts are memory-only — non-sensitive job
   files carry text)
2. `internal/communication/mock.go`, `mock-sent` capture; `Config.MockDir`
3. `internal/communication/config.go` `User`, `ConfigFromEnv`
4. `cmd/hubctl/main.go` op wiring
5. Issue #206

## Slices

### S6.1 Bundle export (R6.1)

Anchors: `cmd/hubctl` new `replay.go`; spool job/mapping readers.

Change:
1. `hubctl replay export --job <id> --spool <dir> --dir <space> -o <bundle>`
   writes `bundle.json`: durable envelope + ids + `text_sha256`, `text` only
   when the durable job file carries it or `--prompt-file` supplies it
   (verified against the recorded sha), `versions` (image pin, model,
   policy_version, hermes build when recorded), `outcome` (last mapping
   status/response), `config_digest` of the space's non-secret documents.
2. Bundle writer rejects any material that looks like a secret value —
   export reads only allowlisted files (agent.yaml, workspace.yaml,
   scope.yaml, spool records for the job id).

Tests (red first):
- `TestReplayExportRedacts`: space with secrets.env → bundle has no value,
  only digests.
- `TestReplayExportSensitiveNeedsPrompt`.

### S6.2 Isolated replay run (R6.2, R6.3)

Anchors: `communication.Gateway` construction seams; mock channel.

Change:
1. `hubctl replay run --bundle <dir> [--runtime-url <replay supervisor>]`
   builds a scratch spool + scratch space, a `Config` whose single user is
   the bundle envelope with channel forced to `mock` and no channel secrets;
   re-admits the preserved job text through the real admission path.
2. Runner selection: `--runtime-url` points at a replay supervisor for the
   full chain; absent it, a stub runner completes the job with a recorded
   marker so the envelope/routing path is still exercised end-to-end.
3. Output: replayed job status + captured mock deliveries vs the bundle's
   recorded outcome — the diff surface.
4. Bundle/space validation fails when any provider token, telegram token or
   file outside the allowlist would be consulted.

Tests (red first):
- `TestReplayRunMockChannel`: exported job replays to a mock delivery;
  report compares statuses.
- `TestReplayNeverTouchesRealChannel`: config built from bundle has empty
  telegram fields and mock channel regardless of bundle tampering with
  channel hints.

## Red-team checklist

- Bundle with smuggled `TELEGRAM_BOT_TOKEN` → export/run rejects or ignores
  it (allowlist, not denylist).
- Replay writes only under `--replay-dir`; never into the source space/spool.
- Sensitive job without prompt refuses to fabricate one.

## Acceptance

SPEC-0076 A5: no secret material in a bundle; replay reaches no real channel
or provider by construction.

## Out of scope

Model-determinism guarantees, byte-exact reply equality, docker-side replay
packaging (operator runs `--runtime-url` on a stage host for that).

## Done checklist

- [ ] Tests red then green; `just check`.
- [ ] state.yaml + issue #206 comment.
