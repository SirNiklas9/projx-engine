# install.ps1 — one-shot ProjX installer for Windows (no build from source).
#
# Downloads the prebuilt projx-engine.exe for this machine from the LATEST GitHub release
# of SirNiklas9/projx-engine, installs it to %LOCALAPPDATA%\projx, puts that on your User
# PATH, then runs the GLOBAL bootstrap (lifecycle hook + global floor + the projx skill).
# Idempotent and self-healing — safe to re-run to upgrade + repair. It NEVER builds from
# source (that's build.ps1, for developers).
#
# Usage (one line):
#   irm https://raw.githubusercontent.com/SirNiklas9/projx-engine/main/install.ps1 | iex
#   # or from a checkout:  .\install.ps1

$ErrorActionPreference = 'Stop'

$repo   = 'SirNiklas9/projx-engine'
$asset  = 'projx-engine_windows_amd64.exe'
$dir    = Join-Path $env:LOCALAPPDATA 'projx'
$exe    = Join-Path $dir 'projx-engine.exe'
$url    = "https://github.com/$repo/releases/latest/download/$asset"

New-Item -ItemType Directory -Force -Path $dir | Out-Null

# Download to a .new file, then move over the target — a RUNNING projx-engine.exe can't be
# overwritten in place on Windows, so this makes upgrade-in-place safe too.
$tmp = "$exe.new"
Write-Host "downloading $asset (latest release)..."
Invoke-WebRequest -Uri $url -OutFile $tmp
Move-Item -Force $tmp $exe
Write-Host "installed -> $exe"

# Put the install dir on the User PATH (idempotent).
$userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
if ($userPath -notlike "*$dir*") {
    [Environment]::SetEnvironmentVariable('PATH', "$userPath;$dir", 'User')
    $env:PATH = "$env:PATH;$dir"   # this session too
    Write-Host "added $dir to your User PATH (open a NEW terminal for other apps to see it)."
} else {
    Write-Host "$dir already on PATH."
}

# Global bootstrap: hook + floor + skill. Self-heals a stale/broken hook on re-run.
& $exe init --global

Write-Host ""
Write-Host "done. Verify with:  projx-engine status"
Write-Host "Per project:        projx-engine init      (or say 'set up ProjX' / 'init ProjX here' in Claude Code)"
