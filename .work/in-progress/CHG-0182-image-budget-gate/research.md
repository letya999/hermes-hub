# M26 baseline evidence (audit 2026-10-27)

Measured inside `ghcr.io/letya999/hermes-hub/hermes-hub:edge-prod`
(4.6GB uncompressed / 1.22GB compressed) with `docker history` + `du`.

## Layer sizes (docker history)

| Layer | Size | Content |
|---|---|---|
| apt stack | 1.34 GB | git, ca-certificates, curl, ripgrep, ffmpeg, espeak-ng, chromium, xvfb, fluxbox, x11vnc, novnc, websockify |
| hermes checkout + venv | 1.40 GB | git fetch pinned commit + `uv sync --extra mcp` + `uv pip install aiohttp faster-whisper av` |
| node binary | 150 MB | `/usr/local/bin/node` + npm 19MB |
| Go binaries | 101 MB + 64 MB | hubctl/hub-runtime/toolhub/communication-hub/credential-broker/hub-media/e2e + cli-proxy-api |
| glab | 50 MB | apt (ADR-0006 GitLab path) |
| poppler+libgs | 55 MB | apt |
| uv binary | 45 MB | `/usr/local/bin/uv` |
| /opt/browser npm ci | 22 MB | @playwright/mcp |

## Inside the image (du -sh)

| Path | Size | Note |
|---|---|---|
| /usr/lib/x86_64-linux-gnu | 664M | libLLVM 124M, libnode.so 53M, libgallium 41M, ffmpeg codec libs ~120M, espeak-ng-data 25M |
| /opt/hermes/.venv | 531M | see below |
| /usr/lib/chromium | 381M | |
| /opt/hermes (tree) | ~270M | .git 70, tests 46, apps 38 (desktop 34), website 29, optional-skills 12, ui-tui 4.7, contributors 4.2, web 3 |
| /usr/share | 129M | novnc, fonts, icons, espeak |
| /usr/local/lib/python3.13 | 28M | stdlib |

## venv top packages (site-packages)

faster-whisper chain ≈330MB: ctranslate2+.libs 135M, av+.libs 103M,
onnxruntime 63M, numpy+.libs 57M, tokenizers 12M, hf_xet 11M,
huggingface_hub, protobuf, flatbuffers.
Core: nemo_relay 24M (native .so, used by agent/relay_runtime.py),
uvloop 16M, cryptography 15M, pillow+.libs 20M, anydoc 8M (read_file PDF).
Duplicated stacks: httpx (openai), httpx2 (mcp 2.0 + hf-hub), requests,
aiohttp (ours, api_server); pyyaml + ruamel; watchfiles via uvicorn[standard].

## Dead-path findings

- `docker/hub-stt`/`hub-tts` scripts: configured only together with
  HUB_STT_URL/HUB_TTS_URL (compose.go ~L541-549); the service URL wins at
  selection — whisper/espeak in the main image is a never-taken fallback.
- Hermes own STT: `stt.enabled=false`, `security.allow_lazy_installs=false`
  (internal/stack/hermes_capability.go, asserted by devcheck).
- Debian `novnc` hard-depends nodejs+libnode115 for a launcher we never call
  (websockify --web serves static files directly).
- `__pycache__` count in venv: 0 — hub-sidecar recompiles imports per op.
- Setuid binaries present: su, mount, umount, passwd, gpasswd, newgrp, chsh,
  chfn, dbus-daemon-launch-helper.
- /var/lib/apt/lists: 21MB left (glab + poppler layers skip cleanup).
- 477 Debian packages installed.

## Supply-chain drift (image vs upstream uv.lock)

numpy 2.5.4 vs 2.4.3 · av 18.1.0 vs 17.0.0 · onnxruntime 1.31.0 vs 1.27.0 ·
huggingface-hub/protobuf unpinned in image.

## Peer harness references

- goclaw: alpine + build-arg variants + browser sidecar (chromedp/lightpanda) + pkg-helper on-demand packages
- nanoclaw: per-session container ~800MB, source RO-mounted, setuid stripped, cli-tools.json manifest
- open-duet: ENGINES build-arg, desktop stack as separate image, UV_COMPILE_BYTECODE=1
- OpenDots: separate app/browser Dockerfile targets
- openclaw: opt-in extension builds, pruned dist, dist-upgrade layer
- Helmdeck ADR: browser sidecar, CDP on internal network only
- agent-sandbox "Hermes Agents as a Service": warm pool + suspend/resume (relates to #345)

## Local docker state

77 images / 55.9GB (30.9GB reclaimable); 29 `hermes-hub-dev-*` overlay tags;
volumes 17.8GB (96% reclaimable); build cache 18.6GB.
