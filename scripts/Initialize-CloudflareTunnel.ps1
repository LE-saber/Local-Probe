[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$PublicHost = 'mcp.example.test',
    [int]$OriginPort = 8788,
    [string]$MetricsAddr = '127.0.0.1:49300',
    [ValidateSet('auto', 'http2', 'quic')]
    [string]$TransportProtocol = 'http2',
    [switch]$ForceConfig
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Protect-LocalPath([string]$Path, [bool]$IsDirectory) {
    $principal = if ([string]::IsNullOrWhiteSpace($env:USERDOMAIN)) {
        $env:USERNAME
    } else {
        "$($env:USERDOMAIN)\$($env:USERNAME)"
    }
    $grant = if ($IsDirectory) {
        @("${principal}:(OI)(CI)F", '*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F')
    } else {
        @("${principal}:F", '*S-1-5-18:F', '*S-1-5-32-544:F')
    }
    & icacls.exe $Path /inheritance:r /grant:r @grant *> $null
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "Could not apply a private ACL to $Path; preflight will report this."
    }
}

function Test-DnsHost([string]$HostName) {
    if ([string]::IsNullOrWhiteSpace($HostName) -or
        $HostName -ne $HostName.Trim() -or
        $HostName -match '[/:?#@\r\n\x00*]' -or
        $HostName -notmatch '^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$') {
        return $false
    }
    return $true
}

function Test-LoopbackAddr([string]$Addr) {
    if ($Addr -notmatch '^(?:127\.0\.0\.1|\[::1\]):([1-9][0-9]{0,4})$') {
        return $false
    }
    return [int]$Matches[1] -le 65535
}

$RepoRoot = Get-FullPath $RepoRoot
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
$SecretRoot = Get-FullPath $SecretRoot
$RuntimeRoot = Get-FullPath $RuntimeRoot

$repoPrefix = $RepoRoot.TrimEnd('\') + '\'
if ($RuntimeRoot.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase) -eq $false) {
    throw 'RuntimeRoot must be inside RepoRoot so it can be ignored by git.'
}
if ($SecretRoot.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'SecretRoot must be outside RepoRoot.'
}
if (-not (Test-DnsHost $PublicHost)) {
    throw 'PublicHost must be an exact DNS hostname (no scheme, wildcard, or path).'
}
if ($OriginPort -lt 1 -or $OriginPort -gt 65535) {
    throw 'OriginPort must be between 1 and 65535.'
}
if (-not (Test-LoopbackAddr $MetricsAddr)) {
    throw 'MetricsAddr must be an explicit loopback address (127.0.0.1:port or [::1]:port).'
}

New-Item -ItemType Directory -Path $SecretRoot -Force | Out-Null
New-Item -ItemType Directory -Path $RuntimeRoot -Force | Out-Null
Protect-LocalPath $SecretRoot $true

$tokenPath = Join-Path $SecretRoot 'cloudflared-tunnel-token.txt'
$tunnelConfigPath = Join-Path $RuntimeRoot 'cloudflare-tunnel.json'
$accessConfigPath = Join-Path $RuntimeRoot 'cloudflare-access.json'
if (-not (Test-Path -LiteralPath $tokenPath -PathType Leaf)) {
    [IO.File]::WriteAllText($tokenPath, '', [Text.UTF8Encoding]::new($false))
}
Protect-LocalPath $tokenPath $false

if ($ForceConfig -or -not (Test-Path -LiteralPath $tunnelConfigPath -PathType Leaf)) {
    $tunnelConfig = [ordered]@{
        public_host = $PublicHost
        origin_url = "http://127.0.0.1:$OriginPort"
        metrics_addr = $MetricsAddr
        transport_protocol = $TransportProtocol
    } | ConvertTo-Json
    [IO.File]::WriteAllText($tunnelConfigPath, $tunnelConfig, [Text.UTF8Encoding]::new($false))
    $tunnelConfigAction = 'created'
} else {
    $tunnelConfigAction = 'kept existing'
}

if ($ForceConfig -or -not (Test-Path -LiteralPath $accessConfigPath -PathType Leaf)) {
    $accessConfig = [ordered]@{
        issuer = 'https://REPLACE_WITH_TEAM.cloudflareaccess.com'
        jwks_url = 'https://REPLACE_WITH_TEAM.cloudflareaccess.com/cdn-cgi/access/certs'
        audience = 'REPLACE_WITH_ACCESS_APPLICATION_AUDIENCE_TAG'
        clock_skew_seconds = 30
        principal_to_connection = [ordered]@{
            REPLACE_WITH_CLOUDFLARE_SUBJECT = 'chatgpt-local'
        }
        public_hosts = @($PublicHost)
    } | ConvertTo-Json -Depth 8
    [IO.File]::WriteAllText($accessConfigPath, $accessConfig, [Text.UTF8Encoding]::new($false))
    $accessConfigAction = 'created'
} else {
    $accessConfigAction = 'kept existing'
}

Write-Output 'Cloudflare Named Tunnel local layout initialized; no Cloudflare API call, login, DNS, tunnel, or Access app was created.'
Write-Output "Tunnel token file (fill one token line; never commit/send it): $tokenPath"
Write-Output "remote Tunnel expectations: $tunnelConfigPath ($tunnelConfigAction)"
Write-Output "Cloudflare Access verifier config: $accessConfigPath ($accessConfigAction; replace all REPLACE_WITH values)"
Write-Output "Origin target: http://127.0.0.1:$OriginPort (configure the same Published application route in Cloudflare)"
Write-Output "Loopback metrics address: $MetricsAddr"
Write-Output 'Next: run Test-CloudflareTunnelPrerequisites.ps1, then start Local-Probe with -ingress cloudflare-access before cloudflared.'
