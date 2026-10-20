# CHG-0024 partial implementation evidence

Branch: feat/m5-personal-data-connectors, base origin/dev d79fba5.
Issues #37, #38, #39 were read using gh issue view; all remain open.

## Implemented, not published

Calendar: protected loopback OAuth/PKCE connect, expected Google subject
verification through OIDC userinfo before binding, independent read/write
manifests/scopes, list/get/create/PATCH/delete, attendee replacement, exact-owner
credential injection, manual refresh and immediate local revoke before Google
cleanup. Calendar v3 source URL and OAuth/OIDC endpoints are immutable official
contracts; no new dependency. Gmail is optional and not implemented.

Slack data: protected user_scope PKCE OAuth; client_secret_basic; capture only
authed_user credentials, discard co-returned bot credentials; bind expected
workspace and user IDs against OAuth metadata and auth.test. Recheck account
on each call. Independent read/write manifests; search/history/replies and
send/reply/update, bounded output, validated channel/timestamp, receipts returned
and audited. Refresh and local-first auth.revoke. Slack App is unchanged.

Registry snapshot writes use the existing flock dependency and a loaded-content
digest precondition. Stale writers fail instead of losing a concurrent revoke.
CLI token lifecycle locks are connection-specific and OS-backed. A conflict
requires reload/retry; never retry an uncertain external mutation blindly.

## Commands and observations

- go test ./internal/oauth ./internal/toolhub ./internal/audit: passed.
- go test ./cmd/hubctl -run TestConnectorCLI -count=1: passed.
- go test ./cmd/hubctl -run TestSlackConnectorCLI -count=1: passed.
- go test ./internal/toolhub -run 'TestPersonalProvider|TestSlack|TestCalendar' -count=1: passed.
- just check: final rerun passed, own Go statement coverage 85.14%.
- govulncheck: final rerun passed, no vulnerabilities found.
- Gitleaks git and working cmd/internal scans: no leaks found.
- TruffleHog git scan: no verified or unverified secrets found.
- Opengrep cmd/internal: passed. Whole-repo scan failed when traversing local
  ignored spaces; scoped source scan avoids personal data.
- npm is absent on host. Go dependencies and lockfiles have not changed.
- just docker-check: initial Calendar implementation snapshot image
  sha256:e4c7edfc6a3eb9d2d9ada74c46492948129a3d0fb0c17e1ff410b7a7e8b2e745
  built; standalone smoke and real pinned Hermes native contracts passed,
  including the five-minute idle shutdown and original-session cold restore.
  Shipped hub-stt probe reported unavailable on that Windows-built image
  (exec /usr/local/bin/hub-stt: no such file or directory). This is not fixed
  or claimed as successful. The image predates final Slack/registry changes;
  this is not final M5 image evidence.

All Google/Slack calls above are official-shaped local HTTP fixtures, not
live account login or live sending. Native Hermes uses the repository pin
869228cab4a8276d3b4c78da9d9939670c47bd0f, 0.21.0; synthetic model/transport
fixtures are distinct from the real upstream runtime.

## Remaining blocker

Telegram upstream pin c9460f8ded6e2457bd70ebabfad840b58d23645d:
telegram_mcp/tools/messages.py send_message and reply_to_message await Telethon
send_message but discard the returned Message. Their strings do not provide
the sent message ID. No receipt has been fabricated. A narrow SDK adapter needs
an explicit decision on the project Go MCP tooling rule, or a verified upstream
with an actual receipt contract. No Telegram implementation is claimed yet.
Groups, live account login/sending, #40-#46, #73/#74 and default runtime switching
remain untouched. No commit/push/PR/issue closure or milestone closure occurred.

## Owner-requested continuation, 2026-09-15

The owner approved Python/Telethon in ToolHub and requested official Google
Workspace MCP. The SDK permission blocker above is resolved, not current.

- Official Google-managed remote MCP read products: calendar, gmail, drive,
  docs, sheets, slides. Connect uses existing OAuth with fixed optional callback
  port and expected verified email or subject. Review allowlists before discovery;
  snapshot schemas and locally validate arguments without remote $ref loading.
  Unknown/mutation tools stay excluded. Calendar write REST grant is retained.
