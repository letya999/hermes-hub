---
description: "Broker не является secret store."
last_verified: "2026-09-18"
---

# ADR-0060 (imported from services/credential-broker ADR-0001): Broker не является secret store

Status: accepted (imported from services/credential-broker).

## Контекст

Нужно согласовать зоопарк MCP credentials с разными хранилищами.

## Варианты

Canonical Vault; mandatory внешняя платформа; replaceable provider SPI.

## Решение

Выбран собственный lightweight broker с optional local и external adapters. Metadata ledger отдельно от secret provider.

## Последствия

Vault не обязателен; каждый новый provider требует contract tests и явных capabilities.

## Когда пересматривать

Когда нужен недоступный backend или distributed transaction semantics.
