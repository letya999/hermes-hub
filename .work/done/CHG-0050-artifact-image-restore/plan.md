# CHG-0050: restore pruned artifact images at admission

## Problem (observed live)

`docker image prune`/`docker-clean` removes tagged `hermes-artifact/*`
images when no container uses them. Published definitions still point at
those images, so the next `confirm`/tool-call admission fails with

> controller returned 503: workload enforcement unavailable:
> artifact-image: reviewed artifact image unavailable

and the onboarding is stuck at `awaiting-confirm` until the image is
manually reloaded.

## Fix

`resolveArtifactImage` now restores the image from the verified
quarantine tar (`<state_root>/artifacts/sha256-<archive_digest>.oci.tar`)
via `LoadStoredOCIArtifact` before falling back to registry pull or
failing. The stored archive is the durable copy that review attested, so
a pruned daemon image is self-healing instead of a hard failure.

Manual recovery used for the stuck notion onboarding: `docker load` the
archive tar + `docker tag` + status (fresh nonce) + confirm + enable —
landed `enabled` with 24 tools.
