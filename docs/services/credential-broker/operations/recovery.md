---
description: "Процедуры, при которых нельзя обойти fail-closed поведение."
last_verified: "2026-11-01"
---

# Восстановление, отзыв и очистка

## Restart

Сначала остановить либо fenced изолировать старые workloads в ToolHub. Запустить Broker с теми же
catalog revisions, provider refs и master key. Journal проверяется целиком; stale plaintext directories
удаляются, прежние leases не восстанавливаются. Получить свежие leases и повторно materialize.
State восстанавливается только из последнего завершённого quiesced checkpoint.

## Backup

После успешного запуска Broker сохраняет private marker `state-initialized`.
Каталог задаётся через `state_witness_dir`; без настройки используется каталог
master key. Если маркер существует, а journal пуст или отсутствует, следующий
запуск возвращает `broker state recovery required` вместо новой инициализации.
Повреждённый или недоступный маркер также блокирует запуск. Маркер не содержит
credentials, ключей или пользовательских идентификаторов.

Для обнаружения потери всего основного volume каталог маркера должен сохраняться
независимо от него. Compose теперь выбирает отдельный external volume по окружению;
его provisioning и обновление образа обязательны до rollout (CHG-0149).
При переносе из каталога master key в новый пустой каталог Broker учитывает
старый маркер: пустой journal не допускается. При исправном journal новый маркер
сохраняется, старый не удаляется. Маркер не является backup,
не обнаруживает откат к непустому корректному префиксу journal и не восстанавливает
credentials сам по себе. Не удалять его, чтобы обойти ошибку восстановления.
Утрата самого маркера вместе с основным состоянием этим механизмом не обнаруживается.

При явной настройке `recovery_dir`, `recovery_anchor_dir`, `recovery_key_file`
и `recovery_identity` Broker ведёт второй зашифрованный journal и отдельно
зашифрованные local provider objects. Recovery key отличается от master key;
ключи отделяются по назначению и `recovery_identity`. Backup записывается
до primary; подтверждение приходит после fsync primary и зашифрованного
current-head v2 с atomic replace и file/directory sync. После потери primary journal восстанавливается только по
backup, чей подтверждённый head совпадает с независимым anchor. Более старая
или подменённая копия отвергается. Неподтверждённый хвост после прерванной
записи отбрасывается; повторный запуск продолжает с последней подтверждённой
позиции. Повреждённый ciphertext и неверный ключ блокируют запуск.

Local provider читает backup по первоначальному locator только если primary
object отсутствует. Удаление managed object удаляет обе копии. Broker
восстанавливает owner/context, contract, grant, revoke и delete из journal;
leases остаются только в памяти и после рестарта создаются заново через
текущую авторизацию. Внешние Vault/Kubernetes providers сохраняются своим
операционным механизмом, не копируются этим режимом.

### Переход существующей установки

1. Остановить Broker и writers, остановить или fenced изолировать связанные
   workloads. Сохранить отдельную проверенную копию текущих state и keys вне
   репозитория.
2. Создать private `recovery_dir` и `recovery_anchor_dir` на независимых
   носителях и отдельно защищённый 32-byte raw recovery key. Права каталогов
   0700, файла ключа 0600, владелец — service UID. Указать четыре поля в
   Broker config. Не печатать ключ и не помещать его в argv/env.
3. Выполнить `credential-broker check-config --config <absolute-path>`, затем
   `credential-broker migrate-recovery --config <absolute-path>`. Команда
   держит эксклюзивный lock primary journal до окончания копирования и не
   запускает HTTP server. Если она прервана, повторить её с теми же key,
   identity и storage; не удалять частичный encrypted backup вручную.
4. Запустить Broker с той же конфигурацией. Проверить TLS health, scoped
   credential status и безопасный provider probe с новой lease. Только после
   этих проверок считать миграцию завершённой.

Compose выделяет witness volume и вдобавок отдельные external volumes под
recovery journal/anchor и recovery key. Он не переводит существующий Broker
в recovery-режим сам: четыре поля recovery_* в config и офлайн
`migrate-recovery` выполняет оператор. Для потери всего primary volume
отдельно сохраняются config, contracts, TLS identity и master key: в текущем
Compose они находятся на primary volume, поэтому одного recovery journal
недостаточно для такого восстановления. Если recovery и anchor потеряны или
откатаны вместе, код не может доказать последний подтверждённый head:
оператор должен блокировать автоматическое восстановление и сверять внешнюю
независимую копию.

Замена master key на установке с данными недопустима: весь journal и все
sealed provider objects связаны им, и после замены запуск fail-closed
откажется читать старое состояние. Штатной re-key процедуры нет; ротация
затрагивает только signing keypairs и bearer tokens. Если master key всё же
утрачен, единственный путь — проверенный replay независимого recovery
журнала в пустой primary: записи перешиваются под новый master при записи.

Прерывание replay может оставить в primary дивергентный хвост: следующий
запуск тогда отвергает префикс и не поднимается. Лечение операторское —
остановить Broker, вынести primary journal каталог в сторону, пересоздать
его 0700 под service UID и дать replay дойти до конца без нового прерывания.

