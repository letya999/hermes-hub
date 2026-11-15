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
4. Initially retain bound credentials and Telegram/proxy auth while the owner
   decides whether to accept loss of external connector data. Do not invent a
   Broker contract to migrate old local references.
5. Verify the active dev space with doctor/render, main local mock E2E and
   repository checks. Record remaining policy and migration limits exactly.
6. Under the owner's explicit loss-accepting follow-up, delete only exact
   unused Hermes Docker volumes after comparing all container mounts and
   running Compose declarations. Reset the active local gateway spool volume
   only after checking empty live queues; restore its dev image and endpoint.
7. Under the owner's subsequent explicit instruction, retire external
   connections and installed CLI/MCP in all spaces. Revoke and delete Broker
   credentials with the control API; stop ToolHub while atomically removing
   external bindings, connections, references and onboarding state from its
   registries. Republish managed profiles where policy versions changed, then
   retire superseded capability history so every external definition can be
   removed without changing current managed catalogs. Remove MCP/CLI config
   wishes, external env fields, browser profiles, local connector ciphertext,
   cached connector artifacts and exact project-owned artifact images.
   Recreate services retaining old external env and verify system bindings,
   Telegram bot configuration and core gates.

## Boundaries

Do not read secret values into diagnostics or commit local state. Do not infer
provider contracts, log into accounts, send messages or use a different route
to delete files rejected by automatic approval review. Keep core model relay
auth and Telegram bot auth outside the external connector retirement.
