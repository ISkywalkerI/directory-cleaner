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
$logs = Join-Path $Root "logs"
New-Item -ItemType Directory -Force -Path $logs | Out-Null

& $nssmPath install $ServiceName $exe "-config `"$Config`"
& $nssmPath set $ServiceName AppDirectory $Root
& $nssmPath set $ServiceName Start SERVICE_AUTO_START
& $nssmPath set $ServiceName AppExit Default Restart
& $nssmPath set $ServiceName AppThrottle 10000
& $nssmPath set $ServiceName AppStdout (Join-Path $logs "service.out.log")
& $nssmPath set $ServiceName AppStderr (Join-Path $logs "service.err.log")
& $nssmPath start $ServiceName
