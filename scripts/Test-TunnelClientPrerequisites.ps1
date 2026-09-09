[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$TunnelClientPath,
    [string]$McpServerUrl = 'http://127.0.0.1:8787/mcp',
    [switch]$RequireCredentials,
    [switch]$CheckMcpEndpoint,
    [switch]$SkipDoctor
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Add-Check([System.Collections.Generic.List[object]]$Checks, [string]$Name, [string]$Status, [string]$Detail) {
    $Checks.Add([PSCustomObject]@{ Name = $Name; Status = $Status; Detail = $Detail })
}

function Test-LoopbackUrl([string]$Url) {
    try {
        $parsed = [Uri]$Url
        return $parsed.Scheme -eq 'http' -and
            $parsed.Host -in @('127.0.0.1', 'localhost', '::1') -and
            $parsed.Port -ge 1 -and $parsed.Port -le 65535
    } catch {
        return $false
    }
}

function Get-TrimmedFileText([string]$Path, [int]$MaxBytes = 4096) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -gt $MaxBytes) {
        throw "file exceeds the local size limit"
    }
    # A UTF-8 BOM is harmless for a manually-created text file.  Trim only
    # surrounding whitespace; a newline in the middle remains detectable.
    return [Text.Encoding]::UTF8.GetString($bytes).Trim()
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
$SecretRoot = Get-FullPath $SecretRoot
$RuntimeRoot = Get-FullPath $RuntimeRoot
$TunnelClientPath = Get-FullPath $TunnelClientPath
$zipPath = Join-Path (Split-Path -Parent $TunnelClientPath) '..\tunnel-client-v0.0.14-windows-amd64.zip'
$cloudflaredPath = Join-Path (Split-Path -Parent $TunnelClientPath) 'cloudflared.exe'
$profilePath = Join-Path $RuntimeRoot 'tunnel-client.yaml'
$apiKeyPath = Join-Path $SecretRoot 'control-plane-api-key.txt'
$tunnelIdPath = Join-Path $SecretRoot 'tunnel-id.txt'
$mcpTokenPath = Join-Path $SecretRoot 'mcp-bearer-token.txt'

$checks = [System.Collections.Generic.List[object]]::new()

