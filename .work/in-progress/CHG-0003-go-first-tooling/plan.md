# CHG-0003: Go-first local tooling

## Goal

Make `just` the only local CI entrypoint and remove project-owned Jest/Python tooling.

## Steps

1. Replace Jest orchestration and Python dev scripts with direct Just recipes and a
   small cross-platform Go checker.
2. Keep upstream Playwright's isolated npm package and replace runtime supervision with Go.
3. Update workflows, documentation and immutable decision/requirement records.
4. Run local CI, security checks and Docker checks where available.

## Acceptance

`just check` works on Windows and Linux, runs Go race tests and an own-code Go coverage
gate, and the repository has no root npm/Jest or project-owned Python tooling.
