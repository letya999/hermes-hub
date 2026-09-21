param([string]$Root = (Join-Path $env:USERPROFILE 'hermes-hub-private'))
$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path -LiteralPath $Root).Path
$hubctl = Join-Path $Root 'bin/hubctl.exe'
$thv = Join-Path $Root 'bin/thv.exe'
$env:HUB_STATE = Join-Path $Root 'spaces/me/runtime'
$env:HUB_TOOLHUB_STORE = Join-Path $env:HUB_STATE 'toolhub/store.json'
$env:HUB_CREDENTIAL_STORE = Join-Path $env:HUB_STATE 'credentials/store.enc'
$env:HUB_CREDENTIAL_KEY_FILE = Join-Path $Root 'keys/credential.key'
$env:HUB_TOOLHIVE_ADMISSION_ENDPOINT = 'http://127.0.0.1:8544/admit'
$env:HUB_TOOLHIVE_ADMISSION_TOKEN = [IO.File]::ReadAllText((Join-Path $Root 'keys/admission.key')).Trim()
$env:XDG_CONFIG_HOME = Join-Path $Root 'toolhive/config'
$env:XDG_DATA_HOME = Join-Path $Root 'toolhive/data'
$env:XDG_CACHE_HOME = Join-Path $Root 'toolhive/cache'
$env:XDG_RUNTIME_DIR = Join-Path $Root 'toolhive/runtime'

function Invoke-Hub([string[]]$Arguments) {
    & $hubctl @Arguments
    if ($LASTEXITCODE -ne 0) { throw 'Подключение не завершено. Доступы в чат не отправляйте; сообщите текст ошибки без личных данных.' }
}

function Get-TelegramWorkload {
    $manifest = [IO.File]::ReadAllText((Join-Path $Root 'telegram-deployment.json')) | ConvertFrom-Json
    $json = (& $thv list --all --format json | Out-String)
    if ($LASTEXITCODE -ne 0) { throw 'ToolHive недоступен. Попросите проверить локальный запуск.' }
    $workloads = $json.Substring($json.IndexOf('[')) | ConvertFrom-Json
    $matches = @($workloads | Where-Object { $_.package -eq $manifest.source.digest -and $_.url -eq 'http://127.0.0.1:8546/mcp' -and $_.status -eq 'running' })
    if ($matches.Count -ne 1 -or $matches[0].name -notmatch '^work-[a-f0-9]{24}$') { throw 'Нужен подготовленный Telegram-сервер. Попросите проверить локальный запуск.' }
    return $matches[0]
}

