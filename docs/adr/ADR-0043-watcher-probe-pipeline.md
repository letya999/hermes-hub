---
description: Watcher probe = fetcher plus deterministic extract pipeline; isolated script/command probes bounded to the owner watch directory; recipes snapshotted at create.
last_verified: 2026-10-18
---
# ADR-0043: Watcher probe pipeline — fetch/extract separation, isolated probe scripts, snapshotted recipes

Status: Accepted 2026-10-18. Implements the probe half of GitHub issue #248.
Spec: [SPEC-0057](../../specs/active/SPEC-0057-watcher-probe-pipeline.md).

## Context

Watchers previously fused fetch and value extraction in `probeValue`: one
regex capture or the raw body was the compared value. Real sources need
more: structured pages (DOM selection), feeds, JSON APIs, noisy pages with
volatile fragments, site-specific parsers, and project-internal state that
is produced by running a small program rather than fetching a URL.

Analogues converge on one shape: urlwatch runs `url`/`navigate`/`command`
jobs through ordered filter chains; changedetection.io splits fetchers from
processors and treats browser rendering as a separate opt-in service;
RSSHub versions per-site parse recipes as data; Huginn separates document
fetch from named extraction. Every one of them keeps rendering as the
expensive last resort and keeps the compared artifact canonical.

## Decision

- **D1**: `probe = fetcher + extract[]`. Fetch produces bytes; the extract
  chain reduces bytes to the canonical compared value. Existing durable
  machinery (baseline, dedup, `.probe-result` receipts, quiet hours, daily
  limits, `notify_mode`) is untouched.
- **D2**: Extraction is deterministic and in-process: `css` (Cascadia),
  `html2text`, `regex`, `grep`, `drop_regex`, `drop_lines`, `sort_lines`,
  `strip`, `json_path`, `rss`. A failing or empty pipeline is a probe error,
  never a silent "no change".
- **D3**: Probe scripts never run in the hub control plane. `kind=script`
  pipes the (optionally pre-extracted) body to the existing disposable
  container as `input()`; `kind=command` mounts only the owner's
  `spaces/<user>/watch` directory read-only and exposes bounded file
  builtins. There is no arbitrary shell execution in probes: anything beyond
  "read state, emit one value" belongs to agent jobs or `mcp_tool`
  connectors.
- **D4**: Recipes are data, snapshotted at create. `probe.recipe` expands to
  concrete fields stored on the schedule with `recipe_version`; editing the
  catalog cannot drift running schedules.
- **D5**: Browser rendering is deliberately absent from this change: an
  opt-in external service behind explicit configuration, never in the base
  image. LLM stays an opt-in filter on deterministic diffs, not a detector.

## Consequences

- New direct dependency `github.com/andybalholm/cascadia` for CSS selectors
  (stdlib has none); `x/net/html` moves from indirect to direct.
- Script probes cost one disposable container spawn per tick; owners should
  prefer the in-process extract chain for plain parsing and keep script
  cadence modest.
- The `watch/` directory is the explicit, owner-controlled boundary for
  file-state probes; nothing else in the space is visible to probe scripts.

## Amendment 2026-10-18 (CHG-0114): generalized probe sources and delivery modes

Scope of issue #248 widened from URL polling to "any source the owner can
read": internal tools, memory/knowledge stores, web search, files,
browser-rendered pages and external parser services such as job_ftch.

- **D6**: `kind=tool` normalizes to `mcp_tool`. The ToolHub `/v1/tool-call`
  path already enforces read-effect tools under owner grants, so the whole
  projected read catalog (file_read, knowledge_search, web_search, MCP
  connectors) becomes probe sources without new plumbing. Probes may never
  exceed what the owner's agent could call.
- **D7**: Sandbox stays networkless; internet access is hub-mediated.
  `probe.needs[]` (≤4 read-tool calls) are prefetched under owner grants
  before the container spawns and reach Starlark via `needs()` in declared
  order (each ≤64 KiB). A failed need aborts the probe as an error. This
  covers "script that needs web search / a fetched page" without opening
  sandbox networking; a dedicated egress-proxy executor (`kind=exec`) stays
  deliberately deferred.
- **D8**: Delivery is a separate axis: `deliver_mode` = `notify` (default),
  `content` (report the value every tick), `artifact` (stage the value as a
  durable outbox blob and deliver it as a file, `artifact_name`-named).
  `schedule.delivery` was already taken by event subscriptions, hence the
  distinct field name.
- **D9**: Heavy parsers stay outside the hub. `kind=render` (browser steps:
  click/fill/scroll/wait/wait_for/evaluate + wait_for condition) and
  `kind=service` (URL-less internal APIs like job_ftch) both POST a
  bounded structured request to an **operator-registered** endpoint from
  `service_endpoints` config — never a URL supplied by the schedule. This
  keeps internal addressing an explicit operator decision and makes D5's
  "opt-in external service" concrete without shipping a renderer.
- **D10**: Baseline seeding uses the same source path as ticks: check-kind
  creation routes render/service probes through `serviceProbe` so the
  baseline reflects what the watcher will actually compare.
