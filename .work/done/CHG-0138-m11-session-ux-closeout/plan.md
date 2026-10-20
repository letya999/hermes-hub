# CHG-0138: M11 closeout — stop/cancel, archive UX, live progress, e2e harness (issues #217/#218/#219/#194/#193)

Milestone: GitHub milestone 12 ("M11 — Telegram & Channel UX"),
branch `feat/m12-telegram-channel-ux` from `dev`. Continues CHG-0098 (#220).

## Goal

Close the remaining milestone issues on the existing durable spool/control
architecture:

- #218: `/stop` cancels the active job of the current session; `/cancel
  <job-id>` resolves the stale references already printed by approval and
  edited-message replies.
- #217: `/archive [id|имя]` retires a session (Hermes session kept for
  audit), `/archived` lists it, `/use` restores it; in forum groups the
  session's topic is closed and reopened to mirror state.
- #219: live progress — typing re-armed every ~5s (Bot API typing expires
  in ~5s) and the single durable status message edited with elapsed time.
- #194: deterministic-command and inline-approval evidence; no local
  command may enter the model queue.
- #193: mock-channel e2e harness as one command — the `e2e` runner ships in
  the communication-hub image so `docker exec <hub> e2e mock-server` works
  without manual binary copying.

## Slices

### S1 /stop and /cancel (issue #218)

Anchors: `task_commands.go` `stopCommand`; `job_mapping.go`
`ActiveJobsForTask`, `RequestCancel`; command switch + inventory.

Tests (red first): `TestStopCancelsActiveJob`, `/cancel` case,
`TestStopCancelInCommandInventory`.

### S2 archive UX + forum mirror (issue #217)

Anchors: `task_commands.go` `archiveCommand`/`archivedCommand`/`useCommand`;
`tasks.go` `ArchiveTask`/`UnarchiveTask`/`TaskBySelector`; `TelegramAPI`
`CloseForumTopic`/`ReopenForumTopic`.

Tests (red first): `TestArchiveCommandClosesForumTopic`,
`TestArchivedListsOnlyArchived`, `TestUseRestoresArchivedSession`,
`TestUseArchivedByNameAndTopicCollision` (occupied topic restored unbound,
no foreign reopen), `TestArchiveCommandBySelector`.

### S3 live progress (issue #219)

Anchors: `communication.go` `progressTick` + `Run` loop; `job_mapping.go`
`ChatID`/`StatusText`; `stream.go` `applyStreamReceiptLocked` persists the
latest status line; `UpsertStatusDelivery` stays the single-message path.

Tests (red first): `TestLiveProgressTickRearmsAndAges`,
`TestProgressLoopRunsDuringGatewayRun`.

### S4 deterministic surface + evidence (issues #194/#193)

`TestAllCommandsStayDeterministic` covers every slash command including the
new ones. `docker/Dockerfile` builds `cmd/e2e` into the image;
`docs/e2e-acceptance.md` documents the one-command harness and records the
honest evidence boundary (unit/fake-API only, no live Bot API claim).

## Acceptance

`just check` green (coverage gate ≥85%); package tests green. Docker rebuild
through `hubctl up --user local --env dev` for image-side verification.
Live acceptance of the new commands against real Telegram stays with the
owner — recorded honestly in docs/e2e-acceptance.md.

## Out of scope

- Provider error propagation into user-facing text (`run.failed` detail) —
  separate UX improvement.
- `/rename` command surface; renaming still happens via auto-title or topic
  rename sync.
