---
description: "Однопроцессный encrypted journal."
last_verified: "2026-11-15"
---

# ADR-0063 (imported from services/credential-broker ADR-0004): Однопроцессный encrypted journal

Status: accepted (imported from services/credential-broker).

**Traceability:** [SPEC-0064](../../specs/active/SPEC-0064-credential-broker-v1.md), broker module `services/credential-broker`.

## Контекст

MVP должен работать без БД, но не терять authorization после restart.

## Варианты

In-memory; SQL service; bounded encrypted append journal.

## Решение

Fsync journal с AEAD/hash chain, flock, fail-closed corruption, replay metadata; leases ephemeral.

## Последствия

Нет HA/compaction/external anti-rollback anchor. Полный диск — ошибка доступа, не silent success.

## Когда пересматривать

При росте ledger, требованиях HA, migration или внешнем audit retention.
