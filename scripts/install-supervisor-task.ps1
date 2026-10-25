# Registers the hermes-hub supervisor as a Windows Scheduled Task so the
# control plane survives sign-out and restarts on its own after a crash.
# Run as the owner account that should host the runtimes:
#   pwsh -File scripts/install-supervisor-task.ps1 -Binary C:\hub\hub-supervisor.exe -Spaces C:\hub\spaces -AuthFile C:\hub\spaces\.control\supervisor.token
param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Spaces,
    [Parameter(Mandatory = $true)][string]$AuthFile,
    [string]$Listen = '127.0.0.1:8876',
    [ValidateSet('dev', 'prod')][string]$Environment = 'prod',
    [string]$Image = 'hermes-hub:0.4.0-prod',
    # Required when $Listen is a wildcard address (for example 0.0.0.0:8876 so
    # Docker containers reach the host); the supervisor refuses it otherwise.
    [switch]$AllowWildcardListen,
    [string]$TaskName = 'hermes-hub-supervisor'
)
$ErrorActionPreference = 'Stop'

$Binary = (Resolve-Path -LiteralPath $Binary).Path
$Spaces = (Resolve-Path -LiteralPath $Spaces).Path
$AuthFile = (Resolve-Path -LiteralPath $AuthFile).Path
$logDir = Join-Path (Split-Path $Spaces -Parent) '.local'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$stdout = Join-Path $logDir 'supervisor.log'
$stderr = Join-Path $logDir 'supervisor-error.log'

# stdout/stderr redirection needs a cmd shim; Scheduled Tasks cannot pipe.
$wildcard = ''
if ($AllowWildcardListen) { $wildcard = ' -allow-wildcard-listen' }
$cmdline = "/c `"`"$Binary`" -watch -env $Environment -image `"$Image`" -spaces `"$Spaces`" -auth-file `"$AuthFile`" -listen $Listen$wildcard >> `"$stdout`" 2>> `"$stderr`"`""
$action = New-ScheduledTaskAction -Execute 'cmd.exe' -Argument $cmdline
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
# The Go watcher restarts the child in 1s; Scheduler recovers the watcher itself.
$settings = New-ScheduledTaskSettingsSet -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable -MultipleInstances IgnoreNew
$principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType S4U -RunLevel Limited

Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force -ErrorAction Stop
Write-Host "Scheduled task '$TaskName' registered. Start it now:  Start-ScheduledTask -TaskName '$TaskName'"
Write-Host 'Remove it:  Unregister-ScheduledTask -TaskName ' + "'$TaskName'" + ' -Confirm:$false'
