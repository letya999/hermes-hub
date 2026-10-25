# CHG-0166: Outbound exposure primitive — implementation (M18, issue #145)

Chain: `docs/adr/ADR-0077-outbound-exposure.md` D1-D7 -> `specs/active/SPEC-0075-outbound-exposure.md` R1-R8
-> this plan -> `state.yaml`. Evidence: `../CHG-0165-exposure-primitive/research.md`.
Previous stage: `CHG-0165` (design). Next stage: #146 (DNS/TLS).

## Goal

Land the exposure primitive: a named, owner-scoped public endpoint backed by
a per-binding outbound tunnel workload (sish reference profile), operated
through `exposure_*` tools with Broker-held credentials, bounds and audit.

## Read before coding

1. ADR-0077, SPEC-0075, internal/sshcap/{config,tunnel,keys}.go,
   internal/audit/audit.go, internal/toolhub/connector_facade.go,
   docs/prepared-connectors.md (admission, tcp_forwards, credential delivery).
2. Issues #145 (this), #146/#147/#148 (downstream).

## Preconditions and rules

- Never reset or commit other tasks' uncommitted work.
- Red test first, then fix. Go stdlib + x/crypto only (sish client is plain ssh).
- `just check` before handoff; Docker gates when image/runtime code changes.

## Slices

### S1 Exposure config + feature flag (R1, R7 partial)

Anchors: `internal/exposure/config.go` (new); `internal/stack/config.go` `Features`.

Change:
1. New package `internal/exposure`: reviewed per-owner config at
   `connections/exposure/config.yaml` — provider enum (sish/cloudflared),
   sish relay (host/port/user/domain/pinned host_keys), user-scoped `zone`
   DNS label, `key_ref` (broker: or file:), bounds (max_exposures, idle,
   lifetime), named `endpoints` with declared targets
   (`hub_public_listener`, `service:<name>:<port>` or literal `host:port`).
   Strict KnownFields YAML, fail-closed validation, sish name resolution
   (`<endpoint>.<zone>` bind, `<endpoint>.<zone>.<domain>` public name).
2. Add `exposure` feature flag entry.

Tests (red first):
- `TestLoad*`: valid sish config; rejects unknown fields, bad provider,
  missing relay/zone for sish, relay on cloudflared, invalid endpoint names,
  undeclared/invalid targets, bound overflows, multi-doc, symlink/oversize.

### S2 sish keypair + enrolment material (R3, R5 partial)

Anchors: `internal/exposure/keys.go` (new); `cmd/hubctl` exposure enrol cmd.

Change:
1. `GenerateKeypair` (ed25519, OpenSSH private+authorized_key public).
2. `hubctl exposure enrol` prints pubkey + VPS/DNS enrolment steps for a
   reviewed config; never prints the private half (delivered via broker
   grant at admission — S4).

Tests:
- `TestGenerateKeypair*`: parseable halves, signer round-trip, fingerprint.

### S3 exposure_* tools + audit kind (R2/R4/R5/R7 partial)

Anchors: `internal/toolhub/exposure_facade.go` (new), `internal/audit/audit.go`,
journal intents, ControlPlane dispatch.

Change:
1. Audit kind `exposure`.
2. `exposure_list/status/open/close` tools on the shared journal facade —
   name-only args, owner scoping, per-owner cap + idle/lifetime enforced at
   open; workload admission behind a `TunnelProvider` seam; without a docker
   runtime ops return `unavailable` (fail closed, receipt recorded).

Tests:
- `TestExposureOpen*`: unknown name not_found; cross-owner fails; cap → busy;
  no provider → unavailable; open/close receipts via fake provider.

### S4 sish workload admission + credential delivery (R2, R3, R5)

Anchors: `internal/toolhub/generic_fallback.go` admission path; cell/net for
the credential-bound TCP forward to the VPS; broker file/env-file delivery.

Change:
1. Tunnel workload = pinned ssh-client image running `-R <bind>:443:
   <target>`; egress = credential-bound `relay.host:port` forward only;
   key material via broker file delivery (credential_mount_root).
2. `hub_public_listener` target resolves to the restricted public listener;
   `service:` targets resolve on the workload's owned network.

Tests:
- Inspect/receipt tests on the docker path (fake docker client);
  `just docker-check-prebuilt` for image-side changes.

### S5 Reaper + receipts (R7)

Idle/lifetime reap kills the workload and appends a close receipt;
`exposure_list/status` show live state.

### S6 Live acceptance + docs

Real sish VPS: open exposure to public listener, rerun ADR-0054 deferred
items; update docs/{operations,threat-model,architecture}.md. Recorded in
state.yaml evidence; VPS availability is a precondition, report honestly.

## Red-team checklist

- Endpoint name outside config → not_found; cross-owner name → fail closed.
- A `-R` bind outside `<endpoint>.<zone>` must be impossible: the bind
  string is composed by the hub from validated fields, never workload input.
- Config symlink/oversize/multi-doc → invalid; unknown YAML keys → invalid.
- Key material never in argv/logs/ledger/tool results.
- Open beyond per-owner cap → busy; reap must emit a close receipt.

## Acceptance

A1..A8 from SPEC-0075. Live acceptance (S6) is separate evidence.

## Out of scope

cloudflared profile implementation (alternate provider lands after sish),
TCP exposure (D7), DNS/cert automation (#146), site hosting (#147), inbound
SSH server (#148), quick tunnels, ngrok/zrok/bore.
