param([string]$Root = (Join-Path $env:USERPROFILE 'hermes-hub-private'))
$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path -LiteralPath $Root).Path
$env:HUB_STATE = Join-Path $Root 'spaces/me/runtime'
$env:HUB_TOOLHUB_STORE = Join-Path $env:HUB_STATE 'toolhub/store.json'
$env:HUB_CREDENTIAL_STORE = Join-Path $env:HUB_STATE 'credentials/store.enc'
$env:HUB_CREDENTIAL_KEY_FILE = Join-Path $Root 'keys/credential.key'
$env:HUB_TOOLHIVE_ADMISSION_ENDPOINT = 'http://127.0.0.1:8544/admit'
$env:HUB_TOOLHIVE_ADMISSION_TOKEN = [IO.File]::ReadAllText((Join-Path $Root 'keys/admission.key')).Trim()
$env:HUB_RUNTIME_AUTH = [IO.File]::ReadAllText((Join-Path $Root 'keys/toolhub.key')).Trim()
$env:HUB_USER_ID = 'me'
$env:HUB_CONTEXT_ID = 'me'
$env:HUB_RUNTIME_ID = 'me'
$env:HUB_TOOLHUB_LISTEN = '127.0.0.1:8545'
$env:HUB_AUDIT_LEDGER = Join-Path $env:HUB_STATE 'credentials/audit.jsonl'
$env:XDG_CONFIG_HOME = Join-Path $Root 'toolhive/config'
$env:XDG_DATA_HOME = Join-Path $Root 'toolhive/data'
$env:XDG_CACHE_HOME = Join-Path $Root 'toolhive/cache'
$env:XDG_RUNTIME_DIR = Join-Path $Root 'toolhive/runtime'

$controllerBinary = Join-Path $Root 'bin/hubctl.exe'
$endpointBinary = Join-Path $Root 'bin/toolhub.exe'
$controller = @(Get-CimInstance Win32_Process -Filter "Name='hubctl.exe'" | Where-Object { $_.ExecutablePath -eq $controllerBinary -and $_.CommandLine -match 'local-controller' })
if ($controller.Count -eq 0) {
    Start-Process -FilePath $controllerBinary -ArgumentList @('connector','local-controller','--config',(Join-Path $Root 'local-controller.json'),'--token-file',(Join-Path $Root 'keys/admission.key')) -WindowStyle Hidden -RedirectStandardError (Join-Path $Root 'controller-error.log') -RedirectStandardOutput (Join-Path $Root 'controller.log')
}
$endpoint = @(Get-CimInstance Win32_Process -Filter "Name='toolhub.exe'" | Where-Object { $_.ExecutablePath -eq $endpointBinary })
if ($endpoint.Count -eq 0) {
    Start-Process -FilePath $endpointBinary -WindowStyle Hidden -RedirectStandardError (Join-Path $Root 'toolhub-error.log') -RedirectStandardOutput (Join-Path $Root 'toolhub.log')
}
Write-Host 'Локальные службы запущены. Если Docker Desktop перезапущен или службы сообщили ошибку, попросите проверить запуск; рабочие контейнеры не удаляйте.'
