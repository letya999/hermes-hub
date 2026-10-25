# Research: DNS/TLS addressing layer (issue #146)

Date: 2026-10-25. Scope: provider-neutral DNS record contract over the four
mandated profiles, plus the TLS fork ADR-0077 D6 handed down. Feeds ADR-0078.

## DNS provider matrix

| Profile | Write API | Credential | A/AAAA/TXT/NS | TTL | Bounds | Notes |
|---|---|---|---|---|---|---|
| `regru` (pure) | REG.API v2, POST `api.reg.ru/api/regru2` JSON | account username+password | `zone/add_alias`, `add_aaaa`, `add_txt`, `add_ns`, `remove_record`, `get_resource_records`, `update_records`, `nop` | no per-record TTL; zone cache TTL via `zone/update_soa` | 1200 req/h per account **and** per IP; 10 identical-error burst ban | **IP allowlist is mandatory** (panel security settings) — calls succeed only from static allowlisted egress IP |
| `digitalocean` (pure) | REST `api.digitalocean.com/v2/domains/{zone}/records` | bearer PAT, `domain:*` scopes | full CRUD incl. wildcard names | per-record, ≥0 s, default 1800 | standard v2 rate limits | zone can be the whole domain or a delegated subzone |
| `gcloud` (delegated) | Cloud DNS v1 `managedZones` + `changes`/`resourceRecordSets` | service-account JSON → oauth2 token | full CRUD inside the managed zone | per-record, ≥0 s | standard Cloud DNS quotas | zone `hub.<domain>` delegated at parent by NS records |
| `digitalocean` (delegated) | same DO API against zone `hub.<domain>` | same PAT | same | same | same | DO hosts a subzone fine: create domain `hub.<domain>`, DO assigns ns{1..3}.digitalocean.com |

Delegation mechanics (profiles 3/4): the parent zone stays at reg.ru; the
subzone `hub.<domain>` gets NS records pointing at the provider's NS set.
REG.API can write them (`zone/add_ns` "hand over a subdomain to other DNS
servers") — but only from an allowlisted IP, so delegation bootstrap is a
one-time operator step (panel or API) that the hub then verifies by
querying NS for `hub.<domain>`.

Sources: reg.ru/reseller/API2-tech + help.reg.ru API limits page;
regru.github.io/reg_api2-ruby RegApi2::Zone; docs.digitalocean.com
domain-records API + DNS how-to; cloud.google.com/dns docs (delegated
subzone, managed zones).

## TTL behaviour per profile

- regru: zone functions take no TTL argument; effective TTL is the zone SOA
  cache TTL (`update_soa`). Contract reports `ttl` as provider-applied:
  regru returns the SOA TTL, DO/GCP the record value. Callers treat DNS
  writes as "visible at authoritative NS now, cached elsewhere by TTL".
- Propagation check is therefore authoritative-NS polling, not resolver
  polling: query the serving zone's NS (ns*.reg.ru / ns*.digitalocean.com /
  ns-cloud-*.googledomains.com) until the record reads back, bounded
  deadline. ACME's own remote validation is the real DNS-01 gate; the
  pre-check exists to fail fast and to report honestly.

## TLS fork (input from ADR-0077 D6)

sish terminates TLS on the operator VPS. Its cert handling: certmagic
on-demand ACME (`--https-ondemand-certificate-email`, TLS-ALPN-01/HTTP-01
against first-touch SNI) plus unmanaged `name.crt`/`name.key` pairs loaded
from `--https-certificate-directory` — sish's own deploy pairs it with
dnsrobocert for wildcard certs. Upstream code warns per-host on-demand
issuance "will burn through your Let's Encrypt quota" with many
subdomains.

Options weighed:

- **Per-host on-demand at sish (certmagic, TLS-ALPN-01).** Zero DNS-API
  work for certs; works over a plain wildcard A record. Costs: any
  internet SNI probe can force issuance for arbitrary names under the
  wildcard (LE 50-certs/registered-domain/week quota burn; no ask-callback
  flag exposed to bound it), first-connect issuance latency, and every
  endpoint name lands in public CT logs. Rejected as default — unbounded
  third-party-triggered issuance is a quota DoS.
