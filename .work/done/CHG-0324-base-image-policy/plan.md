# CHG-0324: Base-image refresh policy (MVP M9)

Chain: operator request -> issue #393 -> this plan -> `state.yaml`.

## Goal

Decide and document the base-image refresh policy in `docs/operations.md`:
cadence, owner, verification, and the rejected alternative — and bring the
existing Dockerfiles into line with it so the policy is true the day it lands.

## Decision

Digest pinning plus scheduled reviewed bumps. Runtime-facing stages reference
`name:tag@sha256:`; build-only toolchain stages may stay tag-only; ARG-supplied
bases follow the caller contract (production passes a digest-pinned ref).
Dependabot `docker` runs weekly and opens bump PRs; the hub maintainer reviews
and merges; green CI `image`/`containers` plus post-merge `just docker-check`
on the exact published SHA is the verification. Rejected: in-layer
`apt-get dist-upgrade` — it mutates image contents without changing the
reviewed digest.

## Slices

### S1 Pin runtime-facing bases

Anchors: `docker/Dockerfile` stages `core`, `cli-tools`, `cell-proxy`,
`credential-broker`; `docker/observability/mockapi/Dockerfile`.

Change:
1. `debian:13.6-slim` -> `debian:13.6-slim@sha256:d7e12182…` (4 stages).
2. mockapi `python:3.13-slim` -> the `3.13-slim-trixie` digest already used
   by every other Dockerfile (`8d9d0b8b…`).
3. Add Dependabot `docker` coverage for `/services/credential-broker/deploy`.

### S2 Document policy

Anchors: `docs/operations.md` "Images and builds".

Change:
1. One paragraph: scope/cadence/owner/verification/rejected alternative.

## Acceptance

- `docs/operations.md` states cadence, owner, verification, rejected
  alternative (issue #393 acceptance).
- `devcheck docs` green; no runtime `FROM` left tag-only.
