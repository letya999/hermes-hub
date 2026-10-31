# CHG-0197 evidence — partial implementation

Updated 2026-10-29. S197.1 is implemented pending acceptance; S197.2 core/exposure are in progress. No done move.

## Red before fix

Three tests failed against the original audit implementation: appending after a
partial tail acknowledged and mutated corrupt evidence; a 32 KiB JobID had no
record cap; an unknown foreign schema was filtered into an apparently empty list.
These regression tests now pass without deleting/truncating any evidence.

## Bounded ledger

Existing initial JSONL is preserved. Versioned consecutive v1 segments are
appended under the existing flock dependency, shared by every new writer.
Limits: 16 KiB/record, 1 MiB/new segment, 16 segments, 16 MiB total and 65536
records. Legacy oversized input is refused for append/read, retained for explicit
operator export/recovery; no silent TTL/GC. File and Linux directory sync precede
acknowledgment; partial/corrupt/unknown records and segment gaps fail closed.
A missing first segment with surviving later files is not reinitialized.

ReadPage uses principal + optional context/environment and bounded pages up to
1000 events. Every record is validated before scope filtering; a cursor advances
only over the scanned canonical prefix. Legacy List refuses silent truncation.
No raw payload/dictionary extension to Event was introduced in this slice.

## Receipts

- Windows `go test -race ./internal/audit ./internal/secrets ./internal/sshcap
  ./internal/exposure -count=1` PASS. Own package coverage respectively:
  88.4%, 86.0%, 86.7%, 79.4% (root package floor is 75%; overall floor 85%).
- Two real child processes concurrently append across multiple segments; all
  32 acknowledged unique events remain readable. No backend/provider contacted.
- Capacity tested separately for byte, record and segment count, including
  reopen; accepted evidence remains unchanged after admission stops.
- Scoped two-page reads, future cursor rejection, 1001-event List refusal,
  partial/corrupt/unknown records and segment/file/lock alias protections tested.
- See state.yaml for final full-root and native Linux receipts.

## Outstanding

S197.2 is partial: core/exposure are implemented; SSH still has legacy ignored
audit errors. S197.3 remains unimplemented. Toolhub outcomes still conflate execution and authorization and its
legacy refresh/retry path remains for CHG-0198. Completion reserves and exposure uncertain semantics are implemented below;
SSH/Toolhub wiring, lifecycle reserve and verified resolution still require the remaining slices. A bounded writer alone does not make mandatory
audit reliable. No Docker deployment, provider mutation or working-data migration.

## Frozen S197.1 root check receipt (2026-10-29)

`just check` was rerun with process-only GOFLAGS=-p=2. Docs and configuration
legs passed. Every package eventually printed PASS, including mediasvc and
runtime; nevertheless the supervising go test process did not complete or emit
its aggregate coverage footer. It was stopped by its verified process identity
only; the gate is FAILED/interrupted, not accepted and no final coverage number
is claimed. Lint separately found three G115 int-to-uint64 counter conversions.
The bounded record counter now uses uint64 throughout, without suppressions;
Windows audit race tests passed again (4.911s). See state for the standalone lint
receipt. No unrelated process, Docker service, provider or source worktree was
stopped or modified.

Standalone `just lint` PASS after unsigned-counter fix: formatting, vet,
module tidy diff, staticcheck and root/Broker gosec. Final native Linux audit
race repeat PASS (2.526s); final Windows audit race repeat PASS (4.911s).
Documentation check and git diff --check PASS after evidence/state updates.
This does not replace the failed/interrupted full just check or establish live
acceptance. No new dependency manifests changed.

## S197.2 core admission/reserve and exposure (2026-10-29)

RED: removing audit before open still called the fixture provider; failing the
completion after a successful fixture effect returned success; a fresh manager
repeated the effect after restoring the unresolved admission. Agent-tool status
also omitted the uncertain flag. Each regression now passes after its fix.

Schema 2 has operation/action/phase and separate authorization/execution/delivery.
Schema 1 remains readable. Begin synchronizes admission and reserves a maximum
16 KiB completion plus record and conservative segment capacity. A second writer
reconstructs reservations from committed records. Capacity exhaustion cannot spend
another operation's completion reserve. Completion is identity-bound and attempted
once; failure returns ErrUncertain, leaves durable intent, and never calls a backend.
Pending recovers scoped unknown admissions without claiming crashed/live status.
A matching unresolved target/correlation fences a new admission atomically.

