---
description: "Ограничения Go-кода, ошибки и работа с секретными данными."
last_verified: "2026-09-18"
---

# Правила кода

Стандартный gofmt, go vet и явные errors. Наружу не возвращать provider response body, token,
физический stack trace или raw internal error. Логирование body/query/Authorization запрещено.
Use-context должен закрываться `defer Finish`; секретные byte buffers очищаются best-effort.

Go string/GC не дают гарантированного wipe каждой копии. Не описывать clear как аппаратное удаление.
Новые goroutines должны иметь bounded lifetime/context; нет бесконтрольных фоновых refresh loops.
Внешние операции выполняются с deadline, redirect policy и ограничением response size.

В публичных Go методах возвращать копии maps/slices, чтобы caller не менял сохранённую политику.
Изменение concurrent state — через core lock и durable commit. Ошибка journal переводит доступ
в unavailable; запрещено продолжать «для доступности» после потери authorization ledger.
