# Hosting implementation evidence — 2026-10-26

Worktree: `hermes-hub-m18-hosting`, branch `feat/m18-exposure-hosting`,
base `5c19f3e027d0fe4f5abcd74c101d362b52fae073`. No Docker command,
deployment or user-code workload was executed. Synthetic temp files and
loopback HTTP servers only. Logs/profiles/binaries are ignored `.work/tmp/`
artifacts, confirmed with `git check-ignore`.

## Requirement-to-evidence map

| Acceptance | Source boundary / tests | Evidence limit |
|---|---|---|
| A1 | hosting store cross-owner denial; stack slot/manual endpoint validation; agent strict input | Real Go filesystem/API, no deployed multi-user routing |
| A2 | second writer/replay; revision concurrency; failed prepare sequence; replacement preservation | Native crash/power-loss injection still pending |
| A3 | snapshot unsafe files, hardlinks, confined roots, source manifest validation | Windows symlink privilege-dependent skips are not Linux proof |
| A4 | quota/replacement/audit pressure tests; file-lock process test; fleet concurrent reservation | Runtime disk-full/host exhaustion acceptance pending |
| A5 | static/control listener separation; share assets/range/HEAD/revoke and exact Host tests | Actual TLS and browser caches not tested |
| A6 | bootstrap CSP regression; namespaced app-cookie and Origin/credential stripping contracts | Browser multi-asset/service-worker/sibling-origin acceptance pending |
| A7 | ToolHub profile drift negatives including root/env/mount/device/network/runtime/caps | Fixtures prove rejection logic, not kernel isolation |
| A8 | secure-runtime absence/profile checks; Linux compilation | Real runsc/Kata resource/escape canaries not run |
| A9 | persisted restart/finalizer/pending/tombstone tests; monotonic failed revisions | Actual daemon/SSH/host restart and expiry checks pending |
| A10 | HTTP body/control overflow, rate/inflight/audit bounds; serve status/bytes journal; bounded relay child output | Runtime exhaustion and load/SLO claims excluded |
| A11 | route reconciliation contract and stack owner/env rendering | Real sish DNS TLS reconnect and post-chat URL checks pending |

## Commands and results

- `go test -race ./internal/hosting ./internal/toolhub ./internal/agenttools ./internal/stack ./internal/exposure ./cmd/cellinit -count=1`: passed (changed production packages).
- After final audit/overflow changes, `go test ./internal/hosting -race -count=1`: passed.
- `go test ./internal/hosting ./internal/exposure -count=1`: passed.
- Linux amd64 `go test -c` for `cmd/cellinit` and `internal/toolhub`: passed; compilation only, binaries never executed.
- `just lint docs-check`: passed before final small endpoint-name change; final rerun in progress.
- Aggregate `devcheck covergo ... 85 75`: in progress; no final coverage claim yet.
- Full `just check`: not run because its config-check invokes Docker, prohibited by the owner.
- Docker/native/sish/browser acceptance: not run. No integration success claim.
- `go.mod`/`go.sum`: unchanged; no new dependency, so dependency-change `just security` gate does not apply.

The first aggregate attempt failed during concurrent file creation (a child build
saw a helper before its platform counterpart existed); a clean rerun is tracked
separately. The earlier pre-existing malformed-profile parser panic has a
red/green regression under CHG-0175; corrupt coverage is still rejected.

## Self-review (not an independent security audit)

Checked authority propagation, immutable release names, recovery ownership,
monotonic failed revisions, metadata/revocation headroom, exact release mounts,
pre/post-start inspection, fleet overlap, control credential separation,
fragment bootstrap CSP and parent-cookie stripping. Found issues were corrected
and negative contracts added. Native claims remain unproven: particularly
volume-subpath support, runsc/Kata semantics, PID1 process isolation and resource
limits, browser cookie/CSP behavior and sish reconnection.

No code was copied from Haloy. The existing Go admission/controller and standard
library are reused. Broker application-env delivery remains unsupported and no
implicit secret inheritance is enabled. There is no issue closure or deployment.
