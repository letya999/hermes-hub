# CHG-0239 evidence — 2026-10-31

Only aggregate counts, size and digest are recorded here; no raw audit events
or auth material.

- Dry-run validated the active local unsegmented ToolHub audit: schema-1-only,
  181,794 records, 85,097,535 bytes, digest
  `sha256:578d3b68a69d4b7f7357d1b32059d3976e8b34089cb01117b883c453a98324f7`.
- Unit regressions passed for verified archive, new bounded append, stale
  digest, mixed schema, corrupt input and symlink refusal.
- The same digest and counts were independently obtained by the Linux `hubctl`
  dry-run on the selected ToolHub state bind. After stopping only the local
  ToolHub, `--apply` succeeded. The active ledger became 255 bytes with a
  rollover marker; the verified gzip archive is 7,653,618 bytes. No `.raw`
  or `.partial` copy remained. ToolHub restarted and HTTP port 8090 responded.
- A separate post-restart read verified the compressed archive expands to the
  original 85,097,535 bytes with the original SHA-256 and that `ReadPage`
  returns the schema-2 rollover marker. `hubctl dev-rebuild` then recreated
  ToolHub; its running image digest matched the rebuilt dev image, HTTP still
  responded, and the archive and marker verification passed again.
- First root `just check` failed under parallel Windows load: the large
  Communication Hub race package timed out at 10 minutes while blocked in
  Windows `fsync`; `TestSurfaceProbeHubChildSelfTestRunsOnce` reported
  `child_spawn_failed`. Both named tests passed three isolated race runs each.
  The full `just check` retry with bounded Go parallelism passed; own Go
  statement coverage was 86.22% (required 85%).
- Selected core mock E2E ToolHub contracts `E2E-050` through `E2E-063` plus
  `E2E-137`: 15 PASS, 0 FAIL.
