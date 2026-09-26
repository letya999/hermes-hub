---
id: CHG-0041-browser-profiles
issue: 45
---
# Harden isolated user browser profiles (issue #45)

## Decision

The browser is a hub-owned local capability, not an upstream service connector.
Playwright MCP runs inside the owning user's runtime against that user's own
Chromium, so profile/cookie/download isolation comes from the per-space mounts
rather than ToolHub admission (ToolHub container workloads sit on dedicated
internal networks and cannot reach the runtime's loopback CDP anyway).
Commit c8d0bd5 removed the rendered `browser` server with the other upstream
connectors; this change restores it in hardened form.

## Rendered capability (feature `browser`)

- `browser`: `node @playwright/mcp --cdp-endpoint http://127.0.0.1:9222`
  attaches to the persistent per-user Chromium (profile
  `spaces/<u>/connections/browser` -> `/state/browser`).
- `browser_guest`: `--isolated --executable-path /usr/bin/chromium` launches a
  second Chromium with an in-memory profile; anonymous browsing gets no
  authenticated state or credentials.
- Both get `--output-dir /workspace/browser`, `--output-max-size 256MiB` and
  `--block-service-workers`; default caps `vision,pdf` as before.
- `tools.include` allowlist: navigation/read tools only. `browser_act`
  (new feature, host-managed like `browser`) extends the allowlist with
  mutation tools (click/type/forms/dialogs/evaluate/coordinate mouse).
- `browser_guest` joins the reserved MCP names; users cannot claim the name.

## Runtime enforcement

- Runtime sweep (60 s) over `/workspace/browser`: removes symlinks,
  non-regular files, files over 64 MiB and non-allowlisted extensions;
  evicts oldest when the directory exceeds 256 MiB. Post-write eviction, not
  a pre-write gate (documented ceiling).
- noVNC unchanged: published on host loopback `browser_port` only; VNC itself
  stays container-loopback with no password.
- Feature off -> no MCP servers, `HUB_BROWSER=false`, no Chromium; stale
  calls fail because the server/tool no longer exists after restart.

## Issue mapping

| Criterion | Mechanism |
|---|---|
| Per-user profiles/cookies/downloads/screenshots/sessions | per-space mounts and per-user runtime container |
| Anonymous browsing gets no authenticated profile | `browser_guest` `--isolated`, own chromium, empty env |
| Downloads only in invoking user's workspace + size/type limits | `--output-dir /workspace/browser` + runtime sweep |
| Authenticated mutations need capability + intent | `browser_act` feature + SOUL instruction |
| Restart preserves only selected profile | persistent mount is per-user; guest is in-memory |
| Disable/revoke removes projection; stale calls fail | feature removal drops the rendered servers |
| No Hermes rebuild | feature flag + materialized config only |

## Verification

- `internal/stack`: render assertions for both servers, read-only default,
  `browser_act` extension, reserved `browser_guest` name.
- `internal/runtime`: sweep policy (type/size/symlink removal, oldest-first
  eviction, missing dir tolerance).
- `just check` gates; live stack verification recorded in state.yaml.
