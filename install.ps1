# install.ps1 — build + install the projx-engine CLI on Windows.
# Produces projx-engine.exe (PowerShell/cmd need the .exe extension) and ensures
# %USERPROFILE%\.local\bin is on your User PATH. Run from the repo:  .\install.ps1
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$bin = Join-Path $HOME '.local\bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null

$env:GOWORK = 'off'
$exe = Join-Path $bin 'projx-engine.exe'
go build -o $exe .
Write-Host "installed -> $exe"

$userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
if ($userPath -notlike "*$bin*") {
    [Environment]::SetEnvironmentVariable('PATH', "$userPath;$bin", 'User')
    Write-Host "added $bin to your User PATH — open a NEW terminal to pick it up."
} else {
    Write-Host "$bin already on PATH."
}
Write-Host "done. In a new terminal:  cd <your repo>;  projx-engine init"
