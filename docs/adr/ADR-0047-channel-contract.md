---
description: Channel contract — 12-method seam, Caps flags, deps injection, registry dispatch and conformance suite.
last_verified: 2026-10-18
---
# ADR-0047: Channel contract and adapter seam

Status: Accepted 2026-10-18. CHG-0122 introduced the seam; CHG-0125 moved it
to `internal/channel`. Planned as ADR-0045 in the phase plan, renumbered
because that record went to watcher probes.

## Context

Pre-refactor, delivery/authorization/routine admission switched on provider
names (`delivery.Channel`, `job.Channel`) scattered through the gateway.
Provider helpers were free functions reaching `g.api`, spool and voice
internals directly.

## Decision

- `Channel` is a 12-method contract (ID, NormalizeDelivery, Deliver,
  AuthorizeOccurrence, ValidateOccurrence, ValidateSchedule, PrepareRoutine,
  RouteBlockedNotice, JobEnvelope, Caps, SendPresence, DeliverArtifact) plus
  the optional `TargetVerifier` extension. See `docs/channels.md` for the
  method table.
- `Caps` (`Artifacts`, `EditableStatus`, `Typing`, `Destinations`,
  `RoutineOrigin`) replaces provider-name conditionals: a false flag skips
  the mechanism entirely.
- Contract errors are part of the durable protocol: `ErrUncertain`
  (provider did not confirm — record, never duplicate-retry),
  `ErrDeliveryRetried` (adapter requeued itself — dispatcher backs off),
  `ErrRoutineDenied` (ownership refusal — maps to 403).
- Adapters are constructed from per-provider `Deps` structs
  (`TelegramDeps`, `SlackDeps`, `MockDeps`) whose closures evaluate gateway
  fields at call time. Adapters never import `communication`; the package
  is a compile-time leaf.
- Dispatch goes through `channelPlugins` (provider id → constructor), so a
  durable record with an unknown channel fails closed instead of falling
  back to a default provider.
- The conformance suite exercises every registered adapter against stubbed
  deps — a new channel proves the contract without a gateway.

## Consequences

- New channel = one package file + registry line + conformance entry +
  inbound adapter. Core mechanics untouched.
- Telegram remains the only production channel; Slack is preserved behind
  the same seam; `mock` is the test/e2e/canary stand.
- Gateway retains genuinely channel-neutral mechanics: spool transitions,
  throttling, reply composition, voice planning.
