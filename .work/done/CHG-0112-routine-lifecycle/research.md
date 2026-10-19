# Routine design evidence — 2026-10-17

The latest request says NanoFlow/GoFlow/OpenFlow; earlier context names Claw
agents. Compare NanoClaw, GoClaw and OpenClaw. Literal NanoFlow is also an LLM
serving research project; do not conflate unrelated products. Sources below are
current upstream documentation fetched on this date, not a claim that these
features exist in the Hub's pinned Hermes binary.

| Primary source | Observation and decision |
|---|---|
| [NanoClaw scheduling](https://raw.githubusercontent.com/nanocoai/nanoclaw/main/docs/scheduled-tasks.md) | Task CRUD, pause/resume and run/logs make management first-class. Adopt explicit lifecycle and history. Do not copy its agent-per-run delivery model for exact reminders. |
| [NanoClaw architecture](https://raw.githubusercontent.com/nanocoai/nanoclaw/main/docs/architecture.md) | Host applies scheduling actions and advances recurrence from scheduled instants. Keep trusted clock outside model; architecture document labels itself draft, so it is evidence of direction, not verified deployment. |
| [GoClaw scheduling](https://raw.githubusercontent.com/nextlevelbuilder/goclaw-docs/master/advanced/scheduling-cron.md) | At/every/cron, timezone, toggle and run history. Adopt interval plus lifecycle; avoid PostgreSQL migration merely to match a competitor. |
| [OpenClaw management](https://docs.openclaw.ai/automation/cron-jobs/managing-jobs) | Get/edit/enable/disable/runs are operational necessities. Adopt distinct reads and mutations rather than natural-language-only management. |
| [OpenClaw schedules](https://docs.openclaw.ai/automation/cron-jobs/schedules) | Separate at/every/cron triggers from payload and delivery. Preserve that separation; do not expose arbitrary shell payloads. |
| [Temporal schedules](https://docs.temporal.io/schedule) | Schedule and execution differ; calendar/interval, bounds, catch-up and overlap require explicit semantics. Keep existing bounded coalescing and add small windows without a workflow service. |
| [Hermes cron](https://hermes-agent.nousresearch.com/docs/user-guide/features/cron/) | No-agent execution and lifecycle tooling reinforce explicit deterministic work. Do not silently replace the pinned dependency. |
| [Eve dynamic scheduling](https://raw.githubusercontent.com/vercel/eve/main/docs/patterns/dynamic-scheduling.md) | Session-derived owner identity and claimed due rows; preserve host-derived identity and durable admission. |

Earlier audit searched X, DEV, Substack, HN, arXiv, Medium, Reddit, GitHub and
Habr. Reports of cron success without channel delivery are useful scenario
inputs, not proof of a current competitor defect. Technical decisions above use
upstream sources. No evidence supports claiming universally 'better than all'.

Local evidence: 74,611-byte list yielded status ok without result; snooze followed
by an earlier expression edit reported the new deadline but tick admitted no
occurrence there. Live managed routine_list failed strict catalog decoding on
unknown source: deployment version drift is an acceptance requirement.
