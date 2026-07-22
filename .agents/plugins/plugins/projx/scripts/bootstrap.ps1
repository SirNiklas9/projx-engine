param(
    [string]$Root = (Get-Location).Path,
    [switch]$GlobalOnly,
    [string]$Version = "latest"
)

$ErrorActionPreference = "Stop"
$repo = "SirNiklas9/projx-engine"
$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
if ($architecture -eq "x64") { $architecture = "amd64" }
if ($architecture -ne "amd64") { throw "ProjX has no published Windows asset for architecture: $architecture" }

$releaseBase = if ($Version -eq "latest") {
    "https://github.com/$repo/releases/latest/download"
} else {
    "https://github.com/$repo/releases/download/$Version"
}
$temporary = Join-Path ([System.IO.Path]::GetTempPath()) ("projx-bootstrap-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporary | Out-Null

try {
    $cli = Join-Path $temporary "projx-engine.exe"
    $headless = Join-Path $temporary "projx-engine-headless.exe"
    $checksums = Join-Path $temporary "projx-engine_checksums.txt"
    Invoke-WebRequest -Uri "$releaseBase/projx-engine_windows_${architecture}.exe" -OutFile $cli
    Invoke-WebRequest -Uri "$releaseBase/projx-engine-headless_windows_${architecture}.exe" -OutFile $headless
    Invoke-WebRequest -Uri "$releaseBase/projx-engine_checksums.txt" -OutFile $checksums

    $expected = @{}
    foreach ($line in Get-Content -LiteralPath $checksums) {
        if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+)$') { $expected[$matches[2].Trim()] = $matches[1].ToLowerInvariant() }
    }
    foreach ($asset in @("projx-engine_windows_${architecture}.exe", "projx-engine-headless_windows_${architecture}.exe")) {
        if (-not $expected.ContainsKey($asset)) { throw "ProjX checksum manifest does not contain $asset" }
        $localPath = if ($asset -like "*-headless_*") { $headless } else { $cli }
        $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $localPath).Hash.ToLowerInvariant()
        if ($actual -ne $expected[$asset]) { throw "ProjX checksum verification failed for $asset" }
    }

    & $cli init --global --codex
    if ($LASTEXITCODE -ne 0) { throw "ProjX global bootstrap failed with exit code $LASTEXITCODE" }
    if (-not $GlobalOnly) {
        $resolvedRoot = (Resolve-Path -LiteralPath $Root).Path
        & $cli --root $resolvedRoot init --codex
        if ($LASTEXITCODE -ne 0) { throw "ProjX project initialization failed with exit code $LASTEXITCODE" }
    }
    & $cli version
    if ($LASTEXITCODE -ne 0) { throw "ProjX version verification failed with exit code $LASTEXITCODE" }
} finally {
    Remove-Item -LiteralPath $temporary -Recurse -Force -ErrorAction SilentlyContinue
}
