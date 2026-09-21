# Third-party components

Original hermes-hub code is AGPL-3.0-only. Hermes and MCP connectors are independent
upstream components fetched at pinned commits during image build; their existing
licenses are retained and not changed by this project's license. Sources/pins are in
docs/integrations.md. Runtime images retain source license files under /opt/hermes and
/usr/share/licenses/slack-mcp-server. Dedicated Telegram and Atlassian
images retain their upstream licenses in /opt/telegram and /opt/mcp-atlassian.

Go dependency and toolchain notices are in THIRD_PARTY_LICENSES.txt. Node, Python, Chromium and OS
packages retain their own licenses. Memory Bank Setup informed the documentation layout.
No CareerGo/JobFetch source or binaries, user accounts, tokens or personal data are shipped.