Exposure records admission before provider.Open. Provider errors leave unresolved
intent instead of authorization deny or a claim that no effect occurred. Known
handles survive errors for cleanup; uncertain resources still consume capacity.
Status/list project uncertain=true, and errors prevent blind reopen. Close attempts
actual access termination even with a failed audit admission, then returns uncertain.
Reap now returns its error to callers; the background producer logs only a fixed
error class. No raw provider errors/arguments or handles enter audit/results.

Tests use real temporary audit files and an injected fixture provider, NOT a live
Docker tunnel. Windows full audit/exposure race suites passed before final field
bounds: 89.3% audit / 80.4% exposure own coverage. Targeted agenttools race projection
also passed. Final native/root receipts are recorded below after completion.

Outstanding: SSH and Toolhub producers are not wired yet; lifecycle reserve,
verified resolution of unresolved effects, broader producer privacy and final full
acceptance remain S197.2/S197.3. An unresolved open may still require operator
reconciliation after a requested close: this slice does not fabricate a terminal
receipt or silently discard its fence. Prepared catalog remains unpublished;
rebind/respawn is deferred by the user's Docker restriction.

## S197.2 core/exposure final receipts (2026-10-29)

- Final Windows full race suites PASS: audit 12.611s/89.1%, exposure
  5.501s/80.4%, secrets 5.698s/86.0%, SSH 7.375s/86.7% own coverage.
  SSH passing here is a regression check, NOT a claim that SSH uses the new API.
- Final native Linux/WSL audit and exposure full race suites PASS:
  2.905s / 1.292s, including the strengthened high-water reservation test.
- The high-water test was reviewed and strengthened: completion now carries
  a valid 12 KiB field so it cannot fit merely by chance into filler remainder.
  A command-local Go overlay disabling completionReserve made this exact test
  FAIL (reserved completion lost). Normal code PASS. Overlay/mutant live only
  under verified ignored .work/tmp; no runtime source or global settings changed.
- Targeted agenttools race suites PASS (9.036s), including uncertainty projection.
- Standalone Windows just lint PASS: format, vet, tidy diff, staticcheck, and
  root/Broker gosec. No dependency manifests changed. Docs/format were checked
  again after evidence/state updates.
- Native root just check FAILED at its Go leg. Unmodified test cases failed:
  runtime TestBrowserStartsAfterReady and TestDeferredBrowserStopsSpawningAfterShutdown
  attempted cleanup of read-only /tmp/.X11-unix/X99; stack
  TestOrganizationOverlayAndMembership did not inherit MCP; Toolhub
  TestStopFailureSurfacesOnEveryCaller/Reload did not surface its expected error.
  These are untriaged failures, not a claim of independently proven baseline bugs.
  No full aggregate coverage or successful full gate is claimed. Native Unix
  just aborts subsequent lint/docs/config legs after this Go failure.
- A separate WSL process-state observation failed with 0x8007274c while the
  original check handle stayed live. The same handle was polled until actual
  exit 1; no duplicate/restarted native full gate, Docker/WSL restart, socket
  deletion workaround, or permissions relaxation was performed.
- Existing CRLF style was retained in four edited tracked files to reduce diff
  noise. devcheck Format explicitly normalizes CRLF. Whitespace check uses
  command-local core.whitespace=blank-at-eol,blank-at-eof,space-before-tab,cr-at-eol;
  no repository/global Git configuration was changed.

Full MVP remains active and incomplete. No CHG was moved to done. No real
provider, working-data migration, Docker deployment or catalog rollout occurred.

## S197.2 SSH producer (2026-10-29)

RED: a missing audit file still allowed an exec on the local fixture SSH server;
post-effect audit failure returned stdout/success, and retry was not fenced.
Those regressions pass after durable admission before dial/effect and identity-
bound completion. Transport errors after an effect attempt preserve unknown
intent/reserve, return ErrUncertain, and withhold the result payload. Pre-effect
errors can complete as not_started; verified process exit distinguishes execution
failure from authorization denial. Repeated normalized commands and write targets
with unresolved intent are fenced without another backend call.

