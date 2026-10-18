---
id: CHG-0120
title: "Broker-only credential intake — legacy credential surface leaves communication"
type: fix
scope: internal/communication
status: done
---

# CHG-0120 — Phase 1, slice 1a: remove the legacy credential surface

The generated stack has run with Broker approval since CHG-0118's gate
blanked the store fields. This slice deletes the dead surface outright:
the local `secrets.Service`, the companion job audit ledger, the store
path fields and their env reads.

## Удаляется

- `Config.CredentialStore`, `Config.CredentialKeyFile`, `Config.AuditLedger`
  + обе ветки чтения `HUB_CREDENTIAL_STORE`, `HUB_CREDENTIAL_KEY_FILE`,
  `HUB_AUDIT_LEDGER` в `ConfigFromEnv` (env и yaml пути). CHG-0118's
  blanking становится избыточным и удаляется вместе с полями.
- `openCredentialSurface`, `g.secrets`, `g.audit` — включая toolhub
  registry attach (`opened.Registry`): `/connections` всегда идёт в
  `toolHubConnectorsHTTP` (HUB_TOOLHUB_URL есть в генерируемом compose).
- `persistCredentialForm`: `g.secrets != nil` ветка — envstore-путь
  остаётся для немanaged режима; broker/runtime путь не меняется.
- `recordJob` audit append — `audit.jsonl` уходил вместе со стором;
  structured `job outcome` log line остаётся.

## Остаётся

- `internal/secrets` — потребители `hubctl secret`/`connector` не трогаем.
- envstore persist path + form lifecycle + `Stack.GatewayOwnedSecret`
  гейтинг в `formKeys` (ModelRelaySecretKeys уже включены — ключ mgmt
  нельзя принять через форму — правильно).

## Тесты

- Удаляются (тестируют убираемое поведение):
  TestOpenCredentialSurfaceSkipsLocalStoreWhenBrokerApprovalIsConfigured,
  TestCredentialFormPersistsThroughEncryptedStore,
  TestWorkerAppendsJobCorrelation.
- TestConfigFromEnvBlanksCredentialStoreUnderBroker →
  TestConfigFromEnvIgnoresLegacyCredentialStore: env-переменные инертны,
  `New()` не трогает путь.
- TestGatewayInterceptsSecretsAndRejectsGroups — assert через envstore
  файл вместо g.secrets.List.
- TestGatewayChatCutsOpenToolHubSession — убрать мёртвые store-поля из
  фикстуры.
