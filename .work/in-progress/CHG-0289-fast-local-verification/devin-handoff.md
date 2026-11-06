# Devin continuation: three existing M32 worktrees

## Shared operating contract

Use the existing worktree and its existing branch. Do not create or rename a branch,
reset, clean, copy files from another dirty worktree, or run alongside another writer
in the same checkout. Have the owner stop the previous writer before taking over.
Keep Probity enabled. Do not launch Docker or alter the shared development stack.

First integrate the reviewed CHG-0289 tooling change once it is committed; until then
run its documented format/docs/vet and affected-package commands explicitly. Do not
claim that another checkout's justfile has changed. Use the repository's pinned Go.

For each stage: inspect current diff, list the concrete remaining callers, finish the
smallest coherent change, run its focused regressions, record commands/results and
commit when the issue's full contract is satisfied. Intermediate stages may remain
in the same working diff; do not declare partial owner cutover ready for deployment.
Full race/coverage and image acceptance are CI gates for a published source revision.
Use gh run status/watch for that exact SHA. No endless repeats of full local checks.
If a provider reports capacity/rate limit or a tool cannot start, record the exact
blocker promptly. Do not leave an apparently active overnight job making no progress.

## Track 1 — C:/Users/User/a_projects/hermes-hub-wt/m32-track-1

Completed issue snapshot: #509/#510/#511/#512. HEAD 33fb159d is a clean
checkpoint of incomplete #508, not another completed issue. Verify current HEAD first.
Remaining #508:

1. Finish routing every settings write through the authored-passport publisher.
   Check model/HTTP/operator callers and remove the independent self-settings writer.
   Gate: unpublished/rejected edits cannot affect effective settings.
2. Remove retired agent/workspace config readers and authoring paths, including
   fixtures and templates. Preserve protected grants, generated state and user data.
   Gate: retired files cannot affect runtime; no migration/compatibility fallback.
3. Complete tool-contract/catalog/docs parity, run affected packages and just check,
   commit #508. Record any live/CI acceptance as pending until actually executed.

## Track 2 — C:/Users/User/a_projects/hermes-hub-wt/m32-track-2

Completed issue snapshot: #513/#514/#515/#516. #516 is fcc6fd89; HEAD
50e6bb0c checkpoints incomplete #517. The checkout was clean at handoff.

1. Preserve #516 at fcc6fd89. Its capture HTTP/temp-vault and scoped race tests
   are recorded as passed; full-chain CI remains pending. Do not implement it again.
2. #517: route recall/CORE through ToolHub with exact-run admission. Prove private
   and group runs cannot mix memory; no direct vault or shared current/run fallback.
3. #518: implement the single remember/card writer with owner authority and revoke
   checks at admission and commit; test retry and two-user isolation.
4. #519: durable owner-bound edit proposals, target/diff/base hash and confirmation;
   stale hash, foreign user, replay and denied rights must not write.
5. #520: finish governed OpenKnowledge write path and remove alternate writers.
   Check issue/SPEC-0098 for the exact accepted interface; do not invent one.

One issue commit after its coherent stages pass. Make completed dependency commits
available for integration without requiring every later issue to be finished first.

## Track 3 — C:/Users/User/a_projects/hermes-hub-wt/m32-track-3

Completed issue snapshot: #521/#522. HEAD 813a37d6 is a clean checkpoint of
incomplete #523; a checkpoint commit is not proof of completed cutover.

1. #523 public API: finish all routine/assignment/watch operations and explicit
   owner/channel routing. Use actual supported supervisor endpoints. No guessed API.
2. #523 callers: switch HTTP/chat/tool callers; prove they reach ToolHub and preserve
   authorization, revision conflict and response contracts.
3. #523 startup/recovery: enable only ToolHub clock/dispatch/writers; remove old
   communication writers and startup paths. Communication outage delays delivery,
   not the underlying automation. Prove restart and no duplicate dispatch.
4. Finish #523 regression matrix and commit the coherent cutover.
5. #524: per-user protected system routine, timezone/local-day key/DST/restart,
   current memory enablement and grants. Integrate only its verified dependencies.
6. #525: exact-user vault/relay nightly pipeline, durable checkpoints and one commit
   per local day, revoke-before-commit; digest delivery separate, proposals stay draft.
7. #526/#527: integrate the remaining verified config/memory commits, run the
   combined race/coverage/container acceptance through CI for the published SHA,
   and report remaining live-provider checks honestly.

Do not wait for all of #520 merely because it was written in an older prompt.
Resolve the actual interface dependencies against SPEC-0098/0099 and committed code.
Do not merge dirty changes or fabricate an admission stub to bypass a dependency.
Only this track coordinates integration; never promote dev on partial local evidence.

## Publication boundary

The changes in this handoff do not themselves authorize push, PR publication or
GHCR publication. Obtain the owner's explicit instruction once the reviewed commit
is ready, then run CI instead of attempting to emulate every hosted job locally.
