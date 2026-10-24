# CHG-0150 — M15 implementation and acceptance closure

Chain: ADR-0073 → SPEC-0070 → this change. Extends CHG-0139; issues #131,
#182, #185 and #150 in milestone 16 (M15).

## Goal

Finish model/provider isolation, deterministic profiles, bounded fallback and
local media routing in the isolated `feat/m15-model-provider-layer` worktree. Preserve the
original checkout and all inherited uncommitted work. Never deploy to Docker.

## Slices

1. Media routing: `mediasvc.genEngineNamed`, `Serve`, `ConfigFromEnv` —
   provider-specific upstreams/grants, Fal result retrieval inside routers,
   validated bounded hops, explicit unsupported-edit errors. Regressions run
   real service handlers against HTTP fixtures, including failure/recovery.
2. Relay: `modelCall`, `relayRegistry` — deadlines, streaming, origin/DNS guard,
   owner authentication, strict model grants and hot-reload validation; Broker
   credential resolution and redacted hop receipts. Negative tests reject
   foreign callers, unsafe endpoints and unknown models before upstream calls.
3. Profiles: `applyProfiles`, `LoadModelRegistry`, `DoctorScope` and rendering —
   reject conflicting group assignments, explicit credential scopes/topologies,
   restart fingerprints and redacted effective diagnostics. Two-owner tests
   cover isolation, idempotence and rotation without live accounts.
4. Local media: opt-in ComfyUI adapter using the upstream `/prompt`, `/history`
   and `/view` contracts, workflow files confined to the service model volume;
   image and durable async video, bounded outputs and no runtime model files.
5. Acceptance tooling: implement the images_inspect live oracle (E2E-021),
   owner/provider read-only probes and benchmark success-path overhead. Keep
   real-provider/channel results distinct from HTTP fixtures.
6. Documentation and full checks: update operations/service docs and trace
   requirements to evidence. `just check`; security if dependencies change.

## Safety and red-team

- No source edits, stash, reset or commits in the original checkout.
- No Docker daemon actions, builds, image pulls, service restarts or account login.
  `just check` includes read-only synthetic `docker compose config` validation;
  it does not contact a daemon or change containers/images/volumes.
- No provider mutations or external sending during verification.
- Redirect, DNS rebinding, foreign owner, missing grant, invalid reload and
  interrupted persistence fail closed; never retry an uncertain submission.
- Broker secrets never enter config, receipts, argv, examples or job records.

## Acceptance

Source and fixture gates can run locally. Docker and live account/channel gates
remain explicitly pending under the owner's constraint; implementing an oracle
does not count as executing it. Do not close GitHub issues or mark this change
done until the required acceptance evidence exists.
