---
description: M17 owner-invoked context compaction — dual engine behind grants, upstream command semantics, turn-boundary safety.
last_verified: 2026-10-24
---
# SPEC-0073: Manual compaction

Draft: 2026-10-24 — pending owner acceptance. Decision: ADR-0075. Changes: CHG-0161.
Issue: #309.

- R1 (#309): Channel command `/compact` (alias `/compress`) accepts `[here N]`, `[focus <text>]`, `prune` and `preview`, matching upstream `/compress` semantics plus a selective hygiene mode; parse errors return usage text without side effects. `prune` runs the tool-output pruning stage only — zero LLM calls, cheapest possible compact; it is the answer to "just drop the noisy tool outputs" without summarizing anything.
- R2: Engine selection is grant-gated (`context.compact` grant lists allowed engines) over `features.compact.engine` (`auto|hub|native`); an ungranted engine request is a named refusal and an audit row. `auto` resolves to `hub` until upstream advertises a structured compact capability.
- R3: Both engines operate only on the caller's current stable conversation session, only at a turn boundary, under upstream cross-process locks (`compression_lock`, `session_turn_lease`); they never create a session, never mutate mid-run, never send `/compress` as model input.
- R4: Busy sessions produce one deterministic outcome — a clear refusal naming the busy state or a bounded queued compact per `busy_policy`; no user turn is lost or silently reordered.
- R5: `preview` performs the full dry-run pipeline (window, estimate, projected savings) and mutates nothing; its report names the cascade stage that would fire (`would_method`) so the owner sees whether a free `prune_only` suffices before paying for a summary. Summary failure preserves the transcript and names `failure_class`; no partial commit is possible (atomic apply only).
- R6: Responses and tool results carry only allowlisted fields: `{applied, tokens_before_est, tokens_after_est, messages_before, messages_after, method, retained_tail, failure_class}`. Summary text never appears in responses, journals, logs or diagnostics.
- R7: ToolHub exposes `context_compact` (write effect, owner/context scoped, grant-checked, audited) and `context_status` (read effect, same scope) bound to the same engine path as the channel command.
- R8: The `native` engine delegates to upstream `ContextCompressor`/`partial_compress` through the sidecar; the `hub` engine implements its own window/prune/prompt/verify/commit pipeline in Go through the model relay. Both honor identical R3–R6 contracts.
- R9: When upstream ships a structured compact endpoint, the `native` engine switches transport without changing R1–R6; absence of required upstream symbols in the pinned image yields `unsupported`, not a crash.

Acceptance A1..A9: regression tests cover arg parsing, grant denial, busy outcomes, preview purity, failure-class naming, response allowlists, owner/context isolation and concurrency on the real Go boundaries; a dev-stack run compacts a real session on a real provider before acceptance is claimed.