All SSH producers are wired: exec, SFTP read/write, shell open/send/read/close,
tunnel open/close and certificate signing. Receipts contain only static codes,
IDs, counts and digests; no paths, commands, inputs, outputs, private keys or raw
provider errors. Denial audit errors are returned; unknown alias values are not
persisted. Sweep/shutdown use the audited close paths and fixed error-class
logging. Cleanup still attempts access termination when admission fails. Newly
created shell/tunnel transports are closed if their completion cannot be recorded.
Existing grants, host-key pinning, bounds and credential lease cleanup remain.

A separate reproduced authorization regression: with sudo:passwordless, leading
or trailing command whitespace caused implicit sudo -n because raw/canonical
strings were compared. Canonicalizing before policy/digest fixes this, and a
regression asserts the actual server command remains uptime rather than sudo.

Verification uses a real in-process loopback SSH/SFTP fixture and temporary audit
files, not a live host/Broker/provider. SFTP counters prove failed admission makes
zero read/write requests. After confirmed fixture rename plus audit failure, data
changes exactly once, payload/path stay out of audit, and retry is fenced. Session
admission failure retains no sessions; missing audit never prevents requested
shell/tunnel termination. Existing fixture constructors now supply explicit
private ledgers instead of disabling mandatory audit.

Windows full SSH race/coverage PASS before final canonicalization: 87.7%; full
agenttools race PASS (86.2%). Canonicalization and all targeted producer/contract
regressions passed again. See final gate receipt below. Toolhub wiring and S197.3
lifecycle reserves/verified recovery remain required; no accepted CHG/done move,
Docker deployment, external account login or working-data migration.

## Latest root gate — SSH producer snapshot (2026-10-29)

Full Windows `just check` PASS, exit 0. Go test/race/coverage, lint (format, vet,
tidy diff, staticcheck, root/Broker gosec), docs/actionlint and configuration legs
all completed. Root statement coverage **85.95%**, package floor accepted. This
is a new successful run; earlier Windows hangs/flakes and native full-root failures
remain historical evidence, not silently relabeled. It does not establish actual
Docker/provider/power-loss acceptance or supersede the blocked SQLCipher gate.

Final native Linux SSH full race PASS (2.383s). Full Windows SSH and agenttools
race/coverage checks passed (87.7% / 86.2%); canonicalization and producer/contract
regressions passed again afterward. Only a certificate-receipt comment and task
state/evidence were updated after the successful full root run; runtime code was
frozen throughout. Final docs/format/whitespace checks were repeated.

Prepared hub-agent-tools@5.8.0-storage.1 includes the SSH uncertainty description,
prompt contract and docs in this same unpublished changeset. No rebind/respawn,
Docker deployment, external host login, provider mutation or data migration.
Toolhub wiring, lifecycle reservations and verified resolution remain required;
no CHG was moved to done and the whole Storage reliability MVP is unfinished.

## S197.2 Toolhub producer and transport outcome (2026-10-29)

RED: post-effect audit failure exposed the private backend result with a generic
error; backend error emitted two authorization denials; checkpoint failure was
not uncertain. Each regression passes after a single deferred completion path.
Durable audit is mandatory for every projected dispatch, including legacy mode.
The production ledger DTO writer is shared by server wiring and real-file tests;
positive test fixtures explicitly attach private ledgers, without a production
fallback or a logging observer substituting for durability.

The audit schema 2 uncertain phase preserves admission identity and reserve.
Unknown completion is not a terminal success. Scoped Pending and a second writer
recover uncertain intent; a stable owner/context/environment/job/call operation ID
fences replay even after reopen or changed arguments. Business IsError is failed
execution with allow authorization; preflight failures finish as not_started.
Post-dispatch, checkpoint, invalid-output and completion-write failures withhold
payload, return ErrUncertain and preserve unresolved intent. Audit diagnostics
iterate a fixed field allowlist, omit opaque receipts/dictionary keys and report
audit-unavailable via fixed status/error class, with a separate observer snapshot.

MCP uncertainty is an IsError application result, not a JSON-RPC transport failure:
status=uncertain, safe hashed operation_id, retryable=false. The internal HTTP
read-tool contract responds 409 with these fields; embedded Go retains a typed
ErrUncertain. Contract guidance, prompt and current docs are updated together in
the same prepared/unpublished catalog changeset. No rebind/respawn was performed.

