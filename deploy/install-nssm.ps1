param(
    [string]$Root = (Split-Path -Parent $PSScriptRoot),
    [string]$ServiceName = "OiSmart3D-DirectoryCleaner",
    [string]$Nssm = "nssm.exe",
    [string]$Config = ""
)

$ErrorActionPreference = "Stop"
$exe = Join-Path $Root "dircleaner.exe"
if (-not (Test-Path -LiteralPath $exe)) { throw "Missing $exe" }
if (-not $Config) { $Config = Join-Path $Root "directory-cleaner.yaml" }
if (-not (Test-Path -LiteralPath $Config)) { throw "Missing $Config" }
$nssmPath = (Get-Command $Nssm -ErrorAction Stop).Source
function Invoke-Nssm {
    & $nssmPath @args
    if ($LASTEXITCODE -ne 0) { throw "nssm $args failed ($LASTEXITCODE)" }
}
$logs = Join-Path $Root "logs"
New-Item -ItemType Directory -Force -Path $logs | Out-Null

Invoke-Nssm install $ServiceName $exe "-config `"$Config`""
Invoke-Nssm set $ServiceName AppDirectory $Root
Invoke-Nssm set $ServiceName Start SERVICE_AUTO_START
Invoke-Nssm set $ServiceName AppExit Default Restart
Invoke-Nssm set $ServiceName AppThrottle 10000
Invoke-Nssm set $ServiceName AppStdout (Join-Path $logs "service.out.log")
Invoke-Nssm set $ServiceName AppStderr (Join-Path $logs "service.err.log")
Invoke-Nssm start $ServiceName
