# Runs the live Bailu tests (models list, streaming chat, tool calling, and a
# full agent loop against a simulated server).
#
# The API key is only set for this process. It is never written to disk or
# to shell history.
#
# Usage (from the repo root):
#   1. Copy the API key to the clipboard, then:
#      powershell -ExecutionPolicy Bypass -File scripts\live-test.ps1 -FromClipboard
#   or type it at the hidden prompt (Ctrl+V does NOT work there; right-click
#   to paste):
#      powershell -ExecutionPolicy Bypass -File scripts\live-test.ps1
#   Add -Model <name> to pick a model instead of the first one listed.
#
# Keep this file ASCII-only: Windows PowerShell 5.1 reads BOM-less scripts as
# the ANSI code page, which garbles non-ASCII text.

param(
    [string]$Model = "",
    [switch]$FromClipboard
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$go = Join-Path $root '.tools\go\bin\go.exe'
if (-not (Test-Path $go)) { $go = 'go' }

if ($FromClipboard) {
    $key = [string](Get-Clipboard -Raw)
} else {
    $secure = Read-Host -Prompt 'Bailu API Key (input hidden; right-click to paste)' -AsSecureString
    $bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try {
        $key = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr)
    } finally {
        [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)
    }
}
$key = $key.Trim()

# If extra text was copied along with the key (e.g. "url:...", "key:sk-..."),
# pick out the single sk-... token.
if ($key -notmatch '^[\x21-\x7E]+$' -or $key -notmatch '^sk-') {
    $found = [regex]::Matches($key, 'sk-[A-Za-z0-9_\-]+')
    if ($found.Count -eq 1) {
        $key = $found[0].Value
        Write-Host 'Extracted the sk-... key from the pasted text.'
    } elseif ($found.Count -gt 1) {
        Write-Host "Found $($found.Count) sk-... keys in the pasted text; copy only one key." -ForegroundColor Red
        exit 2
    }
}

# Validate without ever printing the key itself.
if ($key.Length -eq 0) {
    Write-Host 'No API key received.' -ForegroundColor Red
    exit 2
}
if ($key -notmatch '^[\x21-\x7E]+$') {
    $lines = ($key -split "`r?`n").Count
    Write-Host "The API key contains invalid characters (length $($key.Length), $lines line(s), no sk- token found)." -ForegroundColor Red
    if ($FromClipboard) {
        Write-Host 'Copy only the key itself (it starts with sk-) and rerun.' -ForegroundColor Yellow
    } else {
        Write-Host 'Ctrl+V does not paste into the hidden prompt. Right-click to paste, or copy the key and rerun with -FromClipboard.' -ForegroundColor Yellow
    }
    exit 2
}
Write-Host "API key received (length $($key.Length), prefix $($key.Substring(0, [Math]::Min(3, $key.Length)))...)."

$env:BAILU_API_KEY = $key
$key = $null
if ($Model) { $env:BAILU_MODEL = $Model }
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'https://goproxy.cn,direct'

try {
    & $go -C (Join-Path $root 'client') test -count=1 -v -run Live ./internal/llm/ ./internal/agent/
    $code = $LASTEXITCODE
} finally {
    Remove-Item Env:BAILU_API_KEY -ErrorAction SilentlyContinue
}
exit $code
