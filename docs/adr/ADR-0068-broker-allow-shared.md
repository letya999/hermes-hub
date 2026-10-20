---
description: "Динамические контракты могут быть allow_shared для Sharable shared-определений."
last_verified: "2026-10-16"
---

# ADR-0068 (imported from services/credential-broker ADR-0009): allow_shared в динамических контрактах

## Контекст

ADR-0067 ограничил динамические контракты per-user collect-and-deliver и явно запретил
`allow_shared` вместе с routes/OAuth/state. После этого в ToolHub появился операторский
`scope: shared` (Sharable shared definition, одна policy-owned connection и credential на всех
member'ов): такой коннектор всё ещё собирает единственный секрет через ту же защищённую форму,
но без `allow_shared` в контракте binding shared workload не может на него сослаться — и
установка снова требовала ручной операторской записи.

## Варианты

- Оставить shared-контракты только файловыми — ручной шаг сохраняется ровно в том сценарии,
  ради которого делалась динамическая регистрация.
- Разрешить `allow_shared` всем динамическим контрактам — контракт стал бы шире, чем его
  породившая reviewed definition.
- Разрешить `allow_shared` как выводимое свойство: ToolHub ставит его только когда
  definition помечена `shared + sharable` оператором через `install_shared` — выбрано.

## Решение

`RegisterContract` больше не отвергает `allow_shared`. Запрет на routes, OAuth provider и state
сохраняется: у ToolHub нет producer/consumer для них — remote-OAuth реализован на стороне
ToolHub (internal/oauth + owner locator), а `http_header`-креды remote-MCP собираются локальным
credstore. Решение о «shared» принимает не коннектор, а операторский grant `install_shared`;
форма остаётся consent boundary для значения.

## Последствия

Shared-коннекторы со статическими кредами ставятся без ручных файлов. Credential по-прежнему
единственный на connection — members разделяют его через policy-owned connection, что уже
покрыто тестами ToolHub. Ревизия ADR-0067 о «только per-user» уточняется этим решением; при
появлении per-member shared-секретов потребуется новый ADR.

## Когда пересматривать

Если потребуется маршрутизация секрета через broker routes, broker-side OAuth или
broker-managed state для произвольного коннектора — это отдельный reviewed spec, не расширение
динамической регистрации.
