# CHG-0130: M13 P3 — unified connector_* tools + operation journal (issue #284)

Chain: M13 milestone. Previous stages: `CHG-0127` (P0 stabilization),
`CHG-0128` (P1 recipe model + ledger), `CHG-0129` (P2 human action surface).
Next: #285 ownership/finalizers, #286–#289.

## Goal

"Install this connector" / "remove that one" become one tool call each. The
agent sees exactly nine uniformly named `connector_*` tools; the server owns
the lifecycle through a durable, resumable operation journal. The same verbs
exist on the agent surface, in `hubctl` and over HTTP — one implementation.

## Read before coding

1. `internal/toolhub/control.go` — `Invoke` op dispatch, `statusBody`
   (prose instructions to drop from agent results), phase machine.
2. `internal/toolhub/control_http.go` — `addControlTools` projection,
   `controlToolContract`, `serve*`.
3. `internal/toolhub/control_binding.go`, `control_prepare.go`,
   `cli_prepare.go` — the internal primitives the facade calls.
4. `internal/toolhub/grants.go` — `RequireControlOperation`,
   `AllowedControlOperations`, capability profile plumbing
   (`internal/toolhub/capability_store.go`, `api/capability_policy.go`).
5. `cmd/hubctl/connector.go` — CLI surface to converge.
6. Issue #284; ADR-0046 (recipe/ledger), ADR-0047 (action links).

## Preconditions and rules

- Red test first, then the smallest fix. Go stdlib only.
- `just check` before handoff; own coverage >= 85%.
- Agent-facing results carry `{operation_id, state, code?, summary,
  link_sent?}` only — no prose instructions, no nonce, no internal URL, no
  container id. Typed codes only; never branch on prose prefixes.
- Models never receive tokens, nonces, OAuth state or link material
  (property test from S2.8 keeps covering the facade results too).
- Legacy control operations stay working as internal primitives and for
  e2e; they disappear from the agent projection when the connectors
  capability bundle is set.
- Selector precedence: exact id → alias → source URL → repository basename
  → registry name; scoped to the caller's own live connections;
  removed/failed/superseded are never candidates; ambiguity returns
  `ambiguous_target` with the candidate list.

## Slices

### S3.1 Operation journal record

Anchors: `internal/toolhub` store + control plane.

Change:
1. `api.Operation{id, intent, target, principal/context/runtime/policy,
   request_key, state, steps[], error_code?, receipt?}` with Validate;
   states `queued|awaiting_user|awaiting_confirm|running|done|failed`.
2. `Store` persists operations in the registry snapshot;
   `OperationFor`/`PutOperation` with revision fencing, exact-owner.
3. `request_key` derivation: `(principal, intent, normalized target)` —
   a second call with the same key returns the same operation.

Done when: store round-trip + Validate + idempotent request-key tests pass.

### S3.2 Target selector

Anchors: `resolveOnboarding`/binding lookups in control_*.go.

Change:
1. `resolveConnectorTarget(auth, target)` implementing the precedence chain
   over own live connections + onboardings.
2. Removed/failed/superseded excluded; foreign principals excluded;
   ambiguous → typed `ambiguous_target` with `candidates` (id+source only).

Done when: table test — id, alias, URL, basename, registry name; ambiguity
lists candidates; removed and foreign never match.

### S3.3 Capability bundle `connectors`

Anchors: `api.CapabilityProfile`, `AllowedControlOperations`,
`addControlTools` projection.

Change:
1. Profile field `connectors: none|personal|personal+team`; validation.
2. `personal` admits the nine connector_* tools for own connections;
   `personal+team` adds enable/disable of team connections (membership).
3. Projection: bundle set → exactly the nine tools, zero legacy control ops
   and no `invoke`-shaped escape; bundle unset → today's behavior (legacy
   grants keep working during migration).

Done when: projection test — bundle profile sees exactly nine
`connector_*` tools and no legacy op; unset profile keeps legacy tools.

### S3.4 Read tools: search / list / status

Anchors: `discover`, `catalog`, `status` internals.

Change:
1. `connector_search(query, kind?)` over catalog + registries → candidates
   with id/kind/trust/needed-fields summary.
2. `connector_list()` → own connections: health, tools, writes, expiry,
   trust, pending updates.
