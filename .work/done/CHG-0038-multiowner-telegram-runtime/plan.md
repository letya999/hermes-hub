# CHG-0038: Multi-owner Telegram runtime connectivity

## Goal

Enable the second Telegram owner through the existing reviewed multi-user mapping while preserving per-owner runtime, ToolHub, Broker and state boundaries.

## Changes

- Attach supervised Hermes to the selected owner's Compose network.
- Mount only that owner's Broker runtime secret volume, read-only.
- Keep the communication mapping and supervisor authentication in the private local deployment directory.
- Reuse the already-authorized shared model proxy for this test; keep owner workspaces and Broker runtime keys separate.

## Validation

- Regression test proves two owners receive different Compose networks and Broker volumes.
- Run supervisor and communication package tests plus `just check`.
- Validate generated Compose and `hubctl doctor` for the second owner.
- Verify the bot gateway starts with the reviewed two-owner map and a supervised runtime endpoint; do not report live Telegram success until the owner retries `/start`.
