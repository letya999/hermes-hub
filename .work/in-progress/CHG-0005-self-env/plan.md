# CHG-0005: User self-environment updates

## Goal

Let an explicitly instructed Hermes user update connector credentials without
giving the agent access to host secrets or organization files.

## Scope

1. Persist a user-only environment overlay in the user's runtime state volume.
2. Allow only connector keys declared by the rendered runtime; block runtime and
   organization-owned keys.
3. Load the overlay before Hermes starts and request a supervisor restart after a
   successful update.
4. Keep Telegram bot transport and personal Telegram MCP independent.

## Non-goals

The agent does not edit host `secrets.*.env`, organization secrets, settings,
Docker Compose, or arbitrary files. The Telegram bot and personal-account MCP
remain separate features.
