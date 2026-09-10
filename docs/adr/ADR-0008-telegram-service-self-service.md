---
description: Let an authenticated owner manage connector activation without host mutation.
last_verified: 2026-09-10
---
# ADR-0008: Telegram connector self-service

Accepted 2026-09-10.

## Decision

Expose a read-only service catalog and a narrow `service_enable` hub tool to Hermes.
Persist enabled self-service connectors and the existing allowlisted env overlay in the
user's runtime volume. The supervisor applies connector definitions to the next Hermes
config after restart. Host settings, organization policy, browser/desktop bridges and
the Telegram transport remain outside the agent-controlled boundary.

## Consequences

The owner can connect GitLab, Atlassian, Google and other supported connectors from the
Telegram conversation after sending only the requested env entries. Secrets are not
returned by tools. Organization-scoped spaces still require host configuration.

## Related records

SPEC-0008, SPEC-0005, ADR-0005, ADR-0007.
