## Goal

Prepare the exact-source Google Calendar MCP at https://github.com/nspady/google-calendar-mcp through the generic repository-driven ToolHub lifecycle. This issue covers Calendar only. A full Google Workspace connector remains a separate catalog entry after its exact repository is selected.

## Acceptance criteria

- [ ] Reviewed catalog data records exact commit, MIT license, credential fields, egress, owner state, runbook and user handoff.
- [ ] Default branch resolves to an exact commit; generated Node recipe discovers `build/index.js` independently of the upstream Dockerfile.
- [ ] Prepared selection and direct URL, including registries disabled, use one Resolver/build/preflight/Broker/binding lifecycle with no Google-specific installer in Go.
- [ ] OAuth client JSON and token state use protected Broker delivery and never appear in chat, model arguments or logs.
- [ ] Persistent authorization state belongs to the owner; two users may share the artifact, never credentials, state or grants.
- [ ] Real `initialize`, `tools/list`, `list-calendars` and `list-events` pass using the intended existing account.
- [ ] Reinstall, rotation, reconnect, revoke and upgrade preserve isolation and do not create duplicate bindings or ambiguous owner connections.

Related: #115, #116, #110, #74. Evidence is tracked under CHG-0036. Mock tests are not live integration evidence.
