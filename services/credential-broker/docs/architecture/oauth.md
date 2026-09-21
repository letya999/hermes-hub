---
description: "Предзарегистрированный OAuth-клиент, PKCE, refresh и пределы совместимости."
last_verified: "2026-09-18"
---

# OAuth в Broker

Администратор задаёт authorize/token endpoints, client ID, exact callback, scopes и client-secret ref.
Origin callback формируется как `public_origin + /oauth/callback`. OAuth endpoints должны быть HTTPS;
dev HTTP scaffold не является OAuth Google integration.

Поддержаны client authentication `none`, `post`, `basic`. PKCE S256 обязательна. Browser сначала
подтверждается через Communication Hub; OAuth state привязан к этому браузеру и request. State consume
записывается durable до token exchange, поэтому replay callback не запускает второй exchange.

Access/refresh token находятся в выбранном provider, не в control responses. Access token выдаётся
только внутреннему proxy. Refresh сериализован на credential; новая версия refresh token сохраняется
до её использования следующими запросами. Ошибка refresh или хранения замены переводит credential
в reauthorize_required и закрывает старые leases. Это консервативно: даже транзитная ошибка может
потребовать нового consent вместо опасного повторения обмена rotating refresh token.

Нет OIDC identity login, ID-token validation, device flow, dynamic registration, resource discovery,
универсального provider revocation endpoint или автоматической упаковки токенов в формат чужого CLI.
OAuth state/PKCE verifier — не RFC grant lease хранилища. Обычный JSON OAuth client config сам по себе
не позволяет Broker угадать workflow конкретного Google MCP.

Проверки используют локальный HTTPS OAuth simulator, включая rotation, параллельный refresh,
ошибки и restart. Успех этих проверок не означает выполненный consent в реальном Google tenant.
