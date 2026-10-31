# CHG-0228: Skills platform — live view lifecycle, draft eval, honest inventory, credential refs

Chain: [ADR-0085](../../../docs/adr/ADR-0085-agent-skills-platform.md) ->
[SPEC-0084](../../../specs/active/SPEC-0084-agent-skills-platform.md) (R5/R6/R8 правки) -> этот plan -> state.yaml.

Owner-requested hardening of the M24 skills platform: пять пунктов из
review-сессии 2026-10-29.

## Goal

Закрыть разрыв «публикация ≠ видимость» и смежные пробелы платформы скиллов.

## Scope

1. **Пересборка effective view после publish.** `MaterializeEffectiveSkills`
   пишет `generated/skills-meta/viewmeta.json` (capabilities + pins);
   `/v1/skill-apply` после publish/rollback вызывает
   `skillstore.RebuildEffective` — in-place пересборка `/skills-effective-rw`
   (bind держит inode, rename-свопа нет), re-stamp readiness + pin +
   `index.json`; `hubctl skill approve/install --consent/archive/restore/
   rollback/promote` пересобирает витрину на хосте. Маунты:
   `/skills-platform` (ro), `/skills-effective-rw` (rw), `/skills-meta` (ro)
   в обоих режимах; managed `/skills-store` теперь `spaces/<u>/skills`,
   а не брошенный `managed/<env>/skills`.
2. **Build чистит витрину.** Скиллы, ушедшие из всех источников, и файлы,
   выпавшие из новой ревизии, удаляются — призраки не читаются через
   `root=skills`.
3. **`hubctl skill eval --id <draft>`** оценивает `.drafts/<id>` до
   публикации; отчёт носит имя скилла драфта.
4. **Честный инвентарь.** `skills_list` листует ineligible-записи с
   `eligible:false` + `missing`; tier-0 `<available_skills>` несёт только
   счётчик `+N unavailable (skills_list)`. Каталог hub-agent-tools 5.8.0,
   prompt-contract, docs/tools.md, SPEC-0084 R6.1/R6.2.
5. **`requires.credentials[].ref`** — ссылка на сконфигурированный self-env
   ключ (например PAT коннектора): парсинг + strict-валидация формы,
   readiness через `caps.EnvKeys` (та же allowlist-политика, что у
   runtime), лиз из `/state/self-env.json` в toolhub (policy env
   прокинуты); stored-значение приоритетнее ref. SPEC-0084 R8.2.

## Verification

Фокусные go-тесты по каждому пункту (все красные до реализации):
skillstore (stale dirs/files, RebuildEffective, parse ref, readiness ref),
stack (viewmeta, tier-0 counter оба пути, ref readiness, toolhub env),
runtime (apply rebuild, rollback, org source, stale marker, concurrent),
supervisor (маунты обоих режимов, owner store в managed),
hubctl (refresh после approve/install/archive/restore/rollback/promote,
eval --id), agenttools (eligible inventory), toolhub/api (ref lease),
prompt_contract (guidance).

`just check` — финальный гейт. Docker-gates в CI; локально не прогонялись.

## Out of scope

Self-evolution (R11) — следующий майлстоун; broker-backed credential refs
(когда self-env недостаточно — managed broker leases); hot-reload внутри
запиненной сессии; нативный `~/.claude/skills` путь.
