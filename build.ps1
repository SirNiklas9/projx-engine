# install.ps1 — build + install the projx-engine CLI on Windows.
# Produces projx-engine.exe (PowerShell/cmd need the .exe extension) and ensures
# %USERPROFILE%\.local\bin is on your User PATH. Run from the repo:  .\install.ps1
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$bin = Join-Path $HOME '.local\bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null

$env:GOWORK = 'off'
$exe = Join-Path $bin 'projx-engine.exe'
# Stamp the version from git so the binary reports the real release, never a
# hardcoded number. Falls back to 'dev' outside a git checkout.
$ver = git describe --tags --always --dirty
if ([string]::IsNullOrWhiteSpace($ver)) { $ver = 'dev' }
go build -ldflags "-X main.version=$ver" -o $exe .
Write-Host "installed $ver -> $exe"

$userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
if ($userPath -notlike "*$bin*") {
    [Environment]::SetEnvironmentVariable('PATH', "$userPath;$bin", 'User')
    Write-Host "added $bin to your User PATH — open a NEW terminal to pick it up."
} else {
    Write-Host "$bin already on PATH."
}
Write-Host "done. In a new terminal:  cd <your repo>;  projx-engine init"
