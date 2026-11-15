# CHG-0306 — Overload as explicit state (admission + adaptive throttle)

Incident: toolhub restart drained a ~5k memory-capture backlog; serial pump
drove continuous expensive captures (2x Decide + 2x enrollment + fsync each),
starving /v1/rights/decide (60-187s) -> model-relay timeouts -> denied runs.

Root cause: overload is nowhere a state. Background traffic carries full
priority, the server cannot refuse cheaply, the producer cannot slow itself.

Mechanisms (SRE "Handling Overload" + client-go workqueue invariants):

1. loadGate on toolhub Gateway: in-flight counters + EMA of interactive
   request latency; class wrappers interactive()/background().
2. background() sheds with 429 + Retry-After BEFORE work when overloaded
   (interactive EMA > threshold or background in-flight > cap).
3. communication pump: SRE adaptive throttle — local reject with
   P = max(0,(req - K*acc)/(req+1)), K=2, 2-min decayed window, P<=0.9;
   429/5xx/transport error counts as reject; 3 consecutive failures break
   the pass; sweep ticker retries.
4. Dedupe inside the vault lock: the SECOND memoryEnrollment (adjacent to
   the write) is the real check-at-effect — the pre-Decide call was the
   redundant one; order swapped so enrollment is re-read nearest the
   effect. Regression test TestCaptureRechecksAuthorizationAtCommit
   proves the window stays closed.

Acceptance (CHG-E): pump against a stub returning 429 stops the pass;
gate sheds background while interactive EMA is hot and recovers.
