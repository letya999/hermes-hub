---
description: "Credential Broker remains a separate process and security boundary inside the monorepo."
last_verified: "2026-09-18"
---
# ADR-0022: Credential Broker boundary

## Decision

Keep `services/credential-broker` as a separately licensed, separately built Go
module and process. The monorepo provides its source, image binary, workspace
membership, local gates and CI coverage, but does not flatten its imports or
silently replace ToolHub's current credential path.

ToolHub, Communication Hub and the runtime each receive a purpose-specific
adapter and signing audience before Broker enrollment is enabled. ToolHub keeps
source review, ToolHive admission, binding projection and reconnect ownership.
Credential values and runtime materialization never enter Hermes messages, MCP
tool results or job/audit records.

## Why

The Broker supplies reviewed credential contracts, approval pairing, provider
references, grants, leases and materialization. These concerns are useful for
M5.3, but an in-process package is not a security boundary and the existing
ToolHub model has different connection/reference lifecycles. Keeping the
process boundary allows an opt-in migration and a clean rollback to the current
encrypted store.

## Consequences

- The root image contains `credential-broker` and `hub-credential-broker`, but
  no service, keys, providers or contracts are enabled automatically.
- Root CI runs the Broker's own Linux gates and image build alongside Hermes.
- A future integration must add reviewed contract mapping plus approve, event,
  grant, lease, revoke and same-path runtime tests before replacing the current
  form/store path.
- The copied subtree retains its MIT license; Hermes remains AGPL-3.0-only.

## Amendment 2026-10-19 — Broker as the single credential authority (M13 P6, issue #287)

The opt-in window closes: connector and CLI credentials now treat the Broker
as the sole credential authority. The local credstore remains only as the
Broker's storage substrate and as the sanctioned path for uncontracted
credential-gate admissions — never as a parallel authority.

- **D1 — Submission boundary validation.** `ConnectionField` carries
  `format` (regex) and `max_length`; submitted values are rejected at the
  trust boundary on control bytes (`\x00`, `\r`, `\n`) for env/header/token
  delivery, and `json`/`file` values must parse. Recipe metadata reaches the
  rendered credential hints unchanged.
- **D2 — Durable refresh lease.** Refresh of an OAuth credential requires a
  lease keyed `(connection, provider)` persisted beside the registry
  (`<store>.refresh-leases`) under a separate `leaseMu`, so refresh inside a
  read-locked admission path cannot deadlock and a second caller joins or
  fails instead of double-spending a rotating refresh token. Acquisition
  fails closed unless the mirrored connection status is exactly `active`;
  release is holder-matched so a stale holder cannot drop a successor's
  lease. Leases survive store restart; `Load`/`Reload` resync the status
  mirror so an external revoke wins any in-flight refresh.
- **D3 — `proxy` delivery class.** `ConnectionField.Delivery="proxy"` and
  `CredentialInput.Delivery="proxy"` validate only when the target host sits
  in the definition's reviewed egress allowlist and the prefix is bounded
  and control-character-free. Workload environment receives only the stub
  `hub-managed`; the egress-proxy plan (`api.ProxyStubPlan`) carries the
  credential reference and target host. No secret reaches env, argv,
  container config or logs. Live header injection inside the egress proxy
  remains a deployment-side gate.
- **D4 — Credential-recipe drift reauths.** Connections pin
  `hermes-hub.recipe` (launch digest) and `hermes-hub.credential-recipe`
  (`CredentialRecipeDigest` over provider, endpoint, client claims and
  connection fields — warnings and launch-only noise excluded). The
  reconciler compares the pin to a freshly resolved digest; on drift it
  calls `MarkReauth`, which revokes the credential reference, stops affected
  workloads, denies projected calls (non-`active` statuses deny by default)
  and notifies the owner until reauthorization.
- **D5 — `hubctl credentials migrate`.** Two-phase re-anchoring: pass one
  mints a broker request per local reference bound to a reviewed contract
  (`request_pending` — the owner still submits through the reviewed link,
  so secrets never travel the control plane); a repeated pass anchors the
  reference onto the minted broker credential (`anchored`) and preserves the
  legacy locator. `--rollback` rotates the reference back to the preserved
  legacy locator (`rolled_back`) and needs no broker connectivity. Runtime
  reads during the window stay dual-read: broker-backend references resolve
  through the Broker injector, local references through the legacy reader
  (`mergeCredentialInjectors`).
