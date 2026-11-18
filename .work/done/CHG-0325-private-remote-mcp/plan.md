# CHG-0325: Generic private-remote MCP registration (MVP M9)

Chain: operator request -> issue #340 -> this plan -> `state.yaml`.

## Goal

`hubctl connector remote <name> --endpoint <url>` registers an
operator-pinned private MCP endpoint (loopback, RFC-1918/ULA, Compose
service names) as an immutable user-scoped `remote-mcp` definition plus the
registering principal's own connection — the private twin of
`prepare_source remote_url` (SPEC-0045).

## Design

- `source.tls_mode: operator-pinned` — the only mode that accepts `http`;
  URL stays clean (no userinfo/query/fragment). Public endpoints still
  belong to `prepare_source remote_url` and refuse here.
- Private-proofed three times: `ValidateBackendEndpoint` at registration,
  re-validated at every call, and re-resolved inside `BackendDial` at
  connect time (loopback/private only, no proxy, no redirects) — a rebound
  name cannot steer a credential-bearing call.
- The connection's `mcp_endpoint` must canonical-match the immutable
  `Source.URL` (scheme/host case-folded); enforced at `PutConnection` and
  snapshot load.
- Probe = the real call path (streamable, pinned legacy protocol, ≤256
  tools, 8 MiB body); `--tools` narrows only. Frozen into the definition.
- `--bearer` reads via `readSecretInput` (`--from-file`/stdin, never argv)
  into a per-connection `CredentialReference`. Declared `http_header`
  credentials ride the origin-pinned round tripper; the shared
  `TOOLHIVE_VMCP_TOKEN` is fallback only when no credential is declared.
- `PublicationUser` scoped to the registering principal; credential-less
  definitions still require the connection to bind (it carries the pin).
- Idempotent re-registration; any immutable mutation conflicts.

## Slices

### S1 API surface

`api/types.go` `TLSModeOperatorPinned`; `api/definition.go` pinned source
validation; `api/remote.go` `BackendAddrAllowed`/`BackendDial`;
`api/backend.go` `CanonicalBackendURL`, `backendRoundTripper`, CallEnv
per-connection headers; `api/remote_mcp.go` `collectRemoteTools` (shared
with the public probe), `ProbeBackendMCP`, `BackendMCPDefinition`.

### S2 Store + catalog

`store.go`/`store_io.go` `pinnedEndpointConsistentLocked`; `catalog.go`
credential-less enable for operator-pinned definitions.

### S3 CLI

`cmd/hubctl/connector.go` `remote` verb, `--tools`/`--bearer` flags,
`connectRemotePrivate` (probe → freeze → credstore → publication →
connection → single save; orphan credential cleaned on failure).

### S4 Docs + tests

SPEC-0045 operator-pinned section; `docs/integrations.md` paragraph;
regression tests at api/store/CLI level.

## Acceptance

- Private endpoint registers, enables, lists and calls end-to-end;
  per-connection bearer reaches only the pinned origin.
- Public/metadata/rebound destinations refused at validation and dial.
- Endpoint mismatch, cross-principal enable/resolve, tool-set mutation
  refused; identical re-registration is a no-op.
- `just check` green; focused tests pass.
