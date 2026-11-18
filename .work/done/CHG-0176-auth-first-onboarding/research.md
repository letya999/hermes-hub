# CHG-0176 research: what already exists vs the gap

Verified against dev @ 34620c4 before writing the plan.

## Already built (do not re-implement)

- `auth_before_tools_list` on `ConnectionRecipe` -> `declaredCredentialGate`
  + `PreparedCredentialGate`; draft persisted by `acceptCredentialGate`
  (`internal/toolhub/control_credentials.go`) with Tools stripped,
  `AdmissionPending`, Broker request minted via dynamic contract
  (`bindReviewedContract`).
- Credentialed deferred resume: `admitWithBrokerCredential`
  (`internal/toolhub/broker.go`) clears `AuthBeforeToolsList`, injects
  env+mounts+network and re-runs `Reviewer`, then registers the admitted
  definition. Temporary grant revoked and lease released on every path.
- `form_ready`/`artifact_ready`/`tools_confirmed` separated in `statusBody`;
  `connector_*` facade maps phases; `unsupported_auth` op code exists.
- OAuth declarative flow incl. generic `definition.OAuth`
  (`prepared_oauth.go` definition.OAuth branch), writable state +
  checkpoint (`oauth_state.go`), dead-flow re-arm and restart resume
  (tested: `prepared_oauth_test.go` clears the flow map mid-flight).
- File delivery to probe via read-only Broker mount
  (`broker_admission_file_test.go`, both built and deferred paths).
- Crash/timeout not masked as auth: `CredentialGateFrom` only opens on
  secret-shaped names or ErrEmptyToolList with declared required inputs.
- Expired/canceled Broker request healing: `brokerRequestDead` +
  `refreshBrokerRequest`/`regenerateBrokerRequest` + tests.
- Retry plumbing: `Rotate` clears `ErrorCode`, sets
  `BrokerRotateCredentialID`, mints a new request; `statusBody` reports
  `next_action: rotate` on `probe_failed`.

## Gaps found

1. Retry rebuilds the artifact. `DefaultSourceReviewer` discards the built
   `imported` definition on non-gate preflight failure
   (`return SourceReview{}, err`), `admitWithBrokerCredential` returns
   `ToolDefinition{}` on error, and the draft keeps `Source.Digest == ""`,
   so every retry re-runs fetch+`restrictedBuild`. Fix: carry the built
   definition out on failure, persist it (stripped) on the draft; the
   `Digest != ""` branch then admits via `AdmitWithCredentials` =
   `AdmitWithSubmittedCredentials` (CAS `StoredArtifactLoader` + tools/list).
   `telegram_send.go` caller ignores `admitted` on err — safe.
2. No `interactive` marker exists. `blocked.json` covers known-unsupported
   catalog entries only; an arbitrary source/clarified `connection_recipe`
   cannot declare interactive auth, and the journal maps every prepare
   failure to `probe_failed` — `ErrUnsupportedAuth` exists
   (`api.ErrorCode` -> `unsupported_auth`, `OpCodeUnsupportedAuth`) but is
   never emitted for this case.
3. Concurrent-owner admission has no dedicated test (admissionMu serializes
   `completeBrokerAdmission`; onboardings are per-owner).

## Acceptance criteria mapping (issue #293)

| Criterion | Status |
|---|---|
| auth-before-tools_list server -> correct form -> real tools/list | exists |
| file creds + OAuth-first, cancel, expired, wrong creds, network fail, retry, restart-resume | mostly exists; retry-without-rebuild is the gap |
| crash/bad deps/wrong argv not masked as auth | exists |
| form/artifact/tools readiness distinct | exists |
| isolation/concurrency/auth/HTTP-contract/no-secrets regressions | add concurrent-owners test |
| auto recipe + clarified recipe without Go change | exists; interactive marker must survive merge/filter (new test) |
| interactive servers classified unsupported | new `interactive` marker |
| live run on a real MCP | deferred — owner instruction + Docker |
