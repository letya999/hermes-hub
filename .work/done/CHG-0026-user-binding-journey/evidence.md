---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## CHG-0026 / SPEC-0023 ToolHub control plane

M5.2 in-repo tests drive the shipped ToolHub MCP handler (`NewEndpointHandler`
/ `Gateway`) with two synthetic principals. They cover the eight control
operations, forged owner/locator/backend/policy denial, Alice/Bob isolation,
self-install grant enforcement, catalog assignment, promotion without converting
user bindings, loopback credential elicitation into the encrypted store, OAuth
PKCE/state/redirect/TTL/nonce, expired confirmation and replay, rotate/revoke
cutting an open session, idempotent enable, disable/remove dropping tools and
that binding's volumes, effect/budget escalation, concurrent lifecycle races,
and 95 unique plus 5 shared credential references. A live Hermes chat that
reconnects tools remains #74 / M5.3. Live GitHub import and Docker workload
start reuse M5.1 evidence when present; they are not required to freeze the
control-plane contract.

On 2026-09-17, `just check` passed, including race tests, formatting, vet,
staticcheck, documentation/workflow checks and 85.17% original Go statement
coverage. Production `NewEndpointHandler` wires a default source reviewer,
OAuth broker and `FormOrigin`; `required_credentials` URL elicitation points
at the same loopback `form_url` the status body returns. `go.mod` and `go.sum` were not changed, so `just security` was not
required. `just docker-check` built image `68424bee6191` and passed standalone
smoke plus the pinned Hermes 0.21.0 contract and five-minute gateway
lifecycle. The probe used fixtures and made no live Telegram or provider
call. faster-whisper `hub-stt` on that image was unavailable
(`exec /usr/local/bin/hub-stt: no such file or directory`); that is not
treated as ToolHub control-plane or provider integration success.
