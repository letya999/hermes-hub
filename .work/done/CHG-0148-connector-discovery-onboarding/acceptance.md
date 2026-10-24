# Live acceptance — 2026-10-22

Local dev ToolHub was rebuilt with `hubctl dev-rebuild`. These are source-review,
isolated build, real MCP `tools/list` and protected Broker form checks. No provider
credentials were submitted, provider accounts logged into, or provider data changed.
Form checks used the configured local CA, with TLS verification enabled.

## Confirmed forms

| MCP | Exact source revision | Observed result |
|---|---|---|
| Metabase | easecloudio/mcp-metabase-server @ 8a4dfce8afd45ead9cc4c2764f41ace6f91adf66 | 28 tools; HTTPS 200; exactly two deduplicated alternatives: URL plus API key or URL plus username/password |
| AppMetrica | pavellunev/app_metrica_mcp @ 8eac2640cdbd1a5ddcdcc939aa5c3790d96f848c | 45 tools; HTTPS 200; explicit APPMETRICA_OAUTH_TOKEN recipe |
| DataLens | datalens-tech/datalens-mcp @ 96b3d6baf8839c4ff5ef4adb4c10a720c5c00e74 | 5 tools; HTTPS 200; auth header, organization ID, optional API URL |
| Yandex Metrica | theYahia/yandex-metrika-mcp @ 2c736e0f3ef9a411f1dc172958c54c864b969ead | 15 tools; HTTPS 200; YANDEX_METRIKA_TOKEN |
| Wildberries | ilyautov/wildberries-mcp-ru @ 71987902ac48def69b5fcbb5dbc0822fb5c88b64 | 25 tools; HTTPS 200; WB_API_TOKEN |
| Google Workspace | taylorwilsdon/google_workspace_mcp @ af866c34a1d5d9429b09d7fe5572b07be299b844 | Prepared recipe; 39 tools; HTTPS 200; OAuth client JSON and email |
| Google Analytics | googleanalytics/google-analytics-mcp @ 25cf9265fbd31b0a40e60a269641533824e14a64 | 9 read tools; HTTPS 200; JSON credential file delivered through GOOGLE_APPLICATION_CREDENTIALS |
| Postgres via DBHub | bytebase/dbhub @ 8c09c868894f1ea011565ec35215ac36e5638c1f | Prepared recipe; 2 tools; HTTPS 200; DSN and actual database endpoint; a read-only database role is required |
| Filesystem | portertech/filesystem-mcp-server @ 3b3e5a05d7d2bfb107dc9e7583315976cc8bf897 | Isolated Go build and real tools/list passed, 17 tools; awaiting confirmation, no credentials required; user directories not mounted |
| Kaspi Shop | theYahia/kaspi-mcp @ 58a0834b9448d0d1121a4e50bf6577fd7c869412 | 3 tools; HTTPS 200; only required KASPI_API_KEY, no documentation placeholder field |
| Notion | makenotion/notion-mcp-server @ 730ae781ba28beeaf0865025a3f2ed4c25ea2387 | 24 tools; HTTPS 200; prepared notion-token@2; exact candidate selection also passed |
| Prometheus | prometheus/prometheus-mcp @ cd0e5527617abd4cd8ec360ad901c002304ab247 | 12 read tools; HTTPS 200; required server URL and endpoint configuration |
| LinkedIn | AriOliv/linkedin-mcp @ 6847b8fc9028240d62802a17432513243890415e | 7 tools; HTTPS 200; corrected stdio recipe requires only LINKEDIN_ACCESS_TOKEN; HTTP-mode secrets remain optional |

Kaspi final re-review passed: three tools, HTTPS 200, only required KASPI_API_KEY; the obsolete YOUR_API_KEY input is gone. Both parser filtering and dynamic-contract identity fixes were verified live.
Google Analytics first listed nine tools without credentials; its corrected file
delivery reached the form after retrying a Docker image-tag failure. A successful
`tools/list` alone does not establish that lazy provider authentication is configured.

## Rejected sources

| Source | Actual failure | Consequence |
|---|---|---|
| dontsovcmc/mcp-server-wildberries | Upstream imports mcp.server.fastmcp but installs incompatible MCP dependency | Used a separately identified Wildberries alternative |
| BoxLab-Ltd/yandex-metrica-mcp; askads/mcp-yandex-metrica | Missing package-lock.json | Reproducible Node source build rejected |
| stufently/yandex-metrica-mcp | Conflicting launch recipes | No guessed launch command |
| crystaldba/postgres-mcp @ 15c8e33353546148acc2d8bd784551cf3905d1e2 | Required pglast==7.11 unavailable in the build environment | Prepared DBHub alternative reached its form |
| stickerdaniel/linkedin-mcp-server current head | Conflicting PyPI/OCI manifest launch recipes | AriOliv alternative reached its form |
| Dishant27/linkedin-mcp-server @ 007288d990578a05013eb0ec3e692ad4e74dbaa6 | No MCP tools/list response during startup authentication | AriOliv stdio alternative reached its form |
| stickerdaniel/linkedin-mcp-server registry revision | MCPB contract requires unsupported secret delivery in argv | Secret protection retained |
| rust-mcp-stack/rust-mcp-filesystem @ 27e63b734d89b7a618a81c2a511ef34aab612011 | Isolated Rust build exceeded the 25-minute build budget | No filesystem admission or user-directory mount claimed |

