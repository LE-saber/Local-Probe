[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$ConfigPath,
    [string]$TokenPath,
    [string]$ConnectionId = 'chatgpt-local',
    [string]$ListenAddr = '127.0.0.1:8787'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Test-LoopbackListenAddr([string]$Addr) {
    return $Addr -match '^127\.0\.0\.1:[1-9][0-9]{0,4}$' -or
        $Addr -match '^\[::1\]:[1-9][0-9]{0,4}$'
}

$RepoRoot = Get-FullPath $RepoRoot
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = Join-Path $RuntimeRoot 'local-probe.json'
}
if ([string]::IsNullOrWhiteSpace($TokenPath)) {
    $TokenPath = Join-Path $SecretRoot 'mcp-bearer-token.txt'
}
$RuntimeRoot = Get-FullPath $RuntimeRoot
$ConfigPath = Get-FullPath $ConfigPath
$TokenPath = Get-FullPath $TokenPath

if (-not (Test-LoopbackListenAddr $ListenAddr)) {
    throw 'ListenAddr must be an explicit loopback address (127.0.0.1:port or [::1]:port).'
}
if ([string]::IsNullOrWhiteSpace($ConnectionId) -or $ConnectionId -match '[\r\n]') {
    throw 'ConnectionId must be a single non-empty value.'
}
if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) {
    throw 'Local-Probe JSON config is missing; run Initialize-TunnelClient.ps1 first.'
}
if (-not (Test-Path -LiteralPath $TokenPath -PathType Leaf)) {
    throw 'Local MCP bearer file is missing; run Initialize-TunnelClient.ps1 first.'
}
$tokenText = [IO.File]::ReadAllText($TokenPath).Trim()
if ($tokenText.Length -lt 16 -or $tokenText -match '[\r\n]') {
    throw 'Local MCP bearer file is invalid.'
}

$go = Get-Command go -ErrorAction SilentlyContinue
if ($null -eq $go) {
    throw 'Go is required to run cmd/local-probe-mcp.'
}

# The token is passed as a file path, never as an argv value.  Keep this
# foreground process attached to the terminal so Ctrl+C shuts down the HTTP
# server cleanly through its signal handler.
$runArgs = @(
    'run', './cmd/local-probe-mcp',
    '-config', $ConfigPath,
    '-connection-id', $ConnectionId,
    '-token-file', $TokenPath,
    '-listen-addr', $ListenAddr
)
Write-Output "Starting Local-Probe MCP on http://$ListenAddr/mcp (loopback only)."
Write-Output 'Keep this terminal running; start the tunnel-client in a second terminal.'
& $go.Source @runArgs
exit $LASTEXITCODE
