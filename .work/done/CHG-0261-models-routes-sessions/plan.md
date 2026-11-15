# CHG-0261: models.yaml routes, per-service cliproxy sessions via Broker (M30 stage 7)

Chain: ADR-0093 D8 (refines ADR-0092 D4) -> SPEC-0092 R7 -> this plan -> `state.yaml`.
Evidence: `../CHG-0254-passport-gap-audit/research.md` Findings 1–2; CHG-0248 broker contracts.
Previous stage: CHG-0260. Next stage: CHG-0262.

## Goal

`models.yaml` is the model plane of the space: a default route, ladders of
model services, and per service `via`, `endpoint`, `auth` refs, `sharing`,
`limits` and its models. Each `via: cliproxy` service runs in its own
cliproxy instance (declared in stack) holding exactly one OAuth session
delivered by the Credential Broker; model-relay routes, meters and fails
over by this file, and switching routes requires `models.switch`.

## Read before coding

1. SPEC-0092 normative `models.yaml`; ADR-0092 D4; ADR-0093 D8.
2. `internal/stack/models.go` (`ModelRegistry`, `ModelHopRef`, `ModelLimit`, `Validate`); `materialize.go` `writeModelRegistry`.
3. `internal/runtime/model_relay.go` (registry reload `:676`, `switchLevel` `:730`, `serveSelect` `:917`), `model_relay_quota.go`, `model_meter.go`.
4. `services/credential-broker/examples/contracts/{cliproxy-session,model-api-key}.json`; docs/services/model-relay.md; docs/services/credential-broker/.
5. `compose.go` cliproxy blocks; CHG-0257 `ServicePlan`.

## Preconditions and rules

- No guessed provider APIs: S7.0 must cite the pinned cliproxy source for auth-file layout, api-keys config and login flow before S7.3 code.
- Real OAuth logins and provider calls are provider mutations — run only with the owner's explicit instruction and accounts.
- No auth bytes in passports, argv, DTOs, logs; no copying of `cliproxy/` auth dirs between spaces.

## Slices

### S7.0 Spike: pinned cliproxy contract (R7.2)

Change: read the pinned cliproxy version in `docker/`; record in `research.md` the auth file format/location per account, the `api-keys` client auth config, the login command and its callback port use, and whether one process can be pinned to one auth file. Decide instance-per-service (default per ADR-0093 D8) with evidence.

Tests: none; receipt in state.yaml.

### S7.1 Compile models v2 (R7.1)

NOTE (updated by CHG-0255 S1.0): routes.*/services.* vocab + normative emission landed in CHG-0255 with the structural checks below; this slice keeps ownership of any check not yet enforced.

Anchors: `passport.go` `classes["models"]` + checks; `internal/passport/models.go`.

Change:
1. Leaves per normative shape; `default` ∈ routes; ladder entries ∈ services; `via` ∈ {cliproxy, openai-compatible}; `endpoint` fixed http(s) URL (existing `validateProviderURL`); for `via: cliproxy` the endpoint host must equal a per-space stack instance name of catalog id `cliproxy`.
2. `auth.upstream.kind` ∈ {oauth-session, broker-key}; `proxy_access` required for cliproxy, forbidden otherwise; refs are Broker contract names.
3. `limits[]` grammar `{of: requests|tokens|provider_quota|spend_usd, amount, unit?, per, clock?}`; `models[].reasoning` ∈ {none, low, medium, high}.
4. Migrator maps `models.yaml` providers/ladders → services/routes (`cliproxy` provider → service with `via: cliproxy`).

Tests (red first):
- `TestModelsPassportNormativeShape`
- `TestCliproxyServiceRequiresStackInstance`
- `TestModelsLadderReferencesKnownServices`
- `TestModelsRejectsLiteralCredential`

### S7.2 Relay registry from the passport (R7.3)

Anchors: `materialize.go` `writeModelRegistry`; `stack/models.go` `ModelHopRef` (+`Unit`, `Clock`, reasoning level); `model_relay.go`, `model_relay_quota.go`, `model_meter.go`.

Change:
1. Registry rendered from `Effective.Models` only: route → ordered services → each service's models as in-service fallback; sharing scope per service; reasoning level matched to request.
2. Limits: `requests`/`tokens` existing meters; `provider_quota` active only when the service reports per-session usage/reset (else service disabled with reason `limit_unmeasurable`); `spend_usd` active only with a catalog price for every model of the service (else disabled, same reason).
3. Switch: route selection API requires PDP `models.switch`; listing `models.list`; use `models.use`. Registry `switch` field removed.

Tests (red first):
- `TestRelayRegistryFromPassport`
- `TestUnmeasurableLimitDisablesService`
- `TestModelUnavailableTriesNextModelThenNextService` (existing ladder semantics on the new shape)
- `TestRouteSwitchRequiresGrant`

### S7.3 Per-instance session delivery and login (R7.2)

Anchors: `compose.go` cliproxy instance (from `ServicePlan`); broker lease specs; new `cmd/hubctl/models_login.go`.

Change:
1. Each cliproxy instance gets a Broker workload lease for its `auth.upstream` (`cliproxy-session` contract mounted as its only auth file) and its `proxy_access` key in the cliproxy client-auth config; model-relay gets the matching `proxy_access` key per service.
2. `hubctl models login --dir … --service google-pro-1` runs the pinned cliproxy login inside that instance using `config.ports.oauth-callback`, then enrolls the produced session into Broker under the contract ref and removes the plaintext file from the instance volume.
3. `broker-key` upstream (`openrouter-key`) delivered to the relay hop only.

Tests (red first):
- `TestCliproxyInstanceGetsOnlyItsSession` (lease spec per instance)
- `TestRelayHopUsesServiceProxyAccessKey`
- Broker contract tests stay green (`TestShippedContractsAreValid`).

## Red-team checklist

- Service A's session never mounted in instance B or another space (lease spec test + live inspect of mounts, names only).
- `endpoint` pointing at another space's cliproxy → compile error.
- Quota counters personal vs shared never cross principals.
- Logs/diagnostics show service names and status codes only.

## Acceptance

A7 live (owner-authorized accounts): stack declares `cliproxy-google-1`,
`cliproxy-google-2`, `cliproxy-codex`; `hubctl models login` for each;
model-relay answers `/v1/models` with route `everyday`; a real completion
goes through `google-pro-1`; forcing its limit exhausted moves the next
request to `google-pro-2` (no replay after stream start); route switch
without `models.switch` denies, with it succeeds; revoke the Broker grant of
one session → that service drops from the ladder on the next request.

## Out of scope

New provider integrations beyond cliproxy/openai-compatible; pricing feed
automation (catalog price table is operator-maintained).

## Done checklist

- [ ] Tests red then green; `just check`; Broker module `just check` if contracts change; docker-check; docker-clean.
- [ ] Live A7 receipts (no secrets) in state.yaml.
- [ ] docs/services/model-relay.md, docs/services/credential-broker/ (Russian), docs/operations.md updated.
- [ ] Issue closed; state done; folder moved.
