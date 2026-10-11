# CHG-0079: Finish CLI installation policy and catalog management

User authorized the six remaining Devin tasks on 2026-10-10.

- Make `terminal: toolhub` select the existing networkless scratch executor and remove native terminal/code-execution bypasses. Native mode remains explicitly unrestricted.
- Prove bounded CLI cells never mount runtime home; retain only workspace and controller helper mounts.
- Recognize shipped CLI definitions by their complete compiled definition digest, never by a spoofable id or transport.
- Publish a structured CLI installation schema with release/source examples and actionable source-shape errors.
- Add opt-in registry egress profiles for PyPI, npm, Cargo and Go to the controller ceiling; definitions still declare individual hosts.
- Add a host catalog command that atomically binds credential-free catalog definitions and adds managed-profile selections within existing policy ceilings.
- Regression tests, actual Docker verification, `just check`, docs and delivery evidence.

Do not patch Hermes, widen arbitrary external-tool trust, or publish changes.
