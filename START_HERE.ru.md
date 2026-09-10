# Hermes общего назначения

Один самостоятельный проект. Go управляет запуском и MCP-инструментами; сам Hermes
остаётся upstream-агентом на Python. CareerGo и JobFetch внутрь не входят и не нужны.

1. Установи Docker / Docker Desktop с Compose 2.30+.
2. Выбери бинарник в bin/. На Windows это hubctl-windows-amd64.exe.
3. Выполни `hubctl init --user artem`.
4. Укажи модель и model_url в spaces/artem/settings.yaml, ключи в secrets.prod.env.
5. `hubctl doctor --user artem`, затем `hubctl up --user artem --env prod`.
6. `hubctl chat --user artem` или Telegram-бот, если включил telegram.

В примерах hubctl означает путь к выбранному бинарнику. Сборку запускай из корня проекта.

**Пользовательский путь: spaces/<user>.** Внутри один settings.yaml, SOUL.md, отдельные
secrets.dev.env и secrets.prod.env. Dev/prod — режимы Docker, а не дополнительные спейсы.
Память, skills, hooks, токены OAuth, браузер и рабочие файлы каждого режима лежат в
отдельных Docker-томах. Тестовый контейнер не пишет в память продакшена.

Для dev: заполни secrets.dev.env и выполни `hubctl up --user artem --env dev`.
Dev содержит Go и монтирует исходники в /src. Prod не монтирует исходники проекта.
Для другого человека: `hubctl init --user anna`; укажи её токены и свободные порты.

Skills, hooks и memory — штатные механизмы Hermes. Например:
`hubctl exec --user artem -- hermes skills list`.
Через exec можно запускать остальные штатные команды Hermes. Одновременно запускать
CLI-агента и Telegram gateway с одной памятью нельзя; для экспериментов используй dev.

Подключения выбираются в features: Telegram, Google Workspace, HH, Slack, GitHub,
Atlassian, Meet, браузер, локальная транскрибация. Для Jira через Atlassian MCP указываются
`JIRA_URL`, `JIRA_USERNAME` и `JIRA_API_TOKEN`; для GitLab — `GITLAB_TOKEN`. Google по умолчанию доступен только
для чтения; запись включает `google_write`. GitLab доступен через встроенный `glab`.
Другие MCP добавляются в mcp_servers.
В Telegram попроси «какие сервисы доступны», затем «включи GitLab» или другой
self-service коннектор. Hermes покажет требуемые имена env, примет явные `KEY=value`,
сохранит их в изолированном runtime и перезапустится. Host-managed функции бот только
показывает — их нужно включать в settings.yaml.
Интернет доступен через браузер; для web_search можно добавить Firecrawl/Tavily API key.

Понадобятся твои ключи и входы: Telegram bot token и ID владельца; для чтения личного
аккаунта API ID/hash и сессия; для Google OAuth; для откликов HH токен соискателя и
резюме, уже размещённое на HH. Полные шаги: SETUP.md.

Локальный CI: `just check`. Он запускает Go race-тесты, линтеры и проверки;
порог покрытия всего собственного Go-кода — 85%.
Лицензия исходного проекта AGPL-3.0-only, GitHub Actions и self-hosted Runner подготовлены.
Фактически выполненные проверки и ограничения описаны в docs/validation.md.
