# CHG-0069 Session UX and provider quota

## Scope

Follow-up to CHG-0066..0068 driven by live Telegram acceptance: make the
task/session model usable as a real chatbot session manager and make /usage
honest about provider limits.

## Changes

- Command inventory: unknown slash commands answer with the supported list and
  never reach the model; `/new [name]`, `/sessions`, `/topic`, `/help` added.
- Task registry: nameless `/new` creates an auto-named task (`name_auto`);
  after the first exchange the upstream session title is adopted once and a
  notice is delivered. Explicit names are never overwritten.
- `/task delete [id|name]` removes the registry record and routing state
  (upstream session kept for audit); `/task archived` lists retired tasks;
  `/tasks` and `/task` show creation dates.
- Group forum topics: allowed users' group messages are processed (secrets
  still deleted), `is_topic_message`/`message_thread_id` resolve the task,
  replies use `message_thread_id` for negative chat ids and
  `direct_messages_topic_id` for private chats. Deliveries accept negative
  chat ids; group jobs deliver to the group, not the sender DM.
- `/usage` renders session title/start time plus measured subscription limits
  via CLIProxyAPI management (`auth-files` -> `api-call` ->
  `retrieveUserQuotaSummary`); five-minute cache, `недоступно` on failure,
  disabled without `HUB_CLIPROXY_MGMT_URL`/`_KEY` env. OAuth tokens never
  leave the proxy.

## Verification

- `go test ./internal/communication/` — green incl. new session-UX tests.
- `just check` — pending.
- Live: rebuild image, `hubctl up -env dev`, verify /new, /sessions,
  /task delete, /usage quota lines, unknown-command help in Telegram.
