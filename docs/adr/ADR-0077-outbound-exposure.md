---
description: Outbound exposure primitive — named public endpoints over opt-in per-binding tunnel workloads; cloudflared is the reference provider, sish the defined self-hosted path; run tokens via Broker, name-only agent surface, bounded and audited.
last_verified: 2026-10-25
---
# ADR-0077: Outbound exposure primitive — named endpoints over tunnel workloads

Status: Proposed — design for issue #145 (M18). Evidence:
`.work/in-progress/CHG-0165-exposure-primitive/research.md`.
Downstream: #146 consumes D6 (who terminates TLS); #147 builds on D1/D2;
ADR-0054's deferred live acceptance unblocks once the first tunnel is admitted.

## Context

Every "publish my thing" feature collapses to one primitive: an outbound-only
connector mapping a public URL to a local port — no inbound firewall ports.
The fork that matters is managed edge (Cloudflare/ngrok) vs self-hosted relay
(frp/sish/zrok), because it decides who terminates TLS and therefore what
#146's DNS/certificate layer must do.

Research outcomes:

- `cloudflared` (Apache-2.0): a remotely-managed tunnel runs from one run
  token that can *only* run its own tunnel — public hostnames attach via the
  account API under an operator credential, so a workload holding its token
  cannot mint or steal names. TLS terminates at the Cloudflare edge (the
  provider sees plaintext). Named hostnames need a domain on Cloudflare DNS.
- `sish` (MIT): client is a plain `ssh -R` against an operator VPS with
  wildcard DNS + LE; per-user pubkey auth — but any authenticated key can
  claim any free subdomain on the served domain, so name isolation is a hub
  responsibility, not a sish primitive.
- `frp` (Apache-2.0): one shared server token authenticates all proxies;
  per-user auth needs OIDC. Kept as the TCP-capable fallback.
- ngrok (closed agent, no free TLS endpoints), zrok (OpenZiti self-host
  weight), bore (no auth/name routing) and trycloudflare quick tunnels
  (ephemeral names) are rejected for the primitive.

## Decisions

- **D1**: Exposure is a named mapping `endpoint name → declared internal
  target`, admitted as a per-binding tunnel workload on the existing
  connector admission path (workload-controller receipt, owned network,
  pinned egress, finalizer removal). There is no hub-resident public proxy.
- **D2**: The agent surface is name-only: `exposure_*` tools act on endpoint
  names declared in reviewed connection config. Arbitrary public hostnames,
  ports or targets fail closed — same shape as declared ssh hosts and
  `tcp_forwards`.
- **D3**: Credentials stay behind the Broker. Run credentials (tunnel token,
  SSH key) deliver into the workload env-file; provider admin/API tokens
  (e.g. the CF account token that attaches hostnames) are operator-scoped
  and never enter user workloads.
- **D4**: One provider contract, two profiles. `cloudflared` is the
  reference provider (managed path): per-exposure run token, hostnames
  attached by the hub control path via account API. `sish` is the defined
  second provider (self-hosted path): the hub must allocate user-scoped
  names — per-user subdomain zone or TXT-verified custom domain — because
  sish does not isolate subdomains per key. `frp` remains the recorded
  fallback for TCP-class exposure; ngrok/zrok/bore/quick-tunnels are
  deferred per research.
- **D5**: Bounds: per-owner cap on concurrent exposures, idle/lifetime reap
  (the `sshcap` tunnel pattern), `exposure` audit kind with receipts on
  open/close/failure, opt-in space feature flag. Valid targets are declared
  user-space service ports or the restricted public listener surface —
  hub control and gateway routes are never targets.
- **D6**: TLS termination is a provider property, not hub policy.
  `cloudflared` terminates at the Cloudflare edge (plaintext visible to the
  provider; the operator accepts this at opt-in). `sish` terminates on the
  operator VPS. This is the decision input #146's DNS/TLS layer consumes.
- **D7**: First slices are HTTP(S)-only. Public raw TCP (SSH-forwarding
  exposure, Spectrum-class) is deferred to the self-hosted path.

## Alternatives

- A hub-managed shared tunnel for all users: rejected — a shared run token
  lets one compromised workload repoint every user's hostname; per-binding
  tokens keep naming fail-closed.
- ngrok/zrok/bore as first provider: rejected in research (proprietary agent
  with paid TLS, OpenZiti self-host weight, no auth or routing).
- Quick tunnels for the primitive: rejected — random ephemeral URLs fail the
  named-endpoint contract; may return as a bounded dev convenience.
- Resolving #146 first: rejected — its TLS decision depends on this fork,
  which is why the issue orders #145 before it.

## Invariants kept

Outbound-only: no inbound ports opened on hub-controlled hosts. Egress stays
deny-by-default — tunnel workloads get proxy ACLs to the declared provider
endpoints only (cloudflared forced to `--protocol http2`; sish/frpc ride a
credential-bound TCP forward). No secrets in argv, tool results or logs.
Cross-user name claims fail closed: account-side names on cloudflared,
explicit namespacing on sish. Human-action paths remain the only routes on
the public listener. Removal reuses the finalizer ladder plus provider-side
cleanup.

## Consequences

- `cloudflared` requires `--protocol http2` over the pinned Squid proxy;
  QUIC/UDP 7844 cannot pass CONNECT and stays disabled.
- An operator who will not accept edge plaintext uses the sish profile —
  that is a provider choice per exposure, not a fork of the codebase.
- Threat model gains a row: managed-tunnel edge sees plaintext; every
  exposure open/close is audited; a leaked run token is revoked at the
  provider and the binding's finalizer.
