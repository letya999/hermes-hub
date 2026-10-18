---
id: CHG-0124
title: "Service auth — shared tokens-file contract for communication and ToolHub"
type: refactor
scope: internal/serviceauth, internal/communication, internal/toolhub, internal/e2e
status: in-progress
---

# CHG-0124 — Phase 3a: internal/serviceauth

Карта зон плана v2: service auth — bearer, X-Hub-Principal, tokens-file,
привязка контекста. Файл `{"<token>": Envelope}` сегодня парсится в 4 местах
с разной валидацией:

- `communication/http.go` controlTokenAccepted — ≤1MiB, валидирует только
  найденную запись, пиннит к config user.
- `communication/gateway_toolhub.go` toolHubCredential — без bound,
  валидация envelope отсутствует, скан по PrincipalID + token≥32.
- `toolhub/server.go` loadTokenEnvelopes — без bound, валидирует всё +
  token≥32 + nonempty, ошибка лоудная.
- `internal/e2e/tool_admission.go` — ≤1MiB+1, без валидации.

## Slice

`internal/serviceauth`:
- `MaxTokensFile` bound; `ParseTokenEnvelopes(body)` — decode + reject
  CR/LF/NUL в токене + `Envelope.Validate` каждой записи; ошибка любой
  записи инвалидирует файл целиком (fail-closed, как у ToolHub —
  коммуникационная мягкость «плохая запись не трогает остальные»
  ужесточается, задокументировано).
- `ReadTokenEnvelopes(path)` — bound + parse.

Auth-решения остаются локальными: communication пиннит к config users и
читает файл на каждый вызов; ToolHub — in-memory map + reload-on-miss +
constant-time; разная политика — не копия. Скан по principal и политика
длины токена остаются у вызывающих — у них разные правила.

Переключены 3 production-сайта: `controlTokenAccepted`,
`toolHubCredential`, `loadTokenEnvelopes` (с сохранением локальной
политики ≥32 + nonempty). `internal/e2e/tool_admission.go` намеренно
оставлен на своём чтении: это test-harness, а не security-граница —
файл пишется самим тестом, и переключение требует живого MCP-сервера
для наблюдаемой разницы.

## Acceptance

- `just check` PASS, ≥85%.
- Поведение: только ужесточение — файл с битой записью теперь целиком
  отвергается в communication (было — только найденная запись).
