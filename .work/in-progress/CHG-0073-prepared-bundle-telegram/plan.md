# Prepared bundle and personal Telegram (#39, #115)

1. Keep the generic exact-source `prepare_source` lifecycle and expose all 12 bundle IDs through bounded discovery. Entries without verified source/credential contracts report `blocked` with the missing evidence.
2. Prepare the selected `letya999/telegram-mcp` revision with an owner-scoped Broker contract, server-side read-only default, explicit write grants, restricted egress, and protected session import guidance. Keep the Communication Hub bot separate.
3. Test catalog validation, blocked installation denial, proxy mapping, Telegram authorization and owner isolation. Run `just check`, `just security` if dependencies change, and available Docker gates. Record real-provider evidence separately from fixtures.
