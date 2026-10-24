---
description: "Как применён memory_bank_setup и где хранить текущую информацию."
last_verified: "2026-10-20"
---

# Зоны знания

После слияния в hermes-hub (CHG-0136) зоны — общие, на корне репозитория.
Внутри `services/credential-broker` отдельного дерева документов нет.

| Зона | Режим |
| --- | --- |
| docs/services/credential-broker | Текущее устройство модуля, переписывать при изменении |
| specs/active (корень) | SPEC-0064..0066 — frozen требования, baseline.json держит hash SPEC-0064 |
| docs/adr (корень) | ADR-0060..0069 — импортированные решения, не переписывать задним числом |
| .work/{in-progress,done} (корень) | CHG-NNNN записи: процесс, evidence, review |

Зоны `.work/todo` и `.work/tech-debt` из upstream-шаблона в этом репозитории
не применяются — не создавать их. Исходный work evidence поставки лежит в
`.work/done/CHG-0142-credential-broker-mvp/artifacts/`; брокерские задачи
продолжаются в `CHG-0140`/`CHG-0141` корневого `.work`.
