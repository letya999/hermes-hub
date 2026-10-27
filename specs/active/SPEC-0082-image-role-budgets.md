# SPEC-0082: Image role budgets and capability extraction (M26)

Frozen: 2026-10-27. Implements GitHub milestone 26 issues #371, #373, #375,
#377, #378, #380. Decision: [ADR-0083](../../docs/adr/ADR-0083-lean-images-capability-packs.md).

## Size SLOs (uncompressed, measured on built images)

| Image | Budget | Baseline |
|---|---|---|
| `hub-core` | <= 250 MB | — (part of 4.6 GB shared image) |
| `hub-control` | <= 350 MB | — (4.84 GB control) |
| `hub-runtime` | <= 1.3 GB (<= 450 MB compressed) | 4.6 GB / 1.22 GB |
| `hub-browser` | <= 1.0 GB | — |
| `hub-stt`, `hub-tts`, `cli-tools`, `cell-proxy` | unchanged | stt/tts dedicated; cli-tools 370MB; cell-proxy 277MB |

## Requirements

### Stage 0 — baseline and budget gate (CHG-0182, #371)

- R0.1: `research.md` records the audit baseline: layer sizes, per-directory
  sizes, forbidden-binary inventory, per-image Trivy counts.
- R0.2: `devcheck image-budget` fails when a role image exceeds its SLO or a
  forbidden binary is present in core/control (`chromium`, `node`, `python`,
  `uv`, `glab`, any setuid file).
- R0.3: The gate runs in the CI `image` job; missing image = fail.

### Stage 1 — hygiene (CHG-0183, #373)

- R1.1: The Hermes venv builds in a dedicated stage with
  `UV_COMPILE_BYTECODE=1`; the runtime image receives `.venv` and only the
  upstream files in the import allowlist.
- R1.2: `/opt/hermes/.git` is absent; the pinned commit SHA lives in a file
  devcheck reads for revision verification.
- R1.3: `optional-skills`, `plugins`, `skills`, `compat_manifest.json` and all
  editable-mapping modules are present and importable.
- R1.4: noVNC ships as static files without the Debian `novnc` package;
  `nodejs`/`libnode` are absent from every hub image.
- R1.5: `e2e` exists only in the dev target; every apt layer removes
  `/var/lib/apt/lists`; no setuid binaries remain; `shm_size` applies only to
  the runtime service.
- R1.6: Runtime image loses >= 400 MB vs baseline.

### Stage 2 — voice consolidation (CHG-0184, #375)

- R2.1: No hub image contains faster-whisper, ctranslate2, av, onnxruntime,
  espeak, or the `docker/hub-stt`/`docker/hub-tts` scripts.
- R2.2: communication-hub uses only `HUB_STT_URL`/`HUB_TTS_URL`; with
  `transcription` disabled it reports a typed unavailable status.
- R2.3: `Dockerfile.stt`/`Dockerfile.tts` install via hash-pinned
  requirements (`--require-hashes` or `--generate-hashes` compiled file).

### Stage 3 — hub-core split (CHG-0185, #377)

- R3.1: `hub-core` contains the Go binaries + ca-certificates and nothing
  else; `hub-control` adds only docker CLI + thv + cellinit.
- R3.2: Generated compose assigns each service its role image; no service
  outside runtime/browser references the runtime image.
- R3.3: `cli-proxy-api` exists only in core; every service keeps a working
  healthcheck.

### Stage 4 — browser sidecar (CHG-0186, #378)

- R4.1: `hub-browser` runs chromium headed under Xvfb with the same launch
  flags as today; profile lives on a per-space volume in the browser
  container.
- R4.2: CDP is reachable only on the space-internal network; noVNC requires
  authentication and publishes to host loopback only.
- R4.3: Runtime contains no chromium/X11/VNC binaries; `BROWSER_CDP_URL`
  points at the sidecar; the browser tool surface is unchanged.
- R4.4: Guest `--isolated` mode works inside the browser container without
  access to the owner profile.

### Stage 5 — acceptance (CHG-0187, #380)

- R5.1: Live evidence for voice, browser, noVNC auth, guest isolation and
  forbidden-binary gates on the dev stack.
- R5.2: Before/after sizes and CVE counts recorded in `state.yaml`.

## Acceptance

- A0: `devcheck image-budget` green in CI for every role image.
- A1: Runtime image >= 1.2 GB smaller than baseline uncompressed.
- A2: Voice note transcribed via hub-stt service; TTS reply delivered.
- A3: `docker run hub-core which chromium node python` finds nothing.
- A4: Browser navigate + screenshot on dev stack; noVNC 401 unauthenticated;
  CDP unreachable from a foreign space/cell.
- A5: `hubctl up` with previous tags restores the same service set
  (rollback); coverage >= 85%; `just check` green.
