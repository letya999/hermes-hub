# CHG-0072 — managed-mode hotfixes from live Telegram verification

Five production defects found exercising the managed stack in Telegram
(2026-10-06), plus one artifact-delivery duplicate.

## Root causes (verified against live containers/logs)

1. **Dynamic tools invisible in current session.** ToolHub pushes
   `tools/list_changed` through `Gateway.RefreshProjection`, but the MCP
   notification only reaches a Hermes client while its GET stream is open —
   production had none when the github binding activated. The intended
   deferred-restart marker (`toolhub-reconnect.request`/`restart.request`) is a
   file contract that cannot cross the toolhub→runtime container boundary, so
   nothing consumed it.
2. **`/connections` "реестр недоступен".** `openCredentialSurface` returns nil
   in broker-approve mode, so `g.secrets` is nil; the ToolHub section is wired
   to the secrets object even though it only needs the read-only store. In the
   managed deployment the store lives in the toolhub container — comm-hub never
   sees the file.
3. **`/usage` HTTP 404.** Supervisor `/v1/usage` is lookup-only: it refuses
   when the runtime entry is not Ready/Busy/Idle (runtime was mid-restart /
   stopped at 08:50). Route works when runtime is up (verified 200). User-facing
   error leaks a raw HTTP status instead of the real state.
4. **TTS never fires.** `serviceSynthesizer` is configured, but the model never
   emits the `VOICE:` marker: the convention is only documented inside
   `hermes-hub-tools` server instructions, which the managed config does not
   attach. `job.Instructions` (→ ephemeral_system_prompt) carries only the
   per-task style.
5. **Slow second user.** Three compounding causes:
   - STT `command` engine execs `stt-worker` per call → faster-whisper `small`
     reloads on every voice message (~70s observed for a 4s clip vs ~25s E2E).
   - A `uncertain` supervisor job (`verify-set-1`) bound to dead generation
     gen-183 can never settle: `control` rejects the durable cancel with 409
     "run binding mismatch" forever → lease never sweeps → runtime pinned
     busy/desired.
   - Host disk at 95.4% makes Hermes report `readiness: degraded`, which the
     runtime probe maps to `hermes_readiness: unavailable` (misleading).
6. **`MEDIA: unicorn.jpg` artifact error.** The model referenced the file by
   bare name; `stageMediaArtifact` only resolves workspace-relative paths, so a
   marker for a file already under `artifacts/images/` produced a phantom
   `media file unavailable` ref alongside the real delivered photo.

## Plan

1. **Restart-request bridge.** Runtime gains `POST /v1/restart-request`
   (auth, writes `restart.request` marker — marker only, no signal).
   Supervisor gains `POST /v1/restart-request` (`{principal_id, context_id,
   runtime_id}`; locates the entry, forwards with the stored runtime auth;
   when the entry is Idle/Ready it also posts `/v1/restart` for an immediate
   controlled respawn; Busy defers to the existing post-delivery restart poll;
   Stopped/absent is a no-op since the next spawn is fresh anyway). ToolHub
   gains a per-projection notifier: the existing 500 ms refresh loop diffs
   per-principal projection revisions (first observation = baseline) and POSTs
   to `HUB_RUNTIME_SUPERVISOR_URL/v1/restart-request` with
   `HUB_COMMUNICATION_AUTH` when both are set. Retried on failure by the loop.
2. **Connector listing endpoint.** ToolHub mux gains `GET /v1/connectors`:
   bearer→envelope auth (same as /mcp), plus the control-plane bearer
   (`HUB_COMMUNICATION_AUTH` = supervisor token) selecting an enrolled
   principal via `X-Hub-Principal` — header alone never authenticates.
   comm-hub `/connections` falls back to this HTTP listing when the local
   registry is absent (broker-approve mode): the caller's sibling token from
   `HUB_COMMUNICATION_TOKENS_FILE` pins the principal; in supervised mode the
   supervisor bearer + `X-Hub-Principal` is the fallback (unsupervised never
   pairs the owner token with a foreign header). `HUB_TOOLHUB_URL` wires the
   address (`toolhub` unsupervised, `toolhub-control` managed alias).
3. **`/usage` clarity.** comm-hub maps supervisor 404 → explicit "runtime is
   stopped" message. Keep lookup-only semantics (no hidden wake).
4. **Voice convention instruction.** When the gateway has a synthesizer, the
   run instructions carry a presentation-only line describing `VOICE:` usage
   (only when the user asks for voice). Field on HTTPRunner populated by
   comm-hub config.
5. **Stale uncertain jobs.** `ExpireUncertain` marks a record `interrupted`
   when its recorded generation no longer matches the runtime entry (or the
   entry is gone): the old run is provably unobservable, so the lease can
   sweep. Runtime health: report `degraded` as `degraded`, reserve
   `unavailable` for probe failures.
6. **MEDIA path fallback.** `stageMediaArtifact` additionally tries
   `artifacts/{documents,images,videos}/<base>` for unresolved relative names;
   `mergeArtifacts` then dedupes the scan-hit on the same path.
7. **STT latency.** `stt-worker --serve` resident mode: model loads once,
   wav paths on stdin → JSON-lines on stdout, `{"ready":true}` handshake.
   New `command-serve` engine serializes requests through one goroutine,
   respawns the worker on stream break or cancellation. Render switches the
   hub-stt service to it; TTS stays on `command`.

Tests: HTTP contracts for both new endpoints (auth, wrong principal,
stopped/busy/idle transitions), notifier baseline+retry, /connections via
HTTP listing with sibling tokens, ExpireUncertain generation-mismatch
settlement, MEDIA basename fallback + dedupe, instructions contain VOICE
convention only when TTS configured.
