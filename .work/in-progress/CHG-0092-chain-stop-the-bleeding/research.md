# M19 research: evidence and prior art

Shared by CHG-0092..CHG-0096. Sources are references for design ideas only; they do
not authorize external actions, dependencies or upstream patches.

## Local evidence (2026-10-13, dev stack)

| Observation | Where it was seen | Code anchor |
|---|---|---|
| Stream cut at 130 s -> `uncertain` | `.local/supervisor.log`: `/v1/jobs duration_ms=130124`, `131278` | `internal/supervisor/supervisor.go` `New` (cfg.HTTP 130 s), `execute`, `relayRunStream` |
| Pre-admission connect error -> immediate `uncertain` | gateway log `run_id="" status="uncertain"` 1 s after receive | `internal/communication/runtime.go` `RunOutcome` retry loop (503 only) |
| 15 Hermes restarts, most "exited UNCLEANLY" | `spaces/local/hermes/logs/errors.log` `lifecycle_ledger` | `internal/supervisor/reconcile.go` remediation, `applyRuntimeRestartLocked` (`docker restart` without `--time`), container `StopTimeout=1` |
| Projection change restarts Hermes | code | `internal/toolhub/restart_notify.go`, `Manager.restartRequestHTTP` |
| 131 bindings, 14 policy versions, 1 active under current | `spaces/local/runtime/toolhub/store.json` | `internal/stack/render.go` `policyVersion`; `internal/toolhub/projection.go` `ListProjectedTools` silent `continue` |
| Host Windows path baked into hub child env | CHG-0091 state | `internal/stack/hermesconfig.go` `applyHubCommunicationConfig` |
| Breaker hides all hub tools for 60 s | `errors.log` "unreachable after 3 consecutive failures" | pinned Hermes `tools/mcp_tool.py` (`_CIRCUIT_BREAKER_THRESHOLD=3`) |
| Session with 940 messages, ~52 k input tokens/call | Hermes `state.db` `sessions` | `internal/runtime/server.go` `sessionIDFor`; `internal/communication/occurrences.go` conversation of occurrences |
| Gateway ~60 % idle CPU, 5 600 spool files | `docker stats`, `/data` listing | `internal/communication/communication.go` 100 ms tickers, `findIdempotencyLocked`, `ClaimJob` |
| 1.6 MB `supervisor.json` with prompts, rewritten per event | `spaces/.control/supervisor.json` | `Manager.persistLocked`, `finishJobGenerationLocked` |
| Supervisor manual process on `0.0.0.0:8876`, token in `.cmd` | `.local/bin/run-supervisor.cmd` | `cmd/supervisor/main.go` |
| Routine error spam every 2 min | gateway log `-error` deliveries 12:06-12:44 | `occurrences.go`, delivery of routine outcomes |
| No liveness signal in Telegram | code has no `sendChatAction`/`editMessageText` | `communication.go` Telegram API |
| `workload-controller` silent, mixed UTC/local logs, no job ID in supervisor/ToolHub logs | docker logs | `internal/diagnostics/http.go` |
| Cliproxy 404 storms for unknown model IDs | cliproxy log | model readiness absent |

## Prior art

