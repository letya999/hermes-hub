param([switch]$DryRun)

$ErrorActionPreference = 'Stop'
$vhd = Join-Path $env:LOCALAPPDATA 'Docker\wsl\disk\docker_data.vhdx'
$tempIds = @(
    'FFD2F16E-387B-4F38-ADE7-5A52663A04A7',
    'F3A0D72E-9EC8-46E1-B607-5EF1C8F0EE70',
    '523BF373-EC1B-475E-8FAF-69D3BF3DF23C'
)
$staleSwaps = foreach ($id in $tempIds) { Join-Path $env:LOCALAPPDATA "Temp\$id\swap.vhdx" }

if ($DryRun) {
    Write-Host "Docker VHDX: $vhd"
    $staleSwaps | ForEach-Object { Write-Host "Temporary swap: $_" }
    exit 0
}

$admin = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $admin.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    $arguments = '-NoProfile -ExecutionPolicy Bypass -File "{0}"' -f $PSCommandPath
    Start-Process powershell.exe -ArgumentList $arguments -Verb RunAs -Wait
    exit
}

try {
    if (-not (Test-Path -LiteralPath $vhd -PathType Leaf)) { throw "Docker VHDX not found: $vhd" }
    $disk = Get-PSDrive -Name C
    $before = [math]::Round($disk.Free / 1GB, 2)
    Write-Host "Free on C: before: $before GB"
    Write-Host 'Stopping Docker Desktop and WSL...'
    & docker desktop stop | Out-Host
    & wsl --shutdown | Out-Host

    # The three exact files were inspected on this PC; never sweep Temp broadly.
    foreach ($path in $staleSwaps) {
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            Write-Host "Removing stale temporary swap: $path"
            Remove-Item -LiteralPath $path -Force
        }
    }

    Write-Host "Compacting $vhd ..."
    Import-Module Hyper-V -ErrorAction Stop
    Optimize-VHD -Path $vhd -Mode Full -ErrorAction Stop
    $after = [math]::Round((Get-PSDrive -Name C).Free / 1GB, 2)
    Write-Host "Free on C: after: $after GB"
    Write-Host "Docker VHDX now: $([math]::Round((Get-Item -LiteralPath $vhd).Length / 1GB, 2)) GB"
} catch {
    Write-Error $_
} finally {
    Read-Host 'Press Enter to close this administrator window'
}
