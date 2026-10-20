# Milestone playbook

How to plan and deliver a milestone in this repository so that any agent (including
a smaller model) can implement it step by step. M19 is the reference example:
[ADR-0036](../docs/adr/ADR-0036-execution-chain-reliability.md),
[SPEC-0053](../specs/active/SPEC-0053-execution-chain-reliability.md),
[CHG-0092](done/CHG-0092-chain-stop-the-bleeding/plan.md).

## 1. Prepare

1. Read `AGENTS.md`, `README.md`, `docs/index.md`, `docs/architecture.md`.
2. `git status`: note uncommitted work of other tasks. Never reset, stash or commit it.
3. Create a branch from `dev`: `git switch -c feat/m<N>-<short-name>`.
4. Collect evidence before designing: logs (docker, `.local/supervisor.log`,
   `spaces/<user>/hermes/logs`), state files, failing sessions. Record it in a
   `research.md` next to the first CHG plan, together with prior art links.

## 2. Write the chain (one pass, top to bottom)

| Artifact | Location | Content |
|---|---|---|
| ADR | `docs/adr/ADR-<next>-<name>.md` | Frontmatter `description`/`last_verified`; Status; Context with evidence; numbered decisions D1..Dn; alternatives; invariants kept |
| SPEC | `specs/active/SPEC-<next>-<name>.md` | Frozen date; SLO table if performance matters; requirements `R<stage>.<n>`; acceptance `A<stage>.<n>` |
| CHG per stage | `.work/in-progress/CHG-<next>-<name>/plan.md` | Use [templates/change-plan.md](templates/change-plan.md) |
| State per CHG | same folder `state.yaml` | Use [templates/state.yaml](templates/state.yaml) |
| Indexes | `docs/adr/index.md`, `specs/index.md`, `.work/index.md` | One row each |

Rules for plans meant for smaller models:
- Every slice names exact files and functions (anchors), the change, and test names.
- Tests are written red first; name the scenario in the test name.
- Each slice is one commit; slices are ordered; no slice depends on a later one.
- Include a red-team checklist and the live acceptance procedure.
- State what is out of scope.

Run `go run ./cmd/devcheck docs` (part of `just docs-check`) until it passes.

## 3. GitHub

1. Milestone: `gh api repos/letya999/hermes-hub/milestones -f title="M<N> — <Title>" -f description="<goal, docs, SLO>"`.
2. One issue per stage from [templates/issue.md](templates/issue.md):
   `gh issue create --milestone "M<N> — <Title>" --label <labels> --title "<stage title>" --body-file <file>`.
3. Write the issue number into the CHG `state.yaml` (`github_issue`).
4. Cross-link related existing issues instead of duplicating them.

## 4. Implement (per slice)

1. Re-read the plan slice and anchors; list callers with Serena/LSP or grep.
2. Write the failing test; run it to confirm red.
3. Implement the minimum fix; run the package tests, then `just check`.
4. Update `state.yaml` (`slices`, `validation`, `evidence` without secrets/content).
5. Commit with a message that names the CHG and slice.

## 5. Accept and close

1. Docker gates per AGENTS.md when image or runtime code changed.
2. Live acceptance on the dev stack; record evidence in `state.yaml`; never claim
   integration success from mocks.
3. Update affected docs (`docs/architecture.md`, `docs/operations.md`, ...).
4. Comment evidence on the issue and close it; set `status: done`; move the CHG
   folder to `.work/done/`; update `.work/index.md`.
5. Accepted ADRs are never rewritten; supersede them with a new ADR.

## Documentation rules (enforced by `just docs-check`)

1. Numbering is unique across ADR/SPEC/CHG. Take the next free number, or fill
   a gap when the record belongs there chronologically — never reuse a live
   number. Historical collisions stay but are marked in the index.
2. Every new ADR/SPEC file gets an index row in the same commit; every
   `.work/{in-progress,done}/CHG-*` folder gets a `.work/index.md` row.
3. `last_verified` must be `>=` the file's last commit date: editing a doc
   obliges re-verifying against code and bumping the date. Frozen ADRs are
   exempt (their date records acceptance, not freshness).
4. Docs stay under ~500 lines; evidence and dated acceptance journals live in
   `.work/done/CHG-*/`, not in `docs/`. Service internals go to
   `docs/services/<name>.md`; broker module docs live under
   `docs/services/credential-broker/` (Russian, quoted frontmatter — do not
   translate or reformat).
5. There is one `AGENTS.md` (repo root) and one `docs/`. Module-local rule
   files are not reintroduced; broker rules are a section of root AGENTS.md.
