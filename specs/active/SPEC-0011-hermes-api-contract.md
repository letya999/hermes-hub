---
status: active
title: Pinned Hermes API contract for persistent sessions and runs
---

# Contract

The runtime adapter targets the Hermes Agent artifact pinned in `docker/Dockerfile`:
commit `869228cab4a8276d3b4c78da9d9939670c47bd0f`, version `0.21.0`. The upstream
API server is started by `hermes gateway run --no-supervise --force` and is disabled
unless an explicit `API_SERVER_KEY` enables it. It is private, Bearer-authenticated,
and must not be exposed as a public tenant endpoint.

## Supported operations

- `POST /api/sessions` with `{"id":"...","title":"..."}` creates a durable empty
  session; `GET /api/sessions/{id}` and `/messages` resume and inspect it after a
  gateway restart.
- `POST /v1/runs` with `{"input":"...","session_id":"..."}` admits an asynchronous
  run. `Idempotency-Key` is durable for 24h:
  the same key and payload replay the same `run_id`, while a different payload is
  HTTP 409 `idempotency_key_conflict`.
- `GET /v1/runs/{id}` reports `queued`, `running`, `completed`, `failed`, `cancelled`
  or `interrupted`. `GET /events` is an SSE stream whose data includes `event`,
  `run_id` and `timestamp`; `POST /stop` with `{}` cancels and `POST /approval` with
  `{"choice":"once|session|always|deny"}` responds to an active tool approval.
- The gateway's cron scheduler remains a separate process concern; `/api/jobs` is
  readable while run traffic is active. The probe does not create a recurring job.

## Boundary and limitations

The API server creates a server-side `AIAgent`; `split_runtime=false` means tools run
on the API-server host. It is therefore not the replacement for the private Go
runtime boundary until an explicit split-runtime adapter exists. TUI JSON-RPC
(`session.create`/`session.resume`/`prompt.submit`) is a local stdio/WebSocket
transport, not the durable HTTP run contract, and `prompt.submit` has no durable
idempotency key. The pinned capabilities advertise no public CORS, audio/realtime
voice, memory-write API or admin config writes. Provider authentication and model
success are intentionally not claimed by this contract probe.

The pinned SSE transport is a process-local queue, not a replayable event log. Its
handler drops transport state when the subscriber disconnects and does not consume
`Last-Event-ID`. Recovery therefore queries the admitted durable run status instead
of assuming SSE replay. A gateway generation change can settle that same run as
`interrupted`; it must not silently submit a replacement run or repeat provider work.
Active approval status carries a `request_id`; responses must target that exact
request rather than resolving every waiter for a session.

## Verification and rollback

Run `just docker-check prod` (or `dev`) to build the pinned image, run the existing
Go runtime smoke, then run the real Hermes probe with `go run -tags integration
./cmd/devcheck hermes-contract hermes-hub:test`. The probe validates the commit and
version, sessions, replay/conflict semantics, SSE, cancellation, approval boundary,
cron coexistence and restart-to-`interrupted` recovery. A failed probe blocks the
Docker gate. Roll back by reverting the adapter/probe change and retaining the
previous image; no persisted session or idempotency data is migrated.
