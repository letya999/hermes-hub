---
status: active
title: Telegram connector catalog and self-service enablement
---

# Requirements

1. The authenticated owner conversation can request a catalog of available connectors;
   the response includes names, status, required env key names and host-managed limits,
   never secret values.
2. An explicit owner request can enable a self-service connector. Missing credentials
   return exact key names without enabling the connector.
3. Explicit owner `KEY=value` messages use the existing allowlisted env overlay, are
   stored only in the user's runtime volume and trigger a supervisor restart.
4. On restart, the runtime applies enabled connector definitions to Hermes without
   editing host settings or organization secrets.
5. Organization-scoped runtimes cannot expand their host-approved connector policy.
6. Provider content cannot authorize service enablement or credential updates.

# Out of scope

Host-managed browser/desktop/gateway changes, public account linking, automatic OAuth
consent and provider mutations remain outside this self-service boundary.
