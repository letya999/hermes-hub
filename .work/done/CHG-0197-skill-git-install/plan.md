# CHG-0197 — M24 #365 S10: install skills from git repositories

Issue: #365. Spec: SPEC-0084 R10.*. ADR: ADR-0085.

## Goal

Install skills from GitHub/GitLab (and configured self-hosted GitLab) HTTPS
sources, pinned to an exact commit, tracked by a per-owner bare mirror under
`skills/.sources/<id>.git`. Every install/update produces a scanned draft +
hub-owned `.origin.json`; git itself runs only inside a bounded `/cli-exec`
cell with host-allowlisted egress and broker-leased credentials.

## Layout

```
internal/skillstore/gitsource/   new package (R10 core)
  source.go    ParseSource(raw, gitlabHost) → Source{Host,Repo,Ref,Path,URL}
  registry.go  sources.json index under skills/.sources/, List/Remove
  git.go       GitOps interface + ExecCell runner (CLIExecFunc argv builder)
  discover.go  bounded SKILL.md walk → []Candidate
  install.go   Install/Update/CheckUpdates orchestration → drafts + .origin.json
cmd/hubctl/skill.go            skill install|update|sources list|sources remove
internal/agenttools/skills.go  skill_manage install|update|check_updates
```

## Slices

- S10.1 `ParseSource`: HTTPS only; `github.com/<o>/<r>[/tree/<ref>/<path>]`,
  `gitlab.com/<g>/<p>[/-/tree/<ref>/<path>]`, configured `gitlab_host`;
  reject ssh/git/file schemes, userinfo, query, fragment, `.git` suffix, `..`.
- S10.2 `sources.json` registry: `{id → {url, ref, path, mirror, skills[]}}`;
  `List`/`Remove` (deletes mirror dir, warns skills lose tracking).
- S10.3 `GitOps` + `ExecCell`: hardened argv —
  `git -c core.hooksPath=/dev/null -c core.fsmonitor= -c protocol.file.allow=never -c protocol.allow=never -c protocol.https.allow=always -c submodule.recurse=false -c credential.helper=<askpass-fn>`
  `init --bare`, `remote add`, `fetch --depth=N --filter=blob:none origin <sha|ref>`,
  `archive <commit> <path>` piped to tar, `merge-base --is-ancestor`.
  Networked fetch runs via `/cli-exec` `CliExecRequest` (command `git`,
  `Execution.Egress=[host]`, `Workspace` binding-scope rw bind on the mirror
  dir). Token via `GIT_TOKEN` env + `credential.helper='!f(){echo password=$GIT_TOKEN;};f'`
  — never in argv URL, `.git/config`, logs or `.origin.json`.
- S10.4 `DiscoverSkills`: root-relative walk, depth ≤6, ≤2000 dirs, skip
  `.git`/`node_modules`; convention dirs (`.agents/skills`, `skills`,
  `.claude/skills`) ranked first; >1 unambiguated → candidates returned.
- S10.5 `Install`: parse → resolve ref→SHA (runner `ls-remote`/API) → fetch →
  export tree → discover → draft via `st.CreateDraft` with `.origin.json`
  `{source_id,url,ref,commit,path,tree_sha,fetched_at}` + provenance in
  DraftMeta; `.origin.json` excluded from the effective view at Build.
- S10.6 `Update`/`CheckUpdates`: read `.origin.json` from installed skill,
  refetch ref → moved → diff vs current tree → new draft;
  `merge-base --is-ancestor old new` false → `force_push: true`;
  `check_updates` reports `{name, current_commit, latest_commit, update}` —
  never imports.
- S10.7 `hubctl skill install --url [--ref --path --name]`,
  `hubctl skill update [name|--all]`, `hubctl skill sources list|remove`.
- S10.8 `skill_manage install|update|check_updates` actions — install routes
  the same gitsource path via injected runner; approval card on the draft.
- S10.9 Docs: tools.md rows, routines.md contract, spaces.md `.sources` +
  `.origin.json` notes, ADR-0085 addendum, catalog bump.

## Runner seam

`GitOps` is the test seam. `FakeGit` builds a `git init --bare`-shaped mirror
in-memory (commit registry + trees) so install/update/force-push paths run
fully in unit tests. `ExecCell` is the production runner; its argv is unit
tested for hardening + secret placement. Live fetch stays behind the
docker/cell gate — reported honestly at closeout.

## Out of scope

Pushing changes upstream, PR/MR creation, git over SSH, non-GitHub/GitLab
hosts beyond configured `gitlab_host`, LFS/submodule support (rejected, not
fetched), codeload fallback.
