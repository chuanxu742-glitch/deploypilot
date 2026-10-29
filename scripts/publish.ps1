# Publishes DeployPilot to GitHub without git: creates the repo (if needed),
# optionally commits the source, creates a release and uploads the binaries.
#
# Needs a GitHub token with scopes: public_repo + workflow (classic token).
# The token is only kept in memory for this process.
#
# Usage (from the repo root):
#   copy the token, then:
#   powershell -ExecutionPolicy Bypass -File scripts\publish.ps1 -FromClipboard
#   powershell -ExecutionPolicy Bypass -File scripts\publish.ps1 -FromClipboard -IncludeSource
#
# Keep this file ASCII-only (Windows PowerShell 5.1 reads BOM-less files as ANSI).

param(
    [string]$Owner = "chuanxu742-glitch",
    [string]$Repo = "deploypilot",
    [string]$Tag = "v0.1.0",
    [switch]$IncludeSource,
    [switch]$FromClipboard,
    # Commit the source and stop: no release is created or modified.
    [switch]$SourceOnly,
    # Leave out .github/workflows (needed when the token lacks the workflow scope).
    [switch]$SkipWorkflows,
    # List the files that would be committed, then exit without touching GitHub.
    [switch]$DryRun
)
if ($SourceOnly -or $DryRun) { $IncludeSource = $true }

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
$root = Split-Path -Parent $PSScriptRoot
$dist = Join-Path $root 'dist'

function SourceFiles {
    $exclude = '\\(\.tools|\.probe-tmp|dist|node_modules|\.kiro|\.git)(\\|$)'
    Get-ChildItem $root -Recurse -File -Force | Where-Object {
        $rel = $_.FullName.Substring($root.Length)
        ($rel -notmatch $exclude) -and ($_.Name -ne 'deploypilot.exe') -and ($rel -notmatch '\\webui\\dist\\assets\\') -and
        ($_.Name -notmatch '\.(log|tsbuildinfo)$') -and ($_.Name -ne '.env') -and
        (-not ($SkipWorkflows -and $rel -match '^\\\.github\\workflows\\'))
    }
}

if ($DryRun) {
    $files = @(SourceFiles)
    $files | ForEach-Object { "{0,10}  {1}" -f $_.Length, $_.FullName.Substring($root.Length + 1) }
    "{0} files, {1:N1} KB" -f $files.Count, (($files | Measure-Object Length -Sum).Sum / 1KB)
    exit 0
}

# --- token ------------------------------------------------------------------
if ($env:GITHUB_TOKEN) {
    $token = $env:GITHUB_TOKEN
} elseif ($FromClipboard) {
    $token = [string](Get-Clipboard -Raw)
} else {
    $s = Read-Host -Prompt 'GitHub token (input hidden; right-click to paste)' -AsSecureString
    $b = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($s)
    try { $token = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($b) } finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($b) }
}
$m = [regex]::Match($token, '(ghp_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+|gho_[A-Za-z0-9]+)')
if (-not $m.Success) { Write-Host 'No GitHub token found (expected ghp_... or github_pat_...).' -ForegroundColor Red; exit 2 }
$token = $m.Value

$H = @{ Authorization = "Bearer $token"; Accept = 'application/vnd.github+json'; 'User-Agent' = 'deploypilot-publish'; 'X-GitHub-Api-Version' = '2022-11-28' }
$api = 'https://api.github.com'

function Gh($method, $path, $body) {
    $req = @{ Method = $method; Uri = "$api$path"; Headers = $H; TimeoutSec = 60 }
    if ($null -ne $body) { $req.Body = ($body | ConvertTo-Json -Depth 20 -Compress); $req.ContentType = 'application/json' }
    Invoke-RestMethod @req
}
function Status($err) { try { [int]$err.Exception.Response.StatusCode } catch { 0 } }
function Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }

# --- who am I ---------------------------------------------------------------
$me = Gh GET '/user' $null
if ($me.login -ne $Owner) { Write-Host "Token belongs to '$($me.login)', expected '$Owner'." -ForegroundColor Red; exit 2 }
Step "authenticated as $($me.login)"

# --- repo -------------------------------------------------------------------
try {
    $repoInfo = Gh GET "/repos/$Owner/$Repo" $null
    Step "repo exists: $($repoInfo.html_url)"
} catch {
    if ((Status $_) -ne 404) { throw }
    $repoInfo = Gh POST '/user/repos' @{ name = $Repo; description = 'DeployPilot: one-line remote deploy assistant (Cloudflare Tunnel + AI agent)'; private = $false; auto_init = $true }
    Step "created repo: $($repoInfo.html_url)"
    Start-Sleep 3
}
if ($repoInfo.private) { Write-Host 'Repo is private: customers cannot curl release files. Make it public first.' -ForegroundColor Red; exit 2 }
$branch = $repoInfo.default_branch