Recovery is preserved without replaying business effects: only trusted remote
backend code can mark ErrBeforeEffect, and only authentication rejection during
setup gets ErrAuthBeforeEffect. One refresh/retry of setup is allowed before
any tools/call; post-dispatch errors, including HTTP 401 after a fixture effect,
never receive this marker and never refresh/replay. The wire test counts exactly
one effect. The existing fixture TLS helper now verifies its certificate chain
and expected fixture peer instead of InsecureSkipVerify; production TLS/SSRF,
Origin, CSRF, authorization and nonce gates were not relaxed.

Tests include real temporary ledger failure/reopen, scope-bound replay rejection,
canonical operation ID in application/HTTP error, callback dictionary-key privacy,
nonterminal uncertain reserve at high-water, and a TLS MCP fixture. These are
synthetic file/protocol tests, not a live OAuth/provider integration or Docker proof.
Initial full package run before fixture updates timed out in an unchanged positive
admission-fence fixture lacking its now-required writer; the same handle was polled
to exit, not restarted after an observation timeout. The next full race run had
only two known fixture/expectation failures, now corrected and retested. Its own
coverage was Toolhub 86.0%, API 83.3%, audit 88.7%, SSH 87.7%, exposure 80.4%.
Final full-root and native receipts follow below; no earlier failed run is green.


Full-root follow-up: the first Windows `just check` in this Toolhub slice exited
1: four positive cross-package fixtures omitted the now-mandatory audit writer
(hubctl, Communication, Secrets and gateway contract); the goal pause test also
cancelled before its queued notification was delivered. This run is not green.
All direct Gateway constructors were inspected. Exported the existing DTO writer
within internal/toolhub and reused it in those fixtures, the optional contract
probes, and the working connector call path (which previously wrote legacy DTOs
for admission/completion). Positive endpoint fixtures always allocate private
ledgers rather than inheriting an operator ledger path. No production fallback.
The goal regression now drains the queued delivery after worker shutdown; runtime
retry and delivery behavior were not changed. A temporary Python default-encoding
rewrite was detected in diff review and reversed; UTF-8 file contents are restored.
Cross-package race regressions passed: hubctl 7.987s, Communication 17.368s,
Secrets 5.990s, devcheck 6.415s. Connector typed-pair assertion and deterministic
goal notification passed separately under race: hubctl 2.393s, Communication 2.540s.
The first attempt at the connector assertion used the key-file directory instead
of the store directory; corrected fixture lookup and reran, not claimed as a
production regression. Final root gate and counterfactual writer check pending.

Counterfactual CLI proof: a Go overlay restoring only the former legacy CLI
writer makes the typed admission/completion assertion fail (4.513s); the real
shared writer passes. Integration-tagged devcheck contracts compile with no tests
run (1.109s); no Docker, live MCP/provider or external send executed.
The next full Windows root gate exited 1 with only four Toolhub explicit-ledger
fixtures failing: the positive endpoint helper overrode their deliberately chosen
ledger path. All other packages passed, including Communication 401.743s, hubctl
118.787s, SSH 7.126s and Secrets 5.676s. Restored direct production constructor calls
for every fixture explicitly controlling HUB_AUDIT_LEDGER, retaining the private
positive-only helper for ordinary dispatch fixtures. Their targeted race checks
passed (5.879s), including fail-closed path validation. Repeating the whole gate
is justified by these fixture corrections; failed runs remain failed evidence.


## S197.3 lifecycle reservation and receipt privacy (2026-10-29)

The pre-S197.3 root gate exited 1: all Toolhub explicit-ledger fixtures passed
(241.679s), but TestRunOutcomeStreamIdleDeadlineCancels measured 5.493s against
its 5s wall-clock bound. This assertion included mapping/receipt persistence,
not only idle HTTP cancellation. The test now observes cancellation at the real
HTTP handler, keeping the 5s bound and 50ms configured idle window, and separately
requires an uncertain runner outcome. No runtime deadline or auth was weakened.
The root run compiled its audit tests before the new lifecycle regressions were
added; it is historical evidence, not a claim about the current S197.3 snapshot.

