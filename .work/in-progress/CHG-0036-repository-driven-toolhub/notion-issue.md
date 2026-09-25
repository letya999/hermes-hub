## Goal

Prepare https://github.com/makenotion/notion-mcp-server as an exact-source catalog entry using the generic repository-driven ToolHub lifecycle. No Notion-specific installation path in Go.

## Acceptance criteria

- [ ] Default branch resolves to an exact commit; reviewed data records license, credential contract, effects, egress, runbook and user handoff.
- [ ] A generated Node recipe discovers `bin/cli.mjs`; upstream Dockerfile is untrusted metadata only.
- [ ] Direct URL works with all registries disabled and uses the same lifecycle as prepared discovery.
- [ ] `NOTION_TOKEN` is supplied through Broker using the existing owner connection; no token appears in chat or logs.
- [ ] Real isolated `initialize` and `tools/list`, followed by a safe read of shared root/pages, succeed.
- [ ] Reinstallation creates no second binding; rotation reuses or explicitly replaces the same owner connection.
- [ ] Immutable artifact reuse never shares credentials, grants, writable state or projection across owners.
- [ ] Hermes reconnect exposes the enabled tools without losing the existing session.

Related: #115, #116, #110, #74. Evidence is tracked under CHG-0036. Mock tests are not live integration evidence.
