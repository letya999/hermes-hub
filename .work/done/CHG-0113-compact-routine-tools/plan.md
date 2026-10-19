# CHG-0113: Compact routine tools

ADR-0043 -> SPEC-0057. Preserve the dirty branch and previous task changes.

1. Red/green public five-tool contract in agenttools capability/Server/Call.
2. Red/green edit action validation and get includes in routines.go/Input.
3. Red/green query filtering in communication HTTP/routinePage; update callers,
   managed catalog contracts, E2E selectors and stable prompt instructions.
4. Update current docs; retain accepted ADR-0042 and frozen SPEC-0056 unchanged.
5. just check; supported dev-rebuild for affected services/runtime executor;
   register 3.0.0 through operator CLI preserving current access mode; actual MCP
   lifecycle using a future synthetic fixture, no external send needed.

Acceptance evidence belongs in state.yaml. No dependency, upstream change,
publication or production rollout. Keep old private dispatch for pinned callers.

Local acceptance complete: five-tool deployed MCP lifecycle, final runtime image
contract and full just check passed. See state.yaml for coverage and boundaries.