Backups не сохранять в этом репозитории. Доступ к старым key и ciphertext
может раскрыть удалённый secret; retention и уничтожение старых копий —
отдельная операционная политика. Онлайн-ротации recovery key пока нет:
не заменять файл ключа в работающей установке; требуется офлайн-переход
с проверенным повторным шифрованием и отдельной приёмкой.

Повреждённую подтверждённую запись не обрезать автоматически. Восстановить
согласованный backup и повторить enrollment для неясных операций. При
достижении лимита не удалять audit строки вручную. Compaction и durable
checkpoint migration требуют отдельного формата/решения.

## Revoke и delete

Revoke — локальный authorization fence плюс cancellation tracked requests. Revoke credential
деактивирует и все его grants (событие `grant.revoked` на каждый), иначе мёртвый active grant
блокирует conflict-check `CreateGrant` для того же binding/workload. ToolHub немедленно
прекращает MCP calls и останавливает/перезапускает процесс. File cleanup best-effort при OS error:
mounts остаются операционной ответственностью runtime, даже если API уже запретил lease.

Отказ `POST /v1/leases` пишет в лог `credential-broker lease denied deny_reason=<reason>
grant_id="<id>"`: `grant_missing`, `identity_mismatch` (неактивный grant или расхождение
principal/context/runtime/policy/binding/workload) или `credential_inactive`. Ответ API остаётся
общим `403 denied`.

Delete managed credential удаляет известные managed versions; внешние aliases не удаляет.
Жизненный цикл одного binding не равен удалению общего credential. Провайдерный revoke OAuth/PAT
выполняется отдельной явной операцией оператора/adapter, если нужен глобальный отзыв.

## Ограничение объёма

Journal bounded; metadata maps живут в памяти. Не использовать как безлимитный audit lake.
Expired drafts/proof objects после прерванного OAuth могут оставаться encrypted orphan в provider.
Нет автоматического GC неизвестных путей: планировать retention и reconciliation по ledger/evidence,
не удалять secret prefix «на всякий случай».

## Переход legacy recovery head

Новый anchored startup отказывает на legacy marker; автоматического adoption
нет. До применения остановить Broker и workloads, проверить backup с ключами,
config и contracts, подготовить изолированную копию всего P/R/anchor набора.
На копии выполнить `credential-broker migrate-head --config /absolute/config.json`.
Команда берёт journal locks и требует точного совпадения primary/recovery tip
и legacy latest anchor. При mismatch источники не обрезаются и не compact.
Providers команда не открывает и не очищает. Head сначала сохраняется и
проверяется, затем активируется новый marker. Повтор после staged head
допускается лишь при тех же digest/sequence; повтор после activation идемпотентен.
Старые commit-файлы сохраняются: их удаление не входит в эту команду.

Потерянную `current-head` нельзя восстанавливать выбором старшего оставшегося
commit-файла или запуском `migrate-recovery`: это recovery-required.
Не удалять initialized marker. Полный module/container gate и приёмка этого
перехода ведутся в CHG-0215. В CHG-0237 локальная dev-установка прошла
проверку на копиях P/R/key, затем `migrate-head` на остановленном Broker;
повторный запуск и `/healthz` прошли. Это не перенос production-данных.

## Переход durable store на SQLCipher

ADR-0087 переводит носитель записей на зашифрованную SQLCipher-подложку:
primary `primary.db` в `data_dir` и recovery `recovery.db` в `recovery_dir/journal`
обновляются одной ATTACH-транзакцией с прежним sealed witness head; одиночная
установка использует `ledger.db` рядом с журналом. Ключи файлов выводятся из
master/recovery key material (`sqlcipher-ledger/v1`, `sqlcipher-recovery/v1`),
сами ключи не попадают в argv, env, manifest или вывод.

Порядок оператора на остановленном писателе:

1. Проверенный backup вне репозитория (config, contracts, TLS, keys, ledger,
   recovery, anchor) — legacy `ledger.bin` после активации уходит в
   `substrate-backup/` и остаётся источником сверки, не rollback.
2. `hubctl store-migrate --dir spaces/<user> --env <env> --broker --apply`
   останавливает сервис и запускает `credential-broker store-migrate --config`
   в образе сервиса поверх его external volumes; альтернативно — та же
   команда напрямую при вручную смонтированных каталогах. Импорт отказывает
   при живом писателе, неизменённо сохраняет frames и запечатывает manifest
   (format, records, head sequence/digest, store identity, byte limit) в оба
   каталога. Повтор отказывает, пока существуют db-файлы или маркер.
3. `hubctl store-activate --dir spaces/<user> --env <env> --broker --apply`
   проверяет manifest и оба хранилища через полный путь открытия (replay,
   witness head), пишет парные `sqlcipher.activated`, уносит `ledger.bin` в
   `substrate-backup/` и поднимает сервис. Активация без staged import или с
   расхождением ног отказывает и не меняет состояние.

Открытие без маркера отказывается обслуживать db-файлы; частично записанный
import безопасно удаляется удалением `primary.db`/`recovery.db`/`ledger.db`
и `sqlcipher.manifest` (legacy frames при этом не тронуты). После активации
startup проверяет обе цепочки и witness head: хвост сверх последнего
подтверждённого head обрезается, недостающие в primary строки дозаписываются
из recovery в той же транзакции, расхождение подтверждённого префикса —
fail-closed. Неверный ключ отказывает; совпадение master и recovery key
отклоняется ещё на валидации команды.
