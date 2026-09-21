# CHG-0027 — M5.3 Hermes-mediated MCP onboarding

Deliver the generic Telegram → Hermes → ToolHub control MCP journey without
provider-specific branches. Reuse CHG-0025 artifact/runtime and CHG-0026
control-plane primitives.

## Slices

1. Accept a canonical public GitHub repository URL, resolve it to an exact
   commit SHA, and persist/use only that immutable source.
2. Make installation progress observable through durable onboarding status.
3. Prove protected credentials/OAuth continuation, projection refresh and
   immediate disable/revoke/remove with a generic fixture.
4. Run the real Telegram/Hermes/ToolHive/Notion acceptance only with explicit
   account-login instruction and record external evidence honestly.

No Notion, Telegram or LinkedIn special cases. No ToolHive or Hermes patch.
