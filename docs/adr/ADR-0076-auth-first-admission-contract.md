---
description: Auth-first admission completion — built-evidence reuse on credential retry and an explicit interactive-auth marker.
last_verified: 2026-10-25
---
# ADR-0076: Auth-first admission contract completion

Status: Proposed — implementation in CHG-0164 (issue #293).

## Context

The generic `auth_before_tools_list` route already defers build/pull/probe
until the protected Broker form delivers credentials, then resumes review
with a temporary owner-bound grant. Two gaps remained. First, a failed
credentialed probe discarded the built OCI artifact: the reviewer dropped
the imported definition on non-gate errors, admission returned no evidence,
and the persisted draft kept an empty digest — so every credential retry
rebuilt an unchanged artifact. Second, a server whose authentication lives
in its own interactive terminal or browser profile had no declarative way
to say so: arbitrary sources and clarified `connection_recipe` input could
only imitate a normal credential form.

## Decisions

1. On a failed credentialed probe the built definition's source evidence
   (image/archive/provenance/SBOM/recipe digests and entrypoint) is persisted
   on the onboarding draft — Tools and tool-contract digests stripped, so
   `tools_confirmed` stays false and registration still waits for a real
   tools/list. A credential retry then runs the stored-artifact probe path
   only; rebuild happens only when the artifact itself is missing or the
   source generation changed.
2. `ConnectionRecipe` gains `"interactive": true`: a declaration that the
   server authenticates in its own interactive terminal/browser context.
   Any merge or filter preserves it; review rejects it with
   `ErrUnsupportedAuth` before any form, build or pull, and the connector
   journal reports `unsupported_auth` (never a fake form).
3. Known-unsupported catalog servers continue to live in
   `prepared/blocked.json`; the `interactive` marker covers arbitrary
   sources and clarified recipes, not prepared entries.

## Alternatives

Content-addressed artifact reuse across onboardings was rejected: reuse is
bounded to the same onboarding's pinned artifact, which matches the issue's
"unchanged artifact" wording without a new cache trust domain. Auto-detecting
interactive auth from probe error text was rejected — over-detection turns a
normal failure into a misleading classification; declaration stays explicit.

## Invariants kept

No secrets in build context, Dockerfile, build args, argv or logs; the
protected form stays the only credential intake; `ErrUnsupportedAuth` never
produces a credential surface; owner/context/runtime/policy scoping and the
per-artifact CAS evidence model are unchanged.
