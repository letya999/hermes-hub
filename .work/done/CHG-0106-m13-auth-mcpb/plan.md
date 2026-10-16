# CHG-0106: M13 auth completion + full MCPB support

Chain: ecosystem audit after CHG-0105 -> this plan -> `state.yaml`.
Issues covered: #105 remainder (generic OAuth relay, durable token state) and
packaging coverage (MCPB). Follows the same rules: red test first, fail closed,
no guessed provider APIs, secrets never leave ciphertext.

## Goal

Close the auth gaps the ecosystem audit found and make MCPB a real install
source: a user pastes a remote MCP URL and signs in through the standard
browser OAuth flow (or supplies a client ID), self-refreshing token files work
on self-installed servers, and `.mcpb` bundles (repo manifest or release asset)
install with their declared credential contract.

## Slices

### S2.1 Remote-MCP OAuth handoff (#105)

Anchors: `remote_admit.go`, `prepared_oauth.go`, `internal/oauth/oauth.go`,
`api/remote_mcp.go`, `api/inject.go`, `server.go`.

Change:
1. `oauth.Provider` gains `RegistrationURL`, `Resource`, `Scopes`; discovery
   reads them from PRM (RFC9728) + AS metadata (RFC8414).
   `DiscoverRemoteMCPAs(name, resourceURL)` registers the provider under a
   per-onboarding name (today's fixed "remote-mcp" slot collides for two
   endpoints).
2. `resource` parameter (RFC8707) on authorize + token + refresh requests.
3. `RegisterClient` implements RFC7591 DCR when the AS advertises
   `registration_endpoint`; otherwise the caller may supply
   `oauth_client_id`/`oauth_client_secret` args (pre-registered client); no
   registration endpoint and no client args fails closed `unsupported_auth`.
   CIMD requires a publicly reachable hub origin — documented limitation,
   not implemented here.
4. `prepareRemote`: undeclared credentials + 401 probe -> try PRM discovery;
   on success onboarding enters `awaiting-oauth` with
   `ProviderAuthorizationURL`; discovery failure keeps the existing
   "resubmit with credentials" error.
5. `completeRemoteOAuth` (new /oauth/callback branch): HandleCallback ->
   secrets locator (ACCESS_TOKEN/REFRESH_TOKEN/TOKEN_TYPE/EXPIRES_AT +
   OAUTH_CLIENT_ID/SECRET) -> probe endpoint under `Authorization: Bearer`
   -> `RemoteMCPDefinition` -> register -> confirm -> enable, mirroring the
   prepared handoff. Tokens never touch connection metadata; only
   `oauth_provider`/`oauth_resource`/`oauth_onboarding` names are recorded.
6. Call-time refresh: `wrapRemoteOAuthInjector` on `ControlPlane.Injector` —
   when `connection.Metadata["oauth_provider"]` is set and the stored
   `OAUTH_EXPIRES_AT` is within 60s of now, re-discover the provider, restore
   client creds from the locator, `Broker.Refresh`, patch `ACCESS_TOKEN` in
   the injected env. `CredentialReference.Keys = ["ACCESS_TOKEN"]` keeps
   refresh internals out of the projected env.

Tests (red first):
- `TestRemoteMCPOAuthHandoff`: endpoint 401 -> PRM discovery -> awaiting-oauth
  URL -> callback mints tokens -> endpoint probed with Bearer -> enabled;
  subsequent call uses refreshed token when expired.
- `TestRemoteMCPOAuthRequiresDCROrClientArgs`: AS without
  registration_endpoint and no client args -> `unsupported_auth`.

### S2.2 Durable state on generic installs (#105)

Anchors: `api/recipe_launch.go` (VOLUME -> connection-state already parsed),
`api/control_review.go` `DefaultSourceReviewerWithCatalogs`.