- Telegram stdio account adapter reuses the pinned upstream Python virtualenv.
  No Hermes/upstream patch or new SDK dependency. get_me verifies numeric owner,
  bots/groups/channels are denied, read default and independent write grant.
  Send/reply return provider Message.id, delete requires own direct target and
  pts acknowledgement. Reply/delete also verify target.chat_id, because private
  message IDs must not be treated as proof that a requested peer owns the target.
- Go protected Telegram connect imports only API ID/hash/session, obtains
  controller admission and verifies the live-shaped account response before
  saving a binding. Local revoke blocks next calls; imported provider sessions
  must be explicitly terminated via Telegram Devices for provider invalidation.
- Workload IDs and directories now include principal plus connection. OS-backed
  inject locks serialize credentials until the call completes and wipes files.
  Controller plan adds exact workspace_path, not secret values. The controller
  must map that path to /run/connector with correct private ownership and enforce
  the declared limits. Missing admission still fails closed.

Verification:

- just check final Go rerun: passed, own statements 85.22%, race/static/docs/CLI.
- just security: passed, govulncheck no vulnerabilities, npm audit 0 vulnerabilities.
  npm was unavailable in the initial doctor snapshot but resolved for this gate.
- Ruff and Bandit on the new account adapter: passed. Removed unused HTTP bind
  configuration; the adapter is stdio only, not a public unauthenticated listener.
- Opengrep scoped cmd/internal/new Python source: passed.
- Gitleaks working source scan: no leaks found.
- Actual pinned Python virtualenv Docker test with --network none/read-only:
  four unittest checks passed: account/bot mismatch and disconnect, read surface,
  actual SDK-shaped receipts, read/write/resource denial, cross-peer targets.
  The pinned environment emits a nonfatal pydantic_settings lifespan annotation
  warning; upstream was not patched.
- TestTelegramPythonContainerStdio: passed with real built adapter image
  sha256:087e162e855cc5dd24eacaabc1b497fc3b34c4488f271dcc36ff87510ff005bd.
  Real Python MCP initialization/tools-list, read-only server exposes exactly
  get_account/list_dialogs/get_messages, no writes. Final adapter rebuild follows
  the cross-peer hardening; this initial image is protocol evidence, not live
  account or live ToolHive enforcement evidence.

Final adapter image rebuilt after cross-peer hardening:
sha256:1a458beae6cd1ea62021f7a5900036ce6a581973171c96e46e0c74ff0de6aeef.
TestTelegramPythonContainerStdio rerun against this exact image passed.

Remaining live setup: choose deployment host; implement/configure and exercise
the actual ToolHive controller/vMCP with real image/sidecar pins. No thv binary or
configured live admission service was found on this host. Google needs preview
eligibility, Cloud APIs/MCP services, consent screen and protected OAuth client.
Only after those prerequisites should the owner complete Google consent or
import a Telegram session through the protected host file path. Do not ask for
session strings/tokens in chat. The deployment-target question is awaiting an
answer; no real account login, sends, publication or issue closure occurred.

## Owner-selected local PC preparation (supersedes the host-selection blocker)

ToolHive v0.48.0 Windows portable binary installed from the official release;
ZIP checksum verified against the official checksums.txt:
1f38bb3153a0dbac52bef8d630347e581591923dcea7a56a5442a976daa230d2.
Commit c25e508592bc3bcfa10dea99af04daaf341db95a. Private XDG directories
avoid editing existing MCP client configuration. No auto-update was performed.

Physical user-home deployment is outside the checkout, user/SYSTEM-only ACL.
AppData staging did not expose the same files through Docker/WSL, so it was
not used for the working account mounts. No real account input existed there.
Removed only freshly created failed preparation workers/proxy/network and
the temporary no-account probe; unrelated containers and supervisor remained.

