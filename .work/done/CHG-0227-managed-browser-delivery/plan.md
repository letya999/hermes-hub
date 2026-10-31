# CHG-0227: managed browser delivery end-to-end (live-repair chain)

Status: done 2026-10-29. Commit `39db841` on `fix/m23-live-wake`.
Follows M26 acceptance: live Telegram verification surfaced five independent
defects between the reviewed browser grant and a delivered screenshot; each
was root-caused, covered by a red→green test, and re-verified live.

## Defects fixed

1. **Managed render never projected browser MCP servers.** Managed config
   starts from a zero-capability template; the unmanaged `browser`/`browser_guest`
   mcp_servers render never ran. `BrowserGrant{Persistent,Guest,Act}` now rides
   settings → `MaterializeOptions` → `HUB_MANAGED_BROWSER` env → supervisor →
   in-container `materializeOptionsFromEnv` → `ValidateManagedEffectiveConfig`
   attestation (fail-closed on drift). `internal/stack/{hermes_capability,
   hermesconfig,compose,materialize}.go`, `internal/runtime/toolhub_config.go`,
   `internal/supervisor/env.go`.

2. **Egress proxy killed the MCP handshake.** Spawned runtimes send all HTTP
   through `egress-relay:8319`; `NO_PROXY` exempted toolhub/model-relay/egress
   but not `browser-<user>`, so the streamable-HTTP initialize was proxied and
   refused (`MCPError -32603`, server parked). `internal/stack/render_env.go`
   adds `browser-<user>` to NO_PROXY when the browser wish is present
   (`TestManagedRenderNoProxyCoversBrowserSidecar`).

3. **Stale-policy respawn blocked on a ghost container.** `removeOwnedRuntime`
   returned `ErrRuntimeMissing` when the stale container was already collected;
   the caller treated every error as fatal. `internal/supervisor/ensure.go`
   now ignores `ErrRuntimeMissing` only (`TestEnsureRespawnsAfterStalePolicy
   WhenContainerMissing`). Separately, `RenderEnvironment` provisioned
   `managed/<env>/{runtime,hermes,home,cache,workspace}` but the spawn mounts
   `skills` too — fresh managed spaces could not spawn at all
   (`TestManagedRenderProvisionsSkillsStoreDir`).

4. **Sidecar wrote artifacts into the wrong workspace.** playwright-mcp saves
   screenshots under `--output-dir /workspace/browser` inside the sidecar,
   which mounted `<space>/workspace` while managed runtimes bind
   `managed/<env>/workspace` — staged delivery could never see the file
   (outbox error `artifact fetch returned HTTP 400`). The sidecar now binds
   `managed/<env>/workspace` in managed mode
   (`internal/stack/compose.go`,
   `TestManagedBrowserSidecarMountsManagedWorkspace`).

5. **Upstream MCP media cache was outside delivery containment.** Hermes caches
   MCP image/audio blocks to `<hermes>/cache/{images,audio}` and emits absolute
   `MEDIA:` markers; `stageMediaArtifact` accepted only workspace paths.
   Resolved paths under the media cache are now a legitimate delivery source —
   staged copies still land inside `workspace/artifacts` with the same
   size/mime/symlink contract (`internal/runtime/artifacts.go`,
   `TestStageMediaArtifactStagesUpstreamMediaCache` incl. symlink-escape
   negative).

## Operational repair (store state, not code)

- ToolHub `artem_main-default` profile pinned `hub-agent-tools@5.3.2` while the
  active binding was `5.7.0`; the policy ceiling carried stale digests, so the
  whole ToolHub surface (not just browser) was denied. Republished policy rev12
  (same 77 rules, current 5.7.0 digests) and profile rev17 via the normal
  capability apply ceremony; projection verified live (46 tools).
- `hubctl up` dropped the `generated/dev-runtime-image` pin, so the spawned
  runtime ran the pre-fix base image; restored via `dev-rebuild`.

## Live evidence

- Managed effective config inside the runtime carries `browser`/`browser_guest`
  MCP servers at `http://browser-artem_main:893{1,2}/mcp`.
- Official MCP SDK handshake from inside the runtime container returns 31
  playwright tools (previously `-32603` through the egress relay).
- `browser_navigate` + `browser_take_screenshot` via MCP writes
  `page-*.png` that appears at `/workspace/browser/` inside the runtime.
- Both spaces re-rendered; both sidecars recreated onto the managed workspace
  mount; `internal/{stack,runtime,supervisor}` green.

Also in the commit: `hubctl doctor` effective-view skills report
(`internal/stack/skill_doctor.go`) — hidden/ineligible skills stay
operator-visible; `docs/operations.md` documents the managed browser
projection path.