Change: reviewed `resolution.Launch.Mounts` (only `connection-state`/
`job-state` sources with valid absolute targets) propagate into
`config.Execution.Mounts`; `config.Workload.Stateful = true` when mounts exist.
Covers self-refreshing token files (`token.json`) that a read-only credential
mount cannot serve.

Tests (red first):
- `TestGenericRecipeMountsBecomeDefinitionState`: Dockerfile VOLUME ->
  definition `Workload.Stateful` + `connection-state` mount, entrypoint intact.

### S2.3 server.json multiple remotes

`ParseServerManifest` ignores `len(remotes) > 1` silently — change to
`ErrAmbiguousEntrypoint` (choosing one is guessing).

Tests: `TestServerManifestMultipleRemotesAreAmbiguous`.

### S2.4 Full MCPB

Anchors: `api/recipe_parse.go` `ParseMCPBManifest`, `cli_artifact.go`,
`api/cli_release.go` `FetchReleaseBinary`.

Change:
1. Parse the real manifest schema on top of the existing flat one:
   `server.type` (node|python|binary), `server.entry_point`,
   `server.mcp_config.command`/`args`/`env`, `user_config` entries
   (`type`, `sensitive`, `required`, `title`, `description`),
   `compatibility.platforms` (warn on unsupported only).
2. `${__dirname}` in args/env -> the container install dir.
   `${user_config.<key>}` references: env placeholders become credential
   inputs named by the env var (sensitive -> Secret, required -> Required);
   args containing a sensitive user_config reference fail closed
   (`unsupported_credential_contract`) — secrets never ride argv. Non-secret
   references in args also fail: we cannot substitute static argv at launch,
   and silently dropping the arg is worse.
3. `.mcpb` zip as an install source via `cli` spec:
   `github-release:owner/repo@tag` + `asset: "*.mcpb"` ->
   `ImportMCPBArtifact`: fetch+sha256 verify (existing), bounded unzip
   (4096 files / 64MiB, regular files only, clean paths), manifest parse,
   recipe build: base image per `server.type` (node/python/binary -> scratch
   for a single static binary only when the bundle carries one), COPY bundle
   to `/app`, ENTRYPOINT = resolved mcp_config argv. Container-mcp
   definition; no bundled deps + missing node_modules for node type -> warn
   evidence, still builds (npm i happens in build context if a lock exists? —
   no: restricted build has no network; fail with a clear error when the
   bundle lacks its runtime deps).

Tests (red first):
- `TestMCPBManifestRealSchema`: node bundle manifest -> entrypoint argv with
  `${__dirname}` resolved, user_config -> connection fields with
  sensitive/required flags.
- `TestMCPBBundleImport`: zip asset -> definition with container transport,
  correct entrypoint, credential inputs.

## Red-team checklist

- `.mcpb` zip: path traversal, symlink entries, size/file bombs -> rejected.
- OAuth: provider name collisions between two remote endpoints; refresh path
  must not leak REFRESH_TOKEN into workload env; callback state binding keeps
  principal/context/onboarding checks; redirect allowlist unchanged.
- `server.json` multi-remote: error code `ambiguous_entrypoint`, not a pick.
- Generic state mounts: only reviewed `connection-state`/`job-state` sources,
  absolute in-container targets; Shared defs keep rejecting mounts.
- MCPB argv never carries user_config secrets.

## Out of scope

- CIMD (client_id metadata documents): needs a publicly reachable hub origin;
  DCR and pre-registered client args cover the same ground.
- Query-param API keys, mTLS client certs (anti-pattern / niche).
- GitLab/Bitbucket self-install sources; NuGet/Java language manifests.
- #138 browser-as-workload.

## Done checklist

- [ ] Tests red then green; `just check`; coverage >= 85%.
- [ ] Docs updated (prepared-connectors/remote oauth, SKILL.md, SPEC-0023
      auth paragraph if behavior changed).
- [ ] state.yaml verification; folder moved to `.work/done/`.
