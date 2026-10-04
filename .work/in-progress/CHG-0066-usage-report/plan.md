# CHG-0066 Real task/session usage report (issue #169)

## Problem

`/session` only prints a session ID. The owner cannot see model, token spend or
context state for the current task, and nothing must ever present an estimate
as a measurement.

## Design

- `/usage` resolves the current task's conversation, asks the owning runtime
  (or supervisor lookup-forward) via a new authenticated `POST /v1/usage`
  carrying the job envelope — same trust boundary as `/v1/artifact`.
- The runtime queries the pinned Hermes API only: `GET /api/sessions/{id}`
  (cumulative input/output/cache/reasoning tokens, api_call_count, model,
  estimated/actual cost, message_count) and paginated
  `GET /api/sessions/{id}/messages` (per-message `token_count` summed over
  visible rows = stored context; `display_kind=hidden` rows = compaction
  boundaries, count + last timestamp). `resolve_resume_session_id` gives the
  effective session ID after split/compression continuation.
- Every field is a pointer: absent upstream data renders `unknown`, never a
  fabricated number. Context window and percent are always `unknown` on the
  pinned API — no transcript-length or billed-token estimation, no guessed
  model window. Provider quota/rate/credit stays a separate section, absent
  until an authoritative provider API exists.
- No runtime awake = honest "unavailable" (lookup-only forward, no spawn for
  a read).

## Out of scope

- Persisting usage history on the mapping; provider limit APIs that the pinned
  contract does not expose.
