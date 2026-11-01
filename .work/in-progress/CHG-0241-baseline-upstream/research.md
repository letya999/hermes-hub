# M29 research baseline (2026-11-01)

The planning pass inspected `README.md`, `docs/index.md`, `docs/architecture.md`, `docs/spaces.md`, `docs/agent-home.md`, `docs/rbac.md`, `docs/services/model-relay.md`, Broker docs, the pinned ADR/SPEC records, the stage playbook, source anchors and CI. The attached six-passport design is the accepted target. No runtime integration proof is claimed here.

| Current source / state | Current consumer | M29 owner / action |
|---|---|---|
| `scope.yaml`, `agent.yaml`, `workspace.yaml`, legacy `settings.yaml` | `ReadEnvironment`, `RenderEnvironment`, Compose, supervisor | Map fields to template/user sources; derive six read-only passports; retire readers after migration. |
| `spaces/profiles.yaml`, `policies/profiles/*`, org profiles, `profile.lock` | `applyProfiles`, `resolveProvisioningProfile`, `hubctl profile` | One pinned template chain and provenance/diff; do not turn profile grants into authority. |
| `spaces/models.yaml`, org/user models, model selection JSON | `LoadModelRegistry`, model-relay | Per-space service ladder, account-bound auth refs, quota and switch grant; selection state remains private. |
| ToolHub `CapabilityPolicy`, `CapabilityProfile`, `Governance`, grant history | ToolHub admission/projection | Preserve trusted ledger and per-call authority; compile a simple grants snapshot, not duplicate writable policy. |
| `HubToolsDefinition` 5.10.0 and ToolHub dynamic definitions | ToolHub projection, `tools_list`, `invoke` | Typed catalog, whole-toolset core/extended, pin/version/rebind; M27 retains pack acquisition. |
| `generated/soul-effective.<env>.md`, Hermes config, implicit upstream files | supervisor and pinned Hermes | Verify real load paths, then one explicit layered prompt; M20 vault remains sole long-term memory. |
| `generated/skills-effective` and user/org/platform stores | skill tools, runtime and ToolHub | Keep M24 ownership; project read-only /skills and lazy discovery. |
| Broker provider refs, contracts, leases and separate CLIProxy `auths/` | Broker, CLIProxy, relay | Add bounded model-key/session delivery contracts without secret bytes in snapshots/DTOs. |
| ToolHub/Communication/Broker runtime state and journals | service-specific stores | Preserve; not interchangeable with redundant config JSON/YAML. ADR-0091 storage migration is independent. |

Existing issues to cross-link: M20 #418/#419/#299/#303; M27 #383/#385/#387; M28 #402/#405; workspace manifest #208; budgets #400; job binding #397. No provider login, external send, production rollout or code push was authorized by this planning request.

Unverified before implementation: actual pinned Hermes `--ignore-rules` behavior on every managed entrypoint, per-session CLIProxy quota telemetry/reset semantics, and credential pinning when one proxy hosts multiple sessions. Stage M29.0 must produce real evidence; later stages reject unsupported claims rather than masking them in YAML.

Planning validation: `go run ./cmd/devcheck docs` and `git diff --check` passed. The first `just check` failed when a Windows test could not execute its temporary `docker.exe` (`Access is denied`); the same isolated test passed on retry. A second full run reached Go linking and failed to execute the installed `ld.lld.exe` with the same Windows access error. These are local gate failures, not evidence of M29 runtime acceptance.
