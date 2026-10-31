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

# Operator language: HUB_LANG, then LC_ALL/LANG, then the OS UI culture.
$script:L = @($env:HUB_LANG, $env:LC_ALL, $env:LANG, (Get-UICulture).TwoLetterISOLanguageName) |
    ForEach-Object { if ($_) { ($_ -split '[_.\-]')[0].ToLowerInvariant() } } |
    Where-Object { $_ -in @('en', 'ru', 'kk', 'es') } | Select-Object -First 1
if (!$script:L) { $script:L = 'en' }

$T = @{
    'err.step' = @{
        en = 'The connection step did not finish. Do not send credentials to chat; report the error text without personal data.'
        ru = 'Подключение не завершено. Доступы в чат не отправляйте; сообщите текст ошибки без личных данных.'
        kk = 'Қосылым аяқталмады. Құпияларды чатқа жібермеңіз; қате мәтінін жеке деректерсіз хабарлаңыз.'
        es = 'El paso de conexión no terminó. No envíes credenciales al chat; informa del error sin datos personales.'
    }
    'err.toolhive' = @{
        en = 'ToolHive is unavailable. Ask to check the local launch.'
        ru = 'ToolHive недоступен. Попросите проверить локальный запуск.'
        kk = 'ToolHive қолжетімсіз. Жергілікті іске қосуды тексеруін сұраңыз.'
        es = 'ToolHive no está disponible. Pide que comprueben el arranque local.'
    }
    'err.workload' = @{
        en = 'A prepared Telegram server is required. Ask to check the local launch.'
        ru = 'Нужен подготовленный Telegram-сервер. Попросите проверить локальный запуск.'
        kk = 'Дайын Telegram сервері қажет. Жергілікті іске қосуды тексеруін сұраңыз.'
        es = 'Se necesita un servidor de Telegram preparado. Pide que comprueben el arranque local.'
    }
    'menu.title' = @{
        en = 'Hermes Hub personal connections setup. Secrets stay on this computer.'
        ru = 'Настройка личных подключений Hermes Hub. Секреты остаются на этом компьютере.'
        kk = 'Hermes Hub жеке қосылымдарын баптау. Құпиялар осы компьютерде қалады.'
        es = 'Configuración de conexiones personales de Hermes Hub. Los secretos permanecen en este equipo.'
    }
    'menu.save-google' = @{
        en = '1 - Save Google settings from the downloaded JSON'
        ru = '1 - Сохранить настройки Google из скачанного JSON'
        kk = '1 - Жүктелген JSON-дан Google баптауларын сақтау'
        es = '1 - Guardar la configuración de Google desde el JSON descargado'
    }
    'menu.connect-google' = @{
        en = '2 - Connect Google: mail, drive, docs, sheets, slides and calendar'
        ru = '2 - Подключить Google: почту, диск, документы, таблицы, презентации и календарь'
        kk = '2 - Google қосу: пошта, диск, құжаттар, кестелер, презентациялар және күнтізбе'
        es = '2 - Conectar Google: correo, Drive, documentos, hojas, presentaciones y calendario'
    }
    'menu.telegram' = @{
        en = '3 - Sign in to Telegram and connect full account history reading'
        ru = '3 - Войти в Telegram и подключить чтение всей истории аккаунта'
        kk = '3 - Telegram-ге кіру және аккаунт тарихын толық оқуды қосу'
        es = '3 - Iniciar sesión en Telegram y conectar la lectura de todo el historial de la cuenta'
    }
    'menu.status' = @{
        en = '4 - Show connection status (no data contents)'
        ru = '4 - Посмотреть состояние подключений (без содержимого данных)'
        kk = '4 - Қосылым күйін көру (деректер мазмұнынсыз)'
        es = '4 - Ver el estado de las conexiones (sin contenido de datos)'
    }
    'menu.refresh' = @{
        en = '5 - Refresh Google access if it expired (no new sign-in)'
        ru = '5 - Обновить доступ Google, если он истёк (без повторного входа)'
        kk = '5 - Мерзімі біткен Google рұқсатын жаңарту (қайта кірусіз)'
        es = '5 - Actualizar el acceso a Google si caducó (sin volver a entrar)'
    }
    'menu.quit' = @{
        en = '0 - Close this window'
        ru = '0 - Закрыть окно'
        kk = '0 - Терезені жабу'
        es = '0 - Cerrar la ventana'
    }
    'prompt.choice' = @{
        en = 'Action number'
        ru = 'Номер действия'
        kk = 'Әрекет нөмірі'
        es = 'Número de acción'
    }
    'dialog.title' = @{
        en = 'Choose the OAuth JSON downloaded from Google Cloud'
        ru = 'Выберите OAuth JSON, скачанный в Google Cloud'
        kk = 'Google Cloud-тан жүктелген OAuth JSON-ды таңдаңыз'
        es = 'Elige el JSON de OAuth descargado de Google Cloud'
    }
    'err.client-json' = @{
        en = 'A Web application OAuth client JSON with redirect URI http://127.0.0.1:8543/callback is required. Fix it in Google Cloud and download the JSON again.'
        ru = 'Нужен JSON OAuth клиента Web application с redirect URI http://127.0.0.1:8543/callback. Исправьте его в Google Cloud и скачайте JSON заново.'
        kk = 'http://127.0.0.1:8543/callback redirect URI-і бар Web application OAuth клиентінің JSON-ы керек. Google Cloud-та түзетіп, JSON-ды қайта жүктеңіз.'
        es = 'Se necesita el JSON de un cliente OAuth de tipo Web application con redirect URI http://127.0.0.1:8543/callback. Corrígelo en Google Cloud y descarga el JSON de nuevo.'
    }
    'prompt.email' = @{
        en = 'Email address of the Google account you want to connect'
        ru = 'Адрес Google-аккаунта, который вы хотите подключить'
        kk = 'Қосқыңыз келетін Google аккаунтының email-і'
        es = 'Correo de la cuenta de Google que quieres conectar'
    }
    'err.email' = @{
        en = 'Enter the full Google account address.'
        ru = 'Введите полный адрес Google-аккаунта.'
        kk = 'Google аккаунтының толық адресін енгізіңіз.'
        es = 'Introduce la dirección completa de la cuenta de Google.'
    }
    'info.saved' = @{
        en = 'Saved to the protected local folder. Do not send these files to anyone.'
        ru = 'Сохранено в защищённой локальной папке. Эти файлы никому не отправляйте.'
        kk = 'Қорғалған жергілікті қалтаға сақталды. Бұл файлдарды ешкімге жібермеңіз.'
        es = 'Guardado en la carpeta local protegida. No envíes estos archivos a nadie.'
    }
    'info.connect-product' = @{
        en = 'Connecting {0}. Open the link below in your browser, choose your account and allow reading.'
        ru = 'Подключаем {0}. Откройте следующую ссылку в браузере, выберите свой аккаунт и разрешите чтение.'
        kk = '{0} қосылуда. Төменгі сілтемені браузерде ашып, өз аккаунтыңызды таңдап, оқуға рұқсат беріңіз.'
        es = 'Conectando {0}. Abre el enlace siguiente en el navegador, elige tu cuenta y permite la lectura.'
    }
    'info.telegram-done' = @{
        en = 'Telegram is already set up. Check item 4; signing in again is not needed now.'
        ru = 'Telegram уже настроен. Проверьте пункт 4; повторный вход сейчас не нужен.'
        kk = 'Telegram әлдеқашан бапталған. 4-ші тармақты тексеріңіз; қазір қайта кіру керек емес.'
        es = 'Telegram ya está configurado. Revisa el punto 4; no hace falta entrar de nuevo.'
    }
    'err.proxy' = @{
        en = 'The local Telegram proxy is unavailable.'
        ru = 'Локальный Telegram-прокси недоступен.'
        kk = 'Жергілікті Telegram проксиі қолжетімсіз.'
        es = 'El proxy local de Telegram no está disponible.'
    }
    'err.proxy-ip' = @{
        en = 'Local proxy address not found.'
        ru = 'Не найден адрес локального прокси.'
        kk = 'Жергілікті прокси адресі табылмады.'
        es = 'No se encontró la dirección del proxy local.'
    }
    'info.hidden-input' = @{
        en = 'Enter data only in this window. Pasting into hidden fields may show no characters - that is normal.'
        ru = 'Вводите данные только в этом окне. Вставка скрытых полей может не показывать символы — это нормально.'
        kk = 'Деректерді тек осы терезеде енгізіңіз. Жасырын өрістерде символдар көрсетілмеуі мүмкін — бұл қалыпты.'
        es = 'Introduce los datos solo en esta ventana. Los campos ocultos pueden no mostrar caracteres al pegar: es normal.'
    }
    'err.telegram-login' = @{
        en = 'Telegram sign-in did not finish. Do not forward the code or password; check your input and Telegram attempt limits.'
        ru = 'Вход в Telegram не завершён. Не пересылайте код или пароль; проверьте ввод и ограничения попыток Telegram.'
        kk = 'Telegram-ге кіру аяқталмады. Кодты немесе құпиясөзді ешкімге жібермеңіз; енгізуді және Telegram әрекет шектеулерін тексеріңіз.'
        es = 'No se completó el inicio de sesión en Telegram. No reenvíes el código ni la contraseña; revisa la entrada y los límites de intentos de Telegram.'
    }
    'info.telegram-connected' = @{
        en = 'Telegram connected. The temporary session file was removed; the working access is stored encrypted.'
        ru = 'Telegram подключён. Временный файл сессии удалён; рабочий доступ хранится зашифрованно.'
        kk = 'Telegram қосылды. Уақытша сессия файлы жойылды; жұмыс рұқсаты шифрленген сақталады.'
        es = 'Telegram conectado. El archivo temporal de sesión se eliminó; el acceso se guarda cifrado.'
    }
    'err.choice' = @{
        en = 'Enter 1, 2, 3, 4, 5 or 0.'
        ru = 'Введите 1, 2, 3, 4, 5 или 0.'
        kk = '1, 2, 3, 4, 5 немесе 0 енгізіңіз.'
        es = 'Introduce 1, 2, 3, 4, 5 o 0.'
    }
    'err.step-failed' = @{
        en = 'The step did not finish. Check the chosen JSON, Google settings and previous messages. If you need help, name the step; do not send files, codes or passwords to chat.'
        ru = 'Шаг не завершён. Проверьте выбранный JSON, настройки Google и предыдущие сообщения. Если нужна помощь, сообщите название шага; файлы, коды и пароли в чат не присылайте.'
        kk = 'Қадам аяқталмады. Таңдалған JSON-ды, Google баптауларын және алдыңғы хабарларды тексеріңіз. Көмек керек болса, қадам атын хабарлаңыз; файлдарды, кодтарды немесе құпиясөздерді чатқа жібермеңіз.'
        es = 'El paso no terminó. Revisa el JSON elegido, la configuración de Google y los mensajes anteriores. Si necesitas ayuda, indica el nombre del paso; no envíes archivos, códigos ni contraseñas al chat.'
    }
}
function T([string]$Key) {
    $v = $T[$Key][$script:L]
    if (!$v) { $T[$Key]['en'] } else { $v }
}