# --- source (optional) ------------------------------------------------------
if ($IncludeSource) {
    Step 'uploading source as one commit'
    $files = @(SourceFiles)
    $ref = Gh GET "/repos/$Owner/$Repo/git/ref/heads/$branch" $null
    $parent = Gh GET "/repos/$Owner/$Repo/git/commits/$($ref.object.sha)" $null
    $tree = @()
    foreach ($f in $files) {
        $rel = $f.FullName.Substring($root.Length + 1).Replace('\', '/')
        $blob = Gh POST "/repos/$Owner/$Repo/git/blobs" @{ content = [Convert]::ToBase64String([IO.File]::ReadAllBytes($f.FullName)); encoding = 'base64' }
        $mode = if ($rel -like '*.sh') { '100755' } else { '100644' }
        $tree += @{ path = $rel; mode = $mode; type = 'blob'; sha = $blob.sha }
    }
    $newTree = Gh POST "/repos/$Owner/$Repo/git/trees" @{ base_tree = $parent.tree.sha; tree = $tree }
    $commit = Gh POST "/repos/$Owner/$Repo/git/commits" @{ message = "DeployPilot $Tag"; tree = $newTree.sha; parents = @($ref.object.sha) }
    $null = Gh PATCH "/repos/$Owner/$Repo/git/refs/heads/$branch" @{ sha = $commit.sha; force = $false }
    Step "committed $($files.Count) files ($($commit.sha.Substring(0,7)))"
    if ($SourceOnly) { Step 'source only: releases left unchanged'; exit 0 }
}

# --- release ----------------------------------------------------------------
try {
    $rel = Gh GET "/repos/$Owner/$Repo/releases/tags/$Tag" $null
    Step "release $Tag exists"
} catch {
    if ((Status $_) -ne 404) { throw }
    $notes = "Install on a Linux server:`n`n``````sh`ncurl -fsSL https://github.com/$Owner/$Repo/releases/latest/download/install.sh | sh`n```````n"
    $rel = Gh POST "/repos/$Owner/$Repo/releases" @{ tag_name = $Tag; target_commitish = $branch; name = "DeployPilot $Tag"; body = $notes; draft = $false; prerelease = $false }
    Step "created release $Tag"
}

# --- assets -----------------------------------------------------------------
$assets = 'deploypilot-linux-amd64', 'deploypilot-linux-arm64', 'deploypilot-windows-amd64.exe', 'checksums.txt', 'install.sh', 'install.ps1'
foreach ($a in $assets) {
    $path = Join-Path $dist $a
    if (-not (Test-Path $path)) { throw "missing $path (run client\build.ps1 first)" }
    $old = $rel.assets | Where-Object { $_.name -eq $a }
    if ($old) { $null = Gh DELETE "/repos/$Owner/$Repo/releases/assets/$($old.id)" $null }
    $up = "https://uploads.github.com/repos/$Owner/$Repo/releases/$($rel.id)/assets?name=$a"
    $null = Invoke-RestMethod -Method POST -Uri $up -Headers $H -ContentType 'application/octet-stream' -InFile $path -TimeoutSec 900
    Step ("uploaded {0} ({1:N1} MB)" -f $a, ((Get-Item $path).Length / 1MB))
}

# --- verify -----------------------------------------------------------------
$url = "https://github.com/$Owner/$Repo/releases/latest/download/checksums.txt"
$remote = (Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 60).Content
if ($remote -is [byte[]]) { $remote = [Text.Encoding]::ASCII.GetString($remote) }
$local = [IO.File]::ReadAllText((Join-Path $dist 'checksums.txt'))
if ($remote.Trim() -ne $local.Trim()) { Write-Host 'Uploaded checksums.txt does not match local file.' -ForegroundColor Red; exit 1 }
Step 'verified: latest release serves the new files'

Write-Host ''
Write-Host 'Customer command:' -ForegroundColor Green
Write-Host "  curl -fsSL https://github.com/$Owner/$Repo/releases/latest/download/install.sh | sh"
Write-Host 'If GitHub is slow (mainland China):' -ForegroundColor Green
Write-Host "  curl -fsSL https://gh-proxy.com/https://github.com/$Owner/$Repo/releases/latest/download/install.sh | DP_MIRROR=https://gh-proxy.com/ sh"