Write-Host 'Настройка личных подключений Hermes Hub. Секреты остаются на этом компьютере.'
while ($true) {
    Write-Host "`n1 — Сохранить настройки Google из скачанного JSON"
    Write-Host '2 — Подключить Google: почту, диск, документы, таблицы, презентации и календарь'
    Write-Host '3 — Войти в Telegram и подключить чтение всей истории аккаунта'
    Write-Host '4 — Посмотреть состояние подключений (без содержимого данных)'
    Write-Host '5 — Обновить доступ Google, если он истёк (без повторного входа)'
    Write-Host '0 — Закрыть окно'
    $choice = Read-Host 'Номер действия'
    try {
        switch ($choice) {
            '0' { exit }
            '1' {
                Add-Type -AssemblyName System.Windows.Forms
                $dialog = New-Object Windows.Forms.OpenFileDialog
                $dialog.Title = 'Выберите OAuth JSON, скачанный в Google Cloud'
                $dialog.Filter = 'JSON (*.json)|*.json'
                if ($dialog.ShowDialog() -ne 'OK') { continue }
                $client = ([IO.File]::ReadAllText($dialog.FileName) | ConvertFrom-Json).web
                if (!$client.client_id -or !$client.client_secret -or $client.client_id -match '[\r\n]' -or $client.client_secret -match '[\r\n]' -or 'http://127.0.0.1:8543/callback' -notin $client.redirect_uris) { throw 'Нужен JSON OAuth клиента Web application с redirect URI http://127.0.0.1:8543/callback. Исправьте его в Google Cloud и скачайте JSON заново.' }
                $email = Read-Host 'Адрес Google-аккаунта, который вы хотите подключить'
                if ($email -notmatch '^[^\s@]+@[^\s@]+$') { throw 'Введите полный адрес Google-аккаунта.' }
                [IO.File]::WriteAllText((Join-Path $Root 'input/google-client.env'), "CLIENT_ID=$($client.client_id)`nCLIENT_SECRET=$($client.client_secret)`n")
                [IO.File]::WriteAllText((Join-Path $Root 'input/google-account.txt'), $email)
                Write-Host 'Сохранено в защищённой локальной папке. Эти файлы никому не отправляйте.'
            }
            '2' {
                $email = [IO.File]::ReadAllText((Join-Path $Root 'input/google-account.txt')).Trim()
                foreach ($product in @('gmail','drive','docs','sheets','slides','calendar')) {
                    Write-Host "`nПодключаем $product. Откройте следующую ссылку в браузере, выберите свой аккаунт и разрешите чтение."
                    Invoke-Hub @('connector','connect','--user','me','--provider','google','--official-mcp','--product',$product,'--account',$email,'--connection',"google-$product-read-1",'--client-file',(Join-Path $Root 'input/google-client.env'),'--callback-port','8543')
                }
            }
            '3' {
                $workload = Get-TelegramWorkload
                $sessionFile = Join-Path $Root 'input/telegram/telegram-session.env'
                $accountFile = Join-Path $Root 'input/telegram/telegram-account-id.txt'
                if (!(Test-Path -LiteralPath $sessionFile) -and (Test-Path -LiteralPath $accountFile)) { Write-Host 'Telegram уже настроен. Проверьте пункт 4; повторный вход сейчас не нужен.'; continue }
                if (!(Test-Path -LiteralPath $sessionFile)) {
                    $network = "hermes-$($workload.name)"
                    $proxyNetworks = (& docker inspect "$($workload.name)-proxy" --format '{{json .NetworkSettings.Networks}}' | Out-String) | ConvertFrom-Json
                    if ($LASTEXITCODE -ne 0) { throw 'Локальный Telegram-прокси недоступен.' }
                    $proxyIP = $proxyNetworks.$network.IPAddress
                    $parsedIP = $null
                    if (![Net.IPAddress]::TryParse($proxyIP, [ref]$parsedIP)) { throw 'Не найден адрес локального прокси.' }
                    Write-Host 'Вводите данные только в этом окне. Вставка скрытых полей может не показывать символы — это нормально.'
                    & docker run --rm -it --network $network --cpus 1 --memory 512m --memory-swap 512m --pids-limit 64 --read-only --env "HTTP_PROXY=http://${proxyIP}:3128" --mount "type=bind,source=$(Join-Path $Root 'input/telegram'),target=/login-output" --entrypoint /opt/telegram/.venv/bin/python $workload.package /opt/hub/telegram-account-login.py
                    if ($LASTEXITCODE -ne 0) { throw 'Вход в Telegram не завершён. Не пересылайте код или пароль; проверьте ввод и ограничения попыток Telegram.' }
                }
                $account = [IO.File]::ReadAllText($accountFile).Trim()
                Invoke-Hub @('connector','connect','--user','me','--provider','telegram','--account',$account,'--connection','telegram-read-1','--from-file',$sessionFile,'--deployment-file',(Join-Path $Root 'telegram-deployment.json'),'--endpoint','http://127.0.0.1:8546/mcp')
                Remove-Item -LiteralPath $sessionFile
                Write-Host 'Telegram подключён. Временный файл сессии удалён; рабочий доступ хранится зашифрованно.'
            }
            '4' { Invoke-Hub @('connector','status','--user','me') }
            '5' {
                foreach ($product in @('gmail','drive','docs','sheets','slides','calendar')) {
                    Invoke-Hub @('connector','refresh','--user','me','--connection',"google-$product-read-1",'--client-file',(Join-Path $Root 'input/google-client.env'))
                }
            }
            default { Write-Host 'Введите 1, 2, 3, 4, 5 или 0.' }
        }
    } catch { Write-Host 'Шаг не завершён. Проверьте выбранный JSON, настройки Google и предыдущие сообщения. Если нужна помощь, сообщите название шага; файлы, коды и пароли в чат не присылайте.' -ForegroundColor Red }
}
