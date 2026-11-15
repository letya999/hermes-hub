# CHG-0110 — Routine recovery and bounded history

## Incident

The dev communication hub logged a stage-1 routine failure every five seconds. A historical terminal mapping had a different fingerprint, and one bad record stopped the entire occurrence pass. The spool had ~13,800 occurrence files, including ~7,800 pending watcher ticks; each pass scanned all files. Six active watcher schedules added roughly 330 occurrences/hour.

## Change

- Preserve an active occurrence per schedule revision; skip new ticks while a probe, script or agent job remains in flight.
- Skip historical terminal mappings when finding active work. Isolate malformed occurrence files in a quarantine directory and isolate a nonterminal mapping drift to its own schedule, retaining evidence and blocking overlap.
- Move settled silent occurrences from the hot directory on compaction, keep full detail in an archive for 48 hours and a small deduplication receipt under the existing spool retention window (normally 14 days). Never compact pending/probing/scripting records or dispatched records while delivery/job work is active.
- Include the cause in routine-stage failure logs.

## Verification

- Red/green regressions for coalescing, historical mapping drift, corrupt-record isolation, replay protection and retention.
- `just check`, own Go statement coverage >=85%, security scan, and local Docker gate where available.
- No live schedule mutation or external message in this code worktree. Verify backlog and select the exact schedule in the running stack before a controlled pause/migration.

## Remaining operational work

The existing pending backlog drains at the established limit of 16 settlements per pass. After deployment, observe the count and latency, then pause only the identified artifact schedule through the owner API and confirm it stays paused. A bulk migration is deliberately separate because changing settlement rate affects the frozen routine contract.
