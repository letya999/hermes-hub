---
description: Environment selection, data recovery and operating the standalone agent.
last_verified: 2026-09-07
---
# Operations

Use hubctl with --user and --env dev|prod. The default environment is prod; --dir may
point at an explicit user-space directory instead. up renders/builds, prepares volumes
and waits for health. down stops containers and retains volumes. logs tails output.
exec forwards explicit command arguments to the chosen container, for example:
`hubctl exec --user artem --env dev -- sh -lc 'cd /src && go test ./...'`.

Change settings, then run up. The runtime copies the generated Hermes config at startup;
existing memory and installed skills/hooks persist. SOUL.md initializes a new runtime's
instructions; edits made inside an established Hermes home remain there. To replace it,
explicitly copy the owner's new SOUL into that selected container and restart.
Never run hermes update in prod: update reviewed source pins and rebuild instead.

Settings and source code are shared between the user's dev/prod selections, but env
files and runtime volumes are separate. Test changes in dev, review, then rebuild prod.
For a deployment with stronger release isolation, use a tagged source checkout for prod.
Do not copy production tokens into dev just to make a test pass. All automated tests
are credential-free. Dev uses browser_port+1 and oauth_port+1; reserve both ports per user.

Back up all private user-space files and both selected runtime volumes to encrypted
storage while their containers are stopped. `docker volume ls` identifies project-scoped
volumes; use Docker's volume backup procedure or docker cp from a stopped container.
Do not use down --volumes unless intentionally deleting that user's persistent data.
On restore use the same Compose project identity or explicitly restore into its new
volumes. Never merge two people's memories, Telegram sessions or Google credentials.

Health checks measure supervisor/process liveness and Chromium CDP readiness. They do
not prove OAuth or model access. If a connector fails, inspect its logs and verify its
account login. Google login needs the exact redirect URI and SSH tunnel; personal
Telegram sessions must be regenerated if revoked. Meet needs login, admission and captions.

On a VPS keep OAuth/noVNC on loopback and forward the configured ports over SSH. Native
bridges use a separate bearer token over VPN/SSH and never public plaintext ingress.
A GitHub Runner is a separate CI machine; do not put untrusted pull-request jobs on your
personal agent host. The manual runner workflow only accepts the trusted main branch.
