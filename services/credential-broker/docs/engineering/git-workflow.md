---
description: "Перенос в монорепозиторий, короткие PR и запрет секретов в Git."
last_verified: "2026-09-18"
---

# Ветки и изменения

Для самостоятельного репозитория: feature/* от dev, PR в dev, релиз в main. При переносе соблюдать
существующие protected branches Hermes Hub, а не автоматически создавать новые ветки.
Типы commit: feat/fix/refactor/docs/test/chore. Не менять code и политические security assumptions
в одном непрозрачном массовом PR.

Перед PR — just check. Перед release — just release-check и integration acceptance. Git hook
`.githooks/pre-commit` может запускать just lint/docs-check; включение hooks является явным действием
разработчика. Hook не заменяет CI и не должен отправлять файлы куда-либо.

Runtime state, `.env`, provider tokens, `.private`, `.key`, binaries и generated coverage исключены
из Git. Evidence релиза в `.work/artifacts` добавляется осознанно и не содержит живых секретов.
