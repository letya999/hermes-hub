# CHG-0129: M13 P2 — channel-agnostic human action surface (issue #283)

Chain: M13 milestone. Previous stages: `CHG-0127` (P0 stabilization), `CHG-0128` (P1 recipe model + ledger). Next: #284 unified connector_* tools + operation journal.

## Goal

Every human step of the connector lifecycle — enter credentials, pick a
method, OAuth consent, device flow, confirm a consequential action — happens
on one channel-agnostic surface: a one-time link to a protected ToolHub page
delivered as a system message through any channel adapter or hubctl. The
model never sees the token behind the link; the tool result carries only
`link_sent: true` and the operation state.

## Read before coding

1. `internal/toolhub/control_http.go` — `serveCredentials`, `serveConfirm`,
   `serveRemove`, `serveConnect`, `loopbackHTTP`
2. `internal/toolhub/control_binding.go`, `control_broker.go` — nonce mint
   points (`FormNonce`, `ConfirmationNonce`, `RemoveNonce`, TTLs)
3. `internal/toolhub/api/grants.go` — `Onboarding` (principal/context/runtime
   binding), `internal/communication/` channel `Deliver`/`Caps`
4. `internal/oauth` — `Broker.StartDevice`/`PollDevice` (RFC 8628), fixture AS
5. Issue #283, SPEC-0060 (recipe/ledger fields the form renders)

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only.
- `just check` before handoff; own coverage >= 85%.
- No secrets, tokens, OAuth `state` or link tokens in agent-facing results —
  property test guards this.
- Public exposure is opt-in (`HUB_PUBLIC_ORIGIN`); only `/connect`,
  `/confirm`, `/device`, `/oauth/callback`, `/credentials`, `/remove` answer
  on the public listener. Control/gateway/admin paths 404 there.
- Redirect URIs must be `https` or loopback.
- No push/publication without owner instruction.

## Slices

### S2.1 Unified one-time link tokens

Anchors: `control_http.go` handlers; `api/grants.go` Onboarding nonce fields.

Change:
1. Introduce `?t=<one-time>` link tokens bound to the stored per-operation
   nonce + principal/context/runtime; keep `?nonce=` acceptance for
   back-compat only where a stored nonce already validates identically
   (same code path).
2. Replay/expiry → `410 Gone` page with a "send a fresh link" hint instead of
   401 (form, confirm, remove, connect).
3. POST requires the token (acts as CSRF): missing/mismatch → reject before
   any mutation. `Cache-Control: no-store` and CSP nonce already present —
   extend to all four pages.
4. Second use of a single-use link (confirm/remove) returns 410.

Done when: link-token tests — wrong token, expired, replayed, cross-operation,
CSRF-less POST rejected; replay returns 410.

### S2.2 `HUB_PUBLIC_ORIGIN` opt-in exposure

Anchors: `api.FormOriginForListen`, `credentialNoticeURL`, gateway mux in
`internal/toolhub/gateway.go`.

Change:
1. `HUB_PUBLIC_ORIGIN` env (opt-in https origin). When set, minted notice
   links use it; when unset, behaviour is exactly today (loopback).
2. A public listener that serves only the human-action paths and 404s
   everything else; control/gateway/admin never bind to it.
