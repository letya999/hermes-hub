# SPEC-0044: Communication Hub Telegram authentication

Status: frozen

The `telegram_auth` feature builds an optional Communication Hub variant with
Go MTProto QR login in the gateway process. The standard image has no MTProto
runtime code. A signed ToolHub prepare outcome for the reviewed
`telegram-session` contract creates a short-lived, random invitation delivered
to the verified owner's private Telegram channel. The invitation opens only on
the loopback-bound Communication Hub browser endpoint and is pinned when the
owner clicks Start, so a link preview cannot claim it. The browser supplies
API ID/hash and, when Telegram requires it, the two-step password. Telegram
account identity and QR material are shown
only in that browser. The owner confirms the account ID before transfer.

The gateway holds the fresh authorization in memory for at most ten minutes.
It sends API ID, API hash, Telethon-compatible StringSession and numeric account
ID only to Credential Broker using a signed `broker:approve` request. Broker
accepts that route only for the same owner, a pending unexpired request and the
`telegram-session` contract. Replays, other contracts and other owners fail.
The gateway must not persist or log the session, QR token, API hash or password.
After transfer the existing ToolHub admission and read verification apply.

No account login occurs until a user opens the invitation and supplies their
Telegram API credentials. The plugin does not grant Telegram write tools.
