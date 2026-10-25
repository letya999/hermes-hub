---
description: Delivery evidence extracted from docs/validation.md (moved by CHG-0136).
last_verified: 2026-10-20
---

## Published M1 compatibility acceptance

v0.2.1 was published on 2026-09-12 through PRs 66 and 67. Downloaded Linux
hubctl/runtime/communication assets and published SHA256SUMS match the binaries
in the real Docker acceptance image 7a3f67d90dba. See CHG-0018 acceptance audit
for hashes, actual five-minute deadlines and cold restoration evidence. Linux CI
container fixtures exposed missing host-gateway DNS mapping; production already
provided it, and the standalone fixture now does too. Final v0.3.0 retirement
verification passed in Linux CI 34715743182 for both dev/prod Docker targets; no real Telegram/model account send is claimed.

v0.3.0 is published after PRs 68/69; downloaded runtime/communication assets match
SHA256SUMS and real Docker image c74d6046795a. Both Linux production-adapter gateway
scenarios passed actual five-minute automatic compute removal and original-session
cold restore. Linux own coverage was 85.06%; local `just check` passed at 85.03%.
Issue19 and milestone2 are closed (6/6). v0.2.1 remains the accepted rollback artifact.

The local retirement combined gate exposed a fixture reading terminal mapping
before CompleteJob settled it. The shared bounded mapping wait corrected that
assertion boundary; `just check` 36387 and full real gateway lifecycle 21011 then
passed, including actual five-minute automatic shutdown and cold session restore.
Production execution binaries were unchanged.
