---
id: CHG-0057-datalens-connector
issue: 120
---
# Prepared DataLens connector (issue #120)

## Candidate comparison

| Candidate | Maintainer | Verdict |
|---|---|---|
| `datalens-tech/datalens-mcp` | datalens-tech org (official DataLens vendor org; referenced from Yandex Cloud docs), MIT | **Selected.** Node 18+, stdio, pinned commit. Gateway of 5 tools with server-side OpenAPI `x-mcp-scope` enforcement (read/write/privileged). |
| `osipsemyon/datalens-mcp` | individual | 70 generated tools — floods context; no scope split. |
| `ADIKANT/datalens-dev-mcp` | individual, alpha | Write-oriented dashboard dev workflow, not a read connector. |
| `Goryalera/MCP-connector` | individual | Streamable-HTTP only, no reviewed stdio path. |
| `snevolin/datalens-mcp` | individual | Rust, thin, less maintained evidence. |

Provenance: MIT license file; official Yandex Cloud docs link to the repository;
live `https://api.datalens.tech/json/` OpenAPI schema verified fully classified
(51 read / 24 write / 41 privileged ops, 0 unclassified) — the server fails
startup on an unclassified schema, so live evidence is required, not assumed.

## Selection evidence (datalens-tech/datalens-mcp @96b3d6b, v0.2.0)

- `package.json` bin `datalens-mcp` → `dist/index.js`; generated Node recipe
  (`npm ci` + `npm run build`) infers `node /app/dist/index.js` exactly.
- `DATALENS_ORG_ID` → `x-dl-org-id` header: tenant scope lives in env, outside
  model arguments. Folder/workbook boundaries are enforced by DataLens IAM on
  the token's account — model args carry entry ids only.
- `x-mcp-scope` enforced server-side before every call; upstream annotations
  give ToolHub ReadEffect (`list_commands`, `describe_commands`,
  `invoke_read_command`) vs WriteEffect (`invoke_write_command`,
  `invoke_privileged_command`) — reads/mutations/administration stay distinct.
- Limits outside model args: `DATALENS_MAX_RESPONSE_CHARS` (pinned 100000),
  hard 10 MiB API / 20 MiB schema caps, 30 s timeouts, HTTPS-only endpoints,
  no redirects.
- `yc` CLI is absent from the image → `DATALENS_YC_STATIC_AUTH=1` +
  `DATALENS_API_AUTH_HEADER`. IAM tokens expire in 12h: rotation is the
  documented `rotate` operation; runbook names the ceiling.
- Node `fetch` ignores proxy envs → `NODE_USE_ENV_PROXY=1` (pinned base is
  Node 26.8.2) routes fetch through the workload Squid ACL.
- npm `latest` check hits `registry.npmjs.org` (bounded 2 s/64 KiB, no creds):
  declared in egress so the upgrade notice works.

## Generic gap closed

Upstream fetches the OpenAPI schema at startup, so the unauthenticated
`--network none` tools/list probe can never succeed. One generic catalog bit,
`preflight_network`, lets a reviewed prepared entry run that probe with egress
(the credentialed probe already does). Unprepared sources keep `--network
none`. No provider-specific code.

## Deferred / live gates

Docker is not available on this machine: build, live lifecycle (reconnect,
rotation, revoke, upgrade, two-user isolation) and a real DataLens read stay
unchecked acceptance items. Entry is honest data + the generic flag; issue #120
closes only after live verification.
