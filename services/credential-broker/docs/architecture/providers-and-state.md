---
description: "Какие данные где находятся и как добавить новый storage adapter."
last_verified: "2026-09-18"
---

# Providers, файлы и изменяемое состояние

## SecretProvider

Публичный SPI — [provider/provider.go](../../provider/provider.go): capabilities, Read, Write, Delete.
Bundle содержит байтовые поля; Ref содержит provider/locator/version. Provider выбирается в reviewed
contract `storage`, а не произвольным пользовательским path. External alias заранее связывает ref
с owner и contract. Browser подтверждает использование alias, не получает locator input с доступом ко всему Vault.

| Provider | Read | Write | Поведение |
| --- | --- | --- | --- |
| local | Да | Да | Immutable AEAD files 0600, производный ключ, отдельный каталог |
| vault_kv2 | Да | Опционально | Fixed mount/prefix, existing values, CAS=0 для новых managed locations |
| kubernetes | Да | Нет | Fixed namespace, named Secret, pinned resourceVersion при указании |

Запись формы в read-only backend не поддерживается: использовать writable storage или импортировать
заранее настроенный alias. OAuth drafts/token rotation и state snapshots требуют writable provider.
Local provider не становится обязательным источником истины. Даже без него Broker хранит зашифрованный
metadata ledger и нуждается в ключе для своей служебной информации.

Vault locator трактуется **относительно prefix**: prefix `hermes-broker`, locator `team/github`
соответствуют API `/v1/secret/data/hermes-broker/team/github`. Secret fields должны совпасть с contract IDs.
Vault-managed объекты используют base64 envelope; импорт обычных string/JSON полей также поддержан.
Kubernetes locator — имя Secret внутри настроенного namespace, а не URL или filesystem path.

## Materializer

ENV выдаётся только runtime response. File plaintext создаётся 0400; каталог — 0700. `env_name` у file
означает путь внутри MCP, а не содержимое файла. `json_file.mapping` записывает значения как JSON strings;
он не выполняет шаблон, shell expansion, JSONPath или произвольный код.

Для state объявляются target directory и точный список файлов с bounds. Каждый binding имеет собственный
snapshot. Файлы не загружаются целым архивом; path traversal, symlink и hardlink отвергаются.
State snapshot хранится как отдельный provider Bundle, не как session JSON в metadata ledger.

Плоские JSON session files поддержаны. Произвольные директории browser/CLI, SQLite/WAL-consistent backup,
file watching, SSH agent и облачный STS не реализованы. Их нельзя объявить готовыми просто новым kind в JSON.
