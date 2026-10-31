# CHG-0186: hub-browser sidecar (M26 stage 4)

Chain: `docs/adr/ADR-0083-lean-images-capability-packs.md` D3 ->
`specs/active/SPEC-0082-image-role-budgets.md` R4 -> this plan -> `state.yaml`.
GitHub issue: #378. Previous stage: `CHG-0185`. Next stage: `CHG-0187`.
Cross-link: #345 (lazy chromium boot), #342 (RuntimeProvider seam).

## Goal

The Chromium/X11/noVNC stack (~700MB) leaves the runtime into a per-space
`hub-browser` container. CDP lives on the space-internal network alias;
noVNC requires a password; profile/cookies/history persist on the browser
volume; anti-bot posture unchanged (headed under Xvfb, same flags, real
profile).

## Read before coding

1. `internal/runtime/runtime.go` (browser start ~L359-386, waitForBrowser,
   clearBrowserLocks, profile locks), `internal/stack/browser.go`
   (playwright-mcp args, `--cdp-endpoint`, tool include/exclude),
   `internal/stack/compose.go` (VNC publish, shm_size),
   `docker/browser/package.json`, upstream `tools/browser_cdp_tool.py`
   (`BROWSER_CDP_URL`), `plugins/google_meet` (remote node host v3).
2. AGENTS.md ops notes (dead CDP ports on Windows in 0.5.x).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib only unless an ADR allows a dependency.
- `just check` before handoff; Docker gates.

## Slices

### S4.1 hub-browser image (R4.1)

Anchors: new `docker/Dockerfile.browser` (or target); x11vnc/novnc vendored.

Change:
1. Image: chromium + Xvfb + fluxbox + x11vnc (password file) + noVNC static +
   playwright-mcp; Go entrypoint `browser-agent` or reuse hub-runtime flags.
2. `/state/browser` mounted as the profile volume into the sidecar.

Tests (red first):
- `TestBrowserImageContents`: chromium/Xvfb/novnc present, python absent.

### S4.2 Runtime + compose wiring (R4.2, R4.3)

Anchors: `internal/runtime/runtime.go` browser branch; `internal/stack/compose.go`.

Change:
1. When `HUB_BROWSER`/`HUB_MEET`: compose renders a `browser-<user>` service
   on the space network; runtime renders `BROWSER_CDP_URL=http://browser-<user>:9222`.
2. Remove chromium/X11/VNC apt packages and `/opt/browser` from the runtime
   image; playwright-mcp spawned inside the sidecar, keep tool include/exclude.
3. CDP bound to the internal alias only; noVNC password from broker or
   per-space secret file, published to 127.0.0.1.

Tests (red first):
- `TestComposeBrowserServiceRendered`: service exists only when enabled.
- `TestCdpNotPublished`: rendered compose publishes no 9222 host port.

### S4.3 Spike: upstream browser paths (R4.4)

Change:
1. Verify `browser_supervisor` attaches to remote CDP or degrade to
   playwright-mcp-only surface with documented diff.
2. google_meet remote-node path vs local playwright-in-plugin; choose and
   document (unverified if creds absent).
3. `--isolated` guest mode inside sidecar.

## Red-team checklist

- CDP unreachable from other spaces/cells (network test).
- Browser container mounts only its profile volume — never `/state` or
  broker secrets.
- Profile lock handling survives sidecar restart.

## Acceptance

- A4: navigate + screenshot + snapshot via playwright-mcp; `browser_cdp` via
  `BROWSER_CDP_URL`; cookies survive runtime restart; noVNC 401 unauth.

## Out of scope

- Stealth/fingerprint hardening; headless-shell; Meet live acceptance without creds.

## Done checklist

- [ ] Tests red then green; `just check`; Docker gates.
- [ ] Red-team and live acceptance recorded in state.yaml.
- [ ] Issue #378 closed; folder moved to `.work/done/`.