Behavioral red tests decode the proposed on-disk lifecycle fields into Event;
this made the old implementation run and fail rather than merely fail to compile.
Actual red: completed opens lost cleanup reserve at high water; foreign scope,
generation/action and unknown cleanup parent were accepted as ordinary operations;
cleanup of a known but uncertain opening was fenced. Core reds 7.970s, then
full audit race green 10.128s. Consumer reds confirmed missing linkage in Exposure
and local SSH protocol fixtures (6.326s/4.089s).

Schema-2 optional cleanup_action holds two maximum records plus segment/record
budget before the opening effect, in addition to the opening completion reserve.
The hold survives successful open, rotation and reopen by a different writer.
cleanup_of must reference that exact opening, expected action and full immutable
scope/generation. One cleanup admission consumes that resource's hold; only a
durable succeeded cleanup removes the target fence. The earlier unknown open is
still visibly unknown; its operation ID cannot be replayed. Unconfirmed cleanup
keeps its fence and reserve. An explicitly requested Exposure stop reuses its
unsettled cleanup admission rather than opening the resource or readmitting it.
There is no destructive TTL or inferred cleanup of an unknown provider object.

Exposure and SSH shell/tunnel objects retain their original admission; normal,
expiry/shutdown and post-open error cleanup use the link. Access termination still
runs if the ledger is unavailable and returns/reports uncertainty. Full Windows
race audit/exposure/SSH passed 36.679s/6.684s/31.517s after lifecycle wiring.
Additional invalid lifecycle, unverified-close and requested-stop recovery tests
passed in the final audit/exposure unit/race runs (7.047s/2.870s).

Receipt privacy reds demonstrated accepted auth URLs and serialized payloads
(shared audit 2.346s, API 1.039s). Shared finite receipt validation now rejects
URLs, credential labels, control characters and structured payload shapes;
Toolhub projects only canonical resource identifiers and its reviewed CLI proof
format. The first strict API draft dropped the existing CLI isolation/cell/runtime
receipt: a regression exposed this and it was corrected before acceptance.
CLI serialization now uses only the three mandatory verified isolation labels,
not controller-supplied extras. Reviewed fields remain available for recovery.
Producer field/key allowlists remain separate from this syntax guard: no generic
string filter is claimed to prove arbitrary data is nonsecret.

Targeted Windows race with lifecycle/privacy and idle HTTP cancellation passed:
audit 3.116s, Exposure 2.135s, SSH 11.856s, API 2.119s, Communication 3.116s.
Docs and format verification passed before the final CLI receipt follow-up;
the full current gate is being run below. Current native run did not execute Go:
WSL exited with Wsl/Service/0x8007274c (connection timeout). It is unavailable,
not a native regression pass; prior native receipts belong to earlier snapshots.
WSL/Docker services were not restarted because the original worktree is using
Docker. No image build, live provider mutation, rollout or working-data migration.


Producer review follow-up: the shared finite audit changes make existing audit
failure paths safety-critical. A real encrypted temporary credential store plus
an unavailable private audit file proves legacy Secrets.Set replaces then deletes
an existing credential during its compensating rollback. The Go overlay test
TestAuditFailureNeverDestroysExistingCredential failed with that exact assertion
(1.495s; .work/tmp/secrets-audit-preservation-red.log). Only synthetic data was
used, no working credential store read or changed. The regression is prepared in
.work/tmp/secrets-audit-regression_test.go and its overlay manifest, not hidden in
an accepted gate. S197.3 remains in progress: audit admission before legacy secret
mutation, safe post-effect uncertainty, batch/caller behavior and no destructive
rollback need correction before starting CHG-0198. Current full gate was started
before this overlay-only scenario and cannot establish this missing requirement.
CLI receipt follow-up targeted race passed 2.109s; fixed proof is preserved,
unknown controller isolation labels are omitted. No dependency changes.


