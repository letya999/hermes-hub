---
description: Channel adapter contract, capability flags, registration and conformance — how the communication plane stays provider-agnostic.
last_verified: 2026-10-18
---
# Channels

The communication plane never names providers in core conditionals. Every
provider-specific behavior lives behind the `Channel` contract in
`internal/channel`; `internal/communication` dispatches through a registry.
Telegram is the only production channel; Slack stays supported and `mock` is
the test/canary stand.

## Contract

`internal/channel.Channel` (see `internal/channel/channel.go`) is the seam:

| Method | Called when |
|---|---|
| `ID()` | Registry key; the `Job.Channel`/`Delivery.Channel` value (`telegram_bot`, `slack_app`, `mock`) |
| `NormalizeDelivery(*Delivery)` | Enqueue-time provider identity rules — transport/target defaults and field invariants |
| `Deliver(ctx, *Delivery)` | One claimed outbox record goes out; `ErrUncertain` records uncertain instead of duplicate-retrying; `ErrDeliveryRetried` means the adapter requeued itself |
| `AuthorizeOccurrence(job, user, configured)` | Provider ownership re-check at routine fire time, after shared owner/scope checks |
| `ValidateOccurrence(o, caller)` | Provider envelope invariants on an admitted occurrence |
| `ValidateSchedule(in)` | Provider fields of a routine spec (target format, thread syntax) |
| `PrepareRoutine(*in, caller)` | Finishes a draft at admission — fills the provider target/envelope or refuses with `ErrRoutineDenied` |
| `RouteBlockedNotice(item, *Delivery)` | Retargets a blocked-routine notice to a safe address |
| `JobEnvelope(user, chatID, sender)` | Maps an inbound sender to the caller envelope |
| `Caps()` | Optional feature flags — the gateway asks before offering optional work |
| `SendPresence(ctx, chatID, topicID)` | Typing/progress signal; only when `Caps().Typing` |
| `DeliverArtifact(ctx, *Delivery, ref)` | Binary artifact send; only when `Caps().Artifacts` |

`Caps` flags: `Artifacts`, `EditableStatus`, `Typing`, `Destinations`,
`RoutineOrigin`. A false flag makes the gateway skip the mechanism entirely.

`TargetVerifier` is an optional extension for channels whose delivery target
can be re-bound between enqueue and send (the mock link table works this
way).

## Adapters and deps seams

Adapters never import `internal/communication`. Each gets an injected
`Deps` struct whose closures are evaluated at call time:

- `channel.NewTelegram(TelegramDeps)` — sender API, edit/retry/update spool
  callbacks, approval tracking, blob reads, voice planning and voice send.
- `channel.NewSlack(SlackDeps)` — events API auth and delivery.
- `channel.NewMock(MockDeps)` — file-backed stand for tests, e2e and canary.

The registry lives in `internal/communication/channel.go` as
`channelPlugins`: `provider id → adapter constructor`. `channelFor` resolves
adapters and accepts a nil gateway for the pure methods
(`ValidateOccurrence`, `Caps`).

## What stays in the gateway

Provider-agnostic mechanics are not pushed into adapters: job/occurrence
spool transitions, delivery throttling and coalescing, reply composition,
voice planning, task/topic binding, the routine engine and the mock
directories. Adapters only translate contract calls into provider protocol.

## Import boundary

`internal/commodel`, `internal/channel`, `internal/routines`,
`internal/commops` and `internal/serviceauth` are leaf packages with
parser-enforced import tests (`boundary_test.go` in each). The direction is
fixed: `communication` → leaves, never the reverse; `routines` reaches
channels only through the injected `OccurrenceChannel` interface in its
`Deps`, and `channel` never sees the gateway.

## Conformance

`internal/channel/conformance_test.go` exercises every registered adapter
against stubbed deps: ID/Caps shape, normalize-rejects-broken-identity,
foreign-caller occurrence rejection, routine admission/denial, and
telegram-specific normalize behavior. A new channel must pass the same
table without a gateway.

Durable ID formats (occurrence hashes, routine conversations, failure
digests) are frozen by goldens in `internal/routines` — a refactor that
changes them fails loudly.

## Adding a channel

1. Create `internal/channel/<name>.go` implementing `Channel` behind a
   narrow `<Name>Deps` struct — no communication import.
2. Declare `Caps()` honestly; false means "mechanism skipped".
3. Register in `channelPlugins` with a constructor taking `*Gateway`.
4. Add the adapter to the conformance fixtures and pass the suite.
5. Wire an inbound path (poll or HTTP route) that normalizes to `Job`.

## Boundaries

No provider API may be guessed: adapters implement only documented provider
contracts. Slack App credentials stay in communication-hub env, never in
agent tools. New channels ship disabled until registered and conformance-green;
live provider acceptance is a separate owner gate.
