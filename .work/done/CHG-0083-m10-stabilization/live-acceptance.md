# Real-account acceptance — 2026-10-11

The owner explicitly authorized real Telegram and provider-account checks through
the bot. Existing credentials were used inside their running containers; values,
personal messages and account identifiers were not recorded here.

## Measured results

- Telegram Bot API `getMe`: success, authenticated identity is a bot.
- Authenticated model discovery through the existing CLIProxy: success.
- One synthetic completion using `gemini-3.1-flash-lite`: HTTP 200, exact
  expected test marker, usage metadata present. No tools or private input.
- Existing services were not restarted and bot polling was not duplicated.

These checks prove current service connectivity. The existing gateway and runtime
use the main checkout's previous image, so these results do **not** prove M10
remediation or its Telegram flow.

## Remaining end-to-end checks

Select the owner-approved test audience and authenticate a Telegram sender.
The available browser inventory contains no authenticated Telegram tab.
Use a separate test bot/profile where available; never start two pollers for
the same token. If the existing bot is selected, establish a controlled handoff
to the new gateway before executing commands. Record the running code/image,
retain the old deployment for restoration and avoid replaying the live spool.

Send only synthetic test content: deterministic commands, one bounded model
request, incoming/outgoing files, two images, a reminder, and an approval.
Compare usage and runtime activity before/after deterministic operations;
verify the same audience receives the result and duplicate callbacks do not
repeat work. Verify idle shutdown and session restoration on the new runtime.
Additional provider checks must use existing configured accounts and reviewed
interfaces; do not broaden grants or alter business data for acceptance.

Real inbound/outbound Telegram delivery, inline approval, attachments, reminders,
M10 usage accounting and idle lifecycle remain unverified at this point.