| Source | Idea used | Applies to |
|---|---|---|
| OpenClaw agent loop: https://docs.openclaw.ai/concepts/agent-loop | Per-session lane plus global lane; run returns `runId` immediately; `agent.wait` terminal status; durable writer claim | D1, D2, D11 |
| OpenClaw command queue: https://docs.openclaw.ai/concepts/queue | Queue modes steer/followup/collect/interrupt; lane concurrency caps | D11, future M11 |
| OpenClaw lane task timeout: https://github.com/openclaw/openclaw/issues/48690 | A hung task must not block a session lane forever | D1 |
| OpenClaw message loss: https://github.com/openclaw/openclaw/issues/50563, https://github.com/openclaw/openclaw/issues/40079, https://github.com/openclaw/openclaw/issues/54322 | Persist before ack; write-ahead outbound queue with TTL and idempotent delivery | D2, R3 |
| OpenClaw gateway as daemon: https://github.com/openclaw/openclaw/blob/main/docs/gateway/index.md | Exit non-zero on fatal error; launchd/systemd restarts; loopback bind by default | D4 |
| NanoClaw architecture: https://docs.nanoclaw.dev/concepts/architecture | Wake on write plus periodic sweep; single writer per file; journal mode caveats across VirtioFS | D8, D12b |
| NanoClaw container lifecycle: https://docs.nanoclaw.dev/concepts/container-lifecycle | Disposable long-lived session container; in-flight spawn dedupe; durable session claim; heartbeat file | D3 |
| NanoClaw security model: https://github.com/nanocoai/nanoclaw/blob/main/docs/SECURITY.md | Mount-defined boundary; credential gateway outside the agent container | invariant check |
| GoClaw: https://github.com/nextlevelbuilder/goclaw | Single Go binary, tenant-scoped fail-closed queries, one `RetryDo` for providers | D1 retry helper |
| Iva: https://github.com/smixs/iva-agent | systemd services + watchdog timers; turn journal and `iva trace show`; "broken skill no longer turns off tools"; diagnose never restarts | D4, D6, D10 |
| Open Dots: https://github.com/Anil-matcha/open-dots | Disposable sandboxes from Telegram with visible approvals | invariant check |
| noclaw: https://github.com/angristan/noclaw | Gateway on 127.0.0.1 by default, pairing codes | D4 |
| Hermes API server: https://hermes-agent.nousresearch.com/docs/user-guide/features/api-server | `Idempotency-Key` on `/v1/runs` replayed for 24 h incl. after restart; SSE keepalive every 10 s; `/v1/runs/{id}/stop` | D1, D2 |
| Hermes gateway session lifecycle: https://hermes-agent.nousresearch.com/docs/developer-guide/gateway-session-lifecycle | Session turn leases, restart recovery | D2, D7 |
| Hermes breaker fixes: https://github.com/NousResearch/hermes-agent/pull/67340, https://github.com/NousResearch/hermes-agent/pull/68455, https://github.com/NousResearch/hermes-agent/pull/17016 | Tool-level errors do not trip the breaker; per-server threshold; half-open recovery | D12c, R1.7 |
| MCP client best practices: https://modelcontextprotocol.io/docs/2026-07-28/develop/clients/client-best-practices | Progressive tool discovery above 1-5 % of context; refetch after `list_changed` | D6, D7 |
| MCP list_changed client bugs: https://github.com/openai/codex/issues/33266 | Clients must refetch on `list_changed`; test it | D3 |
| Anthropic Managed Agents: https://www.anthropic.com/engineering/managed-agents | Session log, harness and sandbox as separate interfaces | D2, D7 |
| Anthropic context engineering: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents | Minimal, stable context; token-efficient tools | D7 |
| Anthropic long-running harnesses: https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents | Fresh context with structured handoff instead of endless sessions | D7 |
| Durable execution survey: https://alatirok.com/durable-execution-ai-agents-compared/ and Restate durable agents: https://docs.restate.dev/ai/patterns/durable-agents | Persist step results; never blindly retry side effects; idempotent tools | D2 (no engine adopted) |
| Telegram AI streaming: https://core.telegram.org/api/bots/ai and draft streaming notes: https://alimov.dev/posts/streaming-llm-responses-to-telegram-with-reactive-draft-messages/ | `sendMessageDraft` (Bot API 9.3, all bots since 9.5), finalize with `sendMessage`; chat actions | D9 |
| Habr migration notes: https://habr.com/ru/articles/1044574/ and https://habr.com/ru/articles/1068762/ | Hermes default provider routing pitfalls; group allowlists; agent-to-agent injection | R1.6, invariant check |

Not found during research: a project named `open-grok-bot`. `osinara` is an Eve-based
memory reference and is not relevant to chain reliability.