Public GitLab resolution and verified tree/blob retrieval succeeded against
wanpol/langsmith-mcp-server @ 1c4cdcf0d3aacd8206b7b9c13557b8290e8e1d26:
42 filtered source files, 2,572,288 tar bytes. Full source build succeeded, then
the isolated server crashed: it imports mcp.server.fastmcp with MCP 2.x installed.
The upstream dependency needs a compatible constraint or migration; it was not patched.
Todoist was also attempted: the source declares ambiguous entrypoints, so the generic
review requires a literal launch choice and does not guess one. Notion and Prometheus
both passed as additional prepared-source checks. Notion exposed a contract digest
conflict at revision 1; the prepared revision 2 passed the complete source-to-form run.

## Chat and discovery

The local profile exposes legacy `discover`, `prepare_source`, `status`; the
published skill now supports those as well as the managed `connector_*` facade.
Existing profile permissions were preserved. Rendered effective SOUL files for local and existing dev profiles reference
the actual `hermes-hub-mcp-onboarding` skill name. Managed profiles have no ordinary skill loader, so the common SOUL now embeds the search/select/install/status flow directly. Only the onboarding paragraph was replaced in existing personas; tokens and other persona content were preserved.

Live discovery returned five candidates for generic searches and retained prepared
entries for exact aliases. Registry metadata and GitHub hits remain candidates,
not evidence of installability. The official MCP Registry timed out from both the
service and a separate host request; fallback returned candidates with a warning.
A later live Notion search returned the prepared entry plus four official MCP Registry candidates without warnings. GitHub anonymous API quota exhaustion temporarily returned HTTP 403; retries wait for its reported reset.
Exact selection, source pin retention, private-owner selection rejection and
asynchronous operation reporting have regression coverage. Live selection of the prepared Notion candidate retained repository and commit 730ae781ba28beeaf0865025a3f2ed4c25ea2387 and reached HTTPS 200. An expired candidate ID was rejected before a fresh search succeeded.

## Validation and secret handling

- Final `just check` passed after all code/catalog changes; own Go statement coverage 86.22%.
  Formatting, vet, staticcheck, gosec, docs, workflow validation and deployment-contract integration check passed.
- Docker prebuilt gate passed against the available edge revision f147cd, not the new
  source. New ToolHub behavior is exercised on the local dev overlay image.
- No dependency changes. Final scoped Docker cleanup passed.
- Three internal bearer values were accidentally exposed in diagnostic tool output.
  The user explicitly instructed not to rotate them. Values are absent from this
  report. An exact-value scan of 1,800 tracked/addable working files and the staged
  diff found no matches. `spaces/**`, scratch diagnostic directories and local state
  are Git-ignored. AGENTS.md now forbids dumping auth dictionaries or dictionary keys.

No claim of end-to-end authenticated provider access follows from these checks.
Actual model-led Telegram conversation was not replayed or sent. The live MCP control
path was exercised and effective instructions regenerated for existing dev profiles.
The final ToolHub container runs hermes-hub-dev-local-toolhub:local, image
sha256:d8b8eee36023fc7bebb6493b53ce9d21c84606614a1821ec061928568291677b.
GitLab support is for public gitlab.com projects; private and self-hosted instances
are not covered. Public registry availability and GitHub quotas remain external limits.
LinkedIn provider operations depend on the account/app's API permissions; listing
tools does not validate upstream endpoint availability or authorize messaging.

## Root causes repaired

- Metabase startup errors used plural wording and required a valid URL; auth parsing missed configuration and lost alternative credential groups. Parsing, URL placeholders, retry preservation and group deduplication now have regression coverage and live evidence.
- Registry timeout consumed the GitHub fallback deadline. Fallback now has its own bounded budget.
- Managed install routing did not resolve selected candidate IDs or bare repository URLs correctly. Source identity and correction recipes now survive routing and idempotency.
- Asynchronous failures and OAuth waits were reported with stale operation states. Status follows the current onboarding phase.
- Documentation placeholders produced bogus credential names. Fresh definitions also reused old broad dynamic Broker contracts; generated contracts must now match the current declaration identity.
- Notion's prepared contract collided with the Broker example under revision 1. New prepared enrollment uses revision 2; existing contracts are not rewritten.
- Existing SOUL referenced an obsolete skill name and assumed a skill loader that managed profiles do not have. The common instruction now includes both managed and legacy ToolHub routes directly.
