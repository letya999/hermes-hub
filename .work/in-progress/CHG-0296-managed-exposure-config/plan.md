# CHG-0296: Managed exposure configuration

## Goal

Make an admitted exposure tool usable in a supervisor-spawned managed runtime
without broadening its endpoint authority. The owner reported
`exposure capability is not configured on this runtime` from `exposure_list`.

## Release barrier follow-up

- One `RuntimeConnections` description now feeds Compose mounts, runtime env,
  the stdio hub env, and supervisor mounts. ToolHub admission stays separate.
- Render and supervised spawn validate enabled SSH/exposure config and each
  file credential for the container uid/gid before starting. Disabled features
  add no mount or env.
- The existing managed-supervisor Docker/CI gate now makes a read-only
  `exposure_list` call through real ToolHub admission into its synthetic
  supervisor-spawned runtime. The fixture declares one endpoint and never
  opens a tunnel.

## Scope

The positive tunnel acceptance adds an exact sish relay CONNECT exception to
the managed egress relay. A synthetic static site gets a separate
`germi-test-site` endpoint. The live sequence is list, open, external HTTPS
content check, close and verified disappearance; the existing `onboarding`
endpoint stays untouched. A temporary local sish relay and Cloudflare Quick
Tunnel can prove the live path without claiming a stable native public name.

- Mirror the reviewed exposure config's read-only mount and env path in the
  supervisor spawn contract. Apply the same missing connection mount/env
  correction to SSH, whose supervised path shared the omission.
- Return sorted declared endpoint names and target classes from
  `exposure_list`; keep live tunnel status separate and omit private relay and
  credential metadata.
- Update the agent-tool description, prompt guidance, docs and immutable
  catalog; rebind and respawn the managed dev runtime.

## Acceptance

- Regression tests fail before and pass after the managed env/mount and
  declared-list fixes. No unselected connection directory is mounted.
- `just check` and affected `just test` pass after the final edit.
- The actual WSL dev runtime sees the reviewed config, and a live admitted
  `exposure_list` returns declared names with no tunnel created.
- A test site is opened only if the published config declares a distinct safe
  endpoint; onboarding is never used as its substitute.

## Evidence

The named regressions, `go vet`, `just check` and affected `just test` passed.
The ToolHub, supervisor and managed runtime were rebuilt as scoped dev images
from an Ubuntu ext4 source snapshot. The new catalog was bound at 18.3.12.
The fresh runtime sees `/state/exposure/config.yaml`; a private executor call
returned one declared endpoint (`onboarding`, `hub_public_listener`) and zero
live tunnels. A real model run through the mock channel reached the same
endpoint list and declined to open onboarding. The first model reply misstated
the count as two while listing one; a second mock run re-read the list and
correctly reported one. Both jobs have ToolHub audit pairs for `exposure.list`
with admitted and completed/ok. The config directory and its files were made
readable to runtime gid 0 while retaining 0750/0640 permissions. Native sish
`exposure_create` was initially untested because the configured relay host and
public domain returned NXDOMAIN without the local sish container.
A synthetic private site returned HTTP 200 with the expected marker from a
second container and was removed. From the real managed agent network, the
egress relay returned 403 for an unrelated CONNECT target, 403 for plain HTTP
to the sish target, and 502 for the exact reviewed CONNECT target because its
name did not resolve. The SishProvider regression forwards through an HTTP
CONNECT proxy and rejects proxy denial without direct fallback. Render and
supervisor spawn reject a managed sish config without a declared per-space
egress-relay.

An isolated static site was attached to the managed agent network as
`germi-test-site` and published only on WSL loopback. A pinned Cloudflare
`cloudflared` 2026.10.5 binary (SHA-256 matched the official release) opened a
temporary Quick Tunnel. Local HTTP and public HTTPS both returned 200 and the
same synthetic marker. While the site was live, the reviewed exposure config
declared `germi-test-site: service:germi-test-site:8080`; a fresh model run
called `exposure_list` through ToolHub and saw exactly two endpoints, with no
open native tunnels. The Quick Tunnel, site container and test declaration were
removed; the public URL then returned 530. The running agent initially kept
the old endpoint snapshot, so its idle managed runtime was respawned through
the authenticated supervisor endpoint. A final model call saw only
`onboarding` and zero tunnels. This verifies external reachability and the
ToolHub list projection, not a native `exposure_create`/`exposure_remove` cycle.

The native cycle was then exercised separately. An isolated sish container from
the reviewed pinned image, a static site, and a temporary pinned host key were
bound on the existing agent network; the reviewed config declared only one new
`germi-test-site` endpoint. The managed egress relay returned `200` for the
exact sish CONNECT target and `403` for foreign CONNECT and plain HTTP. After
an idle-runtime respawn, a real model through the mock channel called
`exposure_list` and `exposure_create(name=germi-test-site)` once. ToolHub
returned the declared service target, the native public name and
`uncertain=false`. The local sish HTTP route returned `200` with the marker.
Cloudflare Quick Tunnel, configured with the native Host header, served the
same marker over public HTTPS (`200`) at a temporary `trycloudflare.com` URL.
A second model job called `exposure_remove` and `exposure_list`, which showed
zero live tunnels; the old public URL no longer returned the marker. The Quick
Tunnel, static site, sish container and test declaration were removed, the
exact original config restored, and the managed runtime respawned while idle.
A final model call reported only `onboarding` and zero tunnels. The temporary
Cloudflare URL is not the native `.local.test` public name; stable native
addressing still requires a real relay and DNS.
ToolHub's rotated audit ledger confirms admitted/completed-ok pairs for
`exposure.create` at 12:24:23–24 UTC and `exposure.remove` at 12:25:48 UTC,
plus the before/after `exposure.list` calls. `just check` and `git diff --check`
passed after the evidence update.

The managed-supervisor canary in the existing Docker/CI gate now executes
`exposure_list` through real ToolHub admission and the private executor in a
synthetic supervisor-spawned runtime. It verifies one declared canary endpoint
and zero tunnels, then completes the isolation, restart, rollover, second
context, and cleanup checks. The local run passed against the scoped dev
runtime image; remote CI acceptance awaits published source.
