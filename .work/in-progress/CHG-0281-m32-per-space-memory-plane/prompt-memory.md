# M32: только #513

Issue: https://github.com/letya999/hermes-hub/issues/513
Contract: specs/active/SPEC-0098-m32-memory-slices.md, M1.
Evidence: CHG-0285. Branch: codex/m32-513-vault-backend.
Worktree: C:/Users/User/a_projects/hermes-hub-wt/m32-513-memory.

Подключи server-controlled vault bindings в ToolHub production startup.
Проверь реальный resolver/storage для двух synthetic users и dev/prod, изоляцию
путей/locks и отказ неизвестной identity. Не открывай public memory APIs без #514.

Проверь, что reviewed M32 changes уже в `dev` и gates пройдены на том SHA.
Прочитай AGENTS.md, README.md, docs/index.md, свою issue и инварианты + свою строку
указанной SPEC. Не загружай весь milestone/исторические ADR/спеки.
Создай/переиспользуй worktree этой lane от свежего `dev`; запиши BASE_SHA.
Не меняй исходный dirty checkout. Сделай только назначенную issue.

Найди реальные callers и существующие тесты; не переделывай уже работающий код.
Добавь недостающее доказательство успеха/отказа/recovery для изменяемой границы.
Изменения tool contract включают prompt/docs/catalog и проверку проекции вместе.
Run scoped tests + just check + applicable AGENTS.md gates. Тяжёлые Docker gates
согласуй с интегратором; собственные synthetic users/resources, без broad prune.

Обнови evidence своего CHG: SHA, команды/exit codes, acceptance checked/pending,
удалённые обходы и непроверенные границы. Push своей ветки без force и draft PR
в `dev` с Refs #513 разрешены. Не merge/close issue и не брать
следующую задачу. Не deploy/send/login. PR созданный в Codex прикрепи к чату.
При blocker назови конкретную причину; не включай fallback или mock-only success.

