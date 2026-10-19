# CHG-0105 — Broker state drift self-heal + agent guidance

Live Telegram smoke of M12/M13 exposed a ToolHub ↔ Credential Broker state
drift: onboarding confirm died on `credential broker: not_found (404)` because
rotation revoked a grant the broker no longer holds (ledger reset / external
volume). Two minted credentials were orphaned; `active` connections pointed at
dead broker objects. Separately the agent mis-described routine capabilities
(was/now evidence exists but was unknown to it) and catalog vs enabled state.

## Changes

- `brokerclient.IsNotFound(err)` — typed 404 detection (`APIError`).
- `materializeBinding`: revoke of the previous grant tolerates not_found —
  absent is the post-revoke state.
- `cutAuthorization`: same tolerance on credential revoke.
- `refreshBrokerRequest`: dead request record → fresh request minted; dead or
  non-active recorded credential → onboarding resets to awaiting-creds and a
  fresh request is issued (rotate target kept for broker lineage when the
  credential still exists).
- `ensureBrokerRequest`: dead recorded request → replace (same as
  expired/canceled); rotate target that vanished or is deleted is dropped —
  the broker denies rotation of unknown credentials.
- `admitWithBrokerCredential`: temporary grant revoke tolerates not_found.
- Tool descriptions: `routine_create` documents was/now evidence delivery,
  `daily_limit`, quiet windows, `notify_mode=agent`, 1-minute cron floor;
  `service_catalog` clarifies available vs usable; `prepare_source` routes
  resend-the-link requests to `status`/`required_credentials` instead of a
  fresh onboarding.

## Tests

`internal/toolhub/broker_stale_test.go`: dead grant revoke in rotation,
missing credential in cutAuthorization (+ transport outage must still fail),
dead request heal in both refresh and ensure paths, dead credential heal,
dead rotate target, vanished temp admission grant.
`control_mcp_test.go`: prepare_source resend-guidance contract.
