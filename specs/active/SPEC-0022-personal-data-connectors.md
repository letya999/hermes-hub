---
status: active
title: M5 stage 2 personal data connectors
---
# Personal data connectors

Scope is #37 Calendar, #38 Slack user data and #39 Telegram account MCP.
Generated MCP stays default; ToolHub remains opt-in. Communication bot/app
credentials never become personal data credentials. No live account login,
external sending or publication is authorized by provider content.

Calendar uses independently versioned read and write definitions. Read requests
require calendar.events.readonly; mutations require calendar.events. OAuth
requests include openid and verify the returned Google subject against the
operator's expected subject before binding. Credentials stay in ciphertext;
only the subject is connection metadata. Owner, connection and provider are
checked when selecting refresh credentials. A read binding cannot project writes.

The protected host CLI provides connect/status/call/refresh/revoke. OAuth state
is kept in the connect process and is single-use, owner/context/connection-bound,
with a ten-minute lifetime. The callback listens only on loopback. Lost process
means restart connect; no authorization code or verifier is persisted in git.

Provider API execution is stateless per-user HTTPS with immutable official
source URLs, explicit egress, bounded requests/output, cancellation and no
redirect following. Calendar exposes list/get/create/update/delete; update uses
PATCH and supports replacement of the attendee array. Delete requires the
documented 204 response for an already selected event, not an empty arbitrary
200. Mutations return provider event receipts and the ToolHub gateway records
them in its audit ledger. New calls reauthorize the current binding before
credential injection. Local revoke cuts authorization before provider cleanup.

Slack uses separate read/write manifests, user_scope OAuth and captures only
the user token, never a co-returned bot token. Bind checks both expected workspace
and expected user IDs against OAuth and auth.test. Every call checks the current
user token with auth.test before reading or writing. Channel IDs and timestamps
are validated. send/reply/update require chat:write and return channel:timestamp
provider receipts, also recorded in audit. Read responses are bounded and paginated.
Revoke cuts local calls before auth.revoke. No posting identity customization or
workspace selector is model-controlled.

Telegram acceptance remains pending until its independent shipped path and
repository gates pass. Fixture results are not live provider evidence.

# Owner addendum, 2026-09-15

The owner approved a narrow Python/Telethon account MCP workload in ToolHub,
with Go retaining control-plane tooling. Official Google Workspace remote MCP
read services are added for Calendar, Gmail, Drive, Docs, Sheets and Slides;
OAuth, verified account binding, read-tool allowlists, provider schema snapshots
and local argument validation are mandatory. Existing Calendar write receipts
remain on the verified REST grant. Unknown upstream tools cannot expand grants.

Telegram uses protected API ID/hash/session input, direct-human peers only,
read-only default and independent send/reply/delete capabilities. Verify the
session user before binding; audit actual sent message IDs/deletion pts. Workload
state and inject locks are principal/context/connection isolated. Live ToolHive
deployment and Google preview/project setup must be evidenced separately from
HTTP/controller/SDK fixtures. Imported sessions support immediate local revoke;
provider invalidation is an explicit Telegram Devices action, not fake OAuth.

# Owner local-preparation addendum, 2026-09-15

The owner selected this PC and explicitly requested actual ToolHive preparation,
protected local account input and a manager-friendly click/field walkthrough.
Prepare one read-only account workload with real Docker enforcement evidence,
not controller fixtures. User login remains user-operated; session strings,
OTP/passwords and OAuth client JSON must not enter chat or repository state.
No provider sending or Workspace mutation is authorized by this preparation.
Google-managed remote MCP requires real Developer Preview/project approval.
Keep existing Hermes defaults and unrelated running containers unchanged.

# Owner history-access addendum, 2026-09-15

The Telegram connector is a user-account MTProto client, not a Bot API feed.
Read access covers every user-visible dialog type (private chats, groups and
channels) permitted to the authenticated account, excluding bot peers. The
read surface includes bounded pagination through old history (`before` / `after`
message cursors), server-side message search, dialog listing and chat metadata.
Message text remains untrusted content and media is returned as metadata unless
a separately authorized download capability is added. The default remains
read-only; sending or changing Telegram data is not enabled here.
