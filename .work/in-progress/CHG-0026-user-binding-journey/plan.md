# CHG-0026 — M5.2 user binding journey

Depends on **done** CHG-0025 (trusted SHA artifact + local isolated runtime).
Does not rebuild the builder. Does not patch Hermes or ToolHive.

## Outcome

A person attaches an already-trusted GitHub MCP to *their* account, Hermes
sees only their tools, the workload starts on demand from the shared image,
and disable/rotate cuts the next `tools/call`.

## Slices

1. Bind trusted definition → per-user connection + credential ref (or none
   if credential-free).
2. Project only that principal’s tools; enable/disable reloads; stale
   session denied (SPEC-0019 — prove live).
3. Start on demand from `generic-controller` / `docker_fallback`; idle stop;
   image reused.
4. Revoke/rotate cuts the next call (seed: CHG-0025 Gateway revoke probe).
5. Hermes chat “connect this GitHub MCP” stays #74 / M5.3 if reconnect is
   not ready; M5.2 still ships bind/project/revoke through ToolHub.

## Out of scope

#103 caller catalog mount, CHG-0024 live login, VPS, 100 concurrent stacks.
