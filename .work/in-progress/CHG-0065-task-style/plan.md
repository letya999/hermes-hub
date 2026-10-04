# CHG-0065 Per-task communication style (issue #168)

## Problem

The owner cannot set a per-task presentation style (concise Russian, detailed
with sources, no progress chatter) without editing global SOUL.md or leaking it
into other tasks/channels.

## Design

- `Task.style` + `style_version` live on the durable task record (CHG-0064).
  `/style` shows effective style + scope, `/style <free text>` sets it,
  `/style reset` clears it. Style is bounded (length, control characters
  stripped) and never reaches a provider or permission path.
- The pinned Hermes `/v1/runs` accepts `instructions`, applied upstream as
  `ephemeral_system_prompt` — a trusted per-run instruction, never a synthetic
  user message. Call-time instructions do not persist upstream, so the hub
  reapplies on every admitted run.
- `ExecuteRequest.instructions` carries the snapshot. The gateway pins the
  admitted style (`style`+`style_version` on JobMapping) before POSTing, so a
  queued or retried job keeps its admitted snapshot while `/style` changes take
  effect at the next run. Precedence: hub safety/authorization > task style >
  channel presentation limits.
- Local CLI runner (no API contract) ignores style; the supervised/resident
  HTTP path is the supported one.

## Out of scope

- Adaptive auto-style inference from corrections; `/style` is the explicit
  setting only. Shortcut presets beyond free text.
