# SPEC-0068: Dynamic owner session contracts

Frozen: 2026-10-20. Decision: ADR-0071. Delivery: CHG-0144.

- R1: Authenticated `broker:control` may register a contract with one JSON state
  file bounded to 65536 bytes, using existing state validation and persistence.
- R2: Dynamic state cannot be shared, raw, oversized or contain multiple files.
  Dynamic routes and Broker OAuth providers remain rejected.
- R3: Registration never grants credential access. Existing enrollment,
  immutable digest, owner grants, lease isolation, single writer and audit apply.
- A1: Registration survives restart and permits ordinary owner enrollment.
- A2: Negative variants fail closed; existing authorization and state suites pass.
- A3: Module `just check` retains >=85% overall and per-package coverage.
