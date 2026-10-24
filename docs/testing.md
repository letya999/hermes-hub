---
description: Test pyramid and evidence rules — unit/race, contract probes, Docker gates, mock channels, live acceptance.
last_verified: 2026-10-24
---

# Testing

## Pyramid

1. **Unit** — `go test ./internal/...`; table tests over real handlers, not
   interfaces-under-themselves. Race (`-race`) and shuffle are part of
   `just check`, not opt-in extras.
2. **Contract probes** — `go run -tags integration ./cmd/devcheck
   <probe>`: `hermes-contract` (pinned image `/v1/*` + sessions),
   `toolhub-contract`, `toolhub-hermes-contract`, `forward_live_test`
   (gated on `HERMES_LIVE_FORWARD`). A probe fails closed when its real
   inputs are absent — it never substitutes a fixture.
3. **Docker gates** — `just docker-check[-prebuilt]`: standalone smoke,
   pinned Hermes lifecycle, warm/cold runtime transitions. Heavy image
   build+publish runs in CI; local path is `docker-pull` +
   `docker-check-prebuilt`.
4. **Channel conformance** — `internal/channel/conformance_test.go` runs
   every registered adapter against stubbed deps; durable-ID formats are
   frozen by goldens in `internal/routines`.
5. **Live acceptance** — real account + real provider call + recorded
   artifact, always a separate explicit gate; never inferred from any
   level above.

## Evidence rules

- A test passing means the code did it, not the provider did. httptest
  doubles, fixture Telegram updates, signed-Slack fixtures, fake brokers:
  code evidence only.
- Live claims need the artifact: message id, resource id, provider
  response, recorded in the CHG's `.work/done/.../evidence.md`.
- Docs/validation.md keeps the standing "not yet proven" list — consult it
  before asserting any capability is live.

## Mock chat and offline apply

`channel.NewMock` binds a mock chat atomically to one owner; attachments
land as bytes in the mock receipt and never reach a provider. It exercises
the real queue/delivery pipeline plus slash commands: `/v1/mock/message`
texts starting with `/` run the same dispatcher as Telegram (`/model`,
`/new`, `/delete`, `/usage`…) and reply into the durable `mock-sent`
outbox instead of enqueueing an agent job. `hubctl apply` validates
the whole input before writing (unknown fields, duplicate resources,
unsafe names, implicit routing rejected); routine mutations require the
explicit `--chat-id/--organization/--runtime/--policy/--user` quintuple.

## Local Docker acceptance

Use dedicated verification tags and temporary contexts. The managed
supervisor canary namespaces its runtime and control bridges; fixture
ToolHub/model DNS aliases must never enter a deployment network. Cold-start
budgets include the four-minute supervisor readiness allowance plus
execution; warm-path checks keep tighter bounds.

## E2E runner

`cmd/e2e` is the sequential capability runner (147 scenarios; `--scope
core` = the 116 local/agent/credential-free cases, `--scope all` includes
31 account-dependent ones the owner opted out). A scenario definition is
not evidence of a pass.

- `go run ./cmd/e2e mock --report <path>` — deterministic boundary suite.
- Server modes ship inside the communication-hub image:
  `docker exec <comm-hub> e2e telegram --principal <p> --select E2E-…
  --report /tmp/e2e.json` (real Bot API) and `e2e mock-server …` (real
  `/v1/mock/message` ingress + configured provider, inside the live stack).
- Runner rules: existing grants only (admin token cannot substitute for an
  agent grant), random fixture IDs, independent post-run state reads
  against the owner-authenticated API, self-cleanup of fixtures (retained
  on failure for diagnosis), a timeout never resends a possibly-delivered
  message, the suite stops after a failure. Scoped 15-minute rules for
  install fixtures expire and disable themselves; the runner never
  modifies governance.
- Dated scenario results live in
  `.work/done/CHG-0085-e2e-ci/e2e-acceptance.md` — a merged journal, not a
  fresh full-matrix run.

## Coverage

Own Go statement coverage ≥85% (`just check` enforces; upstream Hermes and
copied broker source excluded from the denominator). Windows zero-statement
profile rows are accepted by the coverage parser. When a gate is
unavailable locally (no Docker, no provider account) — report it
unavailable, never substitute a lighter check as equivalent.
