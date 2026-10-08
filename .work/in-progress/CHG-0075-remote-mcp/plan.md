# CHG-0075: Owner-scoped remote MCP (issues #83, #84, #85, #96)

Milestone 8 first slice. One code path delivers all four issues:

- `prepare_source` gains `remote_url` (plus optional `name`, `tools`, `credentials`)
  gated by the existing self-install grant — catalog-only principals deny before
  any store write (#83, #84).
- `internal/toolhub/remote.go`: HTTPS-only + public-host SSRF validation
  (loopback/private/link-local/CGNAT/metadata/reserved names denied, DNS
  resolved and re-checked at dial time, redirects refused), MCP
  initialize+tools/list probe via the go SDK (#84).
- CredentialInput gains `delivery: http_header` + `target`/`prefix` (remote-mcp
  only); secrets persist through the existing locator/credstore form flow and
  inject as HTTP headers inside the admitted call only (#83).
- `MCPBackend` remote branch: per-call connect to `definition.Source.URL` with
  the guarded client; no ToolHive admission, no credentials.env files (#85).
- Credentialed endpoints defer probe to the existing AdmissionPending form
  submit; `AdmitWithCredentials` gains a remote branch (#96).
- Confirm/enable/disable/revoke/remove lifecycle and exact-owner projection are
  the shipped records; `tools` arg narrows the admitted allowlist, effects are
  verb-classified (mutating => write) and shown at confirm (#96).

Skipped on purpose: OAuth authorization flow for remote endpoints (SPEC-0020
discovery exists; token-collection via the protected form covers the milestone
contract), hubctl register verb (control MCP + `hubctl grant` is the shipped
path), org-shared remote endpoints.
