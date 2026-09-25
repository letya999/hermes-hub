---
status: active
title: Multi-tenant ToolHub control plane — grants, credentials and lifecycle
---
# ToolHub control plane

This specification freezes the M5.2 control plane on top of SPEC-0017,
SPEC-0018, SPEC-0019 and SPEC-0020. It does not switch the default Hermes
runtime to ToolHub, patch Hermes or ToolHive, or claim a live chat
reconnect (#74 / M5.3).

## Separation of records

| Record | Meaning | Who may create or change it |
|---|---|---|
| Catalog `ToolDefinition` | Administrator-preflighted immutable manifest | Operator import/preflight only |
| User `ToolDefinition` | Result of a granted self-install review | ToolHub after `prepare_source`; visibility is the installing principal |
| `Grant` | Catalog-default, definition assignment, or self-install | Operator (`hubctl grant` / `PutGrant`). The model cannot create or widen a grant |
| `Onboarding` | Async install/review/confirm state | Authenticated control operations for that principal only |
| `ToolBinding` | Effective projection for one principal/context/runtime | `confirm`/`enable` for the authenticated principal |
| `CredentialReference` | Opaque locator and key names | Created per binding by default; shared locator only under an explicit admin policy |
| Workload | Running instance | Started on demand; never shared for stateful or static-credential definitions |

Promoting a user MCP to a trusted catalog item publishes a shared
definition. Existing user bindings stay principal-owned and do not become
shared workloads.

## Control MCP

The shipped ToolHub MCP handler exposes these logical operations as tools:

`prepare_source`, `status`, `required_credentials`, `confirm`, `enable`,
`disable`, `revoke`, `remove`.

Principal, context, runtime and policy come from the authenticated
request. Hermes does not pass an arbitrary `user_id`, `owner_id` or
credential locator into ToolHub. Model-supplied `user_id`, `owner_id`,
`principal_id`, `context_id`, `runtime_id`, `policy` / `policy_version`,
`locator`, `backend`, `mcp_endpoint` and grant fields are rejected before
any store write.

`prepare_source` and `enable` are idempotent on a client `request_key`.
Onboarding is persisted so `status` survives process restart.

### Two install modes

1. **Administrator preflight catalog.** The operator imports, builds and
   reviews an MCP. A principal may `prepare_source` / `confirm` / `enable`
   that definition only with a `catalog-default` or definition grant.
2. **User self-install.** Any principal submits a public GitHub URL with an
   exact commit SHA. ToolHub imports, reviews
   permissions/effects/credentials and, after `confirm`, creates only that
   principal's binding. Self-install is allowed by default; a disabled or
   revoked `self-install` grant denies that principal.

The model cannot issue a grant. A principal cannot see or call another
principal's binding.

## Credentials

`required_credentials` returns names, types and hints only. Production
input is a loopback HTTPS/HTTP form bound to the principal and onboarding
id, or MCP URL elicitation that points at that form. Form-mode elicitation
and ordinary chat / Telegram `KEY=value` are not the production MCP
credential path.

Values are stored in `internal/credstore` ciphertext. They are injected
only after `AuthorizeProjected` into the selected workload. They never
appear in Hermes, ToolHub HTTP responses, vMCP, logs, audit content, image
layers or process arguments.

Defaults:

- one credential reference per binding;
- several bindings per user are allowed;
- one external credential shared across users only with an explicit admin
  shared-credential policy;
- workloads stay separate even when a locator is shared;
- a shared workload is allowed only for stateless per-request credentials.

OAuth uses authorization-code with PKCE (S256), state bound to principal /
context / onboarding, allowlisted redirect, TTL and a single-use nonce.
Expired confirmation and replayed nonce fail closed.

Rotate and revoke increment credential and connection revision, revoke the
affected binding, stop starting/running workloads, and deny the next
`tools/call` on an already-open MCP session before backend execution.

## Lifecycle

Repeated `enable` of the same binding is idempotent (same binding id, no
second workload). `disable` and `remove` omit the binding from projection
and delete only that binding's volumes. Effect or budget escalation above
the reviewed definition is denied before write or start. Concurrent
`enable` / `disable` / `revoke` serialize on the registry fence so a later
call observes the new state; an already admitted call finishes under its
recorded revision.

Ninety-five unique credential references plus five uses of one shared
reference keep distinct locators/workloads/state. Sequential uniqueness is
enough; one hundred concurrent MCP+ToolHive stacks are not required.
