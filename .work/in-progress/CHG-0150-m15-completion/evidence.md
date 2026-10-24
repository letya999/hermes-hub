# M15 source and acceptance evidence

Worktree: `codex/m15-complete`, based on `1a6a852` plus the owner's inherited
uncommitted source changes. The original checkout was not edited by this task.
Docker deployment/build/pull/restart and live account/channel actions were not run.

## Source changes

| Issue | Implemented and checked with local fixtures | Still requires live acceptance |
|---|---|---|
| #131 | Default/group/owner overlays; conflict rejection; explicit private/shared proxy scope; owner-specific Broker grants; separate relay tokens; idempotent rerender; two-owner credential rotation; doctor compares registry/env/proxy and reports owner-local apply actions | Two real owners' documented provider identity/read evidence and deployed state isolation |
| #182 | ComfyUI API workflow adapter; service-volume confinement; sync images and durable video; independent endpoint/key/grant/queue references; bounded explicit routes; Fal result-fetch guard inside routing; unsupported edits rejected; uncertain submissions never replayed | Real ComfyUI model/workflow image and video, Fal queue and deployment-volume smoke |
| #185 | Owner chat/catalog authentication; strict model/provider grants; signed Broker acquire/materialize/release; endpoint/DNS guard; explicit private-LAN trust; hop and total deadlines; account cooldown; strict reload; SSE flush; exact JSON numbers; local success benchmark | Real credentials, provider account health/fallback, actual loaded container configuration |
| #150 | Existing 1–8-image one-request contract retained; E2E-021 server dispatch and oracle added: two unknown-color workspace PNGs, incorrect-answer detection, unavailable-third-image denial, own-fixture cleanup | Actual model/channel E2E-021; fixture tests are not a channel integration result |

## Representative regressions

- `stack.TestProfilesRejectConflictingGroups`
- `stack.TestSharedProxyRejectsDeclaredPrivateAccounts`
- `stack.TestSharedModelSettingsNeverShareOwnerStateOrRotation`
- `stack.TestModelDoctorScopesProvidersAndCalculatesOwnerApplyPlan`
- `runtime.TestModelRelayBrokerCredentialIsOwnerBoundAndLeaseReleased`
- `runtime.TestRegistryRelayPrivateEndpointRequiresExplicitTrust`
- `runtime.TestRegistryRelayExplicitPrivateLANEndpoint`
- `runtime.TestRegistryProviderCooldownAppliesAcrossLogicalModels`
- `runtime.TestRegistryOldRequestCannotPoisonReloadedLadder`
- `runtime.TestModelRelayStreamsFirstEventBeforeProviderFinishes`
- `runtime.TestModelHopDeadlineIncludesCredentialResolution`
- `mediasvc.TestMediaProvidersKeepIndependentEndpointsAndKeys`
- `mediasvc.TestMediaProvidersUseIndependentBrokerGrants`
- `mediasvc.TestSecondaryFalNeverInheritsPrimaryEndpointKeyOrGrant`
- `mediasvc.TestComfyWorkflowGeneratesImageFromServiceVolume`
- `mediasvc.TestComfyWorkflowCannotEscapeServiceVolume`
- `mediasvc.TestComfyVideoResumesPersistedHandleWithoutSubmission`
- `mediasvc.TestAsyncRoutingNeverRetriesAnUncertainSubmission`
- `mediasvc.TestMediaSubmissionPersistsIntentBeforeProviderCall`
- `mediasvc.TestMediaRestartQuarantinesUncertainSubmission`
- `media.TestMediaFetchReportsSubmissionAndUncertaintyWithoutDownloading`
- `e2e.TestImageAgentNeedsUnknownPixelsAndActualJPEG/images_inspectok`
- `e2e.TestImageAgentNeedsUnknownPixelsAndActualJPEG/images_inspectlie`

Fixtures use loopback HTTP/TLS servers and synthetic identities/media; the private
endpoint check uses a temporary listener on an available private host interface.
No account credential, identity endpoint, GPU generation or channel send is
inferred from these tests. Symlink confinement is checked when the OS permits
creating a test symlink; traversal and absolute-path checks always run.

## Acceptance progress checklist

Each item has equal weight. This is verified acceptance progress, not an effort
estimate or the GitHub closed-issue counter. Source inspection and local fixture
evidence are distinguished from real account/channel acceptance.

