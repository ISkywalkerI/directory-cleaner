param(
    [Parameter(Mandatory)][string]$Nssm,
    [string]$Exe = (Join-Path $PSScriptRoot '../artifacts/dircleaner.exe')
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$nssmPath = (Resolve-Path -LiteralPath $Nssm).Path
$exePath = (Resolve-Path -LiteralPath $Exe).Path
$admin = [Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $admin.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw 'NSSM acceptance requires an elevated PowerShell' }
$name = 'DirectoryCleaner-Acceptance-' + [guid]::NewGuid().ToString('N')
$root = Join-Path ([IO.Path]::GetTempPath()) "$name with spaces"
$reportDir = Join-Path $PSScriptRoot '../artifacts/nssm'
$config = Join-Path $root 'directory-cleaner.yaml'
$audit = Join-Path $root 'logs/audit.jsonl'
$data = Join-Path $root 'data'
$installed = $false

function Assert($Condition, $Message) { if (-not $Condition) { throw $Message } }
function Invoke-Nssm {
    & $nssmPath @args | Out-Host
    if ($LASTEXITCODE -ne 0) { throw "nssm $args failed ($LASTEXITCODE)" }
}
function Wait-Until([scriptblock]$Condition, [string]$Message) {
    $deadline = [datetime]::UtcNow.AddSeconds(30)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 100
    } while ([datetime]::UtcNow -lt $deadline)
    throw "Timed out: $Message"
}
function Read-Audit {
    if (Test-Path -LiteralPath $audit) { Get-Content -LiteralPath $audit | Where-Object { $_ } | ConvertFrom-Json }
}
function Write-Config([bool]$DryRun, [string]$StableFor = '0s') {
    $yamlPath = (Join-Path $root 'logs/audit.jsonl').Replace('\', '/') | ConvertTo-Json -Compress
    $yamlData = $data.Replace('\', '/') | ConvertTo-Json -Compress
    $preview = $DryRun.ToString().ToLowerInvariant()
    @"
version: 1
defaults:
  scan_interval: 200ms
  stable_for: $StableFor
  min_age: 0s
  dry_run: $preview
  audit_log: $yamlPath
directories:
- path: $yamlData
  trigger_bytes: 10
  target_bytes: 4
"@ | Set-Content -LiteralPath $config -Encoding utf8NoBOM
}
function Get-Child {
    $service = Get-CimInstance Win32_Service -Filter "Name='$name'"
    if ($service -and $service.ProcessId) {
        $child = Get-CimInstance Win32_Process -Filter "ParentProcessId=$($service.ProcessId) AND Name='dircleaner.exe'"
        if ($child) { return [Diagnostics.Process]::GetProcessById($child.ProcessId) }
    }
}
function Stop-Clean {
    $child = Get-Child
    Assert ($null -ne $child) 'Missing service child before stop'
    $null = $child.Handle
    Invoke-Nssm stop $name
    Assert ($child.WaitForExit(5000)) 'Service child did not exit'
    Assert ($child.ExitCode -eq 0) "Service child did not exit gracefully: $($child.ExitCode)"
    Assert ((Get-Service $name).Status -eq 'Stopped') 'Service did not stop'
    $length = (Get-Item -LiteralPath $audit).Length
    Start-Sleep -Milliseconds 500
    Assert ((Get-Item -LiteralPath $audit).Length -eq $length) 'Audit grew after stop'
    Assert ($null -eq (Get-Child)) 'Service child survived stop'
}

New-Item -ItemType Directory -Path $data -Force | Out-Null
New-Item -ItemType Directory -Path $reportDir -Force | Out-Null
try {
    Copy-Item -LiteralPath $exePath -Destination (Join-Path $root 'dircleaner.exe')
    for ($i = 0; $i -lt 3; $i++) {
        $file = Join-Path $data @('old', 'middle', 'new')[$i]
        [IO.File]::WriteAllText($file, '1234')
        (Get-Item $file).LastWriteTimeUtc = [datetime]::UtcNow.AddHours($i - 3)
    }
    Write-Config $true
    Assert ($null -eq (Get-Service $name -ErrorAction SilentlyContinue)) 'Test service already exists'
    $installed = $true
    & (Join-Path $PSScriptRoot '../deploy/install-nssm.ps1') -Root $root -ServiceName $name -Nssm $nssmPath
    Assert ((Get-Service $name).Status -eq 'Running') 'Service installation/start failed'
    # Only Ctrl+C may stop this test service; forced NSSM fallbacks must not hide a broken handler.
    Invoke-Nssm set $name AppStopMethodSkip 14
    Invoke-Nssm set $name AppStopMethodConsole 5000
    Wait-Until { @(Read-Audit | Where-Object action -eq summary).Count -ge 2 } 'repeated preview scans'
    Assert (@(Get-ChildItem -LiteralPath $data -File).Count -eq 3) 'Preview deleted data'
    Stop-Clean
    Invoke-Nssm start $name
    Wait-Until { $null -ne (Get-Child) } 'start after stop'
    $child = Get-Child
    $null = $child.Handle
    $count = @(Read-Audit).Count
    Invoke-Nssm restart $name
    Assert ($child.WaitForExit(5000) -and $child.ExitCode -eq 0) 'Restart did not stop child gracefully'
    Wait-Until { ($null -ne (Get-Child)) -and (Get-Child).Id -ne $child.Id -and @(Read-Audit).Count -gt $count } 'restart resumes scanning'
    $crashed = Get-Child
    Stop-Process -Id $crashed.Id -Force
    Wait-Until { ($null -ne (Get-Child)) -and (Get-Child).Id -ne $crashed.Id } 'automatic recovery after crash'
    Stop-Clean
    Write-Config $true '60s'
    Invoke-Nssm start $name
    Wait-Until { $null -ne (Get-Child) } 'stable observation starts'
    Start-Sleep -Milliseconds 500
    Stop-Clean
    Assert (@((Read-Audit | Select-Object -Last 1).failures) -contains 'cancelled') 'Stop did not flush cancelled scan'
    Write-Config $false
    Invoke-Nssm start $name
    Wait-Until { @(Read-Audit | Where-Object { $_.deleted_files -eq 2 -and $_.deleted_bytes -eq 8 -and $_.after_bytes -eq 4 }).Count -gt 0 } 'real service deletion'
    Stop-Clean
    $remaining = @(Get-ChildItem -LiteralPath $data -File)
    Assert ($remaining.Count -eq 1 -and $remaining[0].Name -eq 'new' -and $remaining[0].Length -eq 4) 'Incorrect remaining files'
    Invoke-Nssm remove $name confirm
    Wait-Until { $null -eq (Get-Service $name -ErrorAction SilentlyContinue) } 'service uninstall'
    $installed = $false
    'PASS: install, preview, stop/start/restart, crash recovery, graceful cancellation, delete, uninstall' | Set-Content (Join-Path $reportDir 'result.txt')
} finally {
    if ($installed) {
        # Cleanup targets only the unique service and process created above.
        Get-Child | Stop-Process -Force -ErrorAction SilentlyContinue
        & $nssmPath stop $name | Out-Host
        & $nssmPath remove $name confirm | Out-Host
    }
    if (Test-Path (Join-Path $root 'logs')) { Copy-Item (Join-Path $root 'logs/*') $reportDir -Force }
    $resolvedRoot = [IO.Path]::GetFullPath($root)
    $tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
    if (-not $resolvedRoot.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -or (Split-Path $resolvedRoot -Leaf) -ne "$name with spaces") { throw 'Unsafe cleanup path' }
    Remove-Item -LiteralPath $resolvedRoot -Recurse -Force
}
