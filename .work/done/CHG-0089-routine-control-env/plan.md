# Routine control environment in legacy hub MCP

Forward the private communication URL, per-runtime auth reference and user/principal
identity to the existing hub MCP child in new renders and legacy materialization.
Do not add a hub server to managed runtimes or weaken routine ownership checks.

Reproduce missing child environment with regression tests, run just check, update
the current local runtime configuration, and verify one real three-minute reminder
with exact text REMINDER-7319. Prove reminder delivery bypasses Hermes execution.
