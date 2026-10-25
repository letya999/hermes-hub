# Sequential capability acceptance and CI latency

Authorized: restart the local dev stack, create a complete scenario registry with
mock and real Telegram modes, accelerate CI while preserving its quality gates.

1. Recreate existing infrastructure on the dev images; restore the host supervisor.
   Verify the routing and selected capability profiles without expanding grants.
2. Inventory the reviewed Hub tools, ToolHub control operations, native tools and
   opt-in connectors. Register input, independently observable result, prerequisites,
   cleanup and the existing automated boundary checks for each scenario.
3. Provide one sequential runner and separate evidence for offline contract checks,
   Docker-backed agent/channel E2E and real Telegram acceptance. Never promote a
   simulated provider response into live evidence. Telegram uses server injection without a sender account. Account-dependent external
   connectors are excluded from the core scope; missing grants or oracles are BLOCKED, not PASS. Keep local reports and account data outside Git.
4. Use measured Actions job durations to shorten the critical path. Split the native,
   supervisor and Telegram lifecycle gates and independent canaries into parallel
   jobs, preserve the required containers status and all current assertions.
   Retain the real five-minute idle and upstream run-lease checks.
5. Review new tests, run just check (race, shuffle, coverage >=85, lint, docs),
   validate the workflow, run the sequential mock suite and local Docker gates.
   Record actual results and distinguish predicted from measured CI acceleration.

No push, publication, broad Docker prune or changes to other projects.
