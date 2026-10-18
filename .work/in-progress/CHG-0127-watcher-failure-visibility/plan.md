# CHG-0127: Watcher failure visibility and quiet/drift fixes

Chain: issue #248 live acceptance (M13 Telegram run, 2026-10-18/19) ->
this plan -> state.yaml. Branch refactor/comm-plane-separation.

## Goal

Close the five defects the live watcher test exposed without changing the
architecture: dead probes were invisible, vacuous extractions seeded
baselines, agent escalation bypassed quiet windows, settled probe
occurrences spammed binding-drift warnings, and probe-script rejections
carried no compiler detail.

## Slices

- S1 failure visibility: probe errors alert the owner once per distinct
  normalized error via Schedule.LastFailureSHA256 dedupe (same mechanism
  as routine job failures); a successful probe re-arms the alert.
  RoutineSummary gains last_error so listings stop showing a dead watch
  as healthy.
- S2 vacuous values: a whitespace-empty extracted value is a probe error,
  never sha256("") baseline.
- S3 quiet escalation: notify_mode=agent honors the quiet window — an
  undeferrable model wake is suppressed and the occurrence settles capped.
- S4 drift noise: DispatchDueOccurrences skips the job-binding check for
  probe kinds (watcher/check/event); their only mapping is the rewritten
  escalation job which can never match by construction.
- S5 error detail: regex/script compile rejections return the compiler's
  reason; routine_create tool description documents command probes,
  read_file('name'), quiet/daily-limit coverage of agent mode and the
  once-per-error failure alert.

## Contract updates

Old silence-on-probe-error expectations in check/watcher tests were the
bug: check retries keep bounded retry semantics, but every distinct
failure produces at most one owner alert; tests updated to assert the
single deduped notification.
