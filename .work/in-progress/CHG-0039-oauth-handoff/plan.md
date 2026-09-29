# OAuth handoff for prepared MCP connections

1. Keep the existing reviewed credential contract and add an explicit OAuth handoff description to prepared catalog entries that need a user token.
2. On a missing-token read probe, create an owner-bound authorization-code+PKCE flow and return its URL through ToolHub status so Communication Hub can send it in chat.
3. Accept the loopback callback once, checkpoint the upstream token file through the existing Broker state lease, and resume the same onboarding.
4. Preserve HTTP MCP OAuth and MCP URL elicitation as separate protocol paths; reject unreviewed URLs in tool text.
5. Add isolation, concurrency, callback and HTTP regressions; run `just check` and Docker gates.
