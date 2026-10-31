# CHG-0237 integration evidence — 2026-10-31

Source `codex/storage-reliability-mvp` remained dirty and unchanged. An ignored
temporary index produced local snapshot `87fbb198` from its tracked and new
files; staged Gitleaks found no leaks. Main worktree branch
`chore/storage-reliability-integration` contains the two-parent merge of that
snapshot.
All 26 textual conflicts were resolved. Duplicate source ADR/SPEC/CHG numbers
were removed; implementation notes were copied to canonical CHG-0215…0225.

Root Go compile and `go run ./cmd/devcheck docs` passed. The first full
`just check` failed at a stale 0.4.0 image pin in the imported runtime runbook
and a race in an exposure test fixture's shared clock. Both were fixed;
`TestReleasePinsAreCurrent` and the isolated `-race`
`TestManagerReapsExpiredInBackground` pass. The final `just check` passed with
86.27% own Go statement coverage (85% floor).

Communication/ToolHub package tests exposed a new confirmation button that
the legacy approval filter would never deliver; a delivery-level regression
now passes. A former 1-byte spool limit test was updated to a valid finite
budget and observes quota refusal. An uncertain-notice test now backdates the
first uncertain observation before testing the localized post-budget notice.
The affected focused tests and entire package sweep pass.

Broker `scripts/quality.py check` passed in `golang:1.27.3-bookworm` with the
whole repository mounted at the expected layout. It ran formatting, vet,
OpenAPI, architecture, documentation, `-race` tests and merged coverage:
90.3% overall, minimum production package 86.3% (`provider`). The native
Windows entrypoint cannot run Linux-only `internal/securefs`; the identical
quality backend was used in the Linux container. A pre-existing test called
the removed `recoveryAnchor.initialized`; it now checks `marker()`.

The new human confirmation is implemented only for Telegram callbacks.
Unsupported channels refuse the confirmation notice instead of showing an
unusable button. No real account login, external message, provider mutation,
publication or production data migration was used for this acceptance. SQLCipher,
secret-object GC and cutover remain design-only CHG-0218…0220 and CHG-0226.

The final scoped image build passed through `hubctl build --dir spaces/local
--env dev cliproxy toolhub`. `hubctl dev-rebuild` then installed local dev
images for Broker, ToolHub, Communication Hub, Workload Controller, Supervisor
and Hermes runtime. All six containers stayed up. Communication and ToolHub
responded at ports 8081 and 8090, Controller denied unauthenticated access
with HTTP 401 at 8545, Broker HTTPS `/healthz` returned 200 at 8787, and the
Supervisor endpoint responded with its expected authorization challenge.
`hubctl storage status` in the updated containers reported 71,406,282 bytes
and 15,327 files for the Communication spool and 186,174,631 bytes and 22 files
for ToolHub state, both below the configured 256 MiB limits. Cleanup was
examined as a dry run only; live files were not deleted.

The local dev Broker had a legacy recovery marker and initially failed closed.
With the Broker stopped, three Docker volume backups of primary, recovery and
key were byte-compared against the originals. `migrate-head` was tested twice
on copied volumes and an isolated `app.Build` succeeded. A second pristine
primary/recovery copy was retained for rollback. The migration was then applied
to the stopped local dev Broker. It restarted and `/healthz` returned 200.
The backup volume names are in ignored `.work/tmp/storage-broker-backup-volumes.json`.
No secret bytes, key names or raw container environments were captured in the
evidence. No production data was migrated.

Docker smoke passed. `supervisor-contract` found that re-observing an admitted
run after a generation change replays its original admission event ID. The
Communication spool treated the old already-applied receipt as a stale new
event and returned an uncertain result. The receipt application now skips
positions already applied before checking old generation; the regression test
uses the real repeated event ID and passes. The full `supervisor-contract` then
passed on the local image, including generation recovery, real native approval,
desired-state sources and preservation canaries. The capability contract and
managed Supervisor canary passed. The first capability attempt used an old
`hermes-hub:test` tag missing the pinned upstream revision marker; the tag was
rebound to the current `hubctl`-built `hermes-hub:0.5.0-dev` image and the same
contract passed. The full combined `hermes-contract` passed on the current
image, including Gateway lifecycle's real five-minute idle window, graceful
shutdown and cold restore. The final Communication Hub dev rebuild passed;
its container uses `hermes-hub-dev-local-communication-hub:local`, runs without
restarts and responds on port 8081. All five Compose services checked again
were running on their respective dev images with zero restarts. The final
staged Gitleaks scan and diff whitespace check passed; no staged path lives
under `spaces/` or deployment/browser-profile/transcript directories.

`just docker-check` and `just docker-check-prebuilt` were not run: the image
build used the authorized scoped `hubctl build` path and local contract probes
ran on its current runtime image. No dependency manifest changed, so the
dependency-change `just security` gate was not triggered. There were no active
managed runtime containers and the owner spaces present in this checkout have
no settings profiles; actual owner-space catalog rebind/respawn remains for a
managed deployment. The managed Supervisor canary verifies the synthetic
projection but cannot replace that deployment step. SQLCipher proof/store,
secret-object GC and production cutover remain separate design-stage CHGs.
