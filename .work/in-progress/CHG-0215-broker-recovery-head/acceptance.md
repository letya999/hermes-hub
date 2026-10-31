# Acceptance matrix — Storage reliability MVP

B = SPEC-0087; H = SPEC-0088. Это дизайн будущих проверок.
ADD означает новый отличающийся risk scenario, KEEP — существующая защита,
а не вынесенный verdict о качестве её реализации. Tests сейчас не запускались
для несуществующего нового поведения.

## Общие условия

Synthetic principals alpha/beta, distinct contexts и отдельные dev/prod test
roots; fixtures не содержат настоящих credentials или персональных сообщений.
Fault injection управляется barriers/steps, не случайными sleep.
Fsync/platform/native claims проверяются настоящим filesystem/process.
Docker host volume-loss проверяется только disposable owned volumes.
Ожидаемое поведение определяется SPEC, не текущим кодом.

| Case / CHG | Решение и setup -> действие | Обязательные наблюдения | Уровень / oracle |
|---|---|---|---|
| B-A1.1 / 0191 | ADD grant seq1, revoke seq2 acknowledged; убрать только latest anchor на копии, reopen | отказ либо доказанное сохранение seq2; не activation seq1, source bytes не truncate | real temp FS; SPEC R1 |
| B-A1.2 / 0191 | ADD explicit genesis, interrupted first append, missing current head при initialized | только доказанный uncommitted first tail допустим; head loss не genesis | journal fault tests |
| B-A2.1 / 0194 | ADD crash before/after each commit point; P исчезает в каждой точке | surviving R/W coherent либо отказ; каждый acked revoke сохранён | native SQLCipher + process kill + actual Docker mount |
| B-A2.2 / 0194 | ADD independent wrong keys, stale witness, temp spill и actual settings | no fallback plaintext; no success on wrong state; patched version/pins recorded | native files and engine PRAGMAs |
| B-A3.1 / 0195 | ADD legacy 2 owners, contracts, revoke/delete, sequence holes; import/new append | exact identities/seq/scope и следующий monotonic seq; leases не оживают | real DB/module integration |
| B-A3.2 / 0195 | ADD fixed current rows/cache, 10k/100k/1M events | memory не хранит все events; page <=100; measurements repeatable | measured native process, no fabricated RAM SLO |
| B-A3.3 / 0195 | ADD foreign/expired cursor, old valid state image + current witness | foreign denied; gap explicit; old authorization rejected | API + DB integrity |
| B-A4.1 / 0196 | ADD 1000 refresh/checkpoint, active pin и unchanged snapshot | current valid, pin retained, unneeded old refs retired; no repeated full ref list | provider/broker real store |
| B-A4.2 / 0196 | ADD create crash до activate, delete fails, restart; unknown/alias рядом | known work resumes, deny retained, unknown и alias intact; both copies deletion verified | provider FS failure injection |
| B-A5.1 / 0199 | ADD near-cap P/R/archive/scratch + max next write | admission stops до превышения; completion/revoke reserve доступен | full footprint + real disk accounting |
| B-A6.1 / 0202 | ADD stage/verify/activate crash + full primary volume absent | import resumes same identity; source intact; last revoke preserved с config/keys отдельно | isolated container integration |
| B-A6.2 / 0202 | KEEP quality/race/fuzz/coverage gates; ADD new tests to native gate | >=85% module overall/per package; actual gates/results recorded | actual module pipeline |
| H-A1.1 / 0192 | ADD paused cursor aged, unrelated terminal files over count cap; Compact/resume | cursor same, no bootstrap/replay duplicate; terminal eligible moved | real temp spool |
| H-A1.2 / 0192 | ADD migration crash, unknown JSON, alpha/beta scope collision | resume/refusal, no foreign overwrite, unknown not deleted | FS + layout manifest |
| H-A1.3 / 0192 | ADD gzip write/close/sync fail before remove source | source retained; partial archive identifiable/retryable | deterministic FS fault |
| H-A2.1 / 0193 | ADD 5000/5001/equal timestamps; host rotate; data written before cursor crash | no silent skip; repeat distinguishable; source loss visible | unit stream + actual Docker log fixture |
| H-A2.2 / 0193 | ADD alpha job requested by beta, missing authority record, ID substrings | no foreign read, bounded result; unknown owner не scans all containers | CLI/diagnostics boundary |
| H-A3.1 / 0197 | ADD audit fail before call и after external effect | before: backend calls=0; after: effect=1/uncertain, no second call | consumers + append fault |
| H-A3.2 / 0197 | ADD 2 writers rotate/append, disk fills, unsettled admission old | valid segments bounded, no lost live admission, no success без durability | multiprocess real FS |
| H-A4.1 / 0198 | UPDATE existing stream receipt recovery на все terminal paths, crash между writes | same mapping/outbox IDs/result; prompt не повторён; artifact refs preserved | spool + gateway wiring |
| H-A4.2 / 0198 | ADD 10 events,8 slots, overflow2, filter error/restart/pause | remainder2 replayable, cursor stable until handoff, no wake after fence | source + sink contract |
| H-A4.3 / 0198 | ADD failed run после tool, OAuth transport timeout after effect, known pre-effect failure | first2 не replay; only verified safe attempt repeats, stable operation | gateway/runtime/toolhub |
| H-A5.1 / 0199 | ADD pending мал, другие dirs/backup большие | total ceiling triggers; counters recover после restart | data-owner accounting |
| H-A5.2 / 0199 | ADD preview alpha; generation changed/beta apply/symlink/unknown path | refusal до delete; live/foreign files intact; reports safe aggregates | operator CLI integration |
| H-A6.1 / 0200 | ADD recreate/missing initialized store/concurrent Save | exact connection/ref retained либо refusal; no successful empty init | ToolHub process + real volume |
| H-A6.2 / 0200 | ADD model confirm, wrong actor/action, expiry, 2 concurrent trusted callbacks | invalid denied; exact human consent consumed once | HTTP + auth concurrency |
| H-A7.1 / 0201 | ADD notifier dies after desired before applied; burst same/new surfaces | restart reconciles latest once; health-only no restart | notifier/supervisor integration |
| H-A7.2 / 0201 | ADD revoke in run; model/title unavailable; tool IsError | immediate deny, no false completed, title doesn't change job | runtime + pinned Hermes container |
| H-A8.1 / 0202 | ADD staged full scenario, pressure, restart, delivery, maintenance | one traceable operation, restored state и bounded footprint; receipts по уровням | controlled dev + explicit live scope |

