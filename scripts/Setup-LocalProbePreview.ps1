[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$AuthorizedRoot,
    [string]$PublicHost = 'mcp.example.test',
    [int]$OriginPort = 8788,
    [string]$MetricsAddr = '127.0.0.1:49300',
    [ValidateSet('auto', 'http2', 'quic')]
    [string]$TransportProtocol = 'auto',
    [string]$CloudflaredPath,
    [switch]$CopyCloudflared,
    [switch]$ForceConfig
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    if ([string]::IsNullOrWhiteSpace($Path)) {
        throw 'A path argument cannot be empty.'
    }
    return [IO.Path]::GetFullPath($Path)
}

function Test-SafeRegularFile([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        return $false
    }
    $item = Get-Item -LiteralPath $Path
    return (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0)
}

function Get-LegacyCloudflaredPath([string]$Root) {
    return Join-Path (Split-Path -Parent $Root) '_tools\tunnel-client-v0.0.14-windows-amd64\bin\cloudflared.exe'
}

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath (Join-Path $RepoRoot 'go.mod') -PathType Leaf)) {
    throw "RepoRoot is not a Local-Probe checkout: $RepoRoot"
}
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

$repoPrefix = $RepoRoot.TrimEnd('\') + '\'
if (-not $RuntimeRoot.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'RuntimeRoot must be inside RepoRoot so generated configuration remains local and ignored by git.'
}
if ($SecretRoot.Equals($RepoRoot, [StringComparison]::OrdinalIgnoreCase) -or
    $SecretRoot.StartsWith($repoPrefix, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'SecretRoot must remain outside RepoRoot.'
}
if (-not (Test-Path -LiteralPath $AuthorizedRoot -PathType Container)) {
    throw 'AuthorizedRoot must be an existing directory.'
}

$initCloudflare = Join-Path $PSScriptRoot 'Initialize-CloudflareTunnel.ps1'
foreach ($required in @($initCloudflare)) {
    if (-not (Test-Path -LiteralPath $required -PathType Leaf)) {
        throw "Required setup script is missing: $required"
    }
}

# Preview uses Cloudflare Access ingress, so it must not initialize the old
# OpenAI tunnel-client profile or create unrelated API-key/tunnel-id files.
# Generate the current full read-only Local-Probe configuration directly and
# keep the only required external secret as cloudflared's token file.
New-Item -ItemType Directory -Path $RuntimeRoot -Force | Out-Null
$localConfigPath = Join-Path $RuntimeRoot 'local-probe.json'
if ($ForceConfig -or -not (Test-Path -LiteralPath $localConfigPath -PathType Leaf)) {
    $localConfig = [ordered]@{
        schema_version = 'local-probe.config.v1'
        roots = @([ordered]@{
                id = 'project'
                path = $AuthorizedRoot
                deny_patterns = @('.env', '.env.*', 'secrets/**', '.git/**', '.runtime/**', '.tools/**', 'manual-test-targets/denied/**', 'manual-test-targets/r2/denied/**')
                ignore_patterns = @('.git/**', 'node_modules/**', 'vendor/**', '*.tmp')
            })
        profiles = @([ordered]@{
                id = 'read_only'
                roots = @('project')
                tools = @('server_info', 'ping', 'read_file', 'batch_read', 'list_directory', 'find_files', 'search_text', 'tree_directory', 'get_environment', 'discover_tools', 'workspace_snapshot')
                deny_patterns = @()
                ignore_patterns = @()
                read_only = $true
            })
        connections = @([ordered]@{
                id = 'chatgpt-local'
                label = 'Local-Probe ChatGPT read-only'
                profile_id = 'read_only'
                credential_ref = 'local_token'
                enabled = $true
            })
        credentials = @([ordered]@{
                id = 'local_token'
                kind = 'local_bearer'
            })
    }
    $localConfigJson = $localConfig | ConvertTo-Json -Depth 8
    [IO.File]::WriteAllText($localConfigPath, $localConfigJson, [Text.UTF8Encoding]::new($false))
    Write-Output "Local-Probe Preview configuration created: $localConfigPath"
} else {
    Write-Output "Local-Probe Preview configuration kept: $localConfigPath"
}

# This existing initializer creates only the token placeholder plus the
# tunnel/access expectation files. It never logs in, downloads, or transmits.

if ($ForceConfig) {
    & $initCloudflare -RepoRoot $RepoRoot -SecretRoot $SecretRoot -RuntimeRoot $RuntimeRoot -PublicHost $PublicHost -OriginPort $OriginPort -MetricsAddr $MetricsAddr -TransportProtocol $TransportProtocol -ForceConfig
} else {
    & $initCloudflare -RepoRoot $RepoRoot -SecretRoot $SecretRoot -RuntimeRoot $RuntimeRoot -PublicHost $PublicHost -OriginPort $OriginPort -MetricsAddr $MetricsAddr -TransportProtocol $TransportProtocol
}
if ($LASTEXITCODE -ne 0) {
    throw "Setup script failed: $initCloudflare"
}

$localToolsRoot = Join-Path $RepoRoot '.tools'
$localCloudflared = Join-Path $localToolsRoot 'cloudflared.exe'
$legacyCloudflared = Get-LegacyCloudflaredPath $RepoRoot
$explicitSource = $null
if (-not [string]::IsNullOrWhiteSpace($CloudflaredPath)) {
    $explicitSource = Get-FullPath $CloudflaredPath
}

if ($CopyCloudflared) {
    if ($null -eq $explicitSource) {
        if (Test-SafeRegularFile $localCloudflared) {
            $explicitSource = $localCloudflared
        } elseif (Test-SafeRegularFile $legacyCloudflared) {
            $explicitSource = $legacyCloudflared
        } else {
            throw 'CopyCloudflared requires -CloudflaredPath or an existing legacy cloudflared.exe; no network download is attempted.'
        }
    }
    if (-not (Test-SafeRegularFile $explicitSource)) {
        throw "CloudflaredPath must be an existing non-reparse regular file: $explicitSource"
    }
    New-Item -ItemType Directory -Path $localToolsRoot -Force | Out-Null
    if ([IO.Path]::GetFullPath($explicitSource) -ne [IO.Path]::GetFullPath($localCloudflared)) {
        Copy-Item -LiteralPath $explicitSource -Destination $localCloudflared -Force
    }
    if (-not (Test-SafeRegularFile $localCloudflared)) {
        throw "The copied cloudflared file did not pass the local regular-file check: $localCloudflared"
    }
    Write-Output "Cloudflared copied to the repo-local ignored tool path: $localCloudflared"
} elseif (Test-SafeRegularFile $localCloudflared) {
    Write-Output "Cloudflared found at the repo-local tool path: $localCloudflared"
} elseif (Test-SafeRegularFile $legacyCloudflared) {
    Write-Output "Cloudflared found at the legacy sibling path; Preview compatibility fallback will use it: $legacyCloudflared"
} elseif ($null -ne $explicitSource) {
    if (-not (Test-SafeRegularFile $explicitSource)) {
        throw "CloudflaredPath must be an existing non-reparse regular file: $explicitSource"
    }
    Write-Warning 'The explicit CloudflaredPath was only checked, not copied. The Preview GUI auto-discovers .tools\cloudflared.exe or the documented legacy path; rerun with -CopyCloudflared for a portable setup.'
    Write-Output "Checked explicit cloudflared path: $explicitSource"
} else {
    Write-Warning 'cloudflared.exe is not installed yet. No network download was attempted; provide -CloudflaredPath <file> -CopyCloudflared or place it at .tools\cloudflared.exe.'
}

Write-Output 'Local-Probe Preview setup completed.'
Write-Output "Runtime config directory (ignored by git): $RuntimeRoot"
Write-Output "Secret directory (outside repository): $SecretRoot"
Write-Output 'Next: fill the external token file, replace Access placeholders, run Test-LocalProbeEnvironment.ps1, then build the Preview.'
