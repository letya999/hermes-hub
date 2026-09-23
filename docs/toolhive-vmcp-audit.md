---
description: Issue 11 compatibility and performance audit for ToolHive local vMCP.
last_verified: 2026-09-11
---
# ToolHive local vMCP audit

## Decision

**No-go for the stable per-user Hermes endpoint in the current release.** Keep Hermes
connected directly to its configured MCP servers. ToolHive remains a useful opt-in
container/proxy runner, but local vMCP 0.48.0 must not become the production ToolHub
gateway until dynamic add/remove and authenticated identity projection pass the probe.

This is deliberately not an integration of ToolHive into the Hermes image. The failed
acceptance criterion makes that extra binary and lifecycle coupling unjustified.

The follow-up implements the smaller split that did pass: a project-owned Go ToolHub
is the stable authenticated policy boundary, while ToolHive is only the replaceable
OCI/command/remote workload runner. This avoids depending on local vMCP reconciliation.

## Artifacts and test host

| Item | Exact artifact |
|---|---|
| Hermes | 0.21.0, commit `869228cab4a8276d3b4c78da9d9939670c47bd0f` |
| ToolHive | 0.48.0, commit `c25e508592bc3bcfa10dea99af04daaf341db95a` |
| Container MCP | `ghcr.io/stacklok/dockyard/npx/server-everything:2026.8.18`, digest `sha256:3669bfd151be945ec945e2053cfe4e1d523884315d17c96e498ffe90edbc10ba` |
| Remote MCP path | ToolHive remote proxy, first to DeepWiki and then to the local container endpoint |
| Context7 | `ghcr.io/stacklok/dockyard/npx/context7:4.0.4`, upstream revision `e456dedf796f2e85bce53fc03482fb3334c0d693` |
| Host | Windows amd64, Docker Desktop 4.73.1, Docker Engine 29.4.3, WSL2, 15.47 GiB Docker memory limit |

The release checksum was verified against `toolhive_0.48.0_checksums.txt`. No account
credential or external mutation was used. Network-isolated startup additionally pulled
`ghcr.io/stacklok/toolhive/egress-proxy:latest` and `dockurr/dnsmasq:latest`; because
those helper tags are mutable, they are unacceptable production pins as observed.

## Compatibility results

| Requirement | Result | Evidence |
|---|---|---|
| Fixed vMCP URL | Pass | MCP initialize, `tools/list`, and `tools/call` succeeded at `http://127.0.0.1:4483/mcp`. |
| Container backend | Pass | `issue11-container_echo` returned `Echo: ok`; tools were filtered to `echo`. |
| Remote backend | Partial | ToolHive created a remote workload, but an unavailable remote was reported healthy by `thv list`; vMCP omitted its capabilities. |
| Add/remove without Hermes rebuild | Fail | Recreating the remote workload changed its proxy from port 30783 to 24976, while live vMCP kept calling 30783. Restart is therefore required. |
| Conflict naming | Pass | vMCP used the documented `{workload}_` prefix (`issue11-container_echo`). |
| `tools/list_changed` | Fail for required flow | Backend resync ran, but it retained the stale endpoint, so no usable live projection change reached a new client session. |
| Tool filtering | Partial | Launch-time allowlist worked. This is visibility, not per-call authorization or per-session policy. |
| Incoming identity | Fail for production | Local group quick mode used anonymous auth and warned that session-ID possession is sufficient. No stable Hermes runtime identity was enforced. |
| Secrets | Capability present, not exercised | Secret-manager references and bearer/OIDC flags exist; no real credential was needed or supplied. |
| Recovery | Partial | A healthy container served calls; failed remote discovery degraded silently and stale endpoint recovery failed without gateway restart. |
| Egress isolation | Partial | ToolHive attempted DNS and egress sidecars. The test used `--isolate-network=false` after helper-image pulls stalled; deny-by-default was not claimed. |

## Go ToolHub and Context7 follow-up

`cmd/toolhub` exposes one stable Streamable HTTP endpoint. A bearer token selects a
server-owned principal/context/runtime/binding/policy tuple; these IDs are never MCP
arguments. `tools/list` contains only bound tools and every `tools/call` resolves the
same current binding again. The gateway bounds serialized output to 1 MiB and passes
cancellation through to the backend. `internal/toolhub` owns only policy and lifecycle
names; `thv` continues to own containers, proxies and network isolation.

| Use case | Result | Real evidence |
|---|---|---|
| User isolated OCI | Pass | `context7-alice`, stdio container to ToolHive Streamable HTTP, isolated DNS/egress sidecars, two tools. |
| Organization isolated OCI | Pass | Independent `context7-acme` workload and port; two tools. |
| User/organization shared stateless | Pass | Both resolve to `context7-shared`; stateful definitions are rejected before start. |
| User ephemeral | Pass | `context7-alice-job` reached running, listed two tools, then was removed. |
| Organization ephemeral | Partial | Container started, but ToolHive did not publish it in `thv list` before cleanup; no call success is claimed. |
| Warm OCI task lifecycle | Pass, slow | Cached-image start returned in 22.20 s, ready 2.00 s later, removal 1.62 s on Docker Desktop. |
| Protocol command (`npx://`) | Fail | A fresh 16-stage image build failed in Alpine `apk` with a v2 database/no-network package resolution error. |
| Streamable HTTP | Pass | Direct Context7 and Go ToolHub calls succeeded. |
| Legacy SSE | Pass, deprecated | ToolHive `--proxy-mode sse` exposed both tools and Go ToolHub forwarded real calls; ToolHive warns it will be removed. |
| Anonymous remote MCP | Pass | ToolHive proxied the local no-auth MCP endpoint; `tools/list` and `echo` succeeded without downstream credentials. Go ToolHub then forwarded ten calls through that proxy. |
| Remote OAuth Context7 | Out of scope | ToolHive generated PKCE for the public remote endpoint; the flow was stopped and the final acceptance path deliberately uses no downstream authentication. |
| MCP OAuth 2.1 resource metadata | Partial pass | Go ToolHub returns 401 with `WWW-Authenticate` and RFC 9728 metadata. Token issuance, audience validation and PKCE remain external. |
| Secret references | Fail until configured | ToolHive reported no secrets provider and would downgrade secret env values to ordinary environment variables. |