## Existing protections to KEEP

- Broker: corrupt anchor, wrong key, mismatched prefix, uncommitted tail,
  revoke/cross-role/Origin/CSRF/materialization isolation tests.
- Communication: atomicJSON directory sync, stream receipt replay, uncertain
  admission observation, epoch/generation/assignment fences и existing retention.
- ToolHub: flock/digest fence, no grant widening, human nonce denial.
- Hosting: existing audit reserve/recovery tests как reference, без импорта
  бизнес-модуля в общий audit.
- Runtime: current cold-wake sawTool guard и configured relay cooldown/fallback.

## Evidence receipt для каждого отличающегося уровня

Записать case ID, exact code/image/engine/catalog revision, synthetic setup,
fault step, command/gate, expected vs observed assertions, safe bytes/files/
head counts и limitation. Sensitive logs только в ignored .work/tmp/ если
действительно нужны и допустимы; committed evidence — безопасная сводка.
Actual external delivery требует actual instruction; до неё live_send остаётся
not_run, а passing fixture/container не переименовывается в live.

## Stop conditions

Нет automatic cleanup при unknown schema, mismatch head, unavailable key,
stale plan или неопределённой ownership. Не размораживать старый backup после
новых revokes. Не публиковать gate PASS при missing native/volume proof.
Не добавлять тесты, которые только повторяют implementation без независимого
наблюдения сохранности/отказа/количества эффектов.
