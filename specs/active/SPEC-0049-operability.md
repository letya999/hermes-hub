# SPEC-0049: Operability — mock channel, declarative apply, build discipline

Frozen: 2026-10-11. Phase C of M10: every behavior added in phases A/B must
be exercisable and describable without external APIs, and image builds must
provably go through the approved BuildKit paths.

## Mock channel

1. Channel name `mock`. It is the local acceptance bench: inbound control
   calls travel the same pipeline as real channel messages (principal →
   task binding → durable job → run), and outbound deliveries travel the
   same durable outbox (enqueue → claim → complete) — they are captured to
   `spool/mock-sent/<delivery-id>.json` instead of calling any external
   API.
2. `POST /v1/mock/message {chat_id, text, client_id?}` requires control
   authorization like every `/v1/*` endpoint. `client_id` is the caller's
   idempotency handle: repeats never enqueue a second job.
3. `GET /v1/mock/outbox` returns the caller principal's captured
   deliveries only.
4. A mock chat binds durably to the first principal that uses it
   (`spool/mock-links/<chat>.json`), like a DM belongs to one account. A
   foreign principal gets 403 on inject and never sees its outbox.
5. `identity.MockEnvelope` issues `mock-<chat_id>` transport ids.
   Schedules, occurrences and routine authorization accept `channel=mock`;
   mock-channel routines carry the mock envelope so watchers, checks,
   digests, scripts and reminders deliver into the captured outbox
   end-to-end.
6. The mock channel never replaces Telegram/Slack verification; it proves
   pipeline correctness only (AGENTS: no integration claims from mocks).

## Declarative apply (`hubctl apply -f`)

1. `hubctl apply -f file.yaml --spool <dir> --user <id>` reconciles desired
   state on the local spool, the same direct-spool authority as
   `hubctl routine`.
2. v1 surface: `routines:` (id, kind, chat_id, timezone, expression,
   input, url, regex, expect) and `knowledge:` (name, source) files
   materialized under `spool/knowledge/<user>/`.
3. Idempotent: reapplying an unchanged spec reports `unchanged` and
   creates nothing. A schedule id that exists with a different spec is a
   loud conflict — apply never mutates a live schedule (baseline and
   history are preserved); delete it explicitly first. Durable bookkeeping
   (revision, next_due, enabled, catch-up window, timestamps) never counts
   as drift.
4. `kind=check` is refused: its baseline must be anchored by a live probe
   at creation, which only `POST /v1/routines` performs — an unseeded
   check would silently skip forever.
5. Apply adds and updates only. Deleting resources absent from the file
   is out of scope for v1.
6. The file is YAML (yaml.v3, already a dependency). It is suitable for
   backup, review and restore of routine/knowledge state.

## Image build discipline

1. `devcheck.BuildKitEnv` (`DOCKER_BUILDKIT=1`, `COMPOSE_DOCKER_CLI_BUILD=1`,
   `COMPOSE_BAKE=true`) is the single source every hub image build path
   carries: `devcheck.streamDocker` and `hubctl.dockerCLIEnv` both append
   it. Duplicated literals that can drift are not allowed.
2. The discipline is verifiable: tests pin the env set, the
   `build --target -t --load -f docker/Dockerfile .` arg shape, cache-flag
   forwarding, the 8 GB buildx cap, compose-referenced keep tags, and
   residue-only cleanup (no broad system or volume prunes).
