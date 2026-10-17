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