3. Loopback remains allowed regardless (today's behaviour preserved).
4. `loopbackHTTP` gate becomes "loopback OR public-origin listener" —
  expressed per-listener, not per-request Host spoofing.

Done when: with `HUB_PUBLIC_ORIGIN` set, links carry it; public listener
serves the four surfaces and 404s `/v1/*`; without it, nothing changes.

### S2.3 Device flow wiring (RFC 8628)

Anchors: `internal/oauth` `StartDevice`/`PollDevice`; `control_broker.go`
OAuth paths; new `serveDevice` page.

Change:
1. When discovered AS metadata advertises `device_authorization_endpoint`,
   ToolHub offers device flow as a `connection.alternatives` entry and starts
   `StartDevice` on user selection.
2. `GET /device/<operation>` status page renders verification URI + user
   code; poll honours `authorization_pending`, `slow_down`, `expired_token`,
   `access_denied`.

Done when: fixture-AS tests cover all four polling outcomes end-to-end
through the ToolHub path.

### S2.4 Paste-the-redirect fallback

Anchors: `serveConfirm`/OAuth finish path; `internal/oauth` state+PKCE
validation.

Change:
1. The credential/confirm page gains a "paste the final redirect URL" field
   for remote-browser flows.
2. Pasted `…/oauth/callback?code=…&state=…` validates `state` and PKCE
   exactly like the real callback; wrong state/verifier/foreign onboarding
   fails closed.

Done when: valid paste completes; wrong state, wrong verifier and
cross-onboarding code all fail closed.

### S2.5 Recipe-rendered form content

Anchors: `serveCredentials` form builder, `writeCredentialFields`,
`contract_synthesis.go`; recipe/ledger types from S1.

Change:
1. Form header shows connector name, kind, `source@commit|version`, trust
   tier and egress hosts from the resolved recipe.
2. Per field: why-needed provenance (ledger source + rank), format hint,
   help text, and recipe `token_url` link (e.g. pre-filled GitHub token URL).
3. R6-only fields render as "suggested from <file>, please verify" —
   visually distinct, never required-marked.
4. Tools split read vs write; write tools listed disabled-by-default.

Done when: rendered form carries trust tier, provenance and token_url from a
recorded corpus recipe; R6 fields are marked suggested.

### S2.6 Transactional save

Anchors: `AuthorizeCredentials` path in `control_binding.go`.

Change:
1. Nothing is persisted — no ciphertext, Broker credential or connection —
   until the read-only probe succeeds.
2. Probe failure re-renders the form with entered values preserved (model
   never sees them — page-side only) and a precise public error.

Done when: probe failure leaves zero residue (store, broker, connections)
and re-renders values.

### S2.7 Delivery surface: channel caps + hubctl

Anchors: `internal/communication` `Channel.Deliver`/`Caps`; notice senders;
`cmd/hubctl` connector commands.

Change:
1. Optional `Caps().Actions` flag; adapters without it keep plain links
   (conformance test through `mock` and `telegram`).
2. `hubctl` connector ops print the link; `--confirm` for scripts.

Done when: conformance test proves link delivery via mock+telegram and
Actions=false fallback; hubctl prints link.

### S2.8 Property test + docs

Change:
1. Property test: no agent-facing result of any connector tool/control op
   contains a link token, nonce or OAuth `state`.
2. ADR "Channel-agnostic human action surface" (appended), operations.md
   public-origin section, threat-model update.

## Acceptance (issue #283 contract)

- [ ] No link token/nonce/OAuth state in agent-facing results (property test)
- [ ] Wrong principal/expired/replayed/cross-operation/CSRF-less POST all
      rejected; second use → 410
- [ ] Device flow wired with fixture-AS coverage of all polling outcomes
- [ ] Paste-redirect completes valid, fails closed otherwise
- [ ] `HUB_PUBLIC_ORIGIN` set → links use it; only public paths answer;
      control/gateway 404 on the public listener
- [ ] Probe failure leaves no ciphertext/Broker credential/connection
- [ ] Adapter conformance: mock+telegram delivery; Actions=false plain-link
      fallback
- [ ] ADR + operations.md + threat-model updated

Live (needs deployed stack — report honestly if unavailable):
- [ ] Public-origin install T0 + remove via `/confirm` from another device
- [ ] Device-flow OAuth for a supporting provider without public origin

## Out of scope

- Outbound tunnel itself (M18 #145/#146 provides it)
- Native adapter buttons beyond the optional `Caps().Actions` contract
- Team/multi-user link fan-out (#288)
