# CHG-0200 evidence

S200.1 uses temporary synthetic ToolHub stores only. A fresh Store and the
original Store both silently rewrote a removed initialized snapshot before the
fix; the regression tests observed failure. `Save` now fences a missing
snapshot using a local initialized marker and the in-memory saved path. The
existing digest conflict fence remains in place. Directory-sync failure after
snapshot rename and after marker creation was independently reproduced with a
fault-injection seam, then made visible to the caller. The marker and snapshot
are file-synced before their parent-directory sync. Windows parent-directory
sync remains the existing no-op; no native/container durability claim is made.

An entire lost volume removes the local marker too. S200.1 remains in progress
until independent installation identity and recreate/restore evidence are
established. No real refs, credentials, providers or Docker resources were touched.

The focused six-case ToolHub race run exited 0. `chg0200-toolhub-store-race.log`
records a full `go test -race ./internal/toolhub -count=1` PASS. The root
`chg0200-snapshot-root-check.log` records a full `just check` PASS after these
ToolHub changes with 85.93% own Go statement coverage.

`TestRecreateRetainsOnboardingAndBrokerReference` copied a synthetic snapshot
and marker into another temporary directory, loaded them there, and verified
that the pending onboarding and team Broker reference remained available. The
targeted race test passed. This does not simulate a real Docker volume loss or
prove that an entirely missing volume can be identified independently.
`chg0200-recreate-root-check.log` records the following full `just check` PASS
with 85.93% own Go statement coverage.

## Onboarding status reasons (partial)

A synthetic owner-isolation regression showed that a foreign onboarding ID
returned `authorization denied` while an unknown ID returned `not found`,
revealing object existence. Both now return the same public not-found error.
The private error type retains safe `scope` or `unknown` codes. Owner-matching
definition lookups distinguish wrong version and removed terminal records
internally, while their public error remains not-found. Each code was driven by
a failing assertion before the corresponding fix. Expiry and storage codes,
status retry behavior, HTTP/agent contract acceptance and the full post-change
gate remain outstanding.
`chg0200-status-reasons-toolhub-race.log` records a full
`go test -race ./internal/toolhub -count=1` PASS after this correction.
`chg0200-status-reasons-root-check.log` records a subsequent full `just check`
PASS with 85.94% own Go statement coverage.

The active/revoked Broker-reference reuse regression now saves and reloads
the synthetic ToolHub store before retrying onboarding. Its focused race run
passed: an active verified reference resumed without a new Broker request,
while a revoked reference opened a new request. This is a mock Broker
contract test, not live Broker or container acceptance.

An unknown `definition_id` initially lacked the internal `unknown` reason;
the focused test failed before the one-line fix and passed with `-race` after it.
The public not-found text and scope masking did not change.

The latest full `just check` failed under parallel load: Communication
`TestControlLoopWakesOnCancelRequest` timed out, SSH
`TestExecBoundsAndCancellation` timed out on a loopback dial, and Windows
blocked a temporary `internal/mediasvc` coverage metadata rename. Each of
those three tests passed on a focused serial repeat. The latest full gate
remains failed; the earlier full PASS with 85.94% coverage predates the
unknown-definition one-line change.

## Human confirmation (partial, synthetic/local)

The MCP `confirm` operation previously accepted a copied nonce and moved an
onboarding to `confirmed`; `TestAgentCannotConfirmItsOwnConsent` failed with
that result. It now reads status and returns `await_user_confirmation`, while
`enable` still rejects an unconfirmed onboarding. Existing MCP end-to-end
fixtures now use the loopback protected HTTP POST. The focused contract tests,
full ToolHub package before the atomic claim, and focused race tests passed.
Description, prompt contract, tools/routines docs and immutable catalog version
`hub-agent-tools@5.8.0-storage.2` were updated together. Runtime rebind and
respawn are not performed while Docker is reserved for the main worktree.