3. `connector_status(target|operation_id)` → operation/connection state,
   typed error code, ledger summary.

Done when: each tool returns the uniform result; no secrets/nonces; a
foreign target denies.

### S3.5 Journal runner + connector_install

Anchors: `prepareSource`, admission, `ensurePreparedOAuth`, binding
materialization; `api.PostPrepareNotice`.

Change:
1. Journal runner: step functions `resolve → fetch artifact → collect
   credentials → admit/probe → bind → project → notify`, each persisted
   before and after it runs; a restart resumes at the last completed step.
2. `connector_install(target, scope?)` infers kind from the target
   (MCP container, remote URL, `github-release:owner/repo@tag`, CLI name,
   registry id/alias) — the agent never chooses source/cli/remote_url.
3. Request-key idempotency + per-connection serialization (operations on
   the same connection serialize; different connections parallel).
4. The install ends `awaiting_user` with `link_sent: true` when a human
   step is needed — no polling contract for the agent.

Done when: install of a catalog CLI def through the facade lands the same
onboarding/binding as the primitive path; kill-after-each-step replay test
completes the operation exactly once; duplicate request-key returns the
same operation_id.

### S3.6 Lifecycle tools: enable / disable / allow_writes / reauth / remove

Change:
1. `connector_enable(target, toolsets?)` / `connector_disable(target)` —
   reversible, credentials kept; `toolsets` projects a subset for large
   surfaces (recipe `default_enabled` first).
2. `connector_allow_writes(target, on)` — `on` routes through a `/confirm`
   human link; `off` applies immediately.
3. `connector_reauth(target)` — new credential form link (or OAuth
   restart) without re-install.
4. `connector_remove(target)` — one call: `/confirm` link; the journal
   purge step owns the cleanup (#285 finalizer hooks land there later).
5. Journal: `remove: confirm → cut authorization → purge → verify zero →
   notify`.

Done when: each verb runs through the journal with receipts; remove leaves
a zero-resource receipt after confirm.

### S3.7 Same verbs on hubctl and HTTP

Anchors: `cmd/hubctl/connector.go`, control HTTP handler.

Change:
1. `hubctl connector search|install|list|status|enable|disable|
   allow-writes|reauth|remove` calling the same facade (existing
   provider-specific ops stay as operator paths).
2. HTTP endpoints `POST /v1/connectors:install`, `POST
   /v1/connectors/{id}:disable`, `GET /v1/operations/{id}` on the control
   listener — control-token auth, same facade.

Done when: one code path, exercised through all three surfaces in tests.

### S3.8 Typed codes, docs, closeout

Change:
1. No prose-marker control flow: `strings.HasPrefix(onboarding.Error,
   "Проверка…")`-style branches replaced by `error_code`; a static check
   (devcheck or test) asserts no Russian prefixes drive logic.
2. `statusBody` agent results drop `instructions` prose; `summary` keeps a
   bounded public line.
3. ADR-0048 (unified connector surface + journal), operations.md verb
   table, skill `mcp-connector-onboarding` shrinks to install/remove lines.
4. `just check` green; state.yaml updated.

Done when: static check + full gate pass.

## Acceptance gates (issue #284)

- [ ] Projection: exactly nine `connector_*` tools, no legacy control op.
- [ ] Selector table test incl. ambiguity + removed/foreign exclusion.
- [ ] Journal crash test: kill after each install and remove step; resume
      completes exactly once.
- [ ] Idempotent request key → same operation, one set of side effects.
- [ ] No prose markers in control flow (static check).
- [ ] `connector_install jq` and `github-release:cli/cli@tag` go through
      the same journal + result shape as an MCP install.
- [ ] hubctl + HTTP + agent share one implementation.

Live (deployed stack — report honestly if unavailable):
- [ ] Install GitHub connector in one call → form link → tools usable same
      conversation without agent-side status polling.
- [ ] disable/enable without credential re-entry; remove → /confirm →
      zero-resource receipt; `gh` CLI flow with GH_TOKEN.

## Out of scope

- Finalizer/ownership reconciliation machinery itself (#285).
- Credential Broker authority migration (#287) — the facade calls existing
  primitives.
- Team connector semantics beyond `personal+team` gating (#288).
