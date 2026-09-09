param(
    [Parameter(Mandatory = $true, Position = 0)]
    [string]$Question,

    [string]$User = "local",

    [ValidateSet("dev", "prod")]
    [string]$Environment = "dev",

    [string]$Model = ""
)

$ErrorActionPreference = "Stop"

if ($User -notmatch '^[a-z0-9][a-z0-9_-]*$') {
    throw "User must contain only lowercase letters, digits, '_' or '-'."
}

$root = Split-Path -Parent $PSScriptRoot
$compose = Join-Path $root ("spaces\{0}\compose.{1}.yaml" -f $User, $Environment)
if (-not (Test-Path -LiteralPath $compose)) {
    throw "Compose file not found: $compose. Run hubctl init/up first."
}

$dockerArgs = @("compose", "-f", $compose, "exec", "-T", "agent", "hermes", "-z", $Question)
if ($Model) {
    $dockerArgs += @("--model", $Model)
}

& docker @dockerArgs
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