Two concurrent direct confirmations previously both reached readiness with
the same nonce (`readiness=2`). `ConsumeConfirmation` now sets the used flag
under the Store lock and saves the snapshot before materialization; the
deterministic synthetic filesystem race test passed with one readiness call.
An error after the claim leaves the nonce used. The existing status path can
re-arm after expiry; focused retry tests passed with a fresh nonce. The first
full ToolHub run after the claim found two old retry expectations; those were
updated, and their focused tests passed. A full post-claim suite and root
`just check` are still required. The protected page does not yet bind a
verified human actor/action digest to the claim, so S200.3 is not accepted.

After updating the retry expectations, a full `go test ./internal/toolhub
-count=1` passed. The subsequent full root `just check` passed with 85.94%
own Go statement coverage; the captured output is under ignored
`.work/tmp/chg0200-human-contract-root-check.log`. This is local fixture
evidence only; Docker and real-account acceptance were not run.

An injected invalid initialized marker reproduced a file-backed
`PutOnboarding` failure that still exposed an updated confirmation nonce in
memory. A second case reproduced exposure of a newly inserted, uncommitted
onboarding. The Store now rolls back either mutation under its write lock when
snapshot persistence fails. Focused `-race` and the full ToolHub package pass;
the root gate has not yet been rerun after this change.

The first root `just check` after that change failed in two unrelated tests:
`TestSupervisorWatchCLIStartsChild` timed out under a port collision and
`TestPopplerRejectsTooManyPages` received `document is invalid`. Both passed
in focused reruns, and the next complete `just check` passed with 85.97% own
Go statement coverage (`.work/tmp/chg0200-root-check-repeat.log`).

A further injected directory-sync failure showed that `PutOnboarding` could
roll memory back even after the snapshot rename had succeeded. The red test
observed the old nonce in memory while the file held the new nonce. The Store
now rolls back only if the snapshot digest did not advance; it still reports
the sync error. Focused `-race` cases for pre- and post-replacement failures
pass. The complete root `just check` after this edge-case change passed with
85.97% own Go statement coverage (`.work/tmp/chg0200-post-sync-root-check.log`).

A separate red regression then showed that a failed pre-replacement
`ConsumeConfirmation` claim remained used only in memory. The same test also
checks a post-replacement directory-sync error and a stale caller copy. The
claim now restores the actual stored record only before replacement, while
keeping memory aligned with disk after replacement; the error is still
returned. Focused `-race` cases, including concurrent single-use claim,
passed. The root `just check` after this final claim change passed with 85.97%
own Go statement coverage (`.work/tmp/chg0200-final-claim-root-check.log`).

## Actor-bound channel callback (local implementation, acceptance excluded)

`/v1/confirmations` now accepts only a service bearer distinct from every
enrolled runtime bearer. It rejects browser Origin, unknown JSON fields,
oversized/trailing input, unknown enrollment, wrong actor/scope/action digest,
stale revision, mismatched expiry, expired/spent nonce and revoked authority.
The Store checks current grants/profile/policy under its claim lock. A safe
`human_consent` record (actor ID, action digest, revision, timestamp; no nonce)
is synced in the same snapshot before materialization. Write upgrades use one
atomic claim and authority update. Consent pointers are copied at Store boundaries.

Communication persists opaque Telegram callback IDs in the existing bounded
spool; verified sender, chat and expiry are checked after restart. Runtime
bearers cannot mint human-confirmation notices. Credential intake, ordinary,
prepared/remote OAuth and device flow now save their inputs and await the
separate callback. Bearer-link pages display status and reject consent POSTs.
Each change had a behavioral red regression. Targeted ToolHub/API/Communication
`-race` passed. This uses temp files, loopback HTTP and channel doubles only.

The first complete three-package run passed API and Communication but found
15 old ToolHub expectations of automatic OAuth/page enablement. Fixtures were
updated to perform an explicit trusted callback before their original tools,
refresh, revocation and readiness checks; all affected cases passed focused.
The full root gate after this work is still pending. Catalog storage.3 and
prompt/tools/routines documentation are prepared together; rebind/respawn,
Docker and acceptance remain excluded. Non-Telegram confirmation transport,
remaining storage classifications and complete gate reruns are still outstanding.
