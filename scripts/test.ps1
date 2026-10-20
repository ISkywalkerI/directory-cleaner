param([switch]$CrossCompile)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
Set-Location (Split-Path -Parent $PSScriptRoot)

function Invoke-Go {
    & go @args
    if ($LASTEXITCODE -ne 0) { throw "go $args failed ($LASTEXITCODE)" }
}

New-Item -ItemType Directory -Force -Path artifacts | Out-Null
Invoke-Go version
Invoke-Go mod download
Invoke-Go mod verify
$sources = Get-ChildItem cmd,internal -Filter '*.go' -File -Recurse
$unformatted = & gofmt -l @($sources.FullName)
if ($LASTEXITCODE -ne 0 -or $unformatted) { throw "Run gofmt on: $unformatted" }
Invoke-Go vet ./...
# JSON includes pass/fail/skip events; coverage is collected in the same uncached run.
& go test -count=1 -timeout=3m -json '-coverprofile=artifacts/coverage.out' ./... | Tee-Object -FilePath artifacts/tests.jsonl
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
Invoke-Go tool cover '-func=artifacts/coverage.out'
Invoke-Go build -o artifacts/dircleaner.exe ./cmd/dircleaner
if ($CrossCompile) {
    $previousOS = $env:GOOS
    $previousArch = $env:GOARCH
    $previousCGO = $env:CGO_ENABLED
    try {
        $env:CGO_ENABLED = '0'
        foreach ($targetOS in @('windows', 'linux')) {
            foreach ($arch in @('amd64', 'arm64')) {
                $env:GOOS = $targetOS
                $env:GOARCH = $arch
                $suffix = if ($targetOS -eq 'windows') { '.exe' } else { '' }
                Invoke-Go build -o "artifacts/dircleaner-$targetOS-$arch$suffix" ./cmd/dircleaner
                Write-Host "BUILD ONLY: $targetOS/$arch (no runtime verification)"
            }
        }
    } finally {
        $env:GOOS = $previousOS
        $env:GOARCH = $previousArch
        $env:CGO_ENABLED = $previousCGO
    }
}
