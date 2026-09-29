---
description: Exact-source prepared catalog, generic lifecycle and connector runbooks.
last_verified: 2026-09-27
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
Official MCP Registry, ToolHive, Docker MCP Catalog or Smithery. An explicitly
enabled `github-search` adapter fills remaining slots after registry matches.
Source-only matches require the
same repository review before installation; registry names and commands grant no
installation authority. This read-only operation never creates credentials,
bindings or workloads. A failed adapter adds a bounded warning while other
sources remain available. Full unprepared registry installation remains open in
#110; a live unprepared GitHub-search result has completed the generic install
lifecycle, while an Official MCP Registry/marketplace result has not. Pass the
selected `candidate_id` and a stable
`request_key` to `prepare_source`. Candidates expire and belong to the current
principal, context, runtime and policy. A direct GitHub `source` works with all
registries disabled; for a repository with a prepared entry a bare URL selects
the reviewed pinned commit, while an explicit `/commit/<sha>` URL keeps the
generic path. Both paths re-resolve and review the source; an overlay applies
only to its reviewed commit.

`prepare_source` waits a bounded window for review+build (about 90 seconds) and
then returns the durable `preparing` record instead of holding the HTTP call:
the work continues on a detached context and the outcome — `awaiting-*` or
`failed` — is read back with `status`. A lost connection no longer loses the
result. Open owner sessions receive a best-effort MCP log when the background
prepare finishes, and communication-hub also delivers a channel notice plus a
continuation job. The log is not the only wake: a client that never set a log
level still gets the outcome in the chat.

After the generated restricted build and real MCP preflight, use
`required_credentials` if requested. Enter credentials only in the protected
Broker form. Compatible owner connections are reused. Call `confirm` with the
returned nonce, then `enable`. For these four exact-source entries, confirmation
also invokes the reviewed zero-argument `probe_tool` through the Broker-backed
workload. A failed provider read leaves the binding unprojected; retry uses the
same owner connection. File-credential readiness starts and then stops the
workload before Broker checkpoints writable state. Other MCPs without a reviewed
provider probe have MCP admission evidence only; their provider access must be
verified separately. Projection changes notify the owner's open MCP session;
a pinned-Hermes fixture confirms PID/session/active-run continuity. Full #74
acceptance remains open. Installing a
connector does not authorize provider writes.

When a reviewed prepared entry declares `oauth`, a failed first provider read
can return `awaiting_oauth` with `authorization_url`. The control tool also
requests URL-mode elicitation; clients without it receive the URL in the tool
response. The user opens the provider URL in a browser on the Docker host.
ToolHub handles the one-time loopback callback, writes the upstream token
format into that owner's Broker state lease, then repeats confirm and enable.
`status` reissues an expired or process-lost link. An existing token file is
never overwritten by this handoff; diagnose a failed read instead. OAuth
metadata must be reviewed for an exact source revision, and token adapters
remain source-specific. Tool text and arbitrary MCP URLs cannot start a flow.
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
With a client JSON but no token, confirmation returns a Google authorization
link for the `calendar.readonly` scope. Google redirects to
`http://127.0.0.1:8090/oauth/callback`; the browser must run on the Docker host.
After consent, ToolHub stores the token under the upstream `normal` account
and checks the read probe. Verify `list-calendars` and bounded `list-events`.
An existing but expired or invalid token requires diagnosis or explicit
rotation; placeholders are only for MCP preflight. This path has mock OAuth
coverage but requires a real account consent for live integration evidence.

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

## Atlassian

