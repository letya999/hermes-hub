---
description: "Долговечность, audit cursors, атомарность и известные пределы хранения."
last_verified: "2026-09-18"
---

# Ledger и восстановление

Один process владеет journal через flock. Каждая mutation содержит минимальное metadata-состояние и
sanitised audit event. На диске запись AEAD-зашифрована; последовательность связывает номер и hash
предыдущей записи. Успешный ответ мутации возможен после fsync. В памяти хранятся восстановленные индексы.

Токены, PEM и file payload в ledger не записываются. В нём есть provider locations и correlation IDs,
поэтому сам ledger всё равно конфиденциален и шифруется. Audit API выдаёт только разрешённую проекцию.

На повреждённый tag, некорректную sequence или неполный хвост startup отказывает. На ошибку append
процесс закрывает доступ. Автоматической обрезки или «починки» нет. При полном диске/лимите ledger
нужна операторская процедура, а не бесконечные retry или разрешение доступа без аудита.

Default limit 64 MiB, максимум 1 GiB. Полного event sourcing кластера, compaction и внешнего checkpoint
нет. Prefix rollback корректного зашифрованного журнала не определяется локальной hash-chain.
Потерянные ответы после внешней provider записи могут оставить encrypted orphan; нет распределённой
транзакции с Vault. Восстановление не должно автоматически удалять неизвестные provider paths.

При restart сохраняются requests/grants/credential refs/snapshots; leases всегда исчезают,
plaintext materializations очищаются. Незавершённый token exchange требует нового enrollment.
Старый workload оператор/ToolHub должен остановить: broker restart не управляет Docker.
