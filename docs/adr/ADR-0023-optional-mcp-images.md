---
description: Optional Google/Atlassian/Telegram Python MCP trees are dedicated images, not the hub runtime.
status: accepted
---
# ADR-0023: Optional MCP trees live outside the hub image

## Decision

The shared `hermes-hub` image contains Hermes (`mcp` extra), apt Chromium for
browser CDP, and hub binaries. It does not install `/opt/google`,
`/opt/mcp-atlassian` or `/opt/telegram`.

Google Workspace reads go through the official remote MCP (ADR-0019). Personal
Telegram account access uses `docker/telegram-account.Dockerfile` as a ToolHub
artifact. Atlassian uses `docker/mcp-atlassian.Dockerfile` with the same
upstream pin as ADR-0010, built only when that connector is needed.

Feature flags `google`, `atlassian` and `telegram_user` still describe the
legacy in-process stdio commands. Those paths are absent from the default
image; enabling them without restoring the trees or using the dedicated
images does not start those servers.

## Rationale

Those three virtualenvs were copied into every compose service even when local
settings only enabled workspace, browser, hh, telegram bot and slack. Each
rebuild then paid for unused Python trees on disk.

## Consequences

`hubctl telegram-login` builds and runs the standalone Telegram account image.
Meet/STT extras remain out of the default image (restore Dockerfile layers).
This narrows ADR-0010: the Atlassian server stays the pinned upstream stdio
MCP, and it is no longer baked into the hub runtime.
