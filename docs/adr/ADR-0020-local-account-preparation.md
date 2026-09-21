---
description: Owner-selected PC, fixed read-only ToolHive controller and protected local account setup.
last_verified: 2026-09-15
---
# ADR-0020: Local account preparation on the owner's PC

- Status: accepted
- Date: 2026-09-15
- Supersedes: no accepted record is rewritten

## Decision

The owner selected this Windows PC and explicitly requested local ToolHive
deployment and manager-friendly account preparation. This adds a local
deployment slice; it does not close #73 or implement a VPS/general controller.

One opt-in Go controller approves one fixed read-only Telegram definition,
owner/context/connection, connection-state path, real local image ID and
digest-pinned proxy. Authenticated loopback admission inspects actual Docker
image IDs, CPU/memory/PID limits, internal network and exact mounts/config.
Unapproved plans, missing enforcement and failed existing workloads fail closed.
ToolHive v0.48.0 wraps stdio. Its native HTTP-only isolation is not sufficient
for raw MTProto: a separate pinned Squid CONNECT proxy joins the internal
workload network and an external bridge, permits port 443 only to Telegram's
reviewed published IPv4 CIDRs, and denies all other destinations. ToolHive's
default DNS/latest-image sidecars are not used. CIDRs are container-only policy;
HTTP provider and bounded-CLI grants cannot adopt them.

The already hash-locked upstream Python proxy extra is enabled. The account
adapter honours HTTP_PROXY and starts read-only without account credentials.

## Owner history-access correction, 2026-09-15

The first preparation description was too narrow: a user-account connector is
not limited to recent direct messages. The pinned Telethon workload exposes the
account's visible users, groups and channels (bots excluded), paginated
historical reads, global/per-chat server-side search, and chat metadata. This
matches the account session's MTProto authority while retaining read-only
defaults, bounded pages, isolated mounts and the existing ToolHub admission
check. Media download and mutations remain separate capabilities.
User-operated phone/OTP/2FA login is a separate bounded container, mounted only
to its own protected input folder. It never prints a session or sends messages.
Go verifies get_me before explicitly committing a staged connection snapshot;
Enable/Resolve cannot publish/reload an unverified file-backed connect attempt.

Windows user/SYSTEM ACLs protect the private deployment outside the checkout.
The selected host uses a physical user-home directory: the AppData staging
path was not visible consistently from Docker/WSL and was not used for secrets
in live workloads. Google OAuth client JSON is imported locally; consent remains
user-driven. Official Workspace services are remote and require Google's
Developer Preview approval. Existing Hermes/communication containers and
default generated-MCP configuration are not switched.

## Evidence boundary

Real ToolHive MCP discovery, Docker limits/mount/network inspection, authenticated
admission and actual allowed/denied TCP checks establish preparation, not account
login, provider reads/writes or Google Preview access. No user credential was
supplied. Local grants remain empty until verified user authorization.
