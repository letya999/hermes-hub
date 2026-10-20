---
description: "Request-scoped extras и понижение обязательности поля на сабмите формы."
last_verified: "2026-10-17"
---

# ADR-0069 (imported from services/credential-broker ADR-0010): effective contract на запрос — extras и demote

## Контекст

Reviewed-контракт по дизайну покрывает только проверенные поля. На практике upstream
метаданные (все .env.example репозитория, README, compose) объявляют больше входов, чем
контракт доставляет: лишний `GITLAB_PROJECT_ID` из docker/.env.example делал reviewed
ToolHub-определение невалидным и ронял онбординг до формы. Владелец при этом лучше знает,
какие поля реально нужны его серверу.

## Варианты

- Расширять контракт новой revision под каждое upstream-поле — шумная ревизия на кривые
  метаданные; форма остаётся жёсткой.
- Понижать все не-reviewed поля до optional на стороне ToolHub — решает «лишние», но не даёт
  владельцу сознательно пропустить required-поле для конкретной попытки.
- Хранить два контракта (reviewed + effective) — выбрано: effective contract вычисляется
  на записи запроса/креденшела, reviewed неизменен.

## Решение

`CreateRequest` принимает request-scoped `Extra` — только опциональные поля и только
env-доставки, без коллизий с reviewed id/target. Форма принимает `opt:<field-id>`:
понижение required-поля действует на данный сабмит и записывается на запрос/креденшел.
`SessionView`, `Submit`, `BeginUse` и материализация работают по effective contract;
`Compatible()` и digest контракта не затрагиваются. См.
specs/active/SPEC-0066-credential-broker-effective-contract.md.

## Последствия

- Форма обязана маркировать обязательность и рендерить extras + demote-checkbox;
  `formnovalidate`-submit нужен, чтобы пониженное поле можно было отправить пустым.
- Несоответствие «пропустил required — сервер не поднялся» ловится admission-probe на
  стороне вызывающего, который заново выпускает форму; Broker за это не отвечает.
- Replay старых journal-записей без extras/demotions интерпретируется как пустые —
  обратной миграции не требуется.

## Когда пересматривать

Если появится спрос на не-env extras (file/json доставки), request-scoped route-поля или
persisted изменение обязательности контракта без новой ревизии.