The Context7 functional call (`resolve-library-id`) succeeded through both ToolHive and
Go ToolHub. Ten network-bound calls averaged 1701 ms direct and 1541 ms through ToolHub;
the apparent inversion is provider/network variance, not a claim that another hop is
faster. The earlier local echo benchmark remains the useful gateway-overhead result.

The final no-auth remote chain was container MCP → ToolHive remote proxy → Go ToolHub.
Ten echo calls averaged 12.42 ms, then the ToolHive workload was removed in 0.30 s and
the next call failed closed. Recreating it returned in 0.59 s on a different port; after
updating the ToolHub backend, ten calls averaged 12.56 ms. This proves fast remote
start/stop and failure propagation, and also exposes the remaining requirement: a
supervisor must reconcile ToolHive's changing allocated URL into the stable ToolHub.

The Go ToolHub process used 52.1 MiB working set / 51.7 MiB private. Context7 initially
used 96.1 MiB and settled near 75.8 MiB; its isolated egress and DNS helpers used about
9.7 MiB and 1.9 MiB. The image is 292 MB. CPU was effectively idle outside calls.

ToolHive's documented surface is materially broader than Hermes direct MCP configuration:
aggregation of tools/resources/prompts, conflict prefixes, OIDC ingress, token exchange or
header injection, audit/telemetry, health, container isolation, secrets, and optional FTS5
or embedding tool search. Hermes 0.21.0 is the MCP client and agent runtime, not an MCP
gateway: it owns reasoning and connects directly to configured servers. That path is
simpler and preserves sessions/memory, but changing the server set requires config and
runtime restart and offers no central aggregation/auth/audit boundary.

## Performance and resources

The reproducible latency command uses one persistent MCP session, 100 sequential echo
calls and the same Go SDK client. It excludes process startup and model latency.

| Path | Mean | p50 | p95 | Min | Max |
|---|---:|---:|---:|---:|---:|
| Direct MCP (Hermes-shaped path) | 3.33 ms | 3.23 ms | 4.44 ms | 2.13 ms | 5.38 ms |
| Through ToolHive vMCP | 15.99 ms | 15.14 ms | 23.26 ms | 11.24 ms | 27.72 ms |

Observed vMCP overhead was **12.65 ms mean / 4.8x end-to-end latency** for this tiny local
tool. This is a worst relative case: real network/provider calls will dominate it.

| Resident component | Idle working set / container memory | Disk/image |
|---|---:|---:|
| `thv vmcp serve` | 55.3 MiB WS; 78.1 MiB private | 126.0 MiB Windows binary |
| container workload proxy (`thv`) | 60.8 MiB WS; 84.0 MiB private | shared binary |
| remote workload proxy (`thv`) | 55.4 MiB WS; 75.6 MiB private | shared binary |
| server-everything container | 63.9 MiB | 290 MB image |
| optional isolation helpers | not stable-measured | 37.9 MB egress + 18.5 MB DNS images |

The measured always-on ToolHive control/proxy working set was about **171.5 MiB**, before
the 63.9 MiB connector container. vMCP reached health in approximately one second in the
warm-image run; cold startup was dominated by image downloads and is registry/network
dependent. CPU was effectively 0% idle. Disk numbers are local Docker-reported image
sizes, not compressed release sizes or a full Hermes image comparison.

## Reproduction and rollback

```text
thv group create hermes-issue-11
thv run io.github.stacklok/everything --group hermes-issue-11 --name issue11-container --tools echo --isolate-network=false
thv run https://mcp.deepwiki.com/mcp --group hermes-issue-11 --name issue11-remote
thv vmcp serve --group hermes-issue-11 --port 4483
go run ./cmd/devcheck mcp-bench http://127.0.0.1:32702/mcp echo 100
go run ./cmd/devcheck mcp-bench http://127.0.0.1:4483/mcp issue11-container_echo 100
```

Ports are allocated dynamically; obtain them from `thv list --format json`. Cleanup is
`thv rm --group hermes-issue-11` followed by `thv group rm hermes-issue-11`. Rollback is
simply continuing with current direct `mcp_servers`; this spike changes no user data or
runtime configuration.

## Go criteria for a later re-test

Pin every binary and helper image by digest; run authenticated config-file mode; bind the
verified Hermes runtime identity; demonstrate different tool projections for concurrent
sessions; enforce the same resolver on list and call; prove remote and stateful container
add/disable/remove plus `tools/list_changed`; kill/restart workloads; and repeat latency,
idle/load CPU, RSS, disk and cold/warm startup measurements on the target Linux VPS.

The accepted downstream test profile is anonymous; remote OAuth is not required for this
decision. Configure and test a real ToolHive secrets provider before any future
credential-bearing connector. Prefer Streamable HTTP; SSE exists only for compatibility
and is deprecated.
