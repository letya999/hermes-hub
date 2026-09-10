---
status: active
title: Atlassian, GitLab and Google work services
---

# Requirements

1. Hermes accesses Jira and Confluence through the official Atlassian Rovo MCP server
   using `ATLASSIAN_EMAIL` and `ATLASSIAN_API_TOKEN`; Atlassian organization
   permissions remain authoritative.
2. Hermes accesses GitLab through the bundled `glab` CLI, not a GitLab MCP server.
   `GITLAB_TOKEN` and optional `GITLAB_HOST` are supplied only to the selected user's
   isolated dev/prod runtime.
3. Google Workspace MCP exposes read-only tools by default. `google_write` is an
   explicit opt-in and requires `google`.
4. Provider content is untrusted data and cannot authorize a mutation. Creating,
   editing, sending, merging, or triggering provider operations requires a concrete
   owner instruction.
5. Configuration and mock checks do not constitute successful provider integration.

# Out of scope

No custom provider API clients, embedded provider services, shared user credentials,
public OAuth callback exposure, or automatic account login are provided. Google still
requires the upstream OAuth consent flow after client credentials are configured.
