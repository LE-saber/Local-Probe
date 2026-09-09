[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$AuthorizedRoot,
    [string]$McpServerUrl = 'http://127.0.0.1:8787/mcp',
    [switch]$ForceProfile,
    [switch]$ForceRuntimeConfig
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Test-LoopbackMcpUrl([string]$Url) {
    $parsed = [Uri]$Url
    if ($parsed.Scheme -ne 'http' -or
        $parsed.Host -notin @('127.0.0.1', 'localhost', '::1') -or
        $parsed.Port -lt 1 -or $parsed.Port -gt 65535) {
        throw "McpServerUrl must be an HTTP loopback URL with an explicit port."
    }
}

function Protect-LocalPath([string]$Path, [bool]$IsDirectory) {
    # New secret paths are created without inherited ACLs.  Keep SYSTEM and the
    # local Administrators group available for normal Windows recovery, and
    # grant the current user access.  Existing paths are not deleted or moved.
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
        Write-Warning "Could not apply a private ACL to $Path; the preflight will report this."
    }
}

$RepoRoot = Get-FullPath $RepoRoot
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
if ([string]::IsNullOrWhiteSpace($AuthorizedRoot)) {
    $AuthorizedRoot = $RepoRoot
}
$SecretRoot = Get-FullPath $SecretRoot
$RuntimeRoot = Get-FullPath $RuntimeRoot
$AuthorizedRoot = Get-FullPath $AuthorizedRoot
Test-LoopbackMcpUrl $McpServerUrl
if (-not (Test-Path -LiteralPath $AuthorizedRoot -PathType Container)) {
    throw "AuthorizedRoot must be an existing directory."
}

$repoPrefix = $RepoRoot.TrimEnd('\') + '\'
if ($RuntimeRoot.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase) -eq $false) {
    throw "RuntimeRoot must be inside RepoRoot so it can be ignored by git."
}
if ($SecretRoot.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw "SecretRoot must be outside RepoRoot."
}

New-Item -ItemType Directory -Path $SecretRoot -Force | Out-Null
New-Item -ItemType Directory -Path $RuntimeRoot -Force | Out-Null
Protect-LocalPath $SecretRoot $true

$apiKeyPath = Join-Path $SecretRoot 'control-plane-api-key.txt'
$tunnelIdPath = Join-Path $SecretRoot 'tunnel-id.txt'
$mcpTokenPath = Join-Path $SecretRoot 'mcp-bearer-token.txt'
if (-not (Test-Path -LiteralPath $apiKeyPath -PathType Leaf)) {
    [IO.File]::WriteAllText($apiKeyPath, '', [Text.UTF8Encoding]::new($false))
    Protect-LocalPath $apiKeyPath $false
}
if (-not (Test-Path -LiteralPath $tunnelIdPath -PathType Leaf)) {
    [IO.File]::WriteAllText($tunnelIdPath, '', [Text.UTF8Encoding]::new($false))
    Protect-LocalPath $tunnelIdPath $false
}
if (-not (Test-Path -LiteralPath $mcpTokenPath -PathType Leaf)) {
    $tokenBytes = New-Object byte[] 32
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $random.GetBytes($tokenBytes)
    } finally {
        $random.Dispose()
    }
    $token = [Convert]::ToBase64String($tokenBytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
    [IO.File]::WriteAllText($mcpTokenPath, $token, [Text.UTF8Encoding]::new($false))
    Protect-LocalPath $mcpTokenPath $false
}

$profilePath = Join-Path $RuntimeRoot 'tunnel-client.yaml'
$localConfigPath = Join-Path $RuntimeRoot 'local-probe.json'
$healthUrlPath = Join-Path $RuntimeRoot 'tunnel-health-url.txt'
$apiKeyRef = ($apiKeyPath -replace '\\', '/')
$mcpTokenRef = ($mcpTokenPath -replace '\\', '/')
$healthUrlRef = ($healthUrlPath -replace '\\', '/')
$profile = @"
config_version: 1
control_plane:
  base_url: https://api.openai.com
  # The start/preflight wrappers pass the value from the external tunnel-id.txt.
  # Keep this valid-looking placeholder until a real tunnel is selected.
  tunnel_id: tunnel_00000000000000000000000000000000
  # This is a Platform Runtime API key with Tunnels Read + Use, not an admin key.
  api_key: 'file:$apiKeyRef'
health:
  # Loopback only; do not change this to 0.0.0.0 for a browser or tunnel test.
  listen_addr: 127.0.0.1:0
  url_file: $healthUrlRef
admin_ui:
  open_browser: false
log:
  level: info
  format: struct-text
mcp:
  server_urls:
    - channel: main
      url: $McpServerUrl
  extra_headers:
    # Generated once locally; the MCP server reads the same protected file.
    X-Local-Probe-Token: 'file:$mcpTokenRef'
  discovery_extra_headers:
    # Discovery/probe requests are authenticated by the same local hop token.
    X-Local-Probe-Token: 'file:$mcpTokenRef'
  startup_wait_timeout: 30s
  max_concurrent_requests: 10
"@

if ($ForceProfile -or -not (Test-Path -LiteralPath $profilePath -PathType Leaf)) {
    [IO.File]::WriteAllText($profilePath, $profile.TrimStart(), [Text.UTF8Encoding]::new($false))
    $profileAction = 'created'
} else {
    $profileAction = 'kept existing'
}

if ($ForceRuntimeConfig -or -not (Test-Path -LiteralPath $localConfigPath -PathType Leaf)) {
    $localConfig = [ordered]@{
        schema_version = 'local-probe.config.v1'
        roots = @([ordered]@{
            id = 'project'
            path = $AuthorizedRoot
            deny_patterns = @('.git/**', '.runtime/**')
        })
        profiles = @([ordered]@{
            id = 'read-only'
            roots = @('project')
            tools = @('server_info', 'ping', 'read_file', 'batch_read')
            deny_patterns = @()
            read_only = $true
        })
        connections = @([ordered]@{
            id = 'chatgpt-local'
            label = 'Local-Probe ChatGPT read-only'
            profile_id = 'read-only'
            credential_ref = 'mcp-local'
            enabled = $true
        })
        credentials = @([ordered]@{
            id = 'mcp-local'
            kind = 'runtime'
        })
    }
    $localConfigJson = $localConfig | ConvertTo-Json -Depth 8
    [IO.File]::WriteAllText($localConfigPath, $localConfigJson, [Text.UTF8Encoding]::new($false))
    $localConfigAction = 'created'
} else {
    $localConfigAction = 'kept existing'
}

Write-Output "Tunnel-client local layout initialized ($profileAction profile)."
Write-Output "Runtime API key file (fill one line, never commit/send it): $apiKeyPath"
Write-Output "Tunnel ID file (fill tunnel_ + 32 lowercase hex characters): $tunnelIdPath"
Write-Output "Local MCP bearer file (generated locally; do not edit/send it): $mcpTokenPath"
Write-Output "Runtime profile: $profilePath"
Write-Output "Local-Probe MCP config: $localConfigPath ($localConfigAction; authorized root is not printed)"
Write-Output "Health URL file: $healthUrlPath"
Write-Output 'Next: run Test-TunnelClientPrerequisites.ps1; use -RequireCredentials only after the API key and tunnel ID files are filled.'
