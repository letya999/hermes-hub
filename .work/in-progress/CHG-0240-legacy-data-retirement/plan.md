# CHG-0240: Local legacy data retirement

Date: 2026-10-31. Branch: `chore/storage-reliability-integration`.

## Scope

1. Inventory local space state by fixed safe categories without emitting
   credential values, dictionary keys, auth material or audit events.
2. Remove aged ignored `.log`, `.old`, `.bak` and `.tmp` files outside the
   previously policy-blocked local paths, only after workspace confinement,
   regular-file, age and backup-counterpart checks.
3. Convert readable legacy `settings.yaml` spaces to `agent.yaml` and
   `workspace.yaml`; verify equivalent parsed settings before removing each
   source. Keep an unreadable space that contains auth files for separate
   recovery.
4. Keep currently bound local credentials and current Telegram/proxy auth
   material. The old local credential references lack reviewed Broker
   contracts, so automatic re-anchoring is not an available migration.
5. Verify the active dev space with doctor/render, main local mock E2E and
   repository checks. Record remaining policy and migration limits exactly.

## Boundaries

Do not read secret values into diagnostics or commit local state. Do not infer
provider contracts, log into accounts, send messages or use a different route
to delete files rejected by automatic approval review.
