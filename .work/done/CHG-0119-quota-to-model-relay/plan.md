---
id: CHG-0119
title: "Quota lookup moves from communication to model-relay"
type: fix
scope: internal/runtime, internal/communication, internal/stack
status: in-progress
---

# CHG-0119 — Quota → model-relay (phase 1, slice 1c)

## Цель

Убрать из communication-hub прямое владение management-ключом CLIProxy:
запрос квоты подписки переносится за HTTP-границу model-relay, который уже
владеет cliproxy-маршрутизацией и secrets. Communication оставляет себе только
рендер строк `/usage` и 5-минутный кэш.

## Контракт

- `GET {HUB_MODEL_RELAY_URL}/v1/quota` — Bearer `HUB_MODEL_RELAY_ADMIN`
  (тот же токен, что у `/v1/model`), ответ `{"lines": ["…"]}` — рендер русских
  строк живёт в релее рядом с источником данных.
- Relay читает `HUB_CLIPROXY_MGMT_KEY` (обязательно; без него честная строка
  «не настроен»), `HUB_CLIPROXY_MGMT_URL` (fallback `HUB_MODEL_RELAY_CLIPROXY`),
  `HUB_CLIPROXY_AUTH_INDEX` — дословно переносится из communication/quota.go.
- Communication при отсутствии `HUB_MODEL_RELAY_URL` отвечает той же строкой
  «источник не настроен»; unmanaged-режим теряет /usage-квоты так же, как уже
  теряет /model.

## Stack

- `HUB_CLIPROXY_*` выходят из `GatewaySecretKeys()` → `ModelRelaySecretKeys()`,
  рендерятся в новый `model-relay.<env>.env`, который добавляется в env_file
  model-relay. Ключ больше не попадает в env communication-hub.

## Тесты

- relay: 401 без/с неверным токеном, строки от fake management-сервера,
  честная строка без ключа.
- communication: `/usage` идёт в relay с bearer; без URL — «не настроен».
- stack: cliproxy-ключи в model-relay.env, отсутствуют в communication.env.
