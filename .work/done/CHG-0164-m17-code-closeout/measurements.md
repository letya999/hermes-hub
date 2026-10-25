# CHG-0164 measurements — #326 watchdog overhead, #327 admission/fairness

Host: Windows dev box, AMD Ryzen 7 7435HS, go1.x -benchtime short runs.
Numbers are local synthetic evidence, not the Docker production gate.

## Watchdog sweep (#326)

`BenchmarkWatchdogSweep20` — one full sweep (`m.sweep`) over 20 owned
runtimes with stubbed docker command/probe and a stats sampler returning
one row per container:

    BenchmarkWatchdogSweep20-16    20    56,571,850 ns/op  (~57 ms/sweep)

Breakdown of that cost: orphan detection, per-entry health probe (stubbed
instant), one stats sample call, supervisor.json persist. At the production
cadence (WarmTTL/2, max 5 s tick) a 57 ms in-process sweep is ~1% of one
core; real `docker stats --no-stream` latency adds once per sweep, not per
container.

## Admission ledger (#327)

`BenchmarkReserveSettle` — enqueue (ledger reserve + job file + mapping) then
terminal outcome (settle) per op:

    BenchmarkReserveSettle-16            30    55,435,463 ns/op  (~55 ms)

`BenchmarkEnqueueToClaimLatency` (pre-existing enqueue→claim→complete path,
now including the ledger write):

    BenchmarkEnqueueToClaimLatency-16    30    41,025,327 ns/op  (~41 ms)

Both are fsync-dominated on a Windows NTFS temp dir; the durable write chain
is the cost, not the ledger bookkeeping. `TestBudgetConcurrentReservation`,
`TestBudgetReplaySettlement`, `TestBudgetIncludesChildrenAndMemory`,
`TestBudgetUnknownPricing` cover correctness; idempotent re-enqueue does not
double-count.

## Fair claim at 20 scopes (#327)

`BenchmarkClaimFair20Scopes` — claim+fail cycle over a pending set of 20
scopes:

    BenchmarkClaimFair20Scopes-16        30    17,960,843 ns/op  (~18 ms)

Fairness itself is regression-tested (not benchmarked):
`TestTenantFloodDoesNotStarveOthers` — an 8-job burst in one scope does not
delay another scope's single job, and rotation survives spool reopen.
`TestBackgroundAging` — a cron job parked >5 min claims as foreground.
`TestControlReserveUnderLoad` — control deliveries claim first and enqueue
past the ordinary outbox cap.

## Ceilings verified by regression

- `TestCeilingsPendingQueue`: per-scope + total pending caps, settlement
  frees headroom.
- `TestCeilingsTokenDaily`: settled+reserved count against the daily cap.
- `TestCeilingsSpoolBytes`: pending-queue byte cap refuses admission.
- `TestToolCallRunCeiling`: per-run tool-call cap; other runs and
  uncorrelated control calls unaffected.
- `TestRuntimePerPrincipalCeiling`: `HUB_MAX_RUNTIMES_PER_PRINCIPAL` bounds
  one principal's live runtimes; other principals unaffected.