if (Test-Path -LiteralPath $TunnelClientPath -PathType Leaf) {
    $binaryItem = Get-Item -LiteralPath $TunnelClientPath
    if (($binaryItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        Add-Check $checks 'tunnel-client binary' 'FAIL' 'binary is a reparse point; use the verified release file'
    } else {
        Add-Check $checks 'tunnel-client binary' 'PASS' 'release binary exists'
    }
    try {
        $versionText = (& $TunnelClientPath --version 2>&1 | Out-String).Trim()
        if ($LASTEXITCODE -eq 0 -and $versionText) {
            Add-Check $checks 'tunnel-client version' 'PASS' 'client responded to --version'
        } else {
            Add-Check $checks 'tunnel-client version' 'FAIL' 'client did not return a version'
        }
    } catch {
        Add-Check $checks 'tunnel-client version' 'FAIL' 'client could not be executed'
    }
} else {
    Add-Check $checks 'tunnel-client binary' 'FAIL' 'official Windows amd64 binary is missing'
}

if (Test-Path -LiteralPath $cloudflaredPath -PathType Leaf) {
    $cloudflaredItem = Get-Item -LiteralPath $cloudflaredPath
    if (($cloudflaredItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        Add-Check $checks 'cloudflared companion' 'FAIL' 'companion is a reparse point'
    } else {
        Add-Check $checks 'cloudflared companion' 'PASS' 'bundled companion exists beside the client'
    }
} else {
    Add-Check $checks 'cloudflared companion' 'FAIL' 'bundled cloudflared.exe is missing'
}

if (Test-Path -LiteralPath $zipPath -PathType Leaf) {
    try {
        $actualHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $zipPath).Hash.ToLowerInvariant()
        if ($actualHash -eq '784ab8da7b5a88f0109f1fd8aaf0a1c86067430b896dddf307ef7e3cc49fa1a5') {
            Add-Check $checks 'release SHA-256' 'PASS' 'v0.0.14 Windows amd64 archive matches the pinned digest'
        } else {
            Add-Check $checks 'release SHA-256' 'FAIL' 'downloaded archive does not match the pinned digest'
        }
    } catch {
        Add-Check $checks 'release SHA-256' 'FAIL' 'could not hash the downloaded archive'
    }
} else {
    Add-Check $checks 'release SHA-256' 'FAIL' 'downloaded release archive is missing'
}

if (Test-Path -LiteralPath $profilePath -PathType Leaf) {
    try {
        $profileText = [IO.File]::ReadAllText($profilePath)
        $literalApiKey = $false
        $literalMcpToken = $false
        foreach ($line in ($profileText -split "`r?`n")) {
            if ($line -match '^\s*api_key:\s*(.+)$') {
                $profileApiValue = $Matches[1].Trim().Trim("'`"")
                if ($profileApiValue -and $profileApiValue -notmatch '^(env:|file:)') {
                    $literalApiKey = $true
                }
            }
            if ($line -match '^\s*X-Local-Probe-Token:\s*(.+)$') {
                $profileTokenValue = $Matches[1].Trim().Trim("'`"")
                if ($profileTokenValue -and $profileTokenValue -notmatch '^(env:|file:)') {
                    $literalMcpToken = $true
                }
            }
        }
        if ($literalApiKey) {
            Add-Check $checks 'profile secret reference' 'FAIL' 'profile contains a literal control-plane API key'
        } elseif ($literalMcpToken) {
            Add-Check $checks 'profile secret reference' 'FAIL' 'profile contains a literal local MCP bearer'
        } elseif ($profileText -match '(?im)^\s*log\.http_raw_unsafe:\s*(true|1|yes)\s*$') {
            Add-Check $checks 'profile logging' 'FAIL' 'raw HTTP logging is enabled'
        } else {
            Add-Check $checks 'profile safety' 'PASS' 'profile uses a file/env secret reference and no raw HTTP logging'
        }
    } catch {
        Add-Check $checks 'profile safety' 'FAIL' 'profile could not be read'
    }
} else {
    Add-Check $checks 'profile' 'PENDING' 'run Initialize-TunnelClient.ps1 first'
}

if (Test-Path -LiteralPath $RuntimeRoot -PathType Container) {
    & git -C $RepoRoot check-ignore --quiet -- '.runtime/tunnel-client.yaml' 2>$null
    if ($LASTEXITCODE -eq 0) {
        Add-Check $checks 'runtime ignore rule' 'PASS' '.runtime profile is ignored by git'
    } else {
        Add-Check $checks 'runtime ignore rule' 'FAIL' '.runtime profile is not ignored by git'
    }
} else {
    Add-Check $checks 'runtime directory' 'PENDING' 'run Initialize-TunnelClient.ps1 first'
}

if (Test-Path -LiteralPath $apiKeyPath -PathType Leaf) {
    try {
        $apiKeyText = Get-TrimmedFileText $apiKeyPath
        if (-not $apiKeyText) {
            Add-Check $checks 'runtime API key file' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'file exists but is empty; fill one runtime key line'
        } elseif ($apiKeyText -match '[\r\n]') {
            Add-Check $checks 'runtime API key file' 'FAIL' 'key file contains multiple lines; keep exactly one line'
        } else {
            Add-Check $checks 'runtime API key file' 'PASS' 'runtime key is present without displaying its value'
        }
        $apiAcl = Get-Acl -LiteralPath $apiKeyPath
        if ($apiAcl.AreAccessRulesProtected) {
            Add-Check $checks 'runtime API key ACL' 'PASS' 'inheritance is disabled on the key file'
        } else {
            Add-Check $checks 'runtime API key ACL' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'key file inherits permissions; rerun initialization or tighten ACL'
        }
    } catch {
        Add-Check $checks 'runtime API key file' 'FAIL' 'key file could not be checked'
    }
} else {
    Add-Check $checks 'runtime API key file' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'fill the external control-plane-api-key.txt file'
}

if (Test-Path -LiteralPath $mcpTokenPath -PathType Leaf) {
    try {
        $mcpTokenText = Get-TrimmedFileText $mcpTokenPath
        if (-not $mcpTokenText) {
            Add-Check $checks 'local MCP bearer' 'FAIL' 'generated local MCP bearer file is empty; rerun initialization'
        } elseif ($mcpTokenText.Length -lt 16 -or $mcpTokenText -match '[\r\n]') {
            Add-Check $checks 'local MCP bearer' 'FAIL' 'local MCP bearer file is invalid'
        } else {
            Add-Check $checks 'local MCP bearer' 'PASS' 'generated local hop token is present without displaying its value'
        }
        $mcpAcl = Get-Acl -LiteralPath $mcpTokenPath
        if ($mcpAcl.AreAccessRulesProtected) {
            Add-Check $checks 'local MCP bearer ACL' 'PASS' 'inheritance is disabled on the local hop token'
        } else {
            Add-Check $checks 'local MCP bearer ACL' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'local hop token inherits permissions; rerun initialization or tighten ACL'
        }
    } catch {
        Add-Check $checks 'local MCP bearer' 'FAIL' 'local MCP bearer file could not be checked'
    }
} else {
    Add-Check $checks 'local MCP bearer' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'run Initialize-TunnelClient.ps1 to generate the local hop token'
}

$tunnelId = ''
if (Test-Path -LiteralPath $tunnelIdPath -PathType Leaf) {
    try {
        $tunnelId = Get-TrimmedFileText $tunnelIdPath 256
        if (-not $tunnelId) {
            Add-Check $checks 'tunnel ID file' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'fill the external tunnel-id.txt file'
        } elseif ($tunnelId -notmatch '^tunnel_[0-9a-f]{32}$') {
            Add-Check $checks 'tunnel ID format' 'FAIL' 'expected tunnel_ followed by 32 lowercase hexadecimal characters'
        } elseif ($tunnelId -match '[\r\n]') {
            Add-Check $checks 'tunnel ID format' 'FAIL' 'tunnel ID file contains multiple lines'
        } else {
            Add-Check $checks 'tunnel ID format' 'PASS' 'tunnel ID format is valid without displaying its value'
        }
    } catch {
        Add-Check $checks 'tunnel ID file' 'FAIL' 'tunnel ID file could not be checked'
    }
} else {
    Add-Check $checks 'tunnel ID file' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'fill the external tunnel-id.txt file'
}

if (-not (Test-LoopbackUrl $McpServerUrl)) {
    Add-Check $checks 'MCP target binding' 'FAIL' 'McpServerUrl must be an HTTP loopback URL with an explicit port'
} else {
    Add-Check $checks 'MCP target binding' 'PASS' 'MCP target is constrained to loopback'
    if ($CheckMcpEndpoint) {
        $uri = [Uri]$McpServerUrl
        try {
            $tcpReady = Test-NetConnection -ComputerName $uri.Host -Port $uri.Port -InformationLevel Quiet -WarningAction SilentlyContinue
            if ($tcpReady) {
                Add-Check $checks 'MCP endpoint socket' 'PASS' 'loopback MCP port accepts TCP connections'
            } else {
                Add-Check $checks 'MCP endpoint socket' 'FAIL' 'loopback MCP port is not listening'
            }
        } catch {
            Add-Check $checks 'MCP endpoint socket' 'FAIL' 'could not test the loopback MCP port'
        }
    }
}

try {
    $networkReady = Test-NetConnection -ComputerName 'api.openai.com' -Port 443 -InformationLevel Quiet -WarningAction SilentlyContinue
    if ($networkReady) {
        Add-Check $checks 'control-plane network' 'PASS' 'api.openai.com:443 is reachable'
    } else {
        Add-Check $checks 'control-plane network' 'FAIL' 'api.openai.com:443 is not reachable'
    }
} catch {
    Add-Check $checks 'control-plane network' 'PENDING' 'network probe unavailable on this host'
}

if ($SkipDoctor) {
    Add-Check $checks 'tunnel-client doctor' 'PENDING' 'skipped by request; run without -SkipDoctor before the real tunnel call'
} elseif ($RequireCredentials -and $tunnelId -and $tunnelId -match '^tunnel_[0-9a-f]{32}$' -and
    (Test-Path -LiteralPath $profilePath -PathType Leaf) -and
    (Test-Path -LiteralPath $TunnelClientPath -PathType Leaf)) {
    try {
        # The profile resolves the API key from file:.  The tunnel ID is passed
        # as a non-secret flag so the user can change tunnel-id.txt without
        # placing that identifier in a checked-in configuration.
        $doctorOutput = (& $TunnelClientPath doctor --config $profilePath --control-plane.tunnel-id $tunnelId --explain 2>&1 | Out-String)
        if ($LASTEXITCODE -eq 0) {
            Add-Check $checks 'tunnel-client doctor' 'PASS' 'doctor completed; output is intentionally not copied to logs'
        } else {
            Add-Check $checks 'tunnel-client doctor' 'FAIL' 'doctor failed; output is intentionally suppressed to avoid accidental disclosure'
        }
    } catch {
        Add-Check $checks 'tunnel-client doctor' 'FAIL' 'doctor could not be executed'
    }
} elseif ($RequireCredentials) {
    Add-Check $checks 'tunnel-client doctor' 'FAIL' 'complete the binary, profile, API key and tunnel ID checks first'
} else {
    Add-Check $checks 'tunnel-client doctor' 'PENDING' 'run with -RequireCredentials after filling the API key and tunnel ID files'
}

Write-Output 'Local-Probe tunnel-client preflight (values and raw doctor output are never printed):'
foreach ($check in $checks) {
    Write-Output (('[{0}] {1}: {2}' -f $check.Status, $check.Name, $check.Detail))
}

$failCount = @($checks | Where-Object Status -eq 'FAIL').Count
if ($failCount -gt 0) {
    exit 1
}
exit 0
