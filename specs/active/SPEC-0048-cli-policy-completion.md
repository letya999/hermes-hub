# SPEC-0048: CLI policy completion

Frozen: 2026-10-10, owner instruction to complete the six CLI policy gaps.
Extends SPEC-0047 without changing its frozen requirements.

1. ToolHub terminal selection suppresses native shell and native code execution
   across channels and unmanaged supervisor spaces. Internet installation can
   be blocked while calling existing image binaries in networkless scratch.
2. CLI cells cannot bind Hermes home. Package installation uses private
   executable tmpfs only when operator and immutable definition both opt in.
   Other callers, stateless pools and non-install cells cannot reuse that home.
3. Shipped CLI trust requires exact compiled definition digest; ids and
   transport alone never confer hub ownership or review status.
4. CLI onboarding advertises structured release/source/argv fields and rejects
   misplaced release sources with the correct field location.
5. Registry ceilings are operator-owned and opt-in; definitions declare exact
   egress subsets. Arbitrary workspace requests cannot widen the ceiling.
6. Trusted host catalog enablement atomically commits binding, profile
   selections and allows, respects denies, and rejects ceiling extensions
   without explicit operator intent. Sibling profiles gain no new allows.

Acceptance: regression tests for channel filtering, trust spoofing, malformed
source, registry defaults, private-home isolation, stale writers and managed
authority; real Docker proof for executable private home and registry install;
`just check` with project statement coverage at least 85%.
