---
description: Exact-source prepared catalog, generic lifecycle and connector runbooks.
last_verified: 2026-09-22
---
# Prepared connectors

The reviewed [catalog](../internal/toolhub/prepared/catalog.json) records exact
commits, licenses, inferred entrypoints, credential overlays, Broker contracts,
runtime settings, egress, read tools and user handoffs. New entries require data,
not provider branches. Live evidence is tracked in
[CHG-0036](../.work/in-progress/CHG-0036-repository-driven-toolhub/plan.md).

Reviewed Broker contracts ship in `internal/toolhub/prepared/contracts/` and
are validated with their prepared entries. Provision them into the Broker's
configured contracts directory before enrollment; the Broker control API does
not expose contract registration. Missing contracts are deployment errors,
not permission to invent a provider authentication API.

File delivery uses the rendered owner-scoped shared tmpfs volume at
`/run/broker-materialized`. ToolHub and the controller resolve its Docker daemon
mountpoint from the named volume and check translated source paths and
read/write modes against the approved lease mounts. Broker state stays encrypted
between leases; its store and keys are never mounted into MCP workloads. The
controller uses the bridge binary from the deployed Hub image.

`discover` accepts `query` and returns at most five candidates: prepared matches,
matching artifact alternatives, and source-only name matches from the enabled
Official MCP Registry or Docker MCP Catalog. Source-only matches require the
same repository review before installation; registry names and commands grant no
installation authority. This read-only operation never creates credentials,
bindings or workloads. ToolHive/Smithery name search and bounded GitHub fallback
remain open in #110. Pass the selected `candidate_id` and a stable
`request_key` to `prepare_source`. Candidates expire and belong to the current
principal, context, runtime and policy. A direct GitHub `source` works with all
registries disabled. Both paths re-resolve and review the source; an overlay
applies only to its reviewed commit.

After the generated restricted build and real MCP preflight, use
`required_credentials` if requested. Enter credentials only in the protected
Broker form. Compatible owner connections are reused. Call `confirm` with the
returned nonce, then `enable`. For these four exact-source entries, confirmation
also invokes the reviewed zero-argument `probe_tool` through the Broker-backed
workload. A failed provider read leaves the binding unprojected; retry uses the
same owner connection. File-credential readiness starts and then stops the
workload before Broker checkpoints writable state. Other MCPs without a reviewed
provider probe have MCP admission evidence only; their provider access must be
verified separately. Projection changes request the existing Hermes runtime's
controlled Hermes reconnect from its persistent owner home. Installing a
connector does not authorize provider writes.
Admitted tool schemas and rich MCP content, including embedded file resources,
are preserved with output bounds. Schema-declared provider resource owners are
ordinary arguments; runtime identity and credential selectors remain reserved.

For replacement, explicitly call `rotate` with the existing `onboarding_id`,
complete the Broker form, confirm and enable. The old owner workload stops
before new credentials are admitted. Ambiguous active connections fail closed.
`status` resumes onboarding; retry transient failures with the same request key.
Upgrade by reviewing the new SHA and catalog data, retaining the old artifact
and evidence. `disable` removes projection, `revoke` denies further access and
releases the workload, and `remove` also cleans its workspace. Shared immutable
artifacts must not be deleted while another owner uses them.

## Notion

Source: [makenotion/notion-mcp-server](https://github.com/makenotion/notion-mcp-server).
Generated Node entrypoint: `bin/cli.mjs`. Broker: `notion-token`, delivering
`NOTION_TOKEN`. Share the intended root/pages with the integration first.
Verify identity, search shared content and retrieve an authorized page. An empty
search may mean no shared pages. Never create/edit pages as an access test.

Handoff: “Install https://github.com/makenotion/notion-mcp-server using my
existing Notion connection. Verify a read of the shared root/pages.”

## Google Calendar

Source: [nspady/google-calendar-mcp](https://github.com/nspady/google-calendar-mcp).
Generated Node entrypoint: `build/index.js`. This entry covers Calendar only;
full Google Workspace needs a separately selected repository. Broker:
`google-file-session`, supplying OAuth client JSON and owner/binding state.
The reviewed token path is inside the Broker state mount. File credentials are
read-only; mutable state is checkpointed only after the writer stops. Existing
tokens may be transferred locally with owner authorization, never through chat.
Verify `list-calendars` and bounded `list-events`. Expired consent requires the
account's actual authorization flow; placeholders are only for MCP preflight.

Handoff: “Install https://github.com/nspady/google-calendar-mcp; reuse my OAuth
client and token state through Broker. Verify my calendars and events.”

## GitHub

Source: [github/github-mcp-server](https://github.com/github/github-mcp-server).
Generated Go recipe selects the MCP main package and literal `stdio` argument.
Upstream Dockerfile directives never become build authority. Broker:
`github-pat`, delivering `GITHUB_PERSONAL_ACCESS_TOKEN`. OAuth/App alternatives
need a matching reviewed contract; source metadata alone does not implement an
OAuth flow. Verify `get_me` and a file read in an authorized repository.
Issue, PR, review, branch and content mutations are separate actions.

Handoff: “Install https://github.com/github/github-mcp-server with my existing
Broker token; verify identity and a safe repository read.”

## GitLab

Source: [zereight/gitlab-mcp](https://github.com/zereight/gitlab-mcp). Both bin
aliases resolve to `build/index.js`. Generated Node build ignores upstream cache
mounts as installation authority. Broker: `gitlab-pat`, delivering
`GITLAB_PERSONAL_ACCESS_TOKEN`, compatibility alias `GITLAB_TOKEN` and non-secret
`GITLAB_API_URL`. Confirm the intended HTTPS API URL in the protected form;
its host becomes an egress destination. Secrets, authenticated URLs and URLs
with queries cannot expand egress. Verify current-user/whoami after rotation.
On failure, check credentials and the confirmed host on the same connection;
do not relax egress or silently fall back to another GitLab host.

Handoff: “Install https://github.com/zereight/gitlab-mcp; reuse or rotate my
existing connection, confirm the API URL and verify whoami.”