### #131 — 7/8 (about 88%)

- [x] Default/group/owner resolution and conflicting-group rejection.
- [x] Independent model, endpoint, credential scope and proxy placement choices.
- [x] Explicit named-owner Broker grant references and provider authorization.
- [x] Separate owner bearers and generated configuration; no implicit state sharing.
- [x] Shared, dedicated-secondary and mixed topology rendering; private/shared mismatch rejected.
- [x] Idempotent rerender, owner-local rotation and doctor apply plan.
- [x] Legacy undeclared configuration and existing bearer migration preserved.
- [ ] Real two-owner provider identity/read and deployed state-isolation acceptance.

### #182 — 7/8 (about 88%)

- [x] Concrete ComfyUI prompt/history/view protocol adapter.
- [x] Service-owned workflow storage, confined reads and no ML dependencies in the base runtime.
- [x] Independent provider endpoints, keys/grants and queue endpoints.
- [x] Explicit logical-to-provider model mapping and bounded deterministic fallback.
- [x] Synchronous images and durable async video with accepting-engine handles.
- [x] Restart recovery, uncertain-outcome quarantine and unsupported-edit rejection.
- [x] HTTP contract tests for local image/video, Fal fetching and failure paths.
- [ ] Real ComfyUI/Fal generation, queue and deployed-volume acceptance.

### #185 — 7/8 (about 88%)

- [x] Owner-scoped endpoint configuration and signed Broker credential delivery.
- [x] Endpoint/DNS fencing, explicit private-network trust and redirect denial.
- [x] Ordered failure/quota fallback with hop and request budgets.
- [x] Per-account cooldown, health metadata and hop receipts.
- [x] Owner catalog/selection authorization, runtime alias and safe reload.
- [x] Streaming delivery and preservation of other JSON request values.
- [x] Healthy-path local HTTP overhead benchmark recorded with its limits.
- [ ] Real provider/Broker account fallback, quota/health and credential-path latency acceptance.

### #150 — 3/4 (75%)

- [x] Existing 1–8-image, one-request, bounded-input contract retained and tested.
- [x] Whole-set denial, model credential isolation and disabled native vision retained.
- [x] Server E2E-021 oracle, random-pixel answer check, negative third file and own-fixture cleanup.
- [ ] Actual model/channel E2E-021 run and its provider-dispatch evidence.

Overall: **24/28 = about 86% verified acceptance**. All remaining checklist items
require live integration evidence; do not replace them with fixture results.

## Local success latency

Command: `go test ./internal/runtime -run '^$' -bench
'^BenchmarkModelRelaySuccess$' -benchtime=100x -count=3 -timeout=45s`.
Same localhost provider, same 32 KiB chat payload, no model inference, Windows
amd64. Milliseconds/request:

| Path | Run 1 | Run 2 | Run 3 |
|---|---:|---:|---:|
| Direct HTTP | 0.355 | 0.345 | 0.320 |
| Static relay | 1.137 | 1.134 | 1.281 |
| Registry relay | 2.041 | 2.282 | 2.515 |

This measures local proxy/validation overhead, not provider generation time or
production percentiles. The benchmark provider is anonymous; Broker resolution
is independently fixture-tested, not included in these timings. It does not
establish zero added latency.

## Gates

All Go packages passed `-race -shuffle=on -count=1` through the `just check`
coverage stage: **86.20% own Go statement coverage**, with the configured 75%
per-package floor also satisfied. The same invocation then stopped at ST1013
style findings in test HTTP status literals. Those literals were replaced by
equivalent `http.Status*` constants, without production-code changes. The
remaining `just lint docs-check config-check` stages subsequently passed:
format, vet, module tidy diff, staticcheck, gosec across both modules, docs,
actionlint and the deployment configuration contract. Record this as **passed
in stages**, not an uninterrupted single successful `just check` invocation.

The Compose gate uses synthetic read-only interpolation and does not contact a
Docker daemon or change deployed resources. The separate Broker module was not
newly modified by this task; inherited changes remain inherited. No dependency
files changed, so the dependency-change `just security` gate was not required.

Live/Docker acceptance remains pending under the owner's constraint. Keep the
change in progress and do not close milestone issues based on fixture results.
