---
description: Connector contracts, scope rules and source pins.
last_verified: 2026-09-16
---
# Integration contracts

Manager instructions for the owner's local PC:
[local-accounts-manager.md](local-accounts-manager.md). Local deployment is
prepared, not logged in; existing Hermes defaults are unchanged.

## Official Google Workspace remote MCP

Use `hubctl connector connect --provider google --official-mcp --product gmail`
with the same absolute registry/ciphertext/key paths as below. Products:
calendar, gmail, drive, docs, sheets, slides. Each product gets a separate
connection and read-only OAuth scopes. `--account` accepts the expected Google
subject or a verified email. Supply CLIENT_ID/CLIENT_SECRET in a protected
local `--client-file`, not chat or argv. For a Web OAuth client register exactly
`http://127.0.0.1:8543/callback` and use `--callback-port 8543`.
Open the printed authorization URL and complete consent yourself.

These are Google-managed `https://<product>mcp.googleapis.com/mcp/v1` services,
not the bundled third-party Workspace MCP. Google's
[official setup](https://developers.google.com/workspace/guides/configure-mcp-servers)
currently requires Developer Preview access, a Cloud project, underlying APIs
and their MCP services enabled, and OAuth consent/client configuration. Each
service must be enabled for the same project as the OAuth client. Discovery
rejects an inaccessible/empty service; it is not assumed to work from a token.
Provider schemas are snapshotted and validated locally without remote $ref
fetches. New/unknown and mutation tools are excluded from read grants.
Calendar writes remain the separate verified REST `--write` grant below.
Refresh/revoke use the same commands; no real Cloud account has been tested.

## ToolHub personal Telegram account MCP

Python workloads are supported behind the existing container-MCP admission
boundary. `docker/telegram-account.Dockerfile` is a standalone Python image
with the pinned Telegram/Telethon virtualenv; it does not `FROM` the hub
image. Build it from the repository root, then use the actual image digest
in an operator deployment manifest. Do not substitute a mutable tag or a
guessed digest. The upstream hash-locked `proxy` extra is enabled so
Telethon can use an HTTP CONNECT proxy; no dependency version is guessed.

The ToolHive controller must be deployed/configured first. It receives
workspace_path, pinned image/sidecars and limits, must mount that exact path
as writable connection-state at `/run/connector`, wrap the image's stdio MCP,
and return its validated running enforcement receipt. Configure absolute
HUB_STATE, HUB_TOOLHIVE_ADMISSION_ENDPOINT and any required private vMCP token.
The public/model path never receives Docker access, session strings or env files.
Drain existing per-user container workloads before upgrading the new connection-
isolated path/identity scheme, then explicitly reconnect them. Old state is not
deleted or automatically copied across connections. The controller must preserve
private ownership compatible with the adapter's UID 10001; do not chmod secrets
world-readable to make an incorrectly owned bind mount work.
The owner-selected PC now has real ToolHive v0.48.0 discovery and authenticated
Go `connector local-controller` admission evidence. The fixed local controller
uses separate pinned Squid egress with Telegram's published IPv4 CIDRs; direct
TCP is blackholed by an internal Docker network. MCP discovery and network
checks do not establish real user login or message reads. A physical protected
user-home deployment is used because AppData staging was not consistently visible
through Docker Desktop/WSL. The account worker and user login helper mount only
their respective connection-state/input directory, never Google input or key files.

Locally prepare a protected file containing only TELEGRAM_API_ID,
TELEGRAM_API_HASH and TELEGRAM_SESSION_STRING (from your own Telegram app and
interactive session setup). Do not send this file through chat. Import using
`hubctl connector connect --provider telegram --account <numeric-user-id>
--connection <unique-id> --from-file <protected-session-file>
--deployment-file <operator-manifest.json> --endpoint <private-toolhive-mcp>`
and the standard absolute registry/ciphertext/key paths. The host command
verifies the session's get_me identity before saving a binding. A bot session
or different user is rejected. Read tools: get_account, list_dialogs,
get_messages, search_messages and get_chat_info. `get_messages` uses Telegram's
real history API with bounded before/after cursors, and `search_messages` can
search globally or in one chat, so old messages are not limited to newly arrived
updates. Dialogs include the user's private chats, groups and channels; bot
peers are excluded. Message text is untrusted and media is reported as metadata,
not downloaded. `--write` creates a separate explicit send_message/reply_message/
delete_message grant; the server independently checks it. Credentials are loaded
per call and wiped by Go after execution; there is no unattended login.

`connector revoke` immediately blocks local calls and revokes ciphertext.
To invalidate the imported session at Telegram itself, explicitly terminate
it in Telegram Devices. Telegram does not use OAuth refresh. Neither fixtures
nor the Docker SDK tests establish live login, sending or provider revocation.

## ToolHub command boundary

ToolHub direct CLI/container command validation rejects shell executables,
including Windows `.exe` variants, and `.bat`/`.cmd` shell scripts. Arguments
are literal argv; repository instructions never authorize host shell execution.

### Generic artifact pipeline — implementation in progress

The Git source helper accepts only a canonical public GitHub repository and an
exact lowercase 40-character commit SHA. It verifies commit/tree/blob identity
and produces a bounded in-memory context without ambient credentials.
`FetchRepositoryArtifactContext` automatically chooses permitted regular files
from the verified tree, excluding sensitive paths before fetching their contents.
It uses two GitHub API requests and exact-SHA raw file URLs with Git blob-ID
checks, instead of consuming an API request for every file. Automatic downloads
use batches of at most eight requests while retaining deterministic tar order
and the total byte/time budgets. Filename filtering
does not replace the still-required content secret scan. The scan treats a
token-shaped match whose secret body is one repeated character as a placeholder
fixture (upstream tests commonly embed `ghp_xxxx...`-style strings) and does
not block on it; any varied body still fails closed.
`GenerateArtifactRecipe` generates a pinned-base Dockerfile for conventional
Python, Node, Go and Rust manifests; when no reviewed base is supplied it selects
the repository-pinned digest for the detected language. An upstream Dockerfile
is not required.
Node requires `package-lock.json` or `pnpm-lock.yaml`; pnpm recipes use pinned
pnpm 10.32.1 and frozen installation. Conventional pnpm monorepositories can
select a unique `*-mcp` package with a declared bin; other layouts remain explicit.
When runtime egress was not explicitly reviewed, import proposes HTTPS hosts
from standard `servers` entries in bundled `*openapi*.json` files. The hosts are
included in the immutable review digest; missing, malformed, credentialed,
port-qualified or oversized declarations fail closed.
Rust requires `Cargo.lock`; ambiguous servers
require literal entrypoint selection.
Python packages with several console commands select the command matching the
project name when present; otherwise selection remains explicit. Generated
contexts are deterministic and validated against SHA-256 file digests.
Python/Go manifest byte pinning does not
yet guarantee fully locked transitive dependencies.

Generated recipes clear the base image's inherited `CMD` before setting their
literal `ENTRYPOINT`. `VerifyOCIArtifact` checks a bounded OCI tar archive without
host extraction: SHA-256 blobs, descriptor sizes, one Linux platform, config and
layer references, and attestation subjects matching the runnable manifest. It
supports BuildKit legacy/OCI-artifact storage, embedded descriptor data,
SLSA v0.2/v1 provenance and SPDX 2.2/2.3 SBOM evidence, following the
[Docker attestation format](https://docs.docker.com/build/metadata/attestations/attestation-storage/).
Layout-index, content-index, runnable-manifest and image-config digests remain
separate. This is integrity/presence validation, not a full SBOM schema audit,
layer unpacking verification, proof of honest provenance, or trusted admission.

`PersistOCIArtifact` stores verified archives in an existing administrator-owned
absolute directory. Objects are addressed by the SHA-256 of the exact archive
bytes and published without replacement only after a complete write, sync and
OCI check. Concurrent identical uploads reuse one object; corruption, symlinks,
cancellation and oversized inputs fail closed. `OpenStoredOCIArtifact` rechecks
checksum and evidence before returning a reader. These are private quarantine
objects, not enabled catalog entries; content secret scans, authenticated build
receipts and capability approval remain required. Workloads must never mount
this host directory. The directory is not created by this API.

The repository now pins the separately reviewed rootless BuildKit bootstrap
policy `rootless-buildkit-bootstrap-v1` in `docker/seccomp-buildkit-rootless.json`.
Hermes accepts that profile only at its exact SHA-256 and generates a fixed Docker create plan: immutable
BuildKit image digest, non-root user, network none or internal-only proxy routing, read-only root, no binds or
devices, bounded CPU/memory/PIDs/tmpfs, capability drop with only SETUID/SETGID,
`systempaths=unconfined` and `--oci-worker-no-process-sandbox` for RootlessKit
`newuidmap`/`newgidmap` bootstrap. Privileged and `seccomp=unconfined` workers
are forbidden. This is a builder-only bootstrap exception; the MCP runtime
uses `docker/seccomp-mcp-runtime.json` (Docker default syscalls, not the
BuildKit SETUID bootstrap and not `seccomp=unconfined`) and still rejects
SETUID capabilities, unconfined seccomp, privileged mode, host
network/PID, Docker socket and host mounts. The integration gate also
proves the controlled dependency path: the builder is attached only to an
internal network, while a pinned ToolHive egress proxy is dual-attached and
permits an explicit TLS package-host allowlist. A real pinned base-image build
exported an OCI archive with SLSA provenance and SPDX SBOM from a pinned scanner.
Restricted BuildKit is allowed 25 minutes so a conventional Rust recipe can finish cargo
through the allowlist proxy; the MCP runtime timeout is unchanged.
and the archive passed the integrity/evidence verifier. `hubctl artifact import`
now drives exact-SHA discovery, automatic Python/Node/Go/Rust recipe generation,
the restricted BuildKit build and quarantine packet creation. The packet carries
source/recipe/archive/provenance/SBOM/review digests but remains untrusted until
an operator runs `hubctl artifact preflight` (live `tools/list` against the
loaded image under the MCP runtime profile, HOME on tmpfs, not the BuildKit
bootstrap) and then `hubctl artifact approve` with `--contract` JSON from that
`preflight-list` or a review manifest that already matched one
(`review-manifest`). Claimed `ArtifactImportConfig` tools
are a preview only and cannot register. Preflight restamps the review digest
from the confirmed tools without rebuilding the OCI archive. Approve
`--review-digest` must match `ImportedReviewDigest` of that restamped packet. Registration is
immutable and rejects review-packet or tool-contract drift. `LoadStoredOCIArtifact` imports a
verified archive into the local Linux Docker image store without ambient Docker
credentials. `WorkloadBudget` provides a FIFO active-process cap and idle
reclaim hook, while `RuntimeSecurityProfile` is the pre-start fail-closed
contract for any ToolHive adapter. Credential-bearing onboarding uses the same
generic admission path before publishing a new projection and restores the
previous workload credential file if admission fails.

M5.3 also exposes a secret-free `RecipeResolver` before source review. It pins
the repository, optional subfolder and commit, reads only bounded declarations,
and returns Launch/Connection Recipes with file-digest evidence. Matching
catalog adapters are queried concurrently, but repository owner/name and the
selected commit must match; a name-only hit is ignored. Docker/Compose are
parsed as declarations: one MCP service may be selected, sidecars are recorded
for review, and host mounts, privileged mode, host namespaces and shell
healthchecks fail closed. Upstream Dockerfile `RUN` options are parsed as
instruction tokens after continuations are folded; `type=cache` mounts are
inert metadata while secret, bind, ssh, tmpfs, typeless or unknown mounts,
host/insecure/privileged options and Docker socket references mark the recipe
`review` and force the generated `.hub` build fallback. Inside that fallback
the upstream Dockerfile remains inert metadata: its last exec-form
`ENTRYPOINT`/`CMD` may only disambiguate Go trees with several `main`
packages (basename match against the selected directory or the `go.mod`
module basename) and supply the matched binary's default arguments.
The isolated `tools/list` preflight retries once with declared-but-optional
secrets as placeholders; a successful retry proves they gate server startup
and promotes them to required onboarding inputs. Recipes are status
metadata, not authorization or
execution; the existing preflight `tools/list`, Credential Broker, ToolHive and
`/reload-mcp` gates remain authoritative.

`.mcp.json` and `.env.example` are read only for names and launch metadata;
their values are never returned and those files are not passed to BuildKit.
External recipe adapters are opt-in through `HUB_RECIPE_CATALOGS`, a
comma-separated allowlist of `mcp-registry`, `toolhive`, `docker-mcp`,
`smithery`, `docker-hub` and `ghcr`; `HUB_RECIPE_CATALOGS=all` enables all six.
The adapters use fixed public endpoints. Smithery additionally requires the
operator-selected environment variable named by `HUB_SMITHERY_TOKEN_ENV`
(default `SMITHERY_API_KEY`); its bearer value is request-only. GHCR package
listing may require an operator-selected `HUB_GHCR_TOKEN_ENV`; that token is
sent only to `api.github.com` and never enters a recipe. Docker Hub namespace
and tag search uses the official Hub API, while image metadata is verified
through the OCI distribution API.

Catalog hits must correlate to the pinned repository/subfolder/commit.
Container hits are accepted only after the OCI registry returns an immutable
manifest, a GitHub source/repository label, the exact revision, and both
provenance and SBOM evidence (OCI referrers or embedded Docker attestation
manifests). The published image path pulls and preflights that exact digest
before the review packet is stored; if it cannot pass, the existing restricted
GitHub BuildKit fallback remains. Connection recipes contain only field names,
types, required/secret flags and delivery targets; Smithery camelCase JSON
fields are normalized to safe env names while retaining their JSON target.

Live acceptance also exercised the Docker MCP Catalog
`mcp/sequentialthinking@sha256:cd3174b2ecf37738654cf7671fb1b719a225c40a78274817da00c4241f465e5f`
from `modelcontextprotocol/servers` at commit
`82064568802e542c3924560aef2cb421b4ce436c`, through ToolHive 0.49.0:
`initialize` and `tools/list` returned the real sequential-thinking tool.

After a projection change, ToolHub writes `toolhub-reconnect.request` under the
absolute `HUB_STATE` directory. The serve runtime watches that marker and sends
Hermes' authenticated `/reload-mcp` command through the pinned API, so tool
discovery refreshes in place without a Hermes process restart. This path is enabled
by default when ToolHub is configured; set `HUB_TOOLHUB_RECONNECT=false` to retain
manual reconnect behavior. The marker and request carry only a monotonic revision
and generic identity metadata, never credential values.

The stdio companion speaks newline JSON-RPC. Go SDK v1.7 first sends
`server/discover`; servers that neither implement it nor return JSON-RPC
method-not-found (notably `rust-mcp-filesystem`) would hang the handshake.
The companion therefore synthesizes `-32601` for `server/discover` and falls
back to `initialize` without sending that RPC to the child.

Live SHA import → preflight contract → `docker_fallback` + stock ToolHive →
Hermes `tools/list` and one safe `tools/call` (2026-09-16, local Docker 29.4.3,
ToolHive 0.49.0, Hermes 0.21.0): Serena `704e8c3d1929bdd74bf0c8eee1596aeddf03fdb2`
`mcp__serena__initial_instructions` (“You have semantic coding tools”); Context7
`b653c3a07d7936bdc4c23fc1c88903120e0ece77` `mcp__context7__resolve_library_id`
(`/reactjs/react.dev`); Go `mark3labs/mcp-filesystem-server`
`ba3f07f22c309d932fa9b1cebe1eb7c55fcbb83b` `mcp__go_filesystem__list_allowed_directories`
(`/state`); Rust `rust-mcp-stack/rust-mcp-filesystem`
`ef4797360ea03eec5375e03a6f10d092dfc6a5e0` `mcp__rust_filesystem__list_allowed_directories`
(`/state`). Isolated MCP still sees only `/state`, not `spaces/<user>` files or
skills; that grant is issue #103, distinct from catalog-default #81.
Live Docker 95-plus-5 is 100 sequential unique networks/volumes/containers
(95 distinct env-files + 5 shared); 100 concurrent MCP+ToolHive stacks are
not claimed (Docker Desktop IPAM).

Start the generic admission endpoint with `hubctl connector
generic-controller`; its JSON config contains the trusted definition, absolute
ToolHive/seccomp paths and bounded `max_active`/`idle_ttl_seconds` values. It
creates an owned internal network, one pinned egress proxy container and one ToolHive
workload per workload ID, injects credential references through ToolHive's
environment secret provider (values never enter RunConfig), and returns a
loopback MCP endpoint only after Docker inspection proves the digest, limits,
non-root user, read-only root, dropped capabilities, no-new-privileges,
seccomp, private network and volume topology. Generic MVP rejects host state
mounts and shared workloads with credentials; stateful fallback workloads use
only controller-owned named volumes. It checks `thv run --help`
before any MCP starts. Stock ToolHive v0.48.0 does not expose all required
create-time security flags, so the controller fails closed rather than applying
an unsafe post-start update. The gateway may use the controller receipt's
private endpoint for credential-free bindings.

For user-authorized self-install deployments, set `dynamic_definitions: true`
once on the authenticated loopback controller. Each admission then carries the
complete immutable ToolHub definition; the controller reruns trusted-artifact,
transport, proxy-image, credential-class and execution-policy validation. This
removes per-MCP controller rewrites or restarts without accepting an arbitrary
command, mutable image or unauthenticated plan. Omit the option to retain the
single operator-pinned definition mode.

For a local MVP with stock ToolHive, set `docker_fallback: true` explicitly in
that controller config. If `thv run` lacks the create-time profile flags, the
controller then creates the reviewed artifact itself with Docker's pre-start
limits, read-only root, non-root user, dropped capabilities, NNP, seccomp and
internal-only network. A Linux ELF copy of `hubctl` runs the
artifact's immutable entrypoint through the existing companion bridge on the
internal network without a forwarding token. Because
Docker Desktop does not publish ports from an `--internal` network, a separate
bounded relay container holds `HERMES_BRIDGE_TOKEN`, publishes a loopback port
and forwards only the authenticated `/mcp` path; it is not a general proxy.
Windows `hubctl.exe` and any PE file are rejected as `bridge_binary`. Set
`bridge_binary` to a Linux ELF or run `hubctl artifact linux-bridge --output
<path>`. ToolHive is still used for the private
remote proxy, forwarding header and lifecycle. The fallback is intentionally
limited to one workload per binding: credentials are read from an owner
workspace `credentials.env`, validated against the definition, and passed to
Docker with `--env-file`; state is kept in per-workload named volumes. Shared
stateful or credential-bearing MCPs stay denied. Idle cleanup keeps stateful
volumes and deletes stateless ones. Docker inspect refuses an MCP container
that received a forwarding secret.
Do not label an artifact trusted on the basis of recipe validation or a claimed
tool list alone. Track remaining evidence in
`.work/in-progress/CHG-0025-trusted-artifacts/verification.md`.

## ToolHub Calendar (M5 stage 2)

`hubctl connector connect` uses protected `--client-file` containing CLIENT_ID
and CLIENT_SECRET, `--account` with the expected Google subject ID, and a unique
`--connection`. Supply absolute `--toolhub-store`, `--store` and `--key-file`
outside the source checkout. The key must be outside the ciphertext directory.
The command prints an authorization URL, waits for a loopback callback and
verifies the account before creating a read binding. `--write` is an explicit
separate mutation connection/grant; it does not upgrade a read connection.
Only open the authorization URL when live login is explicitly intended.

`connector status` lists metadata. `connector call --tool <projected-name>
--from-file <arguments.json>` uses shipped authorization and ciphertext injection.
Arguments are calendar_id, event_id for get/update/delete, event_json for
create/update, and optional page_token for list. Attendee add/remove/change is
an explicit update with the desired attendees array. Listing is paginated at
100 events; follow nextPageToken. No arbitrary API URL is accepted.

`connector refresh --connection <id> --client-file <protected-file>` rotates
only that connection's OAuth token. `connector revoke --connection <id>` denies
new calls first and then requests Google revocation; provider cleanup failures
are reported without restoring access. Restart a failed connect rather than
replaying a used callback. A revoked connection requires a new connection ID.
Unset HUB_TOOLHUB_* to retain the unchanged generated-MCP compatibility path.
Calendar API v3 and Google OIDC/OAuth contracts are documented at
[Calendar](https://developers.google.com/workspace/calendar/api/v3/reference/events),
[OAuth](https://developers.google.com/identity/protocols/oauth2/web-server) and
[OIDC](https://developers.google.com/identity/openid-connect/openid-connect).
Current verification uses official-shaped local HTTP fixtures, not live Google.

## Credential Broker

The copied Broker source is a standalone module under
`services/credential-broker`. Use `just credential-broker-check` for its full
unit/race/coverage/build gate and `just credential-broker-docker-check` for its
minimal image build. The Hermes runtime image contains the binary, but no Broker
service is enabled by default: its config, private signer keys, provider data,
contracts and runtime materialization paths must be provisioned explicitly.

The Broker's public Go SDK signs exact method, URI, body and canonical identity.
The adapters are ToolHub (`broker:control`), Communication Hub
(`broker:approve`) and the runtime materializer (`broker:runtime`). Do not pass
`api/v1.Materialized` through Hermes messages, MCP projections or job/audit
ledgers. Set `HUB_CREDENTIAL_BROKER_CONTROL_*`,
`HUB_CREDENTIAL_BROKER_APPROVE_*` and `HUB_CREDENTIAL_BROKER_RUNTIME_*` URL,
key-file, key-ID and issuer variables to enable Broker-only credentials. The
signing key files must be private files visible in the corresponding Hub
containers; no key is committed to the repository.

For every credential-bearing MCP definition, add the reviewed contract fields
`credential_contract_id`, `credential_contract_revision` and an explicit
`credential_contract_env` mapping. ToolHub creates the Broker request and
grant, Communication Hub accepts `/credentials <request-id> <code>`, and the
runtime acquires/materializes/releases a lease. The generated Compose stack
sets `BROKER_DIRECT_FORM=1` on the broker service, so the connect link opens
the credential form directly and the `/credentials` approval step is skipped;
remove the variable (or set `direct_form` back in the broker config) to
restore the trusted-channel confirmation required by the broker spec. If a
connect link expires before submission, ToolHub issues a fresh request on the
next status poll, so the user simply follows the new link. Existing local
credential
records are intentionally rejected once Broker mode is enabled, so migration
is explicit and cannot silently mix ownership systems. ENV deliveries are
supported. File deliveries use a reviewed read-only runtime mount and a
per-call ToolHive/Docker workload; the controller removes that workload before
the Broker lease is released. Enable them only when the Broker materialization
directory is an explicitly shared, non-symlinked runtime path visible to the
ToolHub/controller and Docker daemon. Without that mount root, file delivery
fails closed. The copied source and its MIT license remain separate from Hermes'
AGPL-3.0-only code.

## ToolHub Slack data (M5 stage 2)

Use the same protected connector CLI with `--provider slack --workspace T...`
and `--account U...` (or W...); neither email nor display name is an identity.
`--write` creates an independent chat:write grant. Read requests use search:read
and the four conversation-history scopes. OAuth PKCE requests only user_scope;
client authentication uses Basic auth. A bot token co-returned with authed_user
is discarded. Slack App SLACK_BOT_TOKEN/SIGNING_SECRET/APP_TOKEN remain solely
communication credentials and cannot enable these tools.

Read tools are search(query,cursor), history(channel,cursor), replies(channel,ts,
cursor); they request 15 results and expose provider pagination. Write tools are
send(channel,text), reply(channel,ts,text), update(channel,ts,text). Channel IDs
must be canonical C/G/D IDs; thread/message timestamps are validated. No bot
posting or username/icon impersonation is supported. auth.test rechecks the bound
workspace and user on every call. HTTP 429/errors are reported without retrying a
mutation. Review an uncertain send in Slack before retrying. Revoke blocks new
calls locally first, then requests auth.revoke; failed external cleanup is reported.
Protected workflows and HTTP fixtures do not prove a live Slack workspace login.
Official contracts: [OAuth](https://docs.slack.dev/authentication/using-pkce),
[user tokens](https://docs.slack.dev/reference/methods/oauth.v2.access/),
[account verification](https://docs.slack.dev/reference/methods/auth.test/).

| Component | Source / pin | Contract |
|---|---|---|
| Hermes | [nousresearch/hermes-agent](https://github.com/nousresearch/hermes-agent), `869228cab4a8276d3b4c78da9d9939670c47bd0f` (`0.21.0`) | CLI, gateway, config.yaml, MCP, Meet plugin; opt-in authenticated API server |
| Telegram account | [chigwell/telegram-mcp](https://github.com/chigwell/telegram-mcp), `c9460f8ded6e2457bd70ebabfad840b58d23645d` | Python stdio; TELEGRAM_EXPOSED_TOOLS server allowlist |
| Telegram bot channel | Telegram Bot API through `hub-communication` | Channel adapter; sender allowlist and durable reply outbox |
| Slack App channel | Slack Events API through `hub-communication` | Official `v0` HMAC request verification; workspace+sender mapping; not Slack data tools |
| Google | Official `https://<product>mcp.googleapis.com/mcp/v1` (ADR-0019); the third-party Workspace MCP is not in the default hub image | ToolHub remote read grants per product |
| Slack | [korotovsky/slack-mcp-server](https://github.com/korotovsky/slack-mcp-server), `b88c0de3f706f4f07337c9eda7133c736d1c9524` | stdio, OAuth user token, channel posting allowlist |
| Playwright | [microsoft/playwright-mcp](https://github.com/microsoft/playwright-mcp), npm `@playwright/mcp@0.0.80` | stdio, persistent Chromium over local CDP |
| GitHub | [official MCP](https://github.com/github/github-mcp-server) | https://api.githubcopilot.com/mcp/ + bearer (`GITHUB_TOKEN`) |
| Atlassian | [`sooperset/mcp-atlassian`](https://github.com/sooperset/mcp-atlassian), `74bdaa8f1d28783cccfe99f7b4d75e6dc947cf76` | Dedicated `docker/mcp-atlassian.Dockerfile`; Jira Cloud API token via `JIRA_URL`, `JIRA_USERNAME`, `JIRA_API_TOKEN` |
| GitLab | Debian `glab` package from the pinned runtime distribution | CLI; `GITLAB_TOKEN` PAT and optional `GITLAB_HOST` |
| HH | [official API](https://api.hh.ru/openapi/redoc) | GET /vacancies, /vacancies/{id}, /resumes/mine; POST /negotiations |
| Memory Bank | [letya999/memory_bank_setup](https://github.com/letya999/memory_bank_setup), `2eb4e41968b86dae4c192dd9f9cff5b72c9d754f` | Documentation separation and change-folder conventions |

The GitHub row is the opt-in remote MCP only (`https://api.githubcopilot.com/mcp/`
with a `GITHUB_TOKEN` bearer); it is independent of `prepare_source`
self-install, which only inspects the pinned repository as metadata and builds
from the generated `.hub` recipe, never the upstream Dockerfile. The two paths
also keep separate credentials: the remote connector takes `GITHUB_TOKEN`,
while a source install declares its own env inputs (for github-mcp-server the
`GITHUB_PERSONAL_ACCESS_TOKEN`). Interactive OAuth inside a workload is not
supported; instead the isolated preflight promotes a declared secret to a
required input when `tools/list` proves startup needs it, and onboarding
satisfies it through the existing protected form or a Credential Broker
contract — including a broker-provisioned OAuth grant where one is reviewed.

Telegram has two independent paths: `hub-communication` is the Bot API channel adapter,
while `telegram_user` is the personal-account MCP data/action connector. Enabling one does
not enable the other. Telegram account access is installed from its pinned Git repository:
the unrelated PyPI package named telegram-mcp is deliberately not used. Each Python MCP has a separate locked virtualenv
because Telegram and Hermes depend on incompatible MCP major versions. Upstream lockfiles
are honored. The Meet browser supplement has a separately pinned requirements lock.
APT system packages follow the pinned Debian release's repositories and security updates;
this is not a bit-for-bit reproducible OS build.

## Organization and user MCP connections

Standalone users may define `mcp_servers` in their own settings. In organization
scope, only the host-owned `spaces/<org>/scope.yaml` can define MCP servers;
the user can only narrow the approved set with `disabled_mcp`. Every organization MCP
must be listed in `read_only_mcp` and have a non-empty `tools.include` allowlist. The
allowlist is passed to Hermes, so shared organization credentials are reserved for
explicitly selected read-only tools. Write-capable servers need their own upstream
permissions and should not be granted as a shared organization connection. Built-in
hub-owned mutations are separately gated by `org_actions`.

## External MCP connections

Add user-owned connections in settings.yaml, then add secrets independently to the
selected secrets.dev.env or secrets.prod.env. Example external service:

```yaml
mcp_servers:
  career:
    url: https://your-career-host.example/mcp
    headers:
      Authorization: Bearer ${CAREER_TOKEN}
  local_tool:
    command: node
    args: [/workspace/tools/server.cjs]
    env:
      TOOL_TOKEN: ${TOOL_TOKEN}
```

These are examples, not preconfigured endpoints. Remote MCP needs an actual MCP server;
an arbitrary REST API is not automatically MCP. StdIO executables and their dependencies
must exist inside the agent image or selected workspace. No automatic unpinned package
installation is performed. `doctor` detects missing referenced credentials.
Native memory, skills and hooks are described in SETUP.md; user configuration passes
through to Hermes without replacing its extension system. Organization skills use the
upstream `skills.external_dirs` setting and are mounted read-only from the organization
home; same-named user skills take precedence in a user-scoped job.
Connector credentials are supplied by `hubctl secret set` or an explicit owner
`KEY=value` chat message intercepted before Hermes. Values are encrypted at rest
and never returned in catalog, audit or replies. The hub `env_update` tool remains
only for the explicit personal-terminal overlay; it is not a host `.env` editor and
cannot alter organization-owned keys.

OAuth for Google, Slack and Atlassian uses the official authorization and token
URLs recorded in SPEC-0020. Remote MCP discovers `authorization_endpoint` and
`token_endpoint` via RFC 9728 and RFC 8414. Tokens persist only in the encrypted
store. Live provider login is a later milestone.

`communication-hub` never mounts scope homes or receives provider credentials. It sends
the immutable job envelope to `hermes-runtime` over a private Bearer-authenticated HTTP
contract. Runtime binding rejects mismatched user, organization, actor or scope before
opening a path; failed and uncertain results are not blindly replayed.

HeadHunter application requests use form fields vacancy_id/resume_id/message. Only HTTP
201 marks `sent: true`; receipt Location is preserved. A timeout may mean the send
succeeded; inspect negotiations before retrying. OAuth grants, already-applied errors,
required tests and CAPTCHA remain platform responsibilities.

Native bridges forward MCP tools and their schemas; they do not forward resources,
prompts, sampling or elicitation. A native MCP requiring those features is not compatible
with this limited bridge. Remote providers are maintained upstream and can change their
contracts independently; verify them after upgrades.

## Pinned Hermes API contract

The exact image pin is validated before a runtime adapter is built. The reproducible
probe is `go run -tags integration ./cmd/devcheck hermes-contract hermes-hub:test`
(run by `just docker-check`). It starts `hermes gateway run --no-supervise --force`
with an isolated state volume and verifies `/v1/capabilities`, durable
`Idempotency-Key` replay/conflict for `/v1/runs`, run status and SSE events, stop and
approval error semantics, `/api/sessions` resume after restart, `/api/jobs` cron
coexistence and non-terminal run recovery as `interrupted`. It also checks the image's
Hermes version and Git commit; a mock server is never used as evidence.

The pinned capabilities report Bearer authentication, `split_runtime=false`, 24-hour
run-idempotency retention, session continuity headers `X-Hermes-Session-Id` and
`X-Hermes-Session-Key`, and no CORS, admin-config, memory-write, audio or realtime
voice API. Because tools execute on the API-server host, this API is an internal,
opt-in compatibility surface and does not replace the private Go runtime boundary.

Google Workspace reads use the official remote MCP (ADR-0019). The settings
`google` feature still describes the legacy third-party stdio server, which is
not in the default hub image. Atlassian uses the pinned upstream
`mcp-atlassian` stdio server from `docker/mcp-atlassian.Dockerfile`.
For Jira Cloud, the owner supplies `JIRA_URL`, `JIRA_USERNAME` and `JIRA_API_TOKEN`;
the server talks directly to Jira REST APIs and does not use the Atlassian Rovo endpoint.
The upstream server also supports Confluence, but this preset currently enables Jira only.
GitLab uses `glab`, not MCP. Provider content never grants permission to create, edit,
send, merge or trigger operations.
