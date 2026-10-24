---
description: M15 completion requirements and acceptance boundaries.
last_verified: 2026-10-20
---
# SPEC-0070 — Model/provider completion

Frozen: 2026-10-20. Source: milestone 16, issues #131, #182, #185, #150.

## Requirements

- R1: Default, selected-group and owner settings resolve deterministically;
  conflicting groups fail with a redacted error. No implicit credential sharing.
- R2: Shared secrets/providers admit only explicitly granted owners. Rotation is
  idempotent and affects authorized consumers only. Shared/private proxy placement
  is independent; unsupported private-account routing fails closed.
- R3: Doctor reports effective profiles, provider/credential scope, placement and
  restart needs. Existing deployments retain their undeclared/default behavior.
- R4: Endpoint configuration is an operator action. Custom endpoints are
  allowlisted and DNS-guarded. Foreign callers and ungranted models are denied
  and audited without exposing credentials.
- R5: Ordered fallback has finite hop/deadline bounds; success streams without
  buffering the full response. Every attempted/skipped hop has a redacted receipt;
  exhaustion returns a clear error. Health is scoped to the owner/account.
- R6: Each media provider has an independent endpoint and credential source;
  fallback preserves the explicit logical-to-provider model mapping. Durable
  video jobs resume the accepting engine without resubmission.
- R7: An optional local ComfyUI engine uses service-owned workflow/model storage,
  bounded image/video results and the documented upstream API. No model files or
  optional ML dependencies are installed in the user runtime/base image.
- R8: images_inspect admits 1–8 workspace images, sends exactly one model request,
  rejects the entire unauthorized set, uses the model credential and returns text.
  Native vision stays disabled.

## Acceptance

- A1: Regression tests cover conflicts, two-owner denial, all proxy placements,
  render idempotence, rotation, endpoint denial, deadlines and failed reloads.
- A2: HTTP provider-contract tests cover independent media backends, Fal result
  fetching through routes, local image/video and durable restart polling.
- A3: A benchmark measures direct and relay success latency with identical
  payloads; record measurements without claiming remote-provider latency.
- A4: A two-owner live identity/read probe verifies credential/state separation.
- A5: E2E-021 compares two real workspace images through the channel and rejects
  an unauthorized third file before provider dispatch.
- A6: just check passes with >=85% own Go statement coverage; Docker and live
  acceptance are separately recorded and never inferred from mocks.
