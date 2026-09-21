---
description: "Процедуры, при которых нельзя обойти fail-closed поведение."
last_verified: "2026-09-18"
---

# Восстановление, отзыв и очистка

## Restart

Сначала остановить либо fenced изолировать старые workloads в ToolHub. Запустить Broker с теми же
catalog revisions, provider refs и master key. Journal проверяется целиком; stale plaintext directories
удаляются, прежние leases не восстанавливаются. Получить свежие leases и повторно materialize.
State восстанавливается только из последнего завершённого quiesced checkpoint.

## Backup

Остановить Broker и writers; сохранить ledger, master/session ключи, contracts и нужные managed
provider versions в защищённом backup. Backup одного ledger без external snapshots не является
полным восстановлением. Backups не сохранять в этом репозитории. Доступ к старому master key
и backup означает возможность восстановить удалённый secret; erase backups — отдельная retention policy.

При повреждении/оборванной записи не обрезать journal автоматически. Восстановить согласованный
backup и повторить enrollment для неясных операций. При достижении лимита не удалять audit строки
вручную. Compaction и durable checkpoint migration требуют отдельного формата/решения.

## Revoke и delete

Revoke — локальный authorization fence плюс cancellation tracked requests. ToolHub немедленно
прекращает MCP calls и останавливает/перезапускает процесс. File cleanup best-effort при OS error:
mounts остаются операционной ответственностью runtime, даже если API уже запретил lease.

Delete managed credential удаляет известные managed versions; внешние aliases не удаляет.
Жизненный цикл одного binding не равен удалению общего credential. Провайдерный revoke OAuth/PAT
выполняется отдельной явной операцией оператора/adapter, если нужен глобальный отзыв.

## Ограничение объёма

Journal bounded; metadata maps живут в памяти. Не использовать как безлимитный audit lake.
Expired drafts/proof objects после прерванного OAuth могут оставаться encrypted orphan в provider.
Нет автоматического GC неизвестных путей: планировать retention и reconciliation по ledger/evidence,
не удалять secret prefix «на всякий случай».
