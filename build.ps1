# build.ps1 - build and stage the ProjX Windows executables.
$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$bin = Join-Path $HOME '.local\bin'
New-Item -ItemType Directory -Force -Path $bin | Out-Null

$env:GOWORK = 'off'
$exe = Join-Path $bin 'projx-engine.exe'
$headless = Join-Path $bin 'projx-engine-headless.exe'
$alias = Join-Path $bin 'projx.cmd'
$ver = git describe --tags --always --dirty
if ([string]::IsNullOrWhiteSpace($ver)) { $ver = 'dev' }

go build -ldflags "-X main.version=$ver" -o $exe .
go build -ldflags '-H=windowsgui' -o $headless .\cmd\projx-headless
$aliasBody = @('@echo off', ('"{0}" %*' -f $exe), '') -join "`r`n"
Set-Content -LiteralPath $alias -Value $aliasBody -NoNewline -Encoding Ascii

Write-Host "installed $ver -> $exe"
Write-Host "installed headless adapter -> $headless"
Write-Host "installed public command -> $alias"

$userPath = [Environment]::GetEnvironmentVariable('PATH', 'User')
if ($userPath -notlike "*$bin*") {
    [Environment]::SetEnvironmentVariable('PATH', "$userPath;$bin", 'User')
    Write-Host "added $bin to your User PATH - open a new terminal to pick it up."
} else {
    Write-Host "$bin already on PATH."
}
Write-Host 'done. In a new terminal: cd YOUR_REPO; projx init'
