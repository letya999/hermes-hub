# CHG-0305: Connector selection pending state

## Problem
`space apply` hard-fails when an authored `via: mcp:` selection has no active
binding — a transient onboarding state blocks the whole passport publish
(wedge: pending has no writer back to bound). Separately, a stale/dead action
link renders bare `unauthorized`, and `hubctl connector` write-verbs race the
live container's store fence.

## Design (ADR to follow)
- Connector selection lifecycle becomes explicit: `pending` (authored, no
  active binding) is a first-class profile state, not an apply error.
- `space apply` degrades missing bindings to `deferred` with a warning;
  structural errors (unknown toolset, ambiguous bindings, unsupported
  transport, unknown selected tools, missing tools.binding) stay fatal.
- `ProfileToolset` carries the authored intent (`access`, `tools.include`,
  `tools.exclude`) so pending selections can be materialized without the
  passport.
- `PutBinding` materializes pending selections for the binding's
  (principal, context, runtime, definition) on activation: stale-connection
  selections for the same toolset are superseded in one journaled
  `CapabilityChange`; profile Revision+1. Idempotent by construction.
- `tools_list` toolset rows gain `state:"pending"` when a connector-via set
  has no materialized selections — the model sees authored intent, not just
  callable tools.
- Dead-link UX: `/credentials/`, `/confirm/`, `/remove/`, `/connect/` render a
  localized "link expired — request a new one" page instead of bare 401.
  Freshness chain already exists (nonce mint → NoticeVersion++ → enqueue →
  PrepareVersion fence purges stale pending → EditMessageID where supported →
  version-scoped delivery confirmation); this adds a regression test.
- `hubctl connector` gains `--service`: facade verbs go through
  `/v1/tool-call` with the space toolhub token; `link`/`confirm` stay
  file-mode (nonce never crosses the API).

## Steps
0. tools_list `state:"pending"` + catalog release 40 + hub-agent-tools 19.0.0
1. Deferred apply (ProfileToolset intent fields + warning)
2. PutBinding materialization hook (pending→bound, supersede stale connID)
3. Dead-link landing pages (4 locales)
4. hubctl --service via /v1/tool-call
5. Tests, docs, live run on artem_two
