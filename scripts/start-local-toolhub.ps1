param([string]$Root = (Split-Path -Parent $PSScriptRoot))
$ErrorActionPreference = 'Stop'
$Root = (Resolve-Path -LiteralPath $Root).Path
$state = (Resolve-Path -LiteralPath (Join-Path $Root 'spaces/local/runtime')).Path
$binary = (Resolve-Path -LiteralPath (Join-Path $Root 'bin/toolhub.exe')).Path
$hubctl = (Resolve-Path -LiteralPath (Join-Path $Root 'bin/hubctl.exe')).Path
$toolhive = (Get-Command thv.exe -ErrorAction Stop).Source
$bridge = Join-Path $state 'hubctl-linux-bridge'
$controllerConfig = Join-Path $state 'generic-controller.json'
$controllerTokenFile = Join-Path $state 'generic-controller.key'
$auth = [IO.File]::ReadAllText((Join-Path $Root 'spaces/local/runtime.auth')).Trim()
if (!$auth.StartsWith('HUB_RUNTIME_AUTH=')) { throw 'Invalid runtime auth file.' }

$env:HUB_RUNTIME_AUTH = $auth.Substring('HUB_RUNTIME_AUTH='.Length)
$env:HUB_STATE = $state
$env:HUB_TOOLHUB_STORE = Join-Path $state 'toolhub/store.json'
$env:HUB_CREDENTIAL_STORE = Join-Path $state 'credentials/store.enc'
$env:HUB_CREDENTIAL_KEY_FILE = Join-Path $state 'credential.key'
$env:HUB_USER_ID = 'local'
$env:HUB_PRINCIPAL_ID = 'local'
$env:HUB_CONTEXT_ID = 'local'
$env:HUB_RUNTIME_ID = 'local'
$policyLine = Select-String -Path (Join-Path $Root 'spaces/local/compose.dev.yaml') -Pattern '^\s+HUB_POLICY_VERSION:\s+(\S+)$' | Select-Object -First 1
if (!$policyLine) { throw 'Generated policy version is missing.' }
$env:HUB_POLICY_VERSION = $policyLine.Matches[0].Groups[1].Value
$env:HUB_TOOLHUB_LISTEN = '0.0.0.0:8090'
$env:HUB_TOOLHIVE_ADMISSION_ENDPOINT = 'http://127.0.0.1:8545/admit'
$env:HUB_ARTIFACT_DIR = Join-Path $state 'artifacts'
$env:HUB_BUILD_SECCOMP = Join-Path $Root 'docker/seccomp-buildkit-rootless.json'
$env:HUB_RECIPE_CATALOGS = 'mcp-registry,toolhive,docker-mcp,docker-hub,ghcr'

if (!(Test-Path -LiteralPath $bridge)) {
    & $hubctl artifact linux-bridge --output $bridge
    if ($LASTEXITCODE -ne 0) { throw 'Failed to build the Linux companion bridge.' }
}
if (!(Test-Path -LiteralPath $controllerTokenFile)) {
    $tokenBytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($tokenBytes)
    [IO.File]::WriteAllText($controllerTokenFile, [Convert]::ToHexString($tokenBytes).ToLowerInvariant())
}
$env:HUB_TOOLHIVE_ADMISSION_TOKEN = [IO.File]::ReadAllText($controllerTokenFile).Trim()
$controllerJson = @{
    state_root = $state
    toolhive_binary = $toolhive
    seccomp_profile = (Resolve-Path -LiteralPath (Join-Path $Root 'docker/seccomp-mcp-runtime.json')).Path
    docker_fallback = $true
    bridge_binary = $bridge
    dynamic_definitions = $true
    max_active = 8
    idle_ttl_seconds = 1800
} | ConvertTo-Json
[IO.File]::WriteAllText($controllerConfig, $controllerJson, [Text.UTF8Encoding]::new($false))

$controller = @(Get-CimInstance Win32_Process -Filter "Name='hubctl.exe'" | Where-Object { $_.CommandLine -like '*connector generic-controller*' })
$controllerReady = Get-NetTCPConnection -LocalPort 8545 -State Listen -ErrorAction SilentlyContinue
if (!$controllerReady) {
    $controller | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Start-Process -FilePath $hubctl -ArgumentList @('connector','generic-controller','--config',$controllerConfig,'--listen','127.0.0.1:8545','--token-file',$controllerTokenFile) -WorkingDirectory $Root -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $state 'generic-controller.stdout.log') `
        -RedirectStandardError (Join-Path $state 'generic-controller.stderr.log')
}

$running = @(Get-CimInstance Win32_Process -Filter "Name='toolhub.exe'" | Where-Object { $_.ExecutablePath -eq $binary })
$toolhubReady = Get-NetTCPConnection -LocalPort 8090 -State Listen -ErrorAction SilentlyContinue
if (!$toolhubReady) {
    $running | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Start-Process -FilePath $binary -WorkingDirectory $Root -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $state 'toolhub-host.stdout.log') `
        -RedirectStandardError (Join-Path $state 'toolhub-host.stderr.log')
}
