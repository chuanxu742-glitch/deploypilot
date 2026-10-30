# Build helper for deploypilot (Windows dev host).
#
# Usage:
#   .\build.ps1                # build all linux targets into ../dist
#   .\build.ps1 -Local         # build a native host binary (for local runs)
#   .\build.ps1 -Test          # run go test ./...
#
# Go is expected at ..\.tools\go\bin\go.exe (see repo setup). Override with
# the GO env var if you installed Go elsewhere.

param(
    [switch]$Local,
    [switch]$Test,
    [string]$Version = ""
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$go = $env:GO
if (-not $go) { $go = Join-Path $PSScriptRoot '..\.tools\go\bin\go.exe' }
if (-not (Test-Path $go)) { $go = 'go' }

$env:GOPROXY = 'https://goproxy.cn,direct'
# go.mod pins the minimum Go version; 'auto' fetches it when the installed Go
# is older (forcing 'local' made the build fail on such hosts).
if (-not $env:GOTOOLCHAIN) { $env:GOTOOLCHAIN = 'auto' }

$version = if ($Version) { $Version } else { (Get-Date -Format 'yyyy.MM.dd') + '-dev' }
$ldflags = "-s -w -X main.version=$version"

Push-Location $PSScriptRoot
try {
    if ($Test) {
        & $go test ./...
        exit $LASTEXITCODE
    }

    if ($Local) {
        $out = Join-Path $PSScriptRoot 'deploypilot.exe'
        & $go build -ldflags $ldflags -o $out ./cmd/deploypilot
        Write-Host "built $out"
        exit $LASTEXITCODE
    }

    $distDir = Join-Path $PSScriptRoot '..\dist'
    New-Item -ItemType Directory -Force -Path $distDir | Out-Null

    $targets = @(
        @{ os = 'linux'; arch = 'amd64' },
        @{ os = 'linux'; arch = 'arm64' },
        @{ os = 'windows'; arch = 'amd64' }
    )

    foreach ($t in $targets) {
        $env:GOOS = $t.os
        $env:GOARCH = $t.arch
        $env:CGO_ENABLED = '0'
        $name = "deploypilot-$($t.os)-$($t.arch)"
        if ($t.os -eq 'windows') { $name += '.exe' }
        $out = Join-Path $distDir $name
        Write-Host "building $name ..."
        & $go build -ldflags $ldflags -o $out ./cmd/deploypilot
        if ($LASTEXITCODE -ne 0) { throw "build failed for $name" }
    }

    # Emit a checksums file consumed by install.sh.
    # Use LF line endings: install.sh parses this file on Linux.
    $checksums = Join-Path $distDir 'checksums.txt'
    $lines = Get-ChildItem $distDir -Filter 'deploypilot-*' | ForEach-Object {
        $h = (Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower()
        "$h  $($_.Name)"
    }
    [IO.File]::WriteAllText($checksums, (($lines -join "`n") + "`n"), (New-Object Text.UTF8Encoding($false)))

    # install.sh is piped into sh on Linux: force LF even if the checkout has
    # CRLF (core.autocrlf), or the first line fails with "set: Illegal option".
    $sh = [IO.File]::ReadAllText((Join-Path $PSScriptRoot '..\scripts\install.sh')).Replace("`r`n", "`n")
    [IO.File]::WriteAllText((Join-Path $distDir 'install.sh'), $sh, (New-Object Text.UTF8Encoding($false)))
    Copy-Item (Join-Path $PSScriptRoot '..\scripts\install.ps1') (Join-Path $distDir 'install.ps1') -Force

    Write-Host "`nartifacts in $distDir :"
    Get-ChildItem $distDir | Format-Table Name, Length
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