- **Wildcard via dnsrobocert on the VPS.** Proven by upstream deploy.
  Rejected for us: DNS API credentials would live in a VPS file outside
  the Broker — violates the mandated lease→materialize→release pattern.
- **Hub-issued cert, pushed to sish.** Hub runs ACME with DNS-01 written
  through this same DNS contract (`x/crypto/acme` in-process; lego as a
  library is the heavy alternative — it already ships `regru`,
  `digitalocean`, `gcloud` providers if a fallback is ever wanted).
  Cert+key travel once per renewal over the existing SSH channel to the
  VPS cert directory; the VPS holds zero provider credentials. Selected.
- **Hub-terminated TLS.** Rejected — reopens ADR-0077 D6 and puts public
  plaintext in the hub process for no gain.

Wildcard depth: `*.hub.<domain>` covers one label only. Issue naming
`<site>.<user>.hub.<domain>` is two levels deep, so the wildcard unit is
per-user: `*.<user>.hub.<domain>` — one DNS-01 cert per user space,
issued at onboarding, renewed ~60d. Matches the exposure config, which
already carries the per-user `zone` label (`<endpoint>.<zone>` bind).
Single-label `<user>-<site>.hub.<domain>` would let one cert cover
everyone but diverges from the merged exposure naming; recorded as an
alternative, not chosen.

## Where operations live

- **DNS executor**: hub control plane (`internal/dns` + `hubctl`), not a
  user workload and not an agent tool. All four profiles call their API
  over HTTPS with broker-leased credentials per operation.
- **regru executor placement**: the allowlist makes the source IP the
  constraint. Hub on a static-IP host works directly; a home deployment
  on dynamic IP **fails closed** at doctor/preflight with a diagnostic
  naming the two exits: static egress IP + panel allowlist, or a
  delegated profile (3/4) where the API needs no allowlist.
- **ACME issuer**: same hub component; holds the LE account key in a
  broker grant, writes `_acme-challenge` TXT via the profile, waits for
  authoritative propagation, downloads the cert, ships `crt`/`key` to the
  sish directory over SSH (reuses sshcap/enrol machinery).
- **sish VPS**: stays credential-free; only stores the resulting
  cert/key files it already loads.

## Credentials pattern (mandated)

DNS creds are operator-scoped broker grants on the control binding:
`NewForBinding` → `control.Acquire` → `runtime.Materialize` →
`runtime.Release`, mirroring `internal/exposure/sish.go`. Profile env
names materialize only into the issuer's memory: `REGRU_USERNAME`/
`REGRU_PASSWORD`, `DIGITALOCEAN_TOKEN`, `GCP_SA_JSON`. Nothing in argv,
tool results, logs or ledger DTOs.

## Cross-user capture (fail-closed chain)

1. Name space: endpoint names compose only from the reviewed per-user
   config (`<endpoint>.<zone>`; shipped in #145) — Alice cannot name
   `x.bob.hub.<domain>` because `zone` is bound to her space's config.
2. DNS writes: the contract only touches names under the operator base
   zone (`hub.<domain>`); onboarding creates `*.<zone>` only for the zone
   label bound to that space.
3. Certs: `*.<zone>` certs are issued only for zone labels present in a
   reviewed space config; a request for an unbound label fails closed.
4. sish side: `--banned-subdomains` + hub-composed bind names keep the
   relay from serving unallocated names; sish-level name claims outside a
   user's zone were already a hub responsibility (ADR-0077 D4).

## Decision summary (in ADR-0078)

Provider-neutral record contract over four profiles; delegated `hub.<domain>`
zones preferred (scoped creds, no IP allowlist); regru-direct valid only
with a static allowlisted executor and fails closed otherwise; hub-issued
per-user wildcard via DNS-01 pushed to sish; authoritative-NS propagation
check; all creds broker-leased.

## Open questions for the SPEC stage

- Exact config schema for the operator DNS profile (`dns.yaml`? section in
  connection config?) and the zone↔space binding record.
- ACME account storage: broker grant vs operator file.
- Renewal driver: supervisor routine vs hubctl cron document.
- Whether `hub.<domain>` apex gets its own A record (landing page) — nice,
  not required.
