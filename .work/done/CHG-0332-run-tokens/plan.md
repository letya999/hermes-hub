# CHG-0332: Signed run tokens and policy bundles (M40 stage 4)

Chain: [ADR-0106](../../../docs/adr/ADR-0106-identity-layers-run-tokens.md)
D4-D7 → [SPEC-0100](../../../specs/active/SPEC-0100-v1-contracts.md)
R6.1-R6.3 → this plan → state.yaml. Issue #576. Requires CHG-0331.

## Goal

Every protected runtime call carries a signed, audience-specific identity
bound to one admitted run. Every enforcement point checks the current
generation and revocation through task/v1 and applies the matching signed
policy bundle before backend dispatch. Grants remain in policy bundles.

## Slices

1. **S4.1 Control signing.** Extend identity/v1 with the public key contract
   and control-owned identityhub with Ed25519 key custody. Use distinct
   signing purposes for run, link, service and policy. Rotation retains
   the previous public keys for one maximum run budget. Publish only public
   material. Test rotation overlap, expiry, restart and public-key projection.
2. **S4.2 Verification.** Verify compact JWS with a fixed algorithm and
   purpose, issuer, audience, key ID and expiry. Run claims bind subject,
   owned actor, organization, space, conversation or assignment, task, run,
   generation, policy revision, origin, mode and deadline. Record system
   initiators separately. Test wrong purpose/audience, missing claims,
   forged signature and cross-space substitution.
3. **S4.3 Policy publication.** Start at `internal/passport/publish.go`
   and supervisor's effective template. Publish the six passports and the
   platform/org/user/agent constraints as signed canonical JSON by digest.
   The pointer changes only after the signed bundle is available. Every
   enforcement point verifies the digest and signature and applies narrowing
   grants, memory scopes, guardrails and mode. Test unsigned, foreign and
   mismatched bundles and null-versus-empty ceilings.
4. **S4.4 Live authorization.** Extend `contracts/task/v1`,
   `internal/supervisor/jobs.go` and `internal/toolhub/memory_run.go`.
   Fence every run-scoped call at the durable job record, current generation,
   current policy and owner/assignment revocation. Cover tool, memory, model
   and egress dispatch, including nested calls. Refusals return typed errors
   and audit fixed safe fields only.
5. **S4.5 Agent-to-service delegation.** Replace principal assertions in
   ToolHub's `controlPrincipal`, automation HTTP and agenttools routines,
   goals and assignments. Bind service operations to their own purpose.
   Recheck assignment ancestry and rights before a child can call a backend.
6. **S4.6 Runtime binding.** Change the supervisor dispatch and the private
   runtime job contract in `internal/runtime/server.go`. Upstream Hermes
   returns its run ID after admission in `server_observe.go`. Bind the
   admitted job to that exact ID in control before letting the MCP proxy
   or other runtime relays forward run-authorized calls. Keep concurrent
   lanes and re-observation isolated. Strip caller-supplied authority at
   each proxy and keep bearer values off disk, out of logs and out of model
   inputs. Preserve the pinned upstream Hermes implementation.

The existing supervisor can run five conversations in one Hermes process.
An active container or runtime bearer cannot select a run for a model request.
The pinned Hermes run executor sets its approval context to the exact run ID.
Request middleware can carry this correlation to a trusted local proxy. Verify
the path with overlapping runs, fallback and retries before live rollout.
Use the runtime's confirmed admission to attach the bearer in Go.

## Acceptance

- Red-first backend-spy tests: `TestForgedRunTokenRefusedBeforeBackend`,
  `TestRunAliasRefused`, `TestStaleGenerationRefused`,
  `TestPolicyRevMismatchRefused` and
  `TestRevocationCascadesToAssignmentRuns`.
- A6.1 requires one token-required case for every agent-facing handler.
  A6.2 requires zero backend calls on refusal, including injected trusted
  headers, a replay after generation change and a request for a foreign
  workspace. A6.3 requires unsigned or mismatched policy bundles to fail
  closed at load.
- Run named regressions and vet during editing, then `just check`, affected
  `just test` packages and the integration build. Record source SHA and
  remote CI URL after authorized publication. Live space changes, CI dispatch
  and remote publication require explicit owner authorization.
