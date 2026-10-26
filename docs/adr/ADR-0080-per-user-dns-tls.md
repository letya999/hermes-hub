---
description: Per-user subdomains — provider-neutral DNS record contract over four mandated profiles (regru, digitalocean, delegated gcloud/DO under a reg.ru parent); hub-issued per-user wildcard certs via DNS-01 pushed to sish; Broker credentials; cross-user claims fail closed.
last_verified: 2026-10-25
---
# ADR-0080: Per-user subdomains — DNS/TLS addressing layer

Status: Accepted 2026-10-25 — design for issue #146 (M18); owner directed
implementation the same day. Evidence:
`.work/in-progress/CHG-0167-dns-tls-addressing/research.md`.
Upstream: consumes ADR-0079 D6 (sish terminates TLS on the operator VPS).

## Context

The exposure primitive maps names to tunnel workloads; it does not decide
how `<site>.<user>.<domain>` resolves or where its certificate comes from.
Issue #146 adds that layer: the operator configures a domain once, the hub
carves per-user subdomains, DNS records and certs follow automatically.
The DNS layer must stay provider-neutral like the exposure Provider seam,
and must cover four operator profiles: pure reg.ru (REG.API v2), pure
DigitalOcean, reg.ru parent + GCP Cloud DNS subzone, reg.ru parent + DO
subzone.

Research outcomes that shape the decision:

- REG.API v2 offers full zone CRUD (`zone/add_alias`, `add_txt`,
  `add_ns`, `remove_record`, `get_resource_records`) but mandates an IP
  allowlist in the panel — the profile is only valid from a static
  egress IP; a dynamic home IP must fail closed with a clear diagnostic.
  It has no per-record TTL (zone SOA TTL only) and a 1200-req/h cap.
- DO and GCP offer per-record TTL and unauthenticated-IP APIs (token /
  service account). A delegated `hub.<domain>` managed zone scopes the
  credential's power to exactly the names the hub owns.
- sish loads unmanaged `name.crt`/`name.key` pairs from
  `--https-certificate-directory`; its built-in on-demand ACME issues
  per-host certs on first SNI touch with no bound on which names trigger
  issuance — a Let's Encrypt quota DoS vector for a wildcard domain.
- A wildcard cert covers one label: `*.<user>.hub.<domain>` is the
  wildcard unit under the issue's two-level naming.

## Decisions

- **D1**: DNS is an operator-scoped control-plane component — a
  provider-neutral record contract (`Ensure`/`Remove` for A, AAAA, TXT
  under the configured base zone; `WaitAuthoritative` polls the serving
  zone's NS to a bounded deadline; typed, credential-free errors).
  Not an agent tool, not a user workload.
- **D2**: Four declarative profiles, one contract. `regru` and
  `digitalocean` manage the whole domain zone; `gcloud` and
  `digitalocean` (delegated) manage only `hub.<domain>`. A profile is
  provider kind + zone + credential ref + provider quirks (regru: no
  per-record TTL, IP-allowlist precondition). The delegated profiles are
  the reference path: their credential cannot touch names outside
  `hub.<domain>`.
- **D3**: All provider credentials are operator-scoped Broker grants
  leased per operation (`Acquire → Materialize → Release`, the
  `internal/exposure/sish.go` pattern). No DNS secret in argv, env of a
  user workload, logs, tool results or ledger DTOs.
- **D4**: Naming is `<site>.<user>.hub.<domain>` — the merged exposure
  config's `<endpoint>.<zone>` shape with `zone` as the per-user label.
  Onboarding writes one wildcard A record `*.<user>` per bound space;
  exposure open/close performs zero DNS calls. Cross-user claims fail
  closed at the config layer (zone label bound to the space) and at the
  contract layer (writes confined to the base zone).
- **D5**: TLS = per-user wildcard `*.<user>.hub.<domain>` issued hub-side
  via DNS-01 (`x/crypto/acme`; TXT records written through this
  contract), delivered as `crt`/`key` to the sish certificate directory
  over the enrolment SSH channel, renewed automatically. The VPS holds
  the resulting key but zero provider credentials. Per-host on-demand
  certmagic stays a documented fallback for operators who accept its
  unbounded-issuance and quota caveats; hub-terminated TLS is rejected
  (ADR-0079 D6).
- **D6**: The `regru` profile fails closed when the executor's egress IP
  is not statically allowlisted — doctor/preflight reports the two exits
  (static IP + panel allowlist, or a delegated profile). Delegation
  bootstrap (NS records for `hub.<domain>` at the parent) is a one-time
  operator step the hub verifies by querying NS.
- **D7**: ACME account key and issued cert/key live as operator-owned
  material (broker grant or `0600` control-home file); they are never
  part of per-user space state.

## Alternatives

- On-demand per-host certs at sish as the default: rejected — third
  parties can force issuance for arbitrary names under the wildcard and
  burn the domain's LE quota; first-connect latency; per-name CT-log
  exposure. Kept as an opt-in fallback.
- dnsrobocert on the VPS (upstream's own pairing): rejected — DNS
  credentials would sit in a VPS file outside the Broker contract.
- Single-label `<user>-<site>.hub.<domain>` naming with one global
  wildcard cert: rejected — diverges from the merged exposure naming and
  couples all users' sites to one shared cert identity; per-user
  wildcards keep issuance, revocation and naming aligned with spaces.
- Hub-terminated TLS on the public listener: rejected — contradicts
  ADR-0079 D6 without new evidence.
- lego as the ACME/DNS library: recorded fallback — it already supports
  `regru`/`digitalocean`/`gcloud`, but the whole-provider-tree dependency
  outweighs ~200 lines of `x/crypto/acme` glue over our own contract.
- Per-endpoint A records instead of per-user wildcards: rejected — adds
  an API call to every exposure open/close and a hard dependency on the
  DNS provider at open time, for no isolation gain.

## Invariants kept

Outbound-only exposure surface unchanged — DNS/TLS adds no inbound ports.
Credentials stay behind the Broker; nothing outside the operator binding
ever holds provider power. Cross-user name claims fail closed at every
layer (config namespace, contract base-zone confinement, per-user cert
names). DNS/ACME failures surface as typed errors and `doctor` output —
never as silently-missing records. Domain registrar stays external: no
registration, transfer or whole-zone migration is a hub function.

## Consequences

- The hub needs three small HTTPS API clients (regru, DO, GCP SA) behind
  the record contract — no new heavyweight SDK is required for two of
  them; GCP needs an oauth2 token exchange for the SA JSON.
- DNS provider outages degrade new onboarding/renewal, not live tunnels —
  wildcard records make open/close independent of the API.
- `hub.<domain>` delegation leaves the parent zone untouched; pure reg.ru
  or DO give the credential whole-zone power — the delegated profiles are
  recommended in docs for that reason.
- Renewal is a hub-side scheduled action; its failure must alert before
  expiry (sish keeps serving the old cert until it lapses).
- Threat model gains a row: DNS credential compromise is bounded to the
  delegated zone on profiles 3/4, unbounded on profiles 1/2 — stated in
  the runbook, not hidden.
