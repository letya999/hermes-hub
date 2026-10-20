# CHG-0080 — M10 Phase A: cheap wins

Worktree `hermes-hub-m10a`, branch `feat/m10-phase-a` off `feat/m8-toolhub-cli` @ 665660a.
No Docker delivery, no image rebuilds — Go code + unit gates only.

Scope (from M10 audit, phase A):

| Issue | Deliverable |
|---|---|
| #181 | `.work` research note on ai-stp fit |
| #150 | `image_inspect` accepts several workspace images in one call |
| #151 | `archive_create` / `archive_unpack` workspace-confined zip ops |
| #214 | lease-vs-idle proof test (job lease pins runtime through tool calls) |
| #195 | append-only usage journal per job attempt + `hubctl usage` read |
| #247 | `job_kind=reminder` routines delivered by the Go outbox path, no runtime/LLM |
| #194 | `/status` + `/routines` deterministic commands; approval inline buttons via callback_query |

Existing surfaces reused: `os.Root` scoping (agenttools), schedule `job_kind` +
`once:` expressions + occurrence dispatch (communication), stream `approval.request`
deliveries, `queueDelivery`, audit ledger pattern for the usage journal.
