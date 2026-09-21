---
description: "Как добавить контракт или adapter без нарушения security boundary."
last_verified: "2026-09-18"
---

# Изменения и review

Перед PR запустить `just check`. Для changes к identity/OAuth/provider/files/proxy добавить
отрицательный тест на границу и выполнить `just fuzz`; перед релизом — `just release-check`.

Контракт не принимается по одному README MCP. Зафиксировать реальные field names, способ чтения,
точный file target, допустимые destination/method/path и ownership. Пользовательская строка URL
не превращается в allowlisted upstream. Совместимость с MCP подтверждается отдельным read-only вызовом.

Новый runtime dependency требует ADR с причиной и альтернативами. Zero-dependency gate не запрещает
обоснованное будущее изменение; запрещает незаметно принести платформу вместо маленького сервиса.
Публиковать только sanitised fixtures; настоящие credentials передавать через защищённую форму.

[Git workflow](docs/engineering/git-workflow.md) описывает ветки и review.
[Testing](docs/engineering/testing.md) объясняет пирамиду и границы E2E.
