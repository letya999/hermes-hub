# CHG-0232: Runtime credential references and spawn diagnostics

## Goal

A sibling principal never receives the ToolHub owner's self-env value. Spawn failures expose only a safe reason class.

## Changes

- Route skill ref leases through the supervisor to the caller's running runtime, with principal/context/runtime/policy fences and runtime allowlists.
- Classify spawn failures as hold, backoff, policy_rotation, spawn or unavailable in bounded HTTP responses and logs.

## Verification

- `TestSkillRefNeverReadsForeignLocalState`, `TestSkillRefLeaseRoutesOnlyMatchingRuntime`, `TestRuntimeFailureClassesAreAllowlisted`.
- `just check`; running dev stack and Docker checks remain separate gates.
