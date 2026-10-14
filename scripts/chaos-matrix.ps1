# Run production services with synthetic accounts in an isolated Docker fixture.
param(
    [ValidateSet('runtime', 'toolhub', 'communication-hub', 'supervisor')]
    [string]$Target,
    [ValidateSet('at-admission', 'admission', 'mid-run', 'mid-delivery')]
    [string]$Timing,
    [switch]$All,
    [string]$Image = 'hermes-hub:test'
)
$ErrorActionPreference = 'Stop'
if (-not $All -and (-not $Target -or -not $Timing)) {
    throw 'Specify -All or both -Target and -Timing.'
}
$filter = '^TestExecutionChaosMatrix$'
if (-not $All) {
    if ($Timing -eq 'at-admission') { $Timing = 'admission' }
    $filter += '/^' + $Target + '$/^' + $Timing + '$'
}
$previousImage = $env:HERMES_CHAOS_IMAGE
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
    $env:HERMES_CHAOS_IMAGE = $Image
    & go test -tags integration -run $filter -v -count=1 -timeout=50m ./internal/devcheck
    if ($LASTEXITCODE -ne 0) { throw "Chaos acceptance failed ($LASTEXITCODE)." }
} finally {
    $env:HERMES_CHAOS_IMAGE = $previousImage
    Pop-Location
}
