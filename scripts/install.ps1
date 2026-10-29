# DeployPilot installer for Windows (PowerShell).
#
# Usage (run in PowerShell):
#   irm https://github.com/66kris-ye/deploypilot/releases/latest/download/install.ps1 | iex
#
# Behind a slow GitHub connection (mainland China), set a mirror first:
#   $env:DP_MIRROR='https://gh-proxy.com/'; irm https://gh-proxy.com/https://github.com/66kris-ye/deploypilot/releases/latest/download/install.ps1 | iex
#
# Environment overrides:
#   DP_VERSION      release tag (default: latest)
#   DP_REPO         owner/repo hosting the release
#   DP_BASE_URL     full base URL for the assets; when set, only this is used
#   DP_MIRROR       mirror prefix tried first, e.g. https://gh-proxy.com/
#   DP_INSTALL_DIR  install directory (default: %LOCALAPPDATA%\DeployPilot)

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$repo = if ($env:DP_REPO) { $env:DP_REPO } else { '66kris-ye/deploypilot' }
$version = if ($env:DP_VERSION) { $env:DP_VERSION } else { 'latest' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'amd64' }  # run the amd64 build under emulation until arm64 is published
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}
$asset = "deploypilot-windows-$arch.exe"

if ($version -eq 'latest') {
    $ghBase = "https://github.com/$repo/releases/latest/download"
} else {
    $ghBase = "https://github.com/$repo/releases/download/$version"
}

$sources = @()
if ($env:DP_BASE_URL) {
    $sources += $env:DP_BASE_URL
} else {
    if ($env:DP_MIRROR) { $sources += "$($env:DP_MIRROR)$ghBase" }
    $sources += $ghBase
    foreach ($m in 'https://gh-proxy.com/', 'https://ghfast.top/') {
        if ($m -ne $env:DP_MIRROR) { $sources += "$m$ghBase" }
    }
}

$dir = if ($env:DP_INSTALL_DIR) { $env:DP_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'DeployPilot' }
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("dp-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

function Get-File($url, $dest) {
    $old = $ProgressPreference
    $ProgressPreference = 'SilentlyContinue'
    try {
        Invoke-WebRequest -Uri $url -OutFile $dest -TimeoutSec 60 -UseBasicParsing
        return $true
    } catch {
        return $false
    } finally {
        $ProgressPreference = $old
    }
}

Write-Host "==> 下载 $asset ($version) ..." -ForegroundColor Cyan
$ok = $false
foreach ($base in $sources) {
    if ((Get-File "$base/$asset" "$tmp\$asset") -and (Get-File "$base/checksums.txt" "$tmp\checksums.txt")) {
        $ok = $true
        break
    }
    Write-Host "    下载源不可用，尝试下一个 ..." -ForegroundColor DarkYellow
}
if (-not $ok) { Remove-Item -Recurse -Force $tmp; throw '所有下载源都失败了，请检查网络后重试' }

$want = (Get-Content "$tmp\checksums.txt" | ForEach-Object { $_ -replace "`r", '' } |
    Where-Object { $_ -match "\s\*?$([regex]::Escape($asset))$" } |
    ForEach-Object { ($_ -split '\s+')[0] }) | Select-Object -First 1
if (-not $want) { Remove-Item -Recurse -Force $tmp; throw "校验和文件中找不到 $asset" }

$got = (Get-FileHash "$tmp\$asset" -Algorithm SHA256).Hash.ToLower()
if ($got -ne $want.ToLower()) { Remove-Item -Recurse -Force $tmp; throw "校验和不匹配 (期望 $want, 实际 $got)" }
Write-Host '==> 校验通过。' -ForegroundColor Cyan

$bin = Join-Path $dir 'deploypilot.exe'
Copy-Item "$tmp\$asset" $bin -Force
Remove-Item -Recurse -Force $tmp

Write-Host '==> 启动 DeployPilot ...' -ForegroundColor Cyan
& $bin
