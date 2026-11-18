---
description: Separate Telegram transports and persist constrained user self-env updates.
last_verified: "2026-11-15"
---
# ADR-0005: Separate Telegram paths and user self-env

Status: accepted.

**Scope note (2026-11-15 audit):** `env_update` still exists but secret intake is fenced — credentials go only through the Broker/loopback form per [ADR-0021](ADR-0021-toolhub-control-plane.md) and [ADR-0049](ADR-0049-broker-only-credential-intake.md).

Accepted 2026-09-10.

## Decision

Keep the Telegram Bot API gateway (`telegram`) separate from the personal-account
MCP (`telegram_user`). Add a hub-owned `env_update` tool that writes a constrained
user-only overlay in the runtime state volume and signals the supervisor to restart.

The overlay is not a host `.env` editor. Its allowlist is rendered from enabled
connector requirements and custom MCP variable references. Runtime-control keys and
organization secret keys are denied. The tool reports only key names and restart
status.

## Consequences

The first model and bot credentials still need local provisioning. A changed overlay
persists in the user's runtime volume until replaced or the volume is deliberately
removed. Telegram chat history may retain a credential message, so the bot is not a
secret-erasing channel.
