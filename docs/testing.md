---
description: Test pyramid and evidence rules — unit/race, contract probes, Docker gates, mock channels, live acceptance.
last_verified: 2026-10-26
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

M16 regression evidence is tracked in CHG-0151 through CHG-0158. E2E-085
now selects `agenttools.TestKnowledgeBindingRefreshRevokeAndIsolation`.
The old gateway `/v1/knowledge` 404 test proves endpoint removal only;
it is not knowledge isolation/revocation evidence. Runtime cache/admission
and real local filesystem tests complement the tool contract. None of
these fixtures prove a live Hermes, Honcho, connector or chat integration.

Own Go statement coverage ≥85% (`just check` enforces; upstream Hermes and
copied broker source excluded from the denominator). Windows zero-statement
profile rows are accepted by the coverage parser. When a gate is
unavailable locally (no Docker, no provider account) — report it
unavailable, never substitute a lighter check as equivalent.

## Hosting regression tests

`internal/hosting` covers owner binding, writer fencing, immutable snapshot isolation, quota/recovery, audit pressure, share revocation and HTTP gates. `internal/toolhub` checks hosting inspect/admission/relay and serialized fleet reservations; `internal/agenttools` checks the four site tools through the private API. `cmd/cellinit` tests bounded destination-fixed relay contracts. Native resource/browser/sish acceptance is separate; see [hosting](services/hosting.md).
