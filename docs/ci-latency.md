---
description: Measured CI critical path and retained verification gates.
last_verified: 2026-10-11
---
# CI latency

Baseline: Actions run 38153315312, revision 21305c548e8e750696d90da3d0ec28527b0860d5. End-to-end elapsed 21m13s. Go shards took 23/44/101/63s. The aggregate Go gate took 105s. Dev/prod image jobs took 307/317s; dev/prod container gates took 899/950s. The critical path is image construction followed by sequential container verification.

The revision argument previously appeared before the runtime dependency installs. Docker applies declared build arguments to later RUN instructions, so each commit invalidated expensive dependency layers. The revision label now belongs to the final dev/prod/control stages, after filesystem changes. Dependencies remain pinned. Core/control cache exports use separate scopes, so the smaller control export cannot replace the core cache. Go cache keys include the root module sum and nested module manifest. Module downloads live in a normal exported Docker layer, keyed only by the root manifest/sum and nested manifest. BuildKit cache mounts are not preserved in the GitHub Actions cache by default; keeping downloads only in those mounts made fresh runners download modules again after source edits. Local compiler cache mounts remain available.

Eight independent container gates now run separately for each dev/prod target: smoke/broker, native upstream contract, supervisor lifecycle, gateway lifecycle, capabilities, managed network, managed supervisor and scratch workloads. The existing combined local entry point remains available. The required containers status waits for every matrix child and accepts only success; failed, skipped and cancelled gates fail the aggregate. A pulled core image must bear the exact checkout revision, preventing a mutable edge fallback from passing the suite.

All assertions and the actual five-minute idle/run-lease waits are retained. Consequently a 5–10x reduction of the entire baseline cannot be promised: a mandatory five-minute wait alone sets a lower bound above 4m15s, which would be required for 5x. Cache effectiveness, runner capacity, image download time and queues determine the actual result. The new workflow has not been pushed or measured in Actions. Its matrix uses more simultaneous runners and may consume more runner minutes.

Verification: run just check, then the local native/supervisor/gateway/capability/network/managed/scratch gates on the built image. After an authorized push, compare at least one cold and two warm Actions runs against the baseline; inspect every matrix result and the exact revision labels. Report both elapsed time and total runner minutes. Do not remove slow assertions merely to meet a speed target.

Sources: [Docker build-cache argument rules](https://docs.docker.com/reference/dockerfile/#impact-on-build-caching), [GitHub Actions cache backend](https://docs.docker.com/build/cache/backends/gha/), [Docker CI caching](https://docs.docker.com/build/ci/github-actions/cache/).
