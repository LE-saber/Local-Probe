[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$CloudflaredPath,
    [string]$TunnelConfigPath,
    [string]$AccessConfigPath,
    [string]$TokenPath,
    [string]$McpServerUrl = 'http://127.0.0.1:8788/mcp',
    [switch]$SkipEndpointCheck
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

$RepoRoot = Get-FullPath $RepoRoot
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
if ([string]::IsNullOrWhiteSpace($CloudflaredPath)) {
    $CloudflaredPath = Join-Path (Split-Path -Parent $RepoRoot) '_tools\tunnel-client-v0.0.14-windows-amd64\bin\cloudflared.exe'
}
if ([string]::IsNullOrWhiteSpace($TunnelConfigPath)) {
    $TunnelConfigPath = Join-Path $RuntimeRoot 'cloudflare-tunnel.json'
}
if ([string]::IsNullOrWhiteSpace($AccessConfigPath)) {
    $AccessConfigPath = Join-Path $RuntimeRoot 'cloudflare-access.json'
}
if ([string]::IsNullOrWhiteSpace($TokenPath)) {
    $TokenPath = Join-Path $SecretRoot 'cloudflared-tunnel-token.txt'
}
$SecretRoot = Get-FullPath $SecretRoot
$RuntimeRoot = Get-FullPath $RuntimeRoot
$CloudflaredPath = Get-FullPath $CloudflaredPath
$TunnelConfigPath = Get-FullPath $TunnelConfigPath
$AccessConfigPath = Get-FullPath $AccessConfigPath
$TokenPath = Get-FullPath $TokenPath

$preflight = Join-Path $PSScriptRoot 'Test-CloudflareTunnelPrerequisites.ps1'
$shellPath = if (Test-Path -LiteralPath (Join-Path $PSHOME 'pwsh.exe')) {
    Join-Path $PSHOME 'pwsh.exe'
} else {
    Join-Path $PSHOME 'powershell.exe'
}
$preflightArgs = @(
    '-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $preflight,
    '-RepoRoot', $RepoRoot, '-SecretRoot', $SecretRoot,
    '-RuntimeRoot', $RuntimeRoot, '-CloudflaredPath', $CloudflaredPath,
    '-TunnelConfigPath', $TunnelConfigPath,
    '-AccessConfigPath', $AccessConfigPath, '-TokenPath', $TokenPath,
    '-McpServerUrl', $McpServerUrl, '-RequireCredentials'
)
if (-not $SkipEndpointCheck) {
    $preflightArgs += '-CheckMcpEndpoint'
}
& $shellPath @preflightArgs
if ($LASTEXITCODE -ne 0) {
    throw 'Cloudflare tunnel preflight failed; no cloudflared process was started.'
}

# The remote Tunnel route and origin parameters are managed in Cloudflare.
# This local file records the expected values and supplies only safe run flags.
$tunnelConfig = Get-Content -Raw -LiteralPath $TunnelConfigPath | ConvertFrom-Json
$transportProtocol = [string]$tunnelConfig.transport_protocol
$previousProtocol = [Environment]::GetEnvironmentVariable('TUNNEL_TRANSPORT_PROTOCOL')
if ($transportProtocol -eq 'auto') {
    # Let cloudflared perform its own protocol negotiation.  Remove a stale
    # inherited process value so an old user/machine setting cannot override
    # the configured auto mode in the child process.
    Remove-Item Env:TUNNEL_TRANSPORT_PROTOCOL -ErrorAction SilentlyContinue
} else {
    # An explicit http2/quic choice is intentionally passed through the
    # supported environment variable for this one cloudflared invocation.
    $env:TUNNEL_TRANSPORT_PROTOCOL = $transportProtocol
}
$runArgs = @(
    'tunnel', '--no-autoupdate', '--metrics', [string]$tunnelConfig.metrics_addr,
    'run', '--token-file', $TokenPath
)
Write-Output 'Starting the remotely-managed cloudflared Named Tunnel in the foreground.'
Write-Output 'No login, tunnel creation, DNS change, or Access app provisioning is performed by this script.'
Write-Output "Token is loaded via --token-file; path: $TokenPath"
Write-Output "Expected dashboard route: https://$($tunnelConfig.public_host) -> $($tunnelConfig.origin_url)"
Write-Output "Tunnel transport protocol: $transportProtocol"
Write-Output 'Keep this terminal running while ChatGPT connects to the configured public hostname.'
try {
    & $CloudflaredPath @runArgs
    $cloudflaredExitCode = $LASTEXITCODE
} finally {
    if ([string]::IsNullOrEmpty($previousProtocol)) {
        Remove-Item Env:TUNNEL_TRANSPORT_PROTOCOL -ErrorAction SilentlyContinue
    } else {
        $env:TUNNEL_TRANSPORT_PROTOCOL = $previousProtocol
    }
}
exit $cloudflaredExitCode