Actual local adapter image:
sha256:b5555940ce8624fdff73f74613009ef6692d2e504dbc619b6d283af4b9044784.
Already hash-locked python-socks 3.0.0 upstream proxy extra enabled with frozen uv.
Actual pinned proxy image:
ghcr.io/stacklok/toolhive/egress-proxy@sha256:72a43857af69e602bdc1285bbb074e829ae967c1ede4144377ddeb048bacb1be.
No latest reference is used for execution. Reviewed Telegram CIDRs came from
https://core.telegram.org/resources/cidr.txt, not assumed DNS rules.

Real ToolHive `mcp list tools` returned exactly get_account, list_dialogs,
get_messages. The tools flag is StringArray: regression asserts repeated flags,
not one comma-containing name. Real authenticated local controller POST returned
running/enforced proof after Docker image, CPU 1000m, memory 512MiB, PIDs 64,
exact mounts and internal network/config inspection. Swap capped at 512MiB.
Real ToolHub authenticated loopback initialization passed with an empty catalog.
The no-account network probe passed: CONNECT to the pinned SDK's default Telegram
DC allowed, CONNECT to another public IP rejected, direct TCP blackholed.
This proves preparation only; no real account login, reads, sends or revocation.

Manager setup launcher installed outside the checkout. Google JSON is imported
only locally. Telegram phone/OTP/2FA input is hidden in a separate user-operated
container mounted only to Telegram input, never Google input or key files.
The session is not printed; Go verifies identity before staged registry commit.
Wrong-account fixture regression leaves the existing registry byte-identical.
Google callback success is reported only after its staged snapshot is committed.

Mandatory repository gates are rerunning on the final local-preparation snapshot.
Ruff and Bandit passed both account adapter and login helper. `just security`
passed govulncheck and npm audit; these gates do not imply a live provider grant.

Real fresh-workload regression TestLocalControllerDockerPreparation passed after
fixing ToolHive's asynchronous start boundary: `thv run` returns before Docker
creates the main container. Go now waits bounded/cancellable for creation,
applies limits, then waits for actual running inspection proof. No-account
fresh worker discovery returned the three reads; get_account failed closed
without a credentials.env. Only fresh check resources were cleaned up.

Final Go snapshot: `just check` passed, statement coverage 85.12%; `just security`
passed (no Go vulnerabilities, npm audit zero). Five Python adapter regressions
passed inside the exact pinned account image. Scoped Gitleaks found no leaks.
Opengrep reported the controller's dynamic exec audit finding: reviewed callers
use fixed operator-owned executable/arguments, not request/model command input.
Fresh real cold-start Docker regression reran successfully (13.76 seconds).
Installed private Go binaries were rebuilt and their hidden services restarted;
actual authenticated admission returned running/enforced and ToolHub init HTTP 200.

`just docker-check` completed on the earlier local-preparation image snapshot:
native pinned Hermes contract and real five-minute gateway lifecycle passed.
The faster-whisper shipped hub-stt executable probe was unavailable (no such file
or directory); no STT success is claimed. Final CLI/controller snapshot is being
rebuilt and smoke-checked separately; the native contract is not mislabeled as
having run against that later image. No actual account login or provider data read.

After the history-access correction, final `just check` passed again with 85.14%
Go statement coverage, race tests, vet, staticcheck, docs and actionlint. The
actual local workload was restarted on the new image and returned an enforced
admission receipt; no account credentials were present during these checks.

Final Docker image 9a97e662f605 built successfully; standalone Docker runtime
smoke passed with no live provider calls. Private Telegram workload remains on
its separately pinned b5555940 image; existing deployments were not switched.

Owner-requested history correction: researched user-session Telegram MCPs and
confirmed MTProto/Telethon is the correct authority (not Bot API). The adapter
now exposes five read tools: account, paginated dialogs, paginated old/new
messages, global/per-peer server-side search and chat metadata. Groups/channels
are accepted using Telegram peer IDs; bot peers are rejected. Media is reported
as metadata and not downloaded. Python unit tests passed; the rebuilt pinned
local image is sha256:930314e465d1b47aaa8ef7adb55ff359c7f378cee4a7995242a249843104d120.
Actual ToolHive discovery returned exactly those five tools; authenticated
admission returned running/enforced for that image. No account login or provider
message read was performed.
Ruff and Bandit re-ran on the expanded adapter/login/test files and passed.
