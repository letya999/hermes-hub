---
description: Every service runs the minimal image for its role; heavy capabilities (browser, local voice) become separate per-space images pulled only when enabled; Python installs are lockfile- or hash-pinned.
last_verified: 2026-10-28
---
# ADR-0083: Lean role images and capability packs — role images, browser sidecar, voice services only, pruned upstream tree, on-demand packs

Status: Proposed 2026-10-27. Implements GitHub milestone 26 (#371, #373,
#375, #377, #378, #380); milestone 27 (#383, #385, #387, #389, #390) extends D6.
Spec: [SPEC-0082](../../specs/active/SPEC-0082-image-role-budgets.md).
Evidence: `.work/in-progress/CHG-0182-image-budget-gate/research.md`.

## Context

Measured on `hermes-hub:edge-prod` (4.6GB uncompressed / 1.22GB compressed):

- One `common` compose stanza (`internal/stack/compose.go`) puts every service
  on the agent image: credential-broker, communication-hub, hub-media,
  model/toolhub/egress relays, cliproxy, supervisor — each carries Chromium,
  Node, npm, Python, uv, glab, git, curl.
- The Chromium/X11/noVNC stack (~700MB incl. mesa/libLLVM) runs inside the
  agent container only under `HUB_BROWSER`/`HUB_MEET`, shares `/state`, runs
  `--no-sandbox`, and exposes unauthenticated noVNC on container interfaces.
- ~330MB of faster-whisper/ctranslate2/av/onnxruntime/numpy plus espeak/ffmpeg
  serve a command fallback that is never selected: `transcription` enabled ->
  service URL wins; disabled -> command is never configured.
- `/opt/hermes` holds ~200MB unused: `.git` 70MB, `tests` 46, `apps` 38,
  `website` 29, `optional-skills` 12 (kept), `ui-tui`/`web`/`evals`/`nix`/`docs`.
- `uv pip install` after `uv sync --frozen` bypasses the lockfile and hashes;
  transitives drifted (numpy 2.5.4 vs locked 2.4.3; av 18 vs 17;
  onnxruntime 1.31 vs 1.27). `Dockerfile.stt`/`.tts` install the same way.
- Debian `novnc` hard-depends on nodejs+libnode115 (~60MB) for a launcher we
  never invoke; `e2e` (12MB) ships in prod; apt lists not cleaned in two
  layers; setuid bits remain.
- `PYTHONDONTWRITEBYTECODE=1` + read-only rootfs means `hub-sidecar`
  recompiles imports on every per-op spawn (zero `__pycache__` dirs in venv).

Feasibility verified before design: upstream `browser_cdp` resolves
`BROWSER_CDP_URL`/`browser.cdp_url`; playwright-mcp already connects via
`--cdp-endpoint`; upstream `google_meet` ships a v3 "remote node host" mode
built for Chrome on a different machine; `hermes` own STT is disabled and
devcheck-asserted. `glab` stays: ADR-0006 sanctioned it as the GitLab
integration path (protected config in runtime.go, devcheck contract).

Surveyed harnesses converge on the same shape: goclaw (alpine + build-arg
variants + remote browser sidecar + on-demand packages), nanoclaw (per-session
containers, source RO-mounted, hardened ~800MB image), open-duet (engines via
build-arg only, desktop stack as a separate image), OpenDots (separate
`app`/`browser` targets), openclaw (opt-in extension builds + pruned dist).

## Decisions

- **D1 — Role images.** `hub-core` (debian-slim + all Go binaries +
  ca-certificates) serves credential-broker, communication-hub, hub-media,
  relays, cliproxy, supervisor, toolhub. `hub-control` = hub-core + docker CLI
  + thv + cellinit for the workload controller. `hub-runtime` = python-slim +
  hub-runtime + hub-sidecar + Hermes venv + node + agent shell tools + glab.
  `hub-browser` = chromium + Xvfb + noVNC + playwright-mcp, one container per
  space when enabled. `hub-stt`, `hub-tts`, `cli-tools`, `cell-proxy` unchanged.
- **D2 — Core contains no interpreters or browsers.** No python, node, uv,
  glab, chromium, or setuid binaries in core/control images; enforced by
  `devcheck image-budget` in CI, fail-closed.
- **D3 — Browser is a per-space sidecar.** `hub-browser` container on the
  space-internal network only; CDP on an internal alias, never published;
  runtime renders `BROWSER_CDP_URL`. Profile volume moves to the browser
  container so cookies/history/logins persist identically. Headed-under-Xvfb
  and launch flags unchanged (anti-bot parity). noVNC requires a password and
  stays on host loopback.
- **D4 — Voice only through services.** communication-hub speaks
  `HUB_STT_URL`/`HUB_TTS_URL` exclusively; disabled `transcription` reports a
  typed "voice unavailable" status. No whisper/espeak/ffmpeg-for-voice in any
  hub image.
- **D5 — Hermes footprint is pruned and byte-compiled.** The venv builds in a
  dedicated stage with `UV_COMPILE_BYTECODE=1`; runtime receives `.venv` plus
  only the editable-mapping modules, `plugins`, `skills`, `optional-skills`,
  `compat_manifest.json` and support files. `.git` is removed; the pinned
  commit SHA is written to a file devcheck reads. Python package installs are
  lockfile-synced or `--require-hashes` everywhere.
- **D6 — Capabilities are declared and pulled on demand.** A space's
  `capabilities` setting resolves each entry to a digest-pinned ToolHub
  artifact/image; `hubctl prefetch` pulls only enabled packs. (M27.)
- **D7 — Node stays in runtime for M26.** It still serves `mcp-raw` stdio
  servers; removal is evaluated in M27.2 after cells cover raw MCP.

## Alternatives considered

- **Distroless core** — smaller CVE surface but no shell for healthcheck/debug;
  deferred: revisit after the split lands, base choice stays in one Dockerfile
  line.
- **Token-authenticated CDP proxy** — rejected: adds a component; per-space
  network isolation already scopes reachability. Revisit if browser is ever
  shared across spaces.
- **Upstream `tools/lazy_deps.py` as the fetch channel** — rejected: name-only
  PyPI installs without hashes; `security.allow_lazy_installs` stays false.
- **eStargz/SOCI lazy pulls** — rejected (same as M21.5): helps pull time, not
  runtime, and Docker Desktop support is partial.
- **Keeping the single fat image** — rejected: attack surface and CVE noise,
  measured above.

## Invariants kept

- Hermes remains an unpatched pinned upstream dependency.
- Read-only rootfs, cap_drop ALL, no-new-privileges, per-space networks,
  broker-held credentials — unchanged.
- `optional-skills`, `plugins`, `skills` keep their upstream semantics;
  provider mutations still require concrete owner instructions.

## Amendment — browser sidecar lifecycle (2026-10-28, CHG-0190)

Accepted: the sidecar extracted by D4 became a resident compose service with
`restart: unless-stopped` and no resource ceiling — a regression against the
in-runtime stack it replaced, which died for free with the runtime's warm-TTL
scale-to-zero. Two decisions close that gap:

- **D8 — Sidecars sleep with their runtime.** The supervisor treats the
  per-space browser sidecar as a warm resource of that space's runtime, not
  project infrastructure. `Ensure` runs `docker start
  hermes-hub-<user>-<env>-browser-<user>-1` for browser/meet spaces before the
  spawn proceeds (idempotent: running is a no-op, stopped is revived, absent
  fails the spawn with the same "run hubctl up" guidance as a missing space
  network). The sweep stops a sidecar whose space has no live runtime —
  supervisor-spawned entry or a compose `hermes-runtime` container — after a
  grace (`--browser-idle-ttl`, default 15m, in-memory deadline). `docker stop`
  is graceful SIGTERM so Chromium flushes its profile; `unless-stopped`
  honours the manual stop; `hubctl up` or the next spawn re-raises it. This is
  the open-duet `idle_minutes`/`auto_start` and hermes-fleet `idle_timeout`
  pattern applied to a compose-owned container, and it restores the pre-split
  posture where the browser stack cost nothing while the agent slept.
- **D9 — The sidecar carries an explicit ceiling.** Chromium under Xvfb plus
  two playwright-mcp processes plus the meet NodeServer is the heaviest
  per-space workload we run: `cpus: "2.0"`, `mem_limit: "3g"` (the 1 GB shm
  counts against it), `pids_limit: 256` — matching the toolhub precedent of
  bounded per-service budgets.

### Alternatives considered for the amendment

- **Idle watcher inside `hub-runtime browser`** — rejected: it cannot observe
  MCP/CDP client activity (pw-mcp holds a permanent CDP socket), so "idle"
  would mean wall-clock since boot and would kill active sessions.
- **Supervisor-owned sidecar spawn (drop compose ownership)** — rejected for
  now: `docker run` would duplicate the compose mounts/env/volume contract in
  a second place. Compose keeps owning the sidecar's shape; the supervisor
  only toggles its run state under label/name verification. Revisit if
  capability packs (D6) make sidecars fully dynamic.
- **Stopping the sidecar the moment the last lease ends** — rejected: cold
  Xvfb+Chromium boot is seconds; a short grace absorbs conversation gaps, and
  matching it to the runtime warm model keeps semantics uniform.
