---
status: active
title: Owner-scoped remote MCP registration and projection
---
# Owner-scoped remote MCP

SPEC-0017 declares the `remote-mcp` transport; this specification freezes how a
user-owned hosted endpoint enters that contract through `prepare_source
remote_url` (issues #83–#85, #96). The stable ToolHub endpoint stays the only
MCP surface Hermes sees; a remote connector is a definition, an owner-scoped
connection, an encrypted credential reference and a binding — never a new
registry, config block or runtime flag.

## Endpoint admission

- `prepare_source` accepts `remote_url` plus optional `name`, `tools` (allowlist
  that may only narrow the advertised set), `credentials`,
  `credential_header`/`credential_prefix` and `scope`. It is mutually exclusive
  with `source`, `definition_id` and `candidate_id`.
- The caller needs the `self-install` grant; catalog-only principals are denied
  before any store write. `scope: shared` replaces it with the operator-only
  `install_shared` control operation: the definition registers `Shared` +
  `Sharable`, publishes to the catalog, and members bind one policy-owned
  connection and credential — including OAuth-managed endpoints, where the
  owner's locator carries `OAUTH_PROVIDER`/`OAUTH_CONNECTION` so member calls
  ride the same refresh path.
- The endpoint must be HTTPS, carry no userinfo, query or fragment, have a
  multi-label public host and resolve only to public addresses. Loopback,
  private, link-local, CGNAT, benchmarking, documentation, multicast,
  unspecified and reserved backend names (`localhost`, `*.internal`, `*.local`,
  `*.corp`, `*.lan`, `toolhive`, `vmcp`, `metadata.google.internal`, …) deny.
- DNS answers are re-checked inside the dialer at connect time; redirects are
  never followed. A non-MCP URL fails the initialize/tools/list probe and is
  denied without a store write.
- Endpoint identity (`definition_id` `remote-*`, version `1.0.0`, immutable
  source URL, tool list, credential delivery) is frozen into the definition;
  duplicate immutable registration is idempotent and a mutated same-version
  record conflicts, per SPEC-0017.

## Credentials

- A remote credential input declares `delivery: http_header` with a `target`
  header (default `Authorization`) and an optional `prefix` (default `Bearer `
  for the authorization header). Header delivery is illegal on any other
  transport.
- Values are collected only through the protected loopback form and stored in
  the encrypted credential backend under an opaque locator. They appear in
  `store.json`, catalog output, audit records and Hermes configuration only as
  locators and symbolic names.
- Injected headers are pinned to the admitted endpoint origin. A legacy SSE
  server picks its own message URL; a hostile one pointing it at another
  origin gets an unauthenticated request — credentials never travel
  cross-origin.
- A credentialed endpoint defers its MCP probe to the form submit
  (`admission_pending`); the probed tool list is then intersected with the
  requested allowlist and frozen. Credential-free endpoints probe at prepare
  time and land directly in `awaiting-confirm`.
- An endpoint whose unauthenticated probe answers 401/403 *and* advertises
  RFC 9728 protected-resource metadata enters `awaiting-oauth` instead of the
  credentials form. The control plane discovers the authorization server
  (RFC 8414), registers a public client when the AS offers RFC 7591 dynamic
  registration, and opens authorization-code+PKCE with the RFC 8707
  `resource` parameter on authorize, token and refresh requests. Provider
  slots are per-definition (`remote-mcp:<id>`) so endpoints cannot overwrite
  each other.
- Callback completion exchanges the code, persists the token set **plus the
  provider document and DCR client** in the owner's encrypted locator, probes
  `tools/list` with the minted bearer and freezes only the authenticated tool
  set. The onboarding's `oauth_provider` marker lands on the materialized
  connection so a rejected call triggers exactly one broker refresh —
  restart-safe because the locator, not process memory, is the durable
  record — and a single retried call with the re-injected credential. Public
  DCR clients persist no empty `client_secret`; endpoints without usable
  OAuth metadata keep the static-credential form path.

## Projection and call

- `enable` materializes the existing `Connection`/`CredentialReference`/
  `ToolBinding` records with `PrincipalOwner` ownership and re-probes the
  endpoint (bound tools must still be advertised) before the binding goes
  active.
- Each `tools/call` re-authorizes against the store, re-validates the endpoint
  (SSRF set is re-checked), decrypts inside admit, maps declared credentials
  onto HTTP headers, and opens a bounded MCP session — no ToolHive admission,
  no credential files, no process. Streamable HTTP is tried first with a
  fallback to the legacy 2024-11-05 SSE transport, both over the same
  SSRF-guarded dialer and origin-pinned headers.
- Every remote response body is capped at 8 MiB inside the transport because
  the MCP SDK buffers bodies with unbounded reads; per-tool output is further
  bounded by the definition's `output_bytes`.
- `remote-mcp` bindings whose endpoint lives in connection metadata
  (`mcp_endpoint`, e.g. operator-pinned vMCP) keep the existing private-backend
  call path; the owner-registered path applies only when the definition itself
  pins the URL.
- Disable/revoke bump the projection revision, drop the tools from `tools/list`
  on refresh and fail `tools/call` even for a stale projected name — the same
  mid-session cut every binding already gets.

## Provider trust

- Remote integrations are opt-in and owner-scoped (`PublicationUser`) unless an
  operator deliberately installs `scope: shared`. Provider names, descriptions
  and tool output are untrusted content; effect is a verb-segment heuristic
  shown to the owner at confirm, not an authorization.
- Organization membership cannot widen this path: the store's publication and
  binding checks keep a user endpoint invisible to every other principal, and a
  member cannot register connectors that expand host policy. Shared installs
  are catalog-visible by explicit operator action only; members then bind
  through the shared-credential policy, never the owner record directly.
