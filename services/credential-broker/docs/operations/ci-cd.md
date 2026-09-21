---
description: "Одни just-команды в GitHub и GitLab, pinned toolchain и честные release gates."
last_verified: "2026-09-18"
---

# CI и выпуск

Обе CI-конфигурации вызывают `just ci`: offline checks, race/coverage, build и fuzz.
Release job вручную вызывает `just ci-release` и требует установленных pinned security tools.
Runner — доверенный Linux образ с Go/Python/just из tools.json. Его provisioning выполняется
инфраструктурой организации, а не curl-скриптом с непроверенной загрузкой в каждом PR.

GitHub workflow рассчитан на self-hosted runner label `credential-broker-ci`; GitLab — на такой же tag.
**Не запускать непроверенные внешние PR на привилегированном постоянном self-hosted runner.**
Использовать ephemeral isolated worker без production secrets. Интегратор вправе заменить provisioning
на свои проверенные pinned actions, сохраняя одинаковые just gates.

Первый release gate — сверка поддерживаемого Go, затем staticcheck/gosec/govulncheck. Missing tool
не означает success. Job не публикует secret-bearing files. В архив входят source/docs/tests/evidence,
а не ключи/volumes/.build. Для binary release отдельно собрать поддерживаемым compiler, зафиксировать
SBOM, digest и происхождение toolchain/container image в release evidence.

[Dockerfile](../../deploy/Dockerfile) принимает GO_IMAGE с точным tag по умолчанию; перед production
pin digest оператором. Digest из сети здесь не придуман. `.build/bin` — результат локального build,
не автоматически разрешённый production artifact.
