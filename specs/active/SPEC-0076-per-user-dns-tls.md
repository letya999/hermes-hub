---
description: Frozen acceptance contract for the M18 per-user DNS/TLS addressing layer (provider-neutral record contract, delegated zones, per-user wildcard certs).
last_verified: 2026-10-25
---
# SPEC-0076: Per-user subdomains — DNS/TLS addressing

Frozen: 2026-10-25. Decision: ADR-0078. Changes: CHG-0167 (design);
implementation CHG-0168.

- R1 (#146): The DNS layer is an operator-scoped control-plane contract:
  `Ensure`/`Remove` for A, AAAA and TXT records relative to the configured
  base zone, and `WaitAuthoritative` polling the serving zone's own
  nameservers to a bounded deadline. Results report the effective TTL —
  providers without per-record TTL (regru) report the zone SOA TTL. It is
  not an agent tool and not a user workload.
- R2: A DNS profile is declarative: provider kind (`regru`,
  `digitalocean`, `gcloud`), base zone, credential ref and provider
  quirks. All four mandated profiles are supported: pure reg.ru
  (REG.API v2), pure DigitalOcean, and reg.ru-parent delegation of
  `hub.<domain>` to GCP Cloud DNS or DO DNS. New providers join the
  contract without forking callers.
- R3: Provider credentials are operator-scoped Broker grants —
  `Acquire → Materialize → Release` per operation (the
  `internal/exposure` key_ref pattern). No DNS secret in argv, user
  workload env, logs, tool results or ledger DTOs.
- R4: Naming is `<site>.<user>.hub.<domain>` with the per-user `zone`
  label bound to a reviewed space config. Onboarding writes one wildcard
  A record `*.<user>` per bound space; exposure open/close performs no
  DNS call. Writes outside the base zone, and names for zone labels not
  bound to the requesting space, fail closed.
- R5: TLS is a per-user wildcard `*.<user>.hub.<domain>` issued hub-side
  over DNS-01 (`x/crypto/acme`; `_acme-challenge` TXT through this
  contract), delivered as `crt`/`key` to the sish
  `--https-certificate-directory` over the enrolment SSH channel, and
  renewed before expiry. The VPS holds no provider credential. Per-host
  on-demand issuance at sish is an opt-in fallback profile with its
  quota caveats documented.
- R6: The `regru` profile requires a statically allowlisted executor IP:
  preflight/doctor checks the configured egress IP and fails closed with
  a diagnostic naming the exits (static IP + panel allowlist, or a
  delegated profile). Delegation bootstrap (parent-side NS records for
  `hub.<domain>`) is a one-time operator step the hub verifies by
  querying NS.
- R7: The ACME account key and issued cert/key material are
  operator-owned (broker grant or `0600` control-home file), never part
  of per-user space state. Renewal failure alerts before expiry and must
  not remove live records or tunnels.
- R8: Bounds: bounded propagation deadline and retry budget per call,
  provider error taxonomy (auth/quota/conflict/network) with sanitized
  messages, `dns` audit events on zone writes, cert issue/renew/push and
  denials.

Acceptance A1..A8: each requirement has regression tests through its real
Go boundary (provider clients against `httptest` fakes, contract tests
shared across profiles), updated docs, and the full local gate. Live
acceptance — a real domain on each profile and a deployed sish VPS — is
separate evidence and cannot be asserted from fixtures.
