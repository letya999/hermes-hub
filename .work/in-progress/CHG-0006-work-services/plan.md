# CHG-0006: Atlassian, GitLab and Google work services

## Goal

Give Hermes bounded access to the owner's work services using existing upstream tools.

## Scope

1. Keep official Atlassian Rovo MCP with personal API-token auth and document its
   provider-side permission controls.
2. Install `glab` in the runtime and teach Hermes safe read and mutation behavior.
3. Make Google Workspace read-only unless `google_write` is selected.
4. Add configuration and container smoke checks, setup guidance and validation limits.

## Non-goals

No GitLab MCP preset, custom REST adapters, shared OAuth store, database or queue.
