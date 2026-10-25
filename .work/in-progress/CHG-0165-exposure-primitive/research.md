# Research: outbound exposure providers (issue #145)

Date: 2026-10-25. Scope: pick the reference provider for the exposure primitive
and define the contract other providers plug into. Feeds ADR-0077.

## Candidates

| Provider | Client | Auth model | Public names | TLS | Infra cost | Fit |
|---|---|---|---|---|---|---|
| Cloudflare Tunnel (`cloudflared`) | Apache-2.0 binary/image, `cloudflared run --token` | Per-tunnel run token; hostnames attached by account API, not by the token | Any hostname on a domain in the account's CF zone via API/dashboard | Terminated at Cloudflare edge (provider sees plaintext) | Free; needs CF account + domain on CF DNS | High — reference provider |
| `sish` (self-host) | Plain OpenSSH client, `ssh -R name:80:target:port` | Per-user pubkey on the VPS | Any free subdomain of the served domain; custom domains need TXT+fingerprint verification | On the operator VPS (wildcard LE via dnsrobocert, or SNI passthrough) | Operator VPS + wildcard DNS | High — sovereign path, second provider |
| `frp` (self-host) | Apache-2.0 `frpc` | Shared `auth.token` per frps (per-user needs OIDC or `{user}.{proxy}` prefixing); `tokenSource` file/exec loading | `subDomainHost` suffix or `customDomains` on frps vhost | frps HTTPS vhost or passthrough | Operator VPS + open inbound ports | Fallback when TCP exposure needed |
| ngrok | Closed-source agent (v3); OSS Go SDK exists but still SaaS | Authtoken per account | 1 dev domain on free tier; paid for reserved/custom | ngrok edge | Free tier: 3 endpoints, 20k req/mo, 1 GB/mo, no self-terminated TLS, TCP needs a card | Deferred — proprietary agent + paid for real use |
| `zrok` | Apache-2.0 `zrok` CLI on OpenZiti | Account token → environment → share tokens | Reserved share names on zrok.io or self-hosted frontend | zrok edge / own frontend | SaaS free tier exists; self-host pulls the whole OpenZiti control plane | Deferred — self-host weight |
| `bore` | MIT Rust `bore` | None / optional shared secret | `bore.pub` random or self-host; no HTTP routing | None (TCP only) | Minimal VPS | Rejected — no auth, no name routing |
| Quick tunnels (`trycloudflare`) | `cloudflared tunnel --url` | None | Random ephemeral `*.trycloudflare.com` | CF edge | Free, no account | Rejected for the primitive — ephemeral names fail the named-endpoint contract; possible bounded dev tool later |
| Tailscale Funnel | `tailscale funnel` | Tailnet identity | `*.ts.net` only | tailscale edge | Tailnet dependency for every visitor path | Rejected — tailnet-centric, not per-user public hosting |

Sources: cloudflare/cloudflared repo (Apache-2.0), developers.cloudflare.com
(tunnel tokens, remotely-managed tunnels, quick tunnels); antoniomika/sish repo
+ docs.ssi.sh (pubkey auth, wildcard DNS, dnsrobocert, `--bind-hosts`,
TXT+fingerprint custom domains); gofrp.org docs (token/OIDC auth,
`tokenSource`, vhostHTTP(S)Port, subDomainHost); ngrok pricing-limits + FAQ
(closed agent, free-plan caps); openziti/zrok intro + zrok.io pricing.

## Per-user name isolation (the deciding property)

- **cloudflared**: a run token can only run its own tunnel. Hostnames are
  attached to a tunnel by the account-level API (operator credential). A user
  workload holding its token cannot mint, steal or repoint names — cross-user
  capture fails closed at the provider layer.
- **sish**: any authenticated key can claim any free subdomain on the served
  domain (`--bind-hosts` restricts domains, not subdomains). Per-key isolation
  exists only for TXT-verified custom domains. The hub must namespace names
  itself (per-user subdomain zones or verified custom domains) — sish alone
  does not make cross-user claims fail closed.
- **frp**: one shared token authenticates all proxies on an frps; proxy naming
  is client-declared (`{user}.{proxy}` is a convention, not enforcement).
  Weakest isolation of the three.

## Fit with existing hub primitives

- A tunnel client is a per-binding workload on the connector admission path:
  owned network, pinned egress, proven receipt, finalizer removal
  (docs/prepared-connectors.md, generic-controller).
- Egress is deny-by-default. `cloudflared` needs TCP/443 to Cloudflare edge
  (force `--protocol http2`: QUIC/UDP 7844 cannot pass the Squid CONNECT
  proxy). `sish`/`frpc` need a credential-bound `host:port` TCP forward —
  the `tcp_forwards` shape (Squid CONNECT to a reviewed destination).
- Run credentials (tunnel token / SSH private key / frp token) deliver via
  Broker env-file injection; provider admin API tokens (CF API) are
  operator-scoped and never enter user workloads.
- Open/close receipts join the audit ledger under an `exposure` audit kind;
  idle/lifetime reap mirrors the `sshcap` tunnel pattern
  (internal/sshcap/tunnel.go: `lifetime`, `idle`, `lastUsed`).
- Targets are declared names only — the public listener surface
  (HUB_PUBLIC_LISTEN, ADR-0054 D2) or user-space service ports. Hub control
  and gateway routes are never valid targets.

## Decision (in ADR-0077)

Provider-abstracted primitive, `cloudflared` as the reference provider:
least new infrastructure (no VPS, no hub-side certs), per-tunnel tokens give
fail-closed naming, and it unblocks ADR-0054's deferred live acceptance plus
shrinks #146 to zone delegation + hostname API calls. `sish` is the defined
second provider for operators who do not accept edge termination — with the
explicit caveat that the hub must namespace names itself. `frp` is the
recorded fallback for TCP-class exposure (CF Spectrum is paid; public raw TCP
on free CF is not available).

## Open questions for the SPEC stage

- Naming scheme owned by #146 (`<site>.<user>.<zone>`); #145 only requires a
  declared per-exposure hostname attached by the operator/control path.
- Whether one tunnel per user (shared endpoints inside it) or one tunnel per
  exposure; per-binding is proposed for revocation simplicity.
- sish namespacing mechanism when the profile lands (per-user domain vs
  TXT-verified custom domains).
- HTTP-only scope for the first slice; TCP exposure explicitly deferred.
