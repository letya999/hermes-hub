# SPEC-0051: Deployment configuration contracts

Frozen: 2026-10-13. This supplements the existing execution and routine
specifications without changing their accepted history.

1. A Hermes hub MCP child receives the routine control URL, runtime bearer,
   authenticated principal and complete runtime/policy identity explicitly.
   The child must not depend on a stdio SDK inheriting arbitrary host variables.
2. The selected execution mode requires its control credentials. A missing or
   empty supervisor bearer fails Compose interpolation before build or startup.
   Static mode does not require supervisor credentials.
3. Local and CI gates render synthetic dev/prod and static/supervised profiles,
   validate them with the real Compose loader, and exercise a real MCP stdio
   reminder call against a local fixture. No personal tokens enter CI.
4. Before `up` mutates Docker, the host rejects a second running Compose
   communication gateway using the same Telegram bot token. The check never
   prints or stores the token and never stops another stack.
   An infra owner also rejects another running CLIProxy on the shared runtime
   network, preventing ambiguous `cliproxy` DNS aliases.
5. Live model diagnostics query the configured OpenAI-compatible `/v1/models`
   catalog and require an exact selected ID. A missing model is reported without
   exposing the provider key. Live provider reachability is separate from
   offline build validation.
6. The base CLI tools and cell proxy images are built, published and pulled with
   the hub image in CI and the prebuilt local gate. Optional CLI artifacts remain
   opt-in and are never silently substituted into the base image.
