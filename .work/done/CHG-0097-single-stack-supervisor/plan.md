# CHG-0097: One Docker stack, no Windows supervisor service

Owner instruction, 2026-10-14: keep one main stack, spawn isolated user workloads
only when needed, remove session test debris and unnecessary Windows services.
Implements SPEC-0053 R4 deployment simplification; follows CHG-0096.

1. `internal/supervisor`: container DNS for runtime/control-relay transport;
   translate only bind sources under the configured spaces root. Keep ownership
   checks, per-principal mounts, durable admission and lifecycle semantics.
2. `internal/stack/render.go`: render the supervisor in the existing control
   image when selection URL is `http://supervisor:8876`. Reuse the Docker CLI
   seam and socket already used by the control plane; no extra image/database.
3. Regression checks cover bind containment, ownership, private networks,
   no host ports on spawned workloads, and unchanged host mode.
4. Drain/stop the Windows writer, back up state, start the Docker writer, verify
   a synthetic job and restart, then remove the session's Windows task.
5. Delete only verified session fixture containers/networks and superseded hub
   images; retain user volumes and configured optional integrations.

Validation: `just check`, targeted Docker contract and restart/idle acceptance.
Rollback: stop container writer before restoring the existing Windows task/state.
Docker startup was blocked by stale AF_UNIX sockets. Deletion was rejected by
automatic approval. After the owner's renewed repair instruction, a reversible
recovery preserved the two socket-only directories under backup names. Docker
Desktop recreated its endpoints and engine 29.4.3 started; no Docker data reset
or volume/image deletion was used to repair startup. Docker AI was already off.

## Verification and cleanup record

- Container/host transport, bind containment, foreign workspace rejection and
  Compose topology have regression tests. Targeted supervisor/stack tests passed
  with race detection; combined `just check` passed at 85.32% coverage.
- Removed the disabled legacy `MCP-Hub` scheduled task pointing at the previous
  `mcp-hub` project. Saved its XML under ignored `.local/` first.
- Removed `HermesHubDevSupervisor` after the container canary and restart
  adoption passed; its pre-migration XML is saved locally. No Microsoft system
  services were modified.
- Stopped only the session's hung Docker diagnostic commands. After engine
  recovery, inventoried containers, images and volumes before scoped cleanup.
- Restored `docs/architecture.md`, which was left empty by the interrupted write.
  Fixed two trace unit tests that contacted the real daemon; trace itself now has
  a ten-second deadline so a failed daemon cannot hang diagnostics indefinitely.
- The OAuth relay failure test now uses an empty executable search path rather
  than contacting a real daemon for a deliberately missing container; three
  race-enabled repeats passed. The Docker CLI smoke probe has a five-second
  timeout and explicitly skips when the daemon is unavailable.
- Removed the old empty `hermes-hub-control` network after checking attached
  containers; Compose created and owns its replacement.
- Engine recovery enabled verified cleanup: five stopped smoke fixture
  containers and forty unused synthetic probe/chaos broker volumes were removed.
  Exact removed volume names are recorded in ignored `.local/`.
- All four live runtime entries were stopped with zero leases. The stopped
  gateway spool reported no pending or uncertain jobs and no pending deliveries.
  Historical uncertain delivery receipts are preserved without replay.
- Backed up the ledger and three execution selections under ignored `.local/`;
  changed only their supervisor endpoints to Docker DNS, keeping the same
  executor mode and native-cron disposition. Re-rendered each affected context.
- Live managed wake passed using internal relay DNS with no host port bindings.
  Restart adopted the same runtime generation; a separate timed restart restored
  authenticated health in 2.854 seconds. The Windows supervisor task was then
  removed, keeping its XML backup. Evidence: `.local/m19-container-restart.json`.
- Legacy wake exposed chmod of a host-owned effective YAML file. Materialization
  now validates a temporary file and atomically replaces the destination for both
  modes. Regression verifies replacement and retention of the last valid config
  after invalid YAML. `just check` passes at 85.32% coverage; image redeploy remains.

## Owner acceptance amendment, 2026-10-14

The owner explicitly authorized autonomous acceptance and real Telegram test
sends, and waived the 24-hour waiting period for this delivery. Run a bounded
accelerated exercise with 30 interactive jobs, fault/recovery checks and actual
observation duration. This waiver does not constitute evidence of 24-hour
stability; keep the frozen original specification and report the shorter window.

- Build networking: long APT queues stalled despite single-file downloads
  completing in about one second inside the same namespace. HTTPS alone did
  not fix it. Disabled HTTP/HTTPS pipelining in the Python runtime stage; the
  107th package then started at 50.69s rather than 334.2s in the HTTP run.
  Source: https://manpages.debian.org/bullseye/apt/apt-transport-http.1.en.html
  (`Pipeline-Depth=0` is the documented compatibility setting). Final download
  duration and deployment are still pending.
