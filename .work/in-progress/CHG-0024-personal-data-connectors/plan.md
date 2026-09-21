# CHG-0024: Personal Data Connectors (M5 stage 2)

## Objective

Add opt-in personal data connectors behind ToolHub for Google Workspace,
Slack data and a personal Telegram account. Close GitHub issues #37, #38 and
#39. Milestone "M5 - Integrations and Operations" stays open: #40-#46, #73 and
#74 are untouched.

Telegram Bot API and the Slack App remain ingress/delivery in
`communication-hub`. Their bot/app credentials stay there, are never mounted
into Hermes and never become data tools.

## Scope

1. One new ToolHub transport `provider-api`: an in-process official HTTPS REST
   data plane with a declared egress allowlist, bounded output, no child
   process, no container and no host filesystem state. `remote-mcp`,
   `container-mcp` and `bounded-cli` keep their existing semantics.
2. Immutable per-capability manifests. Read and mutation are separate
   definitions with separate OAuth scopes, separate connections and separate
   bindings, so a read-only grant never projects a write tool.
3. Connect through the existing OAuth broker with authorization-code+PKCE,
   official provider URLs, requested scopes and client credentials. The
   returned provider account is verified before binding (Google OIDC userinfo;
   Slack `auth.test` workspace and user IDs).
4. Credentials resolve only by authenticated owner plus connection ID. Secret
   values stay in the ciphertext store; identifiers and granted capabilities
   stay in connection metadata.
5. Refresh and revoke without restarting Hermes. Revoke denies the next call
   on an already-open ToolHub session before backend execution.
6. Mutation receipts: Google returns the event identity, Slack returns
   `channel`+`ts`; both are written to the audit ledger.
7. `hubctl connector` is the protected host path: connect, callback, grant,
   refresh, revoke, status and call. It prints names, statuses, capabilities
   and identifiers, never values.
8. Personal Telegram: stateful per-user account workload, API ID/hash, session
   string, device identity and media/transcript cache are credentials. Bot
   token and account session never share a credential scope. Read-only is the
   default; send/reply/delete are separate explicit capabilities. Groups stay
   disabled. Execution needs the pinned upstream MCP workload behind the
   ToolHive controller receipt. Do not implement #73 in this change. The pinned
   upstream send/reply implementations discard sent message IDs; completing
   provider receipts requires an SDK adapter or a verified upstream contract.
   Owner approved the narrow Python/Telethon adapter on 2026-09-15; its stdio
   workload reuses the pinned environment. Go connect verifies get_me identity,
   scopes write separately and validates receipts. Actual controller deployment
   host is now the owner-selected PC; never replace enforcement with a fixture.

9. Owner-requested official Google Workspace remote MCP read grants: Calendar,
   Gmail, Drive, Docs, Sheets and Slides over Streamable HTTP. Review tool names
   before discovery, snapshot schemas, exclude unknown/write tools, validate
   arguments without remote schema fetches. Use verified email or subject,
   fixed registered loopback callback, existing OAuth refresh/revoke. Google
   preview/project setup remains a real external prerequisite.

## Explicitly deferred

Owner addendum 2026-09-15: local PC preparation is explicitly authorized.
Implement the fixed read-only Go ToolHive controller, separate pinned CONNECT
proxy with published Telegram CIDRs, protected local phone/OTP/2FA login helper,
Google OAuth JSON import and manager instructions. Real Google Preview approval,
user-driven login and concrete provider mutations remain distinct. This is not
a VPS/general deployment or an issue #73 completion claim.

#40 GitLab/Atlassian, #41 SQL, #42 SSH, #43 metrics, #44 CareerGo, #45 browser,
#46 capacity, #73 live ToolHive/VPS, #74 Hermes transport reconnect, a default
runtime switch to ToolHub, ZITADEL, Vault, Kubernetes, group disclosure,
image generation, TTS and Telegram photo bytes. Live Google/Slack/Telegram
account login, external sending and publication need a separate explicit owner
instruction and are not claimed here.

## Compatibility and rollback

Generated MCP stays the default runtime path. The existing `google`,
`google_write`, `slack`, `telegram_user` and `telegram_write` features are
unchanged. ToolHub stays opt-in through `HUB_TOOLHUB_*`; connector manifests
enter a registry only through `hubctl connector`. Rollback is to stop using
`hubctl connector` and leave `HUB_TOOLHUB_*` unset; connections, ciphertext and
bindings can be revoked per capability without deleting a space or Hermes home.

## Verification

Shipped-entry tests only: `hubctl connector`, ToolHub `AuthorizeProjected`,
ciphertext inject and the connector data plane against official-shaped local
fixtures. Mandatory regressions: denial, cross-user, stale/revoked, malformed
resource, prompt-injected connection id, cancellation, bounded output and
communication credentials granting no data tools. File isolation, concurrency,
authorization and HTTP-contract changes carry regressions.

Gates: `just check` with own Go statement coverage >=85%, `just security` when
dependencies change, `just docker-check` when a Docker runtime is available.
Evidence records commands, pins and fixture-versus-live status per closed issue.
