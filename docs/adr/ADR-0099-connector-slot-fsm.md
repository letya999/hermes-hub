---
description: One live onboarding per owner/definition, four-stage lifecycle plus a single gate slot, derived-only model action vocabulary and channel-confirmed delivery status.
last_verified: 2026-11-13
---
# ADR-0099: Connector slot lifecycle — small explicit FSM

Status: accepted. Date: 2026-11-13. Amends ADR-0098 decision 1; implemented by CHG-0304.

## Context

Onboarding state was implicit: `Phase` (14 values) multiplied by five parallel
nonce/expiry arm slots, a policy-epoch axis and `SupersededBy`. The journal
steps already carried fine-grained progress, so `preparing/review/verifying/
retry-scheduled/confirmed` duplicated `Steps`; `enabled/disabled` duplicated
`Connection.Status` as a second write. The model result exposed three
competing action vocabularies (`state`, `next_action`, `pending_action`) plus
the raw phase, `link_sent` and `delivery_status`. Two real incidents followed:
duplicate live onboardings for one owner/definition (the idempotency key
derived from request parameters, not from semantic identity), and a
`delivered` status that meant "outbox file reached done", not "the channel
confirmed the send".

## Decisions

1. **Stage, not phase.** `api.Onboarding` carries `Stage` with four values:
   `pending`, `live`, `failed`, `gone`. The 14 legacy phases map: preparing,
   review, verifying, retry-scheduled, confirmed → `pending`; awaiting-*
   → `pending` with a gate; enabled/disabled → `live` (connection status is
   read from the connection, never duplicated onto the onboarding);
   failed/cleanup_incomplete → `failed`; removed/revoked/superseded → `gone`.
   Fine-grained progress remains in journal `Steps` — `Stage` does not
   duplicate it.
2. **One gate slot.** `Gate {kind, nonce, expires}` replaces the five
   parallel nonce pairs (confirmation, form, remove, wizard, broker/device).
   `gate.kind ∈ {confirm_setup, credentials, oauth, remove}`. Arming a gate
   atomically clears the previous one in the same store write: at most one
   live action link exists per record, ever. Step-scoped secrets still gate
   each mutation; the gate nonce is a view/resume capability.
3. **One slot per owner/definition.** `claimOnboardingSlot` is the single
   find-or-create gate under the store mutex, keyed by
   (principal, context, runtime, definition_id). A repeat install on a live
   record returns it with resend semantics; it never creates a sibling.
   Terminal records are history and do not block a fresh install.
4. **Derived-only model vocabulary.** `OperationResult` exposes `state`
   (working|waiting_you|ready|broken|done), `you_must`
   (click_link|wait|retry|remove|enable), and `delivery`
   (not_sent|queued|sent|failed) + `channel`. `next_action`,
   `onboarding_phase`, `link_sent` and `pending_action` are removed from the
   model surface; they collapse into `you_must`, computed from
   (stage, gate, gate expiry) by one function.
5. **Truthful delivery.** `sent` is emitted only when the channel confirmed
   transmission (Telegram/Slack API ok). A completed mock-sink write reports
   `channel:"mock"` — visible as a test sink, never silent "delivered".
   `link_sent` is folded into `delivery`. The honest ceiling is `sent`;
   "user clicked" is never claimed.
6. **One resolver.** `connector_list` and `connector_status` share
   `ResolveConnectorTarget`; any id the list exposes is addressable by
   status, including stale-epoch records (reported `broken/stale_policy`,
   removable only). `id` is accepted as an alias of the `name` argument.
7. **Transitions through one table.** `transition(onboarding, stage)` owns
   the allowed edge set (pending→live|failed|gone, live→failed|gone,
   failed→pending|gone); bare `o.Phase =` writes are replaced by calls.
   Rejected edges return an internal error, not a silent state.
8. **Migration quarantines duplicates.** On load, live records group by
   (owner, definition); the connection-bound record wins, else the newest;
   losers transition to `gone` with `superseded_by=winner` — audit kept,
   nothing deleted.

## Consequences

- Schema bump on `api.Onboarding`: `Stage`, `Gate`, `SupersededBy` persist;
   the legacy `Phase`/nonce pairs load once into the new shape and are
   dropped on the next write.
- The tool contract changes (`you_must`, `delivery`, removed fields) ship in
   one changeset with description, prompt-contract guidance, docs/tools.md,
   docs/routines.md and a bumped immutable hub-agent-tools catalog version,
   followed by rebind and managed-space respawn.
- `Operation.State` stays for journal resume but is no longer model-facing.
- `disabled` stops being an onboarding stage: `connector_edit disable`
   writes connection status only; the onboarding view reads it.
- Rejected alternative: a secondary `slotIndex` map — a second truth to
   maintain; a mutex-guarded scan on the authoritative map is cheaper and
   cannot drift.
- Rejected alternative: collapsing `Operation`/journal steps into `Stage` —
   they serve restart resume, a different concern.

## Amendment 2026-11-13 (implementation findings)

- **Revoked is resumable.** `gone` is absorbing for `removed`/`superseded`,
  but a `revoked` tombstone keeps exactly one out-edge: `gone→pending` via
  reauth, which reopens credential intake on the same record. `DeadEnd()`
  encodes the split — only non-revoked tombstones are unaddressable.
- **Wire labels stay stage-first.** `NoticePhase()` derives the channel
  vocabulary (`preparing`, `awaiting-*`, `confirmed`, `enabled`, `disabled`,
  `failed`, `cleanup_incomplete`, `removed`, `revoked`) from (stage, gate,
  tombstone, binding). Gate labels apply only on `pending`: a gated action
  (reauth, `allow_writes`) drops the record to pending before arming, so a
  gate surviving on live/failed/gone is a spent tombstone — never an
  "awaiting" state.
- **Disabled is rendered, not stored.** `status` and `connector_list` emit
  `phase:"disabled"` for a `live` record whose binding is `DisabledStatus`;
  the record itself never stores a disabled lifecycle.
- **Removal ladder on `gone`.** A revoked record entering confirmed removal
  runs its finalizer ladder in place (gone never bounces through failed);
  `confirmedRemoveInProgress` counts gone+pending-finalizers so a crash
  resumes the ladder.
- **Load migrations must not touch the store digest fence.** Any
  `UnmarshalJSON` fold that changes a record's serialized shape makes the
  persisted bytes differ from the reserialization. The `rows-v1` digest
  therefore verifies the RAW row bytes (`rawIndex` built during
  `readDBSnapshot`), and `loadDB` seeds `dbRows`/`diskDigest` from the same
  raw index — not from `dbRowsFor(state)`. Seeding the write fence from
  reserialized state would do two silent harms: the incremental diff base
  would treat migrated rows as unchanged (they would never flush to disk),
  and the foreign-commit fence would read the first legitimate save as a
  conflict. First save after a migrating load rewrites every migrated row —
  that is the intended flush.
