#requires -Version 7.4
param(
    [Parameter(Mandatory)][string]$ReleaseDirectory,
    [switch]$StructureOnly
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$ReleaseDirectory = (Resolve-Path -LiteralPath $ReleaseDirectory).Path
function Assert([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}
function Assert-Binary([string]$Path, [string]$OS, [string]$Arch) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($OS -eq 'windows') {
        Assert ($bytes[0] -eq 0x4d -and $bytes[1] -eq 0x5a) 'Missing DOS header'
        $pe = [BitConverter]::ToInt32($bytes, 0x3c)
        Assert ([BitConverter]::ToUInt32($bytes, $pe) -eq 0x4550) 'Missing PE header'
        Assert ([BitConverter]::ToUInt16($bytes, $pe + 4) -eq 0x8664) 'Expected Windows AMD64'
    } else {
        Assert ([BitConverter]::ToUInt32($bytes, 0) -eq 0x464c457f -and $bytes[4] -eq 2 -and $bytes[5] -eq 1) 'Expected little-endian ELF64'
        $machine = if ($Arch -eq 'amd64') { 62 } else { 183 }
        Assert ([BitConverter]::ToUInt16($bytes, 18) -eq $machine) "Wrong ELF architecture: $Arch"
        $offset = [BitConverter]::ToInt64($bytes, 32)
        $size = [BitConverter]::ToUInt16($bytes, 54)
        $count = [BitConverter]::ToUInt16($bytes, 56)
        for ($i = 0; $i -lt $count; $i++) {
            Assert ([BitConverter]::ToUInt32($bytes, [int]($offset + $i * $size)) -ne 3) 'ELF unexpectedly needs a dynamic interpreter'
        }
    }
}
function Expand-Checked([string]$Archive, [string]$Destination, [string[]]$Expected) {
    $found = [Collections.Generic.List[string]]::new()
    function Copy-Entry([string]$Name, [IO.Stream]$Stream) {
        Assert ($Name -in $Expected) "Unexpected archive member: $Name"
        Assert (-not $found.Contains($Name)) "Duplicate archive member: $Name"
        $found.Add($Name)
        $path = Join-Path $Destination $Name
        New-Item -ItemType Directory -Path (Split-Path -Parent $path) -Force | Out-Null
        $output = [IO.File]::Create($path)
        try { $Stream.CopyTo($output) } finally { $output.Dispose() }
    }
    if ($Archive.EndsWith('.zip')) {
        $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
        try {
            foreach ($entry in $zip.Entries) {
                $stream = $entry.Open()
                try { Copy-Entry $entry.FullName $stream } finally { $stream.Dispose() }
            }
        } finally { $zip.Dispose() }
    } else {
        $file = [IO.File]::OpenRead($Archive)
        $gzip = [IO.Compression.GZipStream]::new($file, [IO.Compression.CompressionMode]::Decompress)
        $reader = [System.Formats.Tar.TarReader]::new($gzip)
        try {
            while ($entry = $reader.GetNextEntry()) {
                Assert ($entry.EntryType -eq [System.Formats.Tar.TarEntryType]::RegularFile) 'Archive must contain only regular files'
                Copy-Entry $entry.Name $entry.DataStream
                $mode = if ($entry.Name -in @('directory-cleaner/dircleaner', 'directory-cleaner/start.sh')) { '755' } else { '644' }
                Assert ([int]$entry.Mode -eq [Convert]::ToInt32($mode, 8)) "Wrong permissions: $($entry.Name)"
                if (-not $IsWindows) { [IO.File]::SetUnixFileMode((Join-Path $Destination $entry.Name), $entry.Mode) }
            }
        } finally { $reader.Dispose(); $gzip.Dispose(); $file.Dispose() }
    }
    Assert ($found.Count -eq $Expected.Count) "Incomplete archive: $Archive"
}

$hostOS = if ($IsWindows) { 'windows' } elseif ($IsLinux) { 'linux' } else { 'unsupported' }
$hostArch = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant().Replace('x64', 'amd64')
$temp = Join-Path ([IO.Path]::GetTempPath()) ('dircleaner-release-' + [Guid]::NewGuid().ToString('N') + ' with spaces')
New-Item -ItemType Directory -Path $temp | Out-Null
$originalLocation = Get-Location
try {
    $checksums = @(Get-Content -LiteralPath "$ReleaseDirectory/SHA256SUMS")
    Assert ($checksums.Count -eq 3) 'Expected three archive checksums'
    $archives = @()
    $releaseVersion = ''
    foreach ($target in @('windows-amd64', 'linux-amd64', 'linux-arm64')) {
        $os, $arch = $target.Split('-')
        $source = Join-Path $ReleaseDirectory "$target/directory-cleaner"
        $metadata = Get-Content -Raw -LiteralPath "$source/VERSION.json" | ConvertFrom-Json
        $metadata.build_time = ([DateTime]$metadata.build_time).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
        Assert ($metadata.version -match '^[A-Za-z0-9][A-Za-z0-9._-]*$') 'Unsafe version'
        if (-not $releaseVersion) { $releaseVersion = $metadata.version }
        Assert ($metadata.version -eq $releaseVersion -and $metadata.goos -eq $os -and $metadata.goarch -eq $arch -and $metadata.cgo_enabled -eq 0) "Invalid metadata: $target"
        $suffix = if ($os -eq 'windows') { '.zip' } else { '.tar.gz' }
        $archiveName = "directory-cleaner-$releaseVersion-$target$suffix"
        $archives += $archiveName
        $archive = Join-Path $ReleaseDirectory $archiveName
        $hash = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
        Assert ((@($checksums | Where-Object { $_ -ceq "$hash  $archiveName" })).Count -eq 1) "Checksum mismatch: $target"
        $files = @('README.md', 'LICENSE', 'VERSION.json', 'directory-cleaner.yaml.example', 'licenses/Go-LICENSE.txt', 'licenses/yaml-LICENSE.txt', 'licenses/yaml-NOTICE.txt')
        if ($os -eq 'windows') {
            $exe = 'dircleaner.exe'
            $files += @($exe, 'start.ps1', 'deploy/install-nssm.ps1', 'tools/nssm.exe', 'licenses/NSSM-README.txt')
        } else {
            $exe = 'dircleaner'
            $files += @($exe, 'start.sh', 'deploy/directory-cleaner.service')
        }
        $expected = @($files | ForEach-Object { "directory-cleaner/$_" })
        $extracted = Join-Path $temp $target
        Expand-Checked $archive $extracted $expected
        Assert ((@(Get-ChildItem -LiteralPath $source -File -Recurse)).Count -eq $files.Count) 'Unexpected deployment files'
        foreach ($relative in $files) {
            Assert ((Get-FileHash -LiteralPath "$source/$relative").Hash -eq (Get-FileHash -LiteralPath "$extracted/directory-cleaner/$relative").Hash) "Archive differs from deployment directory: $relative"
        }
        $root = Join-Path $extracted 'directory-cleaner'
        Assert-Binary "$root/$exe" $os $arch
        if ($os -eq 'windows') { Assert-Binary "$root/tools/nssm.exe" 'windows' 'amd64' }
        $info = (& go version -m "$root/$exe") -join "`n"
        Assert ($LASTEXITCODE -eq 0) 'Cannot read Go build metadata'
        foreach ($setting in @("GOOS=$os", "GOARCH=$arch", 'CGO_ENABLED=0', $(if ($arch -eq 'amd64') { 'GOAMD64=v1' } else { 'GOARM64=v8.0' }))) {
            Assert ($info -match "(?m)^\s*build\s+$([regex]::Escape($setting))$") "Missing build setting: $setting"
        }
        Assert ($info.Contains($metadata.go_version) -and $info.Contains("vcs.revision=$($metadata.commit)")) 'Go version or commit mismatch'
        # Go omits ldflags from build info with -trimpath; check embedded strings and the native CLI instead.
        $strings = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes("$root/$exe"))
        Assert ($strings.Contains($releaseVersion) -and $strings.Contains($metadata.commit) -and $strings.Contains($metadata.build_time)) 'Embedded version metadata mismatch'
        $template = Get-Content -LiteralPath "$root/directory-cleaner.yaml.example" -Raw
        Assert ($template -match 'dry_run: true') 'Template must default to preview'
        if ($os -eq 'linux') {
            Assert (-not $template.Contains('C:/')) 'Windows path in Linux template'
            Assert (-not (Get-Content -Raw "$root/start.sh").Contains("`r")) 'CRLF in Linux launcher'
        }
        if (-not $StructureOnly -and $os -eq $hostOS -and $arch -eq $hostArch) {
            Set-Location -LiteralPath $temp
            $output = & "$root/$exe" -config missing.yaml -version
            Assert ($LASTEXITCODE -eq 0 -and $output -eq "dircleaner $releaseVersion commit=$($metadata.commit) built=$($metadata.build_time) platform=$os/$arch") 'Version command failed'
            New-Item -ItemType Directory -Path "$root/data", "$root/logs" | Out-Null
            [IO.File]::WriteAllText("$root/data/keep.txt", 'keep')
            $config = "version: 1`ndefaults:`n  dry_run: true`n  stable_for: 0s`n  min_age: 0s`n  audit_log_dir: logs`ndirectories:`n- path: data`n  trigger_bytes: 3`n  target_bytes: 0`n"
            [IO.File]::WriteAllText("$root/directory-cleaner.yaml", $config)
            [IO.File]::WriteAllText("$root/logs/dir-cleaner-20200101-000000.jsonl", "{`"sentinel`":true}`n")
            Expand-Checked $archive $extracted $expected
            Assert ([IO.File]::ReadAllText("$root/directory-cleaner.yaml") -ceq $config) 'Upgrade changed existing config'
            Assert ([IO.File]::ReadAllText("$root/logs/dir-cleaner-20200101-000000.jsonl") -ceq "{`"sentinel`":true}`n") 'Upgrade changed existing audit data'
            if ($os -eq 'windows') { & powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$root/start.ps1" -once -dry-run } else { & bash "$root/start.sh" -once -dry-run }
            Assert ($LASTEXITCODE -eq 0) 'Packaged launcher failed'
            Assert ([IO.File]::ReadAllText("$root/data/keep.txt") -ceq 'keep') 'Preview changed data'
            $events = @(Get-ChildItem -LiteralPath "$root/logs" -Filter 'dir-cleaner-*.jsonl' | ForEach-Object { Get-Content -LiteralPath $_.FullName } | Where-Object { $_ } | ConvertFrom-Json)
            Assert ($events.Count -eq 3 -and $events[-1].action -eq 'summary' -and $events[-1].dry_run -eq $true) 'Preview audit missing; check launcher working directory and arguments'
            Assert ((Get-Content -LiteralPath "$root/logs/dir-cleaner-20200101-000000.jsonl" -First 1) -ceq '{"sentinel":true}') 'Audit was truncated'
            Write-Host "PASS native package: $target (version, launcher, preview, upgrade preservation)"
        } else { Write-Host "PASS structure only: $target (not runtime validation)" }
    }
    $top = @(Get-ChildItem -LiteralPath $ReleaseDirectory -File | ForEach-Object Name)
    Assert ($top.Count -eq 4 -and (@($top | Where-Object { $_ -notin ($archives + 'SHA256SUMS') })).Count -eq 0) 'Unexpected release artifacts'
} finally {
    Set-Location -LiteralPath $originalLocation.Path
    $resolved = [IO.Path]::GetFullPath($temp)
    if ((Split-Path -Parent $resolved) -ne [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\', '/') -or (Split-Path -Leaf $resolved) -notmatch '^dircleaner-release-[a-f0-9]{32} with spaces$') { throw 'Unsafe temporary cleanup path' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