Current S197.3 root `just check` exited 1 (.work/tmp/audit-lifecycle-root-check.log).
Its two failures were the unchanged Windows media fixture
TestComfyVideoResumesPersistedHandleWithoutSubmission (job.json sharing violation,
plus Go coverage metadata rename Access denied), and an existing Toolhub receipt
test expecting normalization of harmless surrounding spaces. Media targeted race
repeat passed 2.065s; this does not make the full gate green or establish current
aggregate coverage. SSH passed 4.736s in that full run; Toolhub/API completed in
137.143s/74.175s, but Toolhub remained failed due to the compatibility assertion.
Preserved the existing outer-whitespace normalization contract after rejecting
control characters/unsafe payload, so controls cannot be trimmed into validity.
Compatibility plus strict privacy and CLI proof regressions now pass under race:
Toolhub 2.178s, API 2.143s. The full current gate remains failed/not accepted,
not replaced by the narrow repeats. No new full-root run started yet: correct
the separately reproduced legacy Secrets.Set preservation failure next, then
rerun the required whole gate. All observed exec sessions are terminal.


## Legacy owner credential preservation follow-up (2026-10-29)

The previous goal turn was progress: lifecycle/privacy code and real filesystem
receipts changed, and a new synthetic encrypted-store regression demonstrated
Secrets.Set data loss on audit failure. No working credential store was accessed.
This follow-up promotes that overlay scenario into the real regression suite.

Red tests (2.207s) reproduced existing-credential deletion, unaudited write,
post-effect failure classified as retryable, partial batch reported rejected,
invalid batch changing an earlier value, concurrent owner writes escaping audit,
rotate/expose before durable audit, delete skipping registry revoke on audit
failure, ignored plaintext cleanup error and unaudited injection. Additional
reds (1.206s) proved sibling values erased by Set/Rotate and rotation accepting a
foreign owner's connection. A dictionary-key error privacy check already passed
after extracting the existing credential validator, not claimed as a baseline red.

Set validates every input first, then uses one durable owner-scoped batch intent
and completion reserve. Locators are resolved inside that admission. There is no
new transaction/queue framework or database. Unknown/partial writes retain their
ciphertext, quarantine attempted records as degraded and fence later owner
mutations without inheriting the fence into another owner. No compensating Delete
runs on audit failure. Readable multi-key records retain unmodified siblings;
unreadable siblings are not overwritten. Credential values and dictionary keys
are absent from the new audit events and static validation errors.

Rotate requires the selected connection to be owned by the credential owner.
Existing fail-closed degradation is preserved. Locally verified invalid input
never reaches Put; only a verified downgrade completes as failed. A lost status
ack remains unknown with its durable intent. An initial full race run exposed the
need to distinguish this known input failure from an ambiguous backend effect;
corrected code passes the existing locator/injection scenario without weakening
that scenario. Delete/terminal-disable attempt every access revocation leg even
when audit is unavailable, and no longer hide cleanup errors behind success.
Enabling exposure and authorized injection require admission before credential
access/materialization. Post-effect failure withholds the result as uncertain.

Activity privacy red (1.075s) proved legacy event.Name leaked a dictionary key.
The CLI projection now reports operation/phase/action/auth/execution/delivery but
omits credential dictionary names. The production CLI already configures its
ledger. Two positive Open-based seeding fixtures (CLI and Communication) now
explicitly attach real private ledgers; no production nil-audit bypass was added.
The compatibility ApplyChat helper reports uncertain rather than rejected after
partial writes; the real Communication gateway still rejects secret text and
uses its existing protected form flow.

Full Windows race Secrets/Credstore passed 4.137s/2.551s. Follow-up caller race
passed Secrets 2.044s, hubctl 2.905s, Communication 2.374s (Credstore in that filtered
run had no matching tests; its full receipt is the earlier 2.551s run). Tests use
real temporary encrypted files and instrumented post-write/status faults, not live
providers or live integration. Source diff whitespace check passed. New full
`just check` started in .work/tmp/secret-admission-root-check.log; no green claim
or aggregate coverage claim until its terminal result. Native WSL proof remains
unavailable from the previously observed Wsl/Service/0x8007274c; no service reset,
Docker access, deployment, working-data migration or external sending occurred.

## Windows root gate after legacy credential admission fixes

`just check` completed with exit 0; own Go statement coverage 85.96%. Local ignored log: `.work/tmp/secret-admission-root-check.log`. This is the pre-CHG-0198 snapshot, not verification of subsequent handoff changes. Docker and live acceptance remain deferred by the user.
