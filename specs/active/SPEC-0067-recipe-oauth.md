# SPEC-0067: Recipe-driven OAuth installation

Frozen: 2026-10-20. Decision: ADR-0070. Delivery: CHG-0144.

## Requirements

- R1: A validated OAuth recipe works independently of repository name and the
  compiled prepared catalog, through existing source/human review.
- R2: Provider URLs, scopes, client extraction, token JSON, state and read probe
  are bounded declarative data included in immutable review/credential digests.
- R3: No scripts, private/non-HTTPS auth endpoints, arbitrary tool-output auth
  links or weakened identity/authorization protections.
- R4: File views stay within the same lease; only quiesced workloads checkpoint.
- R5: Calendar and taylorwilsdon Workspace use normal install/status/confirm;
  no per-repository OAuth handler, upstream patch or new runtime dependency.
- R6: No secret bytes in recipes, results, logs or argv; remote OAuth preserved.

## Acceptance

- A1: Non-catalog recipe with different provider/layout reaches browser auth.
- A2: Unsafe URLs/selectors/templates, path escape/symlink, foreign identity,
  replay and missing scopes fail closed.
- A3: Both selected MCPs complete normal control-plane setup and real reads;
  mocks alone do not satisfy live acceptance.
- A4: New leases/restarts retain sessions without exchanging owners' state.
- A5: `just check` passes with >=85% own Go coverage; unavailable live/Docker
  gates are recorded honestly.