Source: [sooperset/mcp-atlassian](https://github.com/sooperset/mcp-atlassian).
Generated Python image entrypoint: `/opt/venv/bin/mcp-atlassian` (stdio). The
server registers **zero** tools until connection config exists, so review
collects the full six-field contract instead of trusting placeholder probing.
Broker: `atlassian-env`, delivering `JIRA_URL`, `JIRA_USERNAME`,
`JIRA_API_TOKEN`, `CONFLUENCE_URL`, `CONFLUENCE_USERNAME` and
`CONFLUENCE_API_TOKEN`. The products are alternatives: a complete Jira block
or a complete Confluence block satisfies the credential groups, so the form
marks every field optional while ToolHub rejects a submission with no full
block. `atlassian.net`/`atlassian.com` are pinned egress; each entered URL's
host joins egress automatically, so self-hosted Server/DC instances work
without relaxations. The provider probe follows the delivered block —
`jira_get_all_projects` or `confluence_list_page_templates` — because a bad
token still passes `tools/list`; on failure rotate the same connection,
never create a second one.

Handoff: “Install https://github.com/sooperset/mcp-atlassian; reuse or rotate
my existing connection, fill the Jira block, the Confluence block or both in
the protected form and verify the matching read probe.”

## TXT-to-SQL

Source: [letya999/txttsql-mcp](https://github.com/letya999/txttsql-mcp), the
reviewed primary SQL engine (AGPL-3.0-only). Generated Rust entrypoint
`/app/target/release/txttsql-mcp` plus the upstream `--config
/app/config.toml` tail; the repository is built into an immutable artifact
and never mounted at runtime. Upstream `Config::resolve` keeps the long-lived
server up without a file: an existing `config.toml` wins, otherwise
`TXTTSQL_SOURCES` — the single-line JSON form of `[[sources]]` — configures
databases, and a fully missing configuration degrades to a zero-source server
that still answers `initialize`/`tools/list` and returns typed tool errors
instead of exiting. Broker: `txttsql-sql`, delivering the sources JSON, three
optional `TXTTSQL_PASSWORD_{1,2,3}` env values referenced from inside it
(`{"kind":"env","name":"TXTTSQL_PASSWORD_1"}`), an optional full `config.toml`
mounted read-only at `/app/config.toml` (per-user memory, plugins, ontology,
metadata providers) and an optional CA bundle at `/run/txttsql/ca.pem`.

PostgreSQL, CockroachDB and ClickHouse drivers do not speak HTTP CONNECT, so
the entry declares `tcp_forwards`: companion loopback listeners `15432`,
`15433` and `15434` inside the workload network, each relaying accepted
connections through the workload's reviewed Squid CONNECT proxy to exactly
one credential-bound `host:port` held in `TXTTSQL_ENDPOINT{,_2,_3}`. The same
non-secret value joins the egress ACL at readiness, so a forward can only
reach what admission allowed — there is no direct TCP bypass. Each
`[[sources]]` entry in the JSON points at `127.0.0.1:<forward port>` while
the real endpoint lives in the protected form; host, database, role and
private destinations stay outside tool arguments. The catalog `egress`
sentinel is `127.0.0.1`: every real destination is credential-derived. When
TLS verifies a certificate hostname, the forward makes the server name an IP
literal — `verify-ca` (CA validation without hostname) and per-source
`ca_file` remain available, and `allow_insecure` stays an explicit operator
choice.

Read-only is enforced server-side by upstream read-only transactions plus
per-source `allowed_schemas`/`allowed_tables` — not SQL string matching —
together with `max_rows`, `timeout_seconds` and `max_connections` bounds.
There is no write profile: mutations stay a separately granted action.
Verify `list_sources` (the readiness probe), then `list_tables` and one
bounded `SELECT` through the forward. Memory and ontology stay
file-configured opt-ins on read-only mounts, not default state.

Handoff: “Install https://github.com/letya999/txttsql-mcp as the reviewed
read-only SQL engine. Fill the sources JSON and the matching endpoints in the
protected Broker form, verify list_sources and one safe SELECT, then do not
change provider data without an explicit instruction.”

## DBHub

Source: [bytebase/dbhub](https://github.com/bytebase/dbhub), the reviewed
fallback SQL engine (MIT) — a separate prepared entry selected explicitly,
never a silent provider failover. Generated pnpm workspace build runs
`node /app/dist/index.js` on stdio; this entry never enables the HTTP
transport (unauthenticated by default upstream). DBHub exits without any
configuration, so the reviewed `preflight_environment` injects
`DSN=sqlite:///:memory:` for the unauthenticated `tools/list` probe only —
owner credentials always override it. This revision rejects the legacy
`READONLY` and `MAX_ROWS` environment variables: env-mode DBHub has no TOML
`[[tools]] readonly = true` policy, so read-only must be enforced by the
database role named in the DSN — an explicit requirement of this contract,
not a recommendation.

Broker: `dbhub-sql` — secret `DSN`, non-secret `DBHUB_ENDPOINT` and
`DBHUB_SSH_ENDPOINT` host:port values bound to the two `tcp_forwards`
(database on `15432`, SSH bastion on `15433`), optional `SSH_USER`,
`SSH_PASSWORD`, `SSH_PASSPHRASE`, an `SSH_KEY` private-key file at
`/run/dbhub/id_rsa`, optional `SSH_HOST`/`SSH_PORT` overrides and a CA bundle
at `/run/dbhub/ca.pem`. Direct path: the DSN targets `127.0.0.1:15432` and
the endpoint field carries `db.example.com:5432`. Managed-tunnel path:
`SSH_HOST=127.0.0.1`/`SSH_PORT=15433` make DBHub's ssh2 client dial the
forwarded bastion while the DSN addresses the database as the bastion sees
it. Verify `tools/list`, one bounded `SELECT 1` through the forward, and the
tunnel when `DBHUB_SSH_ENDPOINT` is configured.

Handoff: “Install https://github.com/bytebase/dbhub as the fallback
read-only SQL engine. Fill the DSN and endpoint in the protected Broker form
against a read-only database role, verify one safe SELECT, then do not change
provider data without an explicit instruction.”
