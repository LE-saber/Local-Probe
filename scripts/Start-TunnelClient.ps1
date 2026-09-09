[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$TunnelClientPath,
    [string]$ProfilePath,
    [string]$McpServerUrl = 'http://127.0.0.1:8787/mcp',
    [switch]$SkipDoctor
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Test-LoopbackMcpUrl([string]$Url) {
    try {
        $parsed = [Uri]$Url
        return $parsed.Scheme -eq 'http' -and
            $parsed.Host -in @('127.0.0.1', 'localhost', '::1') -and
            $parsed.Port -ge 1 -and $parsed.Port -le 65535
    } catch {
        return $false
    }
}

$RepoRoot = Get-FullPath $RepoRoot
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
if ([string]::IsNullOrWhiteSpace($TunnelClientPath)) {
    $TunnelClientPath = Join-Path (Split-Path -Parent $RepoRoot) '_tools\tunnel-client-v0.0.14-windows-amd64\bin\tunnel-client.exe'
}
if ([string]::IsNullOrWhiteSpace($ProfilePath)) {
    $ProfilePath = Join-Path $RuntimeRoot 'tunnel-client.yaml'
}
$SecretRoot = Get-FullPath $SecretRoot
$RuntimeRoot = Get-FullPath $RuntimeRoot
$TunnelClientPath = Get-FullPath $TunnelClientPath
$ProfilePath = Get-FullPath $ProfilePath
$apiKeyPath = Join-Path $SecretRoot 'control-plane-api-key.txt'
$tunnelIdPath = Join-Path $SecretRoot 'tunnel-id.txt'
$healthUrlPath = Join-Path $RuntimeRoot 'tunnel-health-url.txt'

if (-not (Test-LoopbackMcpUrl $McpServerUrl)) {
    throw 'McpServerUrl must be an HTTP loopback URL with an explicit port.'
}

$preflight = Join-Path $PSScriptRoot 'Test-TunnelClientPrerequisites.ps1'
$shellPath = if (Test-Path -LiteralPath (Join-Path $PSHOME 'pwsh.exe')) {
    Join-Path $PSHOME 'pwsh.exe'
} else {
    Join-Path $PSHOME 'powershell.exe'
}
$preflightArgs = @(
    '-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $preflight,
    '-RepoRoot', $RepoRoot, '-SecretRoot', $SecretRoot,
    '-RuntimeRoot', $RuntimeRoot, '-TunnelClientPath', $TunnelClientPath,
    '-McpServerUrl', $McpServerUrl, '-RequireCredentials'
)
if ($SkipDoctor) {
    $preflightArgs += '-SkipDoctor'
}
& $shellPath @preflightArgs
if ($LASTEXITCODE -ne 0) {
    throw 'Tunnel-client preflight failed; no daemon was started.'
}

$tunnelId = [IO.File]::ReadAllText($tunnelIdPath).Trim()
if ($tunnelId -notmatch '^tunnel_[0-9a-f]{32}$') {
    throw 'The external tunnel-id.txt file does not contain a valid tunnel ID.'
}

New-Item -ItemType Directory -Path $RuntimeRoot -Force | Out-Null
[IO.File]::WriteAllText($healthUrlPath, '', [Text.UTF8Encoding]::new($false))

# The API key is referenced by the profile's file: URI and therefore never
# appears in this process's argument list.  The tunnel ID is an identifier,
# not a credential, and is passed as a CLI override so the external file is
# the only value that needs updating when the selected tunnel changes.
$runArgs = @(
    'run', '--config', $ProfilePath,
    '--control-plane.tunnel-id', $tunnelId,
    '--health.listen-addr', '127.0.0.1:0',
    '--health.url-file', $healthUrlPath,
    '--mcp.server-url', "url=$McpServerUrl,channel=main"
)

Write-Output 'Starting tunnel-client in the foreground (loopback health/UI only).'
Write-Output 'Keep this terminal running while ChatGPT discovers or calls the MCP app.'
Write-Output "Health URL will be written to: $healthUrlPath"
& $TunnelClientPath @runArgs
exit $LASTEXITCODE
