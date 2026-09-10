---
status: active
title: User self-env and Telegram boundaries
---

# Requirements

1. `telegram` is only the Bot API transport that delivers messages to Hermes.
2. `telegram_user` is only the personal Telegram-account MCP; it is independent of
   the bot transport.
3. An explicit current-user `KEY=value` request may update the user runtime env
   overlay and trigger a supervisor restart.
4. The overlay is stored in the user's runtime volume, loaded before Hermes starts,
   and never returned with secret values.
5. Only connector keys declared by the rendered runtime may be updated.
6. Runtime-control keys, organization-owned keys and host settings/secrets are not
   writable through the agent tool.
7. Untrusted connector content cannot authorize an env update.

# Out of scope

The feature does not provide public tenant authentication, Telegram webhook hosting,
host `.env` editing, organization-secret editing, or live provider credentials.
