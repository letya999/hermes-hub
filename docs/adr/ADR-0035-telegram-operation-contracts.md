---
description: Proposed repair of Telegram image intake, routine intents, approval delivery and exact file handoff.
last_verified: 2026-10-13
---
# ADR-0035: Deterministic contracts around Telegram agent operations

Status: accepted, 2026-10-13, by the owner's explicit instruction to implement
the reviewed design. Implementation and live acceptance are recorded separately.

Requirements: [SPEC-0052](../../specs/active/SPEC-0052-telegram-operation-contracts.md).
Execution: [CHG-0091](../../.work/in-progress/CHG-0091-telegram-operation-contracts/plan.md).
Evidence and alternatives: [research](../../.work/in-progress/CHG-0091-telegram-operation-contracts/research.md).

## Context

The October Telegram acceptance exposed two image failures (extensionless photo
names and missing model credentials in a stdio child), a false refusal of an
already implemented watcher, a wrong relative deadline followed by a second
reminder, stale approval UI and file replies containing paths rather than bytes.
CHG-0088/0089 repaired parts of approval and routine wiring; CHG-0090 is still
being changed. Its source/CI work must be reconciled before implementation here.

Existing decisions remain: Go control/runtime tools; pinned upstream Hermes;
one local spool writer; owner-scoped authorization; canonical receipts before
outbox handoff; originals preserved by SPEC-0050. No new queue, database,
workflow framework, model provider or native Hermes vision enablement is needed.

## Proposed decisions

1. **Image bytes determine input format.** Confine and bound the read first,
   then reuse `sniffImage` and `imageBounds` for PNG/JPEG/WebP. Filename and
   channel MIME are hints, never parser authority. Apply the same rule to
   inspection and edit input (`imageFile`); output conversion format remains an
   explicit argument. Return the actual MIME to the model endpoint. A supported
   header with an invalid body is still an error. Validate every image before
   sending any member of a multi-image set.
2. **Application env is explicit.** The existing trusted `hub` stdio child gets
   only the declared variables it consumes, including `OPENAI_API_KEY` and
   `HUB_HERMES_CONFIG` when a config override is selected. Never inherit the whole
   parent environment. Fix both new rendering and existing-config materialization.
   Managed ToolHub execution keeps its current relay/private-executor boundary.
   A real pinned-Hermes config-loader canary proves interpolation, child spawn
   and calls; a raw SDK canary remains useful but proves a narrower boundary.
3. **Routine descriptions agree across transports.** Share reviewed descriptions
   for reminder/watcher/check/script/digest between direct MCP and ToolHub. Explain
   watcher silent baseline, capture groups, old/new preview and errors. Actual
   grants and handler checks remain authoritative. Malformed URLs get an explicit
   error; trim surrounding whitespace only, never silently strip internal Unicode
   characters or rewrite a destination. Keep the existing watcher state machine.
4. **Relative time resolves once at the destination.** For reminder creation,
   `expression=after:180s` is normalized to `once:<UTC deadline>` using one trusted
   gateway clock sampled for the committed creation. The delay starts when the
   create operation is accepted, not at the earlier natural-language message.
   Persist the requested expression and resolved deadline in a creation receipt.
   Replays return the receipt and current state without moving the deadline.
   Store UTC; keep timezone for display. Persisted wall time survives restart;
   process-monotonic time is not a durable schedule identity.
5. **Creation replay and new intent are distinct.** Reuse schedule ID as the
   existing client request identifier. Under the spool lock, compare an immutable
   creation-intent digest, including owner/context and delivery audience. Identical
   owner requests replay; changed parameters conflict; foreign owners fail closed;
   retired IDs never reactivate. Store receipt in the schedule's atomic write and
   preserve it in new deletion tombstones. Never deduplicate solely by text, URL
   or a hash of LLM output. A new ID is a new operation: this does not claim
   universal deduplication of a model inventing a different ID. Instructions and
   agent E2E must reject silent replacement after an unknown/already sent result.
6. **Approval UI follows the exact pending request.** Immediately before send,
   recheck the complete delivery audience, current approval ID/state, deadline
   and saved decision. Suppress superseded, expired, decided or terminal requests.
   Saving an intent stops further actionable buttons but is not execution success.
   Keep durable message receipts through cleanup failure; remove resolved messages
   only after application/reconciliation or terminal resolution. Preserve existing
   ownership, choice, policy, generation, replay and lease checks.
7. **File delivery uses original bytes.** Keep the current artifact allowlist and
   `MEDIA:` routing. Describe the actual auto-delivery buckets precisely; use an
   explicit marker for an older file or a file at the artifact root. For a request
   for existing files, copy/attach original bytes, not model-generated substitutes.
   Delivery progress belongs to outbox receipts. Paths or a model's success text
   are not proof of attachment. No implicit export of private Hermes state is added.

## Album follow-up boundary

The core repair covers single-photo intake and explicit multi-image inspection.
Album aggregation is a subsequent slice, not a prerequisite for either repair.
Telegram links separately delivered messages with `media_group_id`; there is no
authoritative last-member signal in that field. Proposed follow-up: a durable,
owner/chat/topic/context/group-bound collector, a bounded quiet window, stable
message-ID ordering and duplicate-update suppression. Late members are reported
as late, never silently dropped or used to rerun a closed group. The collection
window is an operational choice, not proof of album completeness. Group jobs carry
trusted multiple inbound IDs and fetch every member through existing ownership
checks before model admission. Limits and recovery are in SPEC-0052.

## Alternatives and consequences

- Renaming through a shell leaves valid existing extensionless files broken and
  makes vision depend on CLI availability. Reuse byte sniffing instead.
- Whole-parent env inheritance leaks unrelated credentials. Adding a new vision
  service is unnecessary for the existing model endpoint contract.
- A prompt-only deadline fix cannot control clock arithmetic or replay. A new
  scheduler framework duplicates already durable occurrences and receipts.
- A duplicate request error without read-back encourages a new ID after timeout.
  Immutable receipt replay addresses transport uncertainty, not semantic identity.
- Auto-delivering every new workspace file expands disclosure and breaks the
  existing artifact contract. Precise instructions plus explicit markers suffice.
- An in-memory album debounce loses members on restart. Aggregation must use the
  existing spool and be separately tested before being advertised.

New catalog descriptions require a new immutable definition version/digest and
reviewed binding renewal; no selection/grant expansion. New receipt fields are
additive. Old schedules/tombstones without a receipt keep conservative conflict
behavior. Older binaries must not be used to mutate the new receipt-bearing spool
until downgrade preservation is verified; rollback first drains and backs up it.

Acceptance distinguishes local logical deduplication, API response receipt and
provider delivery. Telegram transport timeouts remain uncertain unless reconciled;
there is no blanket exactly-once claim. Sources supporting these choices are
linked and qualified in the research record; they do not grant external actions.
