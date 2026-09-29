# CHG-0054: Diagnostics hardening

Follow-up to CHG-0053 (issue #125). Critical-review fixes for the bounded
diagnostics pipeline.

## Scope

- Redact secrets at collection time (combined file must not store plaintext
  secrets), keep projection-time redaction as a second layer.
- Continue collection when one container's `docker logs` fails; keep its
  cursor position and report the failure instead of aborting the cycle.
- Per-container throttle: at most 5000 lines per collection cycle.
- Seek-based supervisor.log reading instead of full-file read each cycle.
- Wider provider-prefix redaction (ATATT, AIza, xoxe., lin_api, SG.,
  sk_live/pk_live, github_pat) plus a bare high-entropy token filter that
  preserves lowercase hex digests/SHAs.
- Host-supervisor projection: whole-token container-name match so a foreign
  container extending the caller's name cannot leak.
- Per-principal request cap (2) under the global cap (4) for diagnostics.

## Verify

- New unit tests: ingest redaction, continue-on-error + cursor stability,
  Redact cases, prefix-extension supervisor line.
- `just check`, package tests for internal/diagnostics and internal/toolhub.
