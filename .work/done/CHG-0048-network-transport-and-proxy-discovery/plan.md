# CHG-0048 network-transport fallback + proxy-env discovery

Parent: CHG-0047 (MCP admission chain). Trigger: owner asked for (a) SSE as a
last-resort transport fallback, (b) automatic discovery of custom `*_PROXY`
env names from repository source, (c) a tracking issue for a privileged
admin/diagnostics principal.

## Problem

- Artifact entrypoints that declare `--transport sse|http` are rewritten to
  `stdio` for both preflight and runtime. Servers that cannot speak stdio at
  all die in the probe and never reach the credential form.
- Servers with custom proxy variables (`SLACK_MCP_PROXY`) cannot be discovered
  from `--help`; only reviewed `proxy_environment` metadata could supply them,
  and nothing populated it for generic GitHub installs.

## Design

- `source.network_transport` (`sse`/`http`/`streamable-http`) is stamped by
  preflight only when the stdio probe fails AND a detached network probe
  confirms `tools/list`. Probing and runtime stay on the same transport.
- Runtime: companion config gains `transport`; the child is spawned unchanged
  and the companion connects as an MCP client to the loopback listener it
  discovers via `/proc/net/tcp{,6}` in the workload's own network namespace.
  The bridge HTTP client hard-disables proxies; the authenticated
  companion/relay front remains the only externally visible endpoint, and the
  unauthenticated MCP listener can only be reached by sibling containers of
  the same workload (same trust domain as today's stdio pipes).
- Preflight: `hubctl mcp-bridge` runs in a second container attached to the
  probe server's netns (`--network container:X`) and pumps newline JSON-RPC
  between stdin/stdout and the discovered endpoint (raw `mcp.Connection`
  level, no re-serving). hubctl is staged into `HUB_STATE` and bind-mounted
  read-only (dev hosts cross-compile via `BuildLinuxCompanionBridge`).
- Proxy-env discovery scans source call-sites (`os.Getenv`/`LookupEnv`,
  `process.env.X`, `os.environ`/`os.getenv`, `env::var`/`var_os`,
  `ENV["X"]`, dotenv/Dockerfile assignments) for `*_PROXY` names, excluding
  runtime-owned standard names, `NO_PROXY`, placeholders, vendored trees and
  fixtures. Candidates merge into `proxy_environment` — names only, the value
  is always the controller-owned egress URL.

## Issue (deferred): hermes-admin principal

Privileged, deliberately slow, fully audited principal that can read all
onboarding/controller state across principals and, as a last resort, inspect
upstream source to determine the correct credential recipe
(`credential_groups`, `proxy_environment`, prepared entry). Proposals require
human approval; no provider mutations. Tracked as a GitHub issue.

## Tests

- `/proc/net/tcp{,6}` listen-table parsing, path/port candidates, transport
  ordering (network_test.go).
- `networkTransportValue`, `networkTransportFallback` mark/propagate
  (artifact_contract_test.go).
- `artifactCommand` keeps the declared entrypoint when marked
  (generic_fallback_test.go).
- `DefinitionSource.NetworkTransport` validation (toolhub_test.go).
- `discoverProxyEnvironment` across Go/Node/Python/Rust/Ruby + dotenv +
  Dockerfile fixtures, exclusions (artifact_language_test.go).
