---
description: Use the same direct upstream Atlassian MCP tool surface as Codex.
status: accepted
---
# ADR-0010: Direct mcp-atlassian server

## Decision

Install the pinned upstream [`sooperset/mcp-atlassian`](https://github.com/sooperset/mcp-atlassian)
package inside the Hermes image and launch it as a stdio MCP server. Configure the Jira
Cloud connection with `JIRA_URL`, `JIRA_USERNAME` and `JIRA_API_TOKEN`.

Hermes does not use the Atlassian Rovo remote endpoint. The upstream MCP is not an
official Atlassian product; its Jira and Confluence clients remain external provider
boundaries and provider permissions remain authoritative.

## Rationale

The Codex Atlassian tool surface (`jira_search`, `jira_get_issue`, `jira_create_issue`,
and related tools) matches this upstream server. A local pinned install works inside
Hermes's isolated Docker runtime and avoids the organization's Rovo API-token policy.

## Consequences

The image carries a separate locked Python environment for this MCP. Jira credentials
are accepted by the owner self-service flow and passed only to the MCP process. The
built-in preset configures Jira; Confluence can be enabled later by adding its upstream
credentials and explicit configuration.
