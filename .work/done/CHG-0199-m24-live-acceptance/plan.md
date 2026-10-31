---
title: "M24 #368 live acceptance"
issue: 368
status: in-progress
---

# M24 #368 — live acceptance

Prove the skills platform's core legs against real Docker/git where feasible,
record what could not run honestly. Mock coverage exists for every unit; this
CHG collects live evidence only.

## Scope decision

The dev stack on this machine serves `spaces/local` of the **main** worktree,
which is mid-flight on `fix/m23-live-wake` acceptance (containers recreated
minutes before this run). Host ports 8090/8545/8787/8081 are held by that
stack and compose ports are rendered unconditionally — a second infra stack
cannot coexist without new features. Instead the live legs run through a
**standalone `hubctl connector generic-controller`** on 127.0.0.1:18545 with
its own state root, spaces root (`spaces/me`), scope `m24-accept` and a
`skillgit@1.0.0`-only approved tuple (github.com egress). This is the real
admission path: real `/cli-exec` cells on the real daemon, real
`hermes-hub-cli-tools:0.4.0-dev` image, real `cell-proxy` egress, real
GitHub HTTPS fetch. Everything the M24 git-install code drives is exercised;
only the compose-rendered wiring around it is absent.

## Scenario legs (issue) mapped

| Issue step | Live proof |
|---|---|
| 7. Git install public GitHub | bare URL → candidates; `--path` → draft; approve → publish; update → diff draft; sources list/remove — all via the real cell |
| 7. Private GitLab | NOT RUN — no GitLab token; grant→broker path unit-tested |
| 5/6. Resources/secrets legs | NOT RUN — needs a live runtime/gateway chain; unit-covered |
| 4/9. Discovery/managed mode | NOT RUN — needs real model + managed relay; no credentials on this space |
| 1–3. Org init/members/catalog | NOT RUN — `hubctl org-*` legs are filesystem-local but the catalog projection needs the rendered stack |
| 10/11. Isolation/restart | partial — admission wall proven live (403 on unapproved egress); drafts/mirrors persist on disk |
| 8. Self-evolution | OUT OF SCOPE — #366 deferred post-MVP |
| — | Bonus: `hubctl skill eval` real run → `model_unavailable` honest report |

## Slices

- S13.1: standalone controller bring-up (cellinit from image, seccomp, approved tuple)
- S13.2: live git legs (candidates, install, approve, update, sources, negatives)
- S13.3: live eval leg (`model_unavailable`), stats surface
- S13.4: evidence record, fixes found live, docs re-verify, issue comments
