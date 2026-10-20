#requires -Version 7.4
param([string]$NssmArchive = '')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
Set-Location (Split-Path -Parent $PSScriptRoot)

function Invoke-Go {
    $result = & go @args
    if ($LASTEXITCODE -ne 0) { throw "go $args failed ($LASTEXITCODE)" }
    return $result
}
function Invoke-Git {
    $result = & git @args
    if ($LASTEXITCODE -ne 0) { throw "git $args failed ($LASTEXITCODE)" }
    return $result
}
function Write-Text([string]$Path, [string]$Value) {
    [IO.File]::WriteAllText($Path, $Value.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
}

if ((Invoke-Go env GOVERSION) -ne 'go1.25.12') { throw 'Release builds require Go 1.25.12; use the existing test scripts for other Go versions.' }
$revision = Invoke-Git rev-parse HEAD
$dirty = [bool](Invoke-Git status --porcelain)
$tags = @(Invoke-Git tag --points-at HEAD --list 'v*' --sort=-version:refname)
$tag = if ($env:GITHUB_REF_TYPE -eq 'tag') { $env:GITHUB_REF_NAME } elseif ($tags.Count) { $tags[0] } else { '' }
$built = [DateTime]::UtcNow
if ($tag) {
    if ($dirty) { throw 'Tagged release requires a clean working tree.' }
    if ((Invoke-Git rev-parse "$tag^{commit}") -ne $revision) { throw 'Release tag does not point to HEAD.' }
    $version = $tag
} else {
    $version = '0.0.0-{0}-g{1}' -f $built.ToString('yyyyMMddHHmmss'), $revision.Substring(0, 12)
    if ($dirty) { $version += '-dirty' }
}
if ($version -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]*$') { throw 'Version must contain only letters, digits, dots, underscores and hyphens.' }
$buildTime = $built.ToString('yyyy-MM-ddTHH:mm:ssZ')
$releaseRoot = Join-Path (Get-Location) 'artifacts/releases'
$destination = Join-Path $releaseRoot $version
if (Test-Path -LiteralPath $destination) { throw "Release already exists: $destination" }
# Failed builds stay visibly incomplete; never clean a deployment or a prior release.
$stage = Join-Path $releaseRoot ('.incomplete-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $stage -Force | Out-Null
Invoke-Go mod download | Out-Host
Invoke-Go mod verify | Out-Host
$yaml = Invoke-Go list -m -f '{{.Dir}}' gopkg.in/yaml.v3
$goRoot = Invoke-Go env GOROOT

# Reuse the same fixed upstream archive and checksum as service acceptance.
if (-not $NssmArchive) { $NssmArchive = 'artifacts/tools/package/tools/nssm-2.24-101-g897c7ad.zip' }
if (-not (Test-Path -LiteralPath $NssmArchive)) {
    $download = Join-Path $stage 'nssm-download'
    New-Item -ItemType Directory -Path $download | Out-Null
    Invoke-WebRequest 'https://community.chocolatey.org/api/v2/package/nssm/2.24.101.20180116' -OutFile "$download/package.zip"
    Expand-Archive -LiteralPath "$download/package.zip" -DestinationPath "$download/package"
    $NssmArchive = "$download/package/tools/nssm-2.24-101-g897c7ad.zip"
}
if ((Get-FileHash -LiteralPath $NssmArchive -Algorithm SHA256).Hash -ne '99F5045FFFBFFB745D67FE3A065A953C4A3D9C253B868892D9B685B0EE7D07B8') { throw 'NSSM checksum mismatch' }
$nssm = Join-Path (Get-Location) ('artifacts/tools/nssm-release-' + [Guid]::NewGuid().ToString('N'))
Expand-Archive -LiteralPath $NssmArchive -DestinationPath $nssm
# Downloaded packaging inputs are not deployment assets.
if (Test-Path -LiteralPath (Join-Path $stage 'nssm-download')) {
    Move-Item -LiteralPath (Join-Path $stage 'nssm-download') -Destination (Join-Path $nssm 'download')
}

$previous = @{}
foreach ($key in @('GOOS', 'GOARCH', 'CGO_ENABLED', 'GOAMD64', 'GOARM64', 'GOFLAGS', 'GOEXPERIMENT')) {
    $previous[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
}
try {
    $env:GOFLAGS = ''
    $env:GOEXPERIMENT = ''
    $env:GOAMD64 = 'v1'
    $env:GOARM64 = 'v8.0'
    foreach ($target in @('windows-amd64', 'linux-amd64', 'linux-arm64')) {
        $env:GOOS, $env:GOARCH = $target.Split('-')
        $env:CGO_ENABLED = '1'
        $cgo = @(Invoke-Go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./cmd/dircleaner | Where-Object { $_ })
        if ($cgo.Count) { throw "CGO dependencies for ${target}: $cgo. A C cross-toolchain and runtime dependency review are required." }
        $env:CGO_ENABLED = '0'
        $platformRoot = Join-Path $stage $target
        $root = Join-Path $platformRoot 'directory-cleaner'
        New-Item -ItemType Directory -Path "$root/deploy", "$root/licenses" -Force | Out-Null
        $exe = if ($env:GOOS -eq 'windows') { 'dircleaner.exe' } else { 'dircleaner' }
        $binary = Join-Path $root $exe
        Invoke-Go build -trimpath -buildvcs=true -ldflags "-X main.version=$version -X main.commit=$revision -X main.buildTime=$buildTime" -o $binary ./cmd/dircleaner | Out-Host
        $metadata = @{
            version = $version; commit = $revision; build_time = $buildTime; dirty = $dirty
            go_version = 'go1.25.12'; goos = $env:GOOS; goarch = $env:GOARCH; cgo_enabled = 0
        }
        Write-Text "$root/VERSION.json" ($metadata | ConvertTo-Json)
        Copy-Item -LiteralPath "$yaml/LICENSE" -Destination "$root/licenses/yaml-LICENSE.txt"
        Copy-Item -LiteralPath "$yaml/NOTICE" -Destination "$root/licenses/yaml-NOTICE.txt"
        Copy-Item -LiteralPath "$goRoot/LICENSE" -Destination "$root/licenses/Go-LICENSE.txt"
        Copy-Item -LiteralPath 'deploy/README.md' -Destination "$root/README.md"
        $template = Get-Content -Raw directory-cleaner.yaml.example
        if ($env:GOOS -eq 'windows') {
            Copy-Item -LiteralPath deploy/start.ps1 -Destination "$root/start.ps1"
            Copy-Item -LiteralPath deploy/install-nssm.ps1 -Destination "$root/deploy/install-nssm.ps1"
            New-Item -ItemType Directory -Path "$root/tools" | Out-Null
            Copy-Item -LiteralPath "$nssm/nssm-2.24-101-g897c7ad/win64/nssm.exe" -Destination "$root/tools/nssm.exe"
            Copy-Item -LiteralPath "$nssm/nssm-2.24-101-g897c7ad/README.txt" -Destination "$root/licenses/NSSM-README.txt"
        } else {
            $template = $template.Replace('C:/3Dshape/app/', '/srv/3Dshape/app/')
            Copy-Item -LiteralPath deploy/start.sh -Destination "$root/start.sh"
            Copy-Item -LiteralPath deploy/directory-cleaner.service -Destination "$root/deploy/directory-cleaner.service"
        }
        Write-Text "$root/directory-cleaner.yaml.example" $template
        $name = "directory-cleaner-$version-$target"
        if ($env:GOOS -eq 'windows') {
            [IO.Compression.ZipFile]::CreateFromDirectory($root, "$stage/$name.zip", [IO.Compression.CompressionLevel]::Optimal, $true)
        } else {
            $file = [IO.File]::Create("$stage/$name.tar.gz")
            $gzip = [IO.Compression.GZipStream]::new($file, [IO.Compression.CompressionLevel]::Optimal)
            $writer = [System.Formats.Tar.TarWriter]::new($gzip, $true)
            try {
                foreach ($item in (Get-ChildItem -LiteralPath $root -File -Recurse | Sort-Object FullName)) {
                    $relative = [IO.Path]::GetRelativePath($root, $item.FullName).Replace('\', '/')
                    $entry = [System.Formats.Tar.PaxTarEntry]::new([System.Formats.Tar.TarEntryType]::RegularFile, "directory-cleaner/$relative")
                    $mode = if ($relative -in @('dircleaner', 'start.sh')) { '755' } else { '644' }
                    $entry.Mode = [IO.UnixFileMode][Convert]::ToInt32($mode, 8)
                    $entry.DataStream = [IO.File]::OpenRead($item.FullName)
                    try { $writer.WriteEntry($entry) } finally { $entry.DataStream.Dispose() }
                }
            } finally { $writer.Dispose(); $gzip.Dispose(); $file.Dispose() }
        }
    }
} finally {
    foreach ($key in $previous.Keys) { [Environment]::SetEnvironmentVariable($key, $previous[$key], 'Process') }
}
$checksums = foreach ($archive in (Get-ChildItem -LiteralPath $stage -File | Sort-Object Name)) {
    '{0}  {1}' -f (Get-FileHash -LiteralPath $archive.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $archive.Name
}
Write-Text "$stage/SHA256SUMS" (($checksums -join "`n") + "`n")
& "$PSScriptRoot/test-release.ps1" -ReleaseDirectory $stage -StructureOnly
Move-Item -LiteralPath $stage -Destination $destination
Write-Host "Verified release packages: $destination"
Write-Output $destination