function Invoke-Hub([string[]]$Arguments) {
    & $hubctl @Arguments
    if ($LASTEXITCODE -ne 0) { throw (T 'err.step') }
}

function Get-TelegramWorkload {
    $manifest = [IO.File]::ReadAllText((Join-Path $Root 'telegram-deployment.json')) | ConvertFrom-Json
    $json = (& $thv list --all --format json | Out-String)
    if ($LASTEXITCODE -ne 0) { throw (T 'err.toolhive') }
    $workloads = $json.Substring($json.IndexOf('[')) | ConvertFrom-Json
    $matches = @($workloads | Where-Object { $_.package -eq $manifest.source.digest -and $_.url -eq 'http://127.0.0.1:8546/mcp' -and $_.status -eq 'running' })
    if ($matches.Count -ne 1 -or $matches[0].name -notmatch '^work-[a-f0-9]{24}$') { throw (T 'err.workload') }
    return $matches[0]
}

Write-Host (T 'menu.title')
while ($true) {
    Write-Host "`n$(T 'menu.save-google')"
    Write-Host (T 'menu.connect-google')
    Write-Host (T 'menu.telegram')
    Write-Host (T 'menu.status')
    Write-Host (T 'menu.refresh')
    Write-Host (T 'menu.quit')
    $choice = Read-Host (T 'prompt.choice')
    try {
        switch ($choice) {
            '0' { exit }
            '1' {
                Add-Type -AssemblyName System.Windows.Forms
                $dialog = New-Object Windows.Forms.OpenFileDialog
                $dialog.Title = T 'dialog.title'
                $dialog.Filter = 'JSON (*.json)|*.json'
                if ($dialog.ShowDialog() -ne 'OK') { continue }
                $client = ([IO.File]::ReadAllText($dialog.FileName) | ConvertFrom-Json).web
                if (!$client.client_id -or !$client.client_secret -or $client.client_id -match '[\r\n]' -or $client.client_secret -match '[\r\n]' -or 'http://127.0.0.1:8543/callback' -notin $client.redirect_uris) { throw (T 'err.client-json') }
                $email = Read-Host (T 'prompt.email')
                if ($email -notmatch '^[^\s@]+@[^\s@]+$') { throw (T 'err.email') }
                [IO.File]::WriteAllText((Join-Path $Root 'input/google-client.env'), "CLIENT_ID=$($client.client_id)`nCLIENT_SECRET=$($client.client_secret)`n")
                [IO.File]::WriteAllText((Join-Path $Root 'input/google-account.txt'), $email)
                Write-Host (T 'info.saved')
            }
            '2' {
                $email = [IO.File]::ReadAllText((Join-Path $Root 'input/google-account.txt')).Trim()
                foreach ($product in @('gmail','drive','docs','sheets','slides','calendar')) {
                    Write-Host ("`n" + ((T 'info.connect-product') -f $product))
                    Invoke-Hub @('connector','connect','--user','me','--provider','google','--official-mcp','--product',$product,'--account',$email,'--connection',"google-$product-read-1",'--client-file',(Join-Path $Root 'input/google-client.env'),'--callback-port','8543')
                }
            }
            '3' {
                $workload = Get-TelegramWorkload
                $sessionFile = Join-Path $Root 'input/telegram/telegram-session.env'
                $accountFile = Join-Path $Root 'input/telegram/telegram-account-id.txt'
                if (!(Test-Path -LiteralPath $sessionFile) -and (Test-Path -LiteralPath $accountFile)) { Write-Host (T 'info.telegram-done'); continue }
                if (!(Test-Path -LiteralPath $sessionFile)) {
                    $network = "hermes-$($workload.name)"
                    $proxyNetworks = (& docker inspect "$($workload.name)-proxy" --format '{{json .NetworkSettings.Networks}}' | Out-String) | ConvertFrom-Json
                    if ($LASTEXITCODE -ne 0) { throw (T 'err.proxy') }
                    $proxyIP = $proxyNetworks.$network.IPAddress
                    $parsedIP = $null
                    if (![Net.IPAddress]::TryParse($proxyIP, [ref]$parsedIP)) { throw (T 'err.proxy-ip') }
                    Write-Host (T 'info.hidden-input')
                    & docker run --rm -it --network $network --cpus 1 --memory 512m --memory-swap 512m --pids-limit 64 --read-only --env "HTTP_PROXY=http://${proxyIP}:3128" --env "HUB_LANG=$script:L" --mount "type=bind,source=$(Join-Path $Root 'input/telegram'),target=/login-output" --entrypoint /opt/telegram/.venv/bin/python $workload.package /opt/hub/telegram-account-login.py
                    if ($LASTEXITCODE -ne 0) { throw (T 'err.telegram-login') }
                }
                $account = [IO.File]::ReadAllText($accountFile).Trim()
                Invoke-Hub @('connector','connect','--user','me','--provider','telegram','--account',$account,'--connection','telegram-read-1','--from-file',$sessionFile,'--deployment-file',(Join-Path $Root 'telegram-deployment.json'),'--endpoint','http://127.0.0.1:8546/mcp')
                Remove-Item -LiteralPath $sessionFile
                Write-Host (T 'info.telegram-connected')
            }
            '4' { Invoke-Hub @('connector','status','--user','me') }
            '5' {
                foreach ($product in @('gmail','drive','docs','sheets','slides','calendar')) {
                    Invoke-Hub @('connector','refresh','--user','me','--connection',"google-$product-read-1",'--client-file',(Join-Path $Root 'input/google-client.env'))
                }
            }
            default { Write-Host (T 'err.choice') }
        }
    } catch { Write-Host (T 'err.step-failed') -ForegroundColor Red }
}
