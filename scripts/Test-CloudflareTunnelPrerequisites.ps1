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
    [switch]$RequireCredentials,
    [switch]$CheckMcpEndpoint
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
        return $Url -match '^http://(?:127\.0\.0\.1|localhost|\[::1\]):[1-9][0-9]{0,4}/mcp$' -and
            $parsed.Port -ge 1 -and $parsed.Port -le 65535 -and
            $parsed.AbsolutePath -eq '/mcp'
    } catch {
        return $false
    }
}

function Test-LoopbackAddr([string]$Addr) {
    if ($Addr -notmatch '^(?:127\.0\.0\.1|\[::1\]):([1-9][0-9]{0,4})$') {
        return $false
    }
    $port = [int]$Matches[1]
    return $port -le 65535
}

function Get-TrimmedFileText([string]$Path, [int]$MaxBytes = 262144) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -gt $MaxBytes) {
        throw 'file exceeds the local size limit'
    }
    return [Text.Encoding]::UTF8.GetString($bytes).Trim()
}

function Test-PathInside([string]$Child, [string]$Parent) {
    $parentPrefix = $Parent.TrimEnd('\') + '\'
    return $Child.Equals($Parent, [StringComparison]::OrdinalIgnoreCase) -or
        $Child.StartsWith($parentPrefix, [StringComparison]::OrdinalIgnoreCase)
}

function Test-TcpEndpoint([string]$HostName, [int]$Port, [int]$TimeoutMilliseconds = 4000) {
    $client = [Net.Sockets.TcpClient]::new()
    try {
        $connect = $client.ConnectAsync($HostName, $Port)
        if (-not $connect.Wait($TimeoutMilliseconds)) {
            return $false
        }
        return $client.Connected
    } catch {
        return $false
    } finally {
        $client.Dispose()
    }
}

function Get-HttpStatusCode([string]$Url, [int]$TimeoutMilliseconds = 3000) {
    $request = [Net.HttpWebRequest]::Create($Url)
    $request.Method = 'GET'
    $request.Proxy = $null
    $request.Timeout = $TimeoutMilliseconds
    $request.ReadWriteTimeout = $TimeoutMilliseconds
    try {
        $response = [Net.HttpWebResponse]$request.GetResponse()
        try {
            return [int]$response.StatusCode
        } finally {
            $response.Dispose()
        }
    } catch [Net.WebException] {
        $response = $_.Exception.Response
        if ($response -is [Net.HttpWebResponse]) {
            try {
                return [int]$response.StatusCode
            } finally {
                $response.Dispose()
            }
        }
        return $null
    } catch {
        return $null
    }
}

function Get-HttpResponseBody([string]$Url, [int]$TimeoutMilliseconds = 3000) {
    $request = [Net.HttpWebRequest]::Create($Url)
    $request.Method = 'GET'
    $request.Proxy = $null
    $request.Timeout = $TimeoutMilliseconds
    $request.ReadWriteTimeout = $TimeoutMilliseconds
    try {
        $response = [Net.HttpWebResponse]$request.GetResponse()
        try {
            $reader = [IO.StreamReader]::new($response.GetResponseStream())
            try {
                return $reader.ReadToEnd()
            } finally {
                $reader.Dispose()
            }
        } finally {
            $response.Dispose()
        }
    } catch {
        return $null
    }
}

$RepoRoot = Get-FullPath $RepoRoot
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
if ([string]::IsNullOrWhiteSpace($CloudflaredPath)) {
    $CloudflaredPath = Join-Path $RepoRoot '.tools\cloudflared.exe'
    $legacyCloudflaredPath = Join-Path (Split-Path -Parent $RepoRoot) '_tools\tunnel-client-v0.0.14-windows-amd64\bin\cloudflared.exe'
    if (-not (Test-Path -LiteralPath $CloudflaredPath -PathType Leaf) -and
        (Test-Path -LiteralPath $legacyCloudflaredPath -PathType Leaf)) {
        $CloudflaredPath = $legacyCloudflaredPath
    }
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

$checks = [System.Collections.Generic.List[object]]::new()
$tunnelPublicHost = $null
$metricsAddress = $null
if (Test-PathInside $SecretRoot $RepoRoot) {
    Add-Check $checks 'secret directory location' 'FAIL' 'SecretRoot must be outside RepoRoot'
} else {
    Add-Check $checks 'secret directory location' 'PASS' 'secret directory is outside the repository'
}
if (-not (Test-PathInside $TokenPath $SecretRoot) -or (Test-PathInside $TokenPath $RepoRoot)) {
    Add-Check $checks 'token file location' 'FAIL' 'TokenPath must remain inside SecretRoot and outside RepoRoot'
} else {
    Add-Check $checks 'token file location' 'PASS' 'token file is constrained to the external secret directory'
}
if (-not (Test-PathInside $TunnelConfigPath $RuntimeRoot) -or -not (Test-PathInside $AccessConfigPath $RuntimeRoot)) {
    Add-Check $checks 'runtime config location' 'FAIL' 'both config files must remain inside RuntimeRoot'
} else {
    Add-Check $checks 'runtime config location' 'PASS' 'both config files are constrained to RuntimeRoot'
}
if (-not (Test-PathInside $RuntimeRoot $RepoRoot)) {
    Add-Check $checks 'runtime directory location' 'FAIL' 'RuntimeRoot must be inside RepoRoot'
} else {
    Add-Check $checks 'runtime directory location' 'PASS' 'runtime directory is inside the ignored repository runtime area'
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

if (Test-Path -LiteralPath $CloudflaredPath -PathType Leaf) {
    $binary = Get-Item -LiteralPath $CloudflaredPath
    if (($binary.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        Add-Check $checks 'cloudflared binary' 'FAIL' 'binary is a reparse point'
    } else {
        try {
            $null = & $CloudflaredPath --version 2>$null
            if ($LASTEXITCODE -eq 0) {
                Add-Check $checks 'cloudflared binary' 'PASS' 'cloudflared responds to --version'
            } else {
                Add-Check $checks 'cloudflared binary' 'FAIL' 'cloudflared did not return a version'
            }
        } catch {
            Add-Check $checks 'cloudflared binary' 'FAIL' 'cloudflared could not be executed'
        }
    }
} else {
    Add-Check $checks 'cloudflared binary' 'FAIL' 'cloudflared.exe is missing; install it separately'
}

if (Test-Path -LiteralPath $TunnelConfigPath -PathType Leaf) {
    try {
        $tunnelConfig = Get-Content -Raw -LiteralPath $TunnelConfigPath | ConvertFrom-Json
        $required = @('public_host', 'origin_url', 'metrics_addr', 'transport_protocol')
        $missing = @($required | Where-Object { $null -eq $tunnelConfig.PSObject.Properties[$_] })
        if ($missing.Count -gt 0) {
            Add-Check $checks 'remote Tunnel expectations' 'FAIL' 'public_host, origin_url, metrics_addr, and transport_protocol are required'
        } else {
            $tunnelPublicHost = [string]$tunnelConfig.public_host
            $originUrl = [string]$tunnelConfig.origin_url
            $metricsAddress = [string]$tunnelConfig.metrics_addr
            $transportProtocol = [string]$tunnelConfig.transport_protocol
            if ($tunnelPublicHost -notmatch '^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$') {
                Add-Check $checks 'public Tunnel hostname' 'FAIL' 'public_host must be one exact DNS hostname'
            } else {
                Add-Check $checks 'public Tunnel hostname' 'PASS' 'one exact public hostname is configured'
            }
            if (-not (Test-LoopbackAddr $metricsAddress)) {
                Add-Check $checks 'metrics binding' 'FAIL' 'metrics_addr must listen on loopback only'
            } else {
                Add-Check $checks 'metrics binding' 'PASS' 'metrics listener is loopback-only'
            }
            if (-not (Test-LoopbackUrl ($originUrl.TrimEnd('/') + '/mcp'))) {
                Add-Check $checks 'origin binding' 'FAIL' 'origin_url must be an HTTP loopback URL with an explicit port and no path'
            } else {
                Add-Check $checks 'origin binding' 'PASS' 'expected dashboard route targets loopback only'
            }
            if ($transportProtocol -notin @('auto', 'http2', 'quic')) {
                Add-Check $checks 'Tunnel transport protocol' 'FAIL' 'transport_protocol must be auto, http2, or quic'
            } else {
                Add-Check $checks 'Tunnel transport protocol' 'PASS' "transport protocol is $transportProtocol"
            }
        }
    } catch {
        Add-Check $checks 'remote Tunnel expectations' 'FAIL' 'config is not valid JSON'
    }
} else {
    Add-Check $checks 'remote Tunnel expectations' 'PENDING' 'run Initialize-CloudflareTunnel.ps1 first'
}

$activeConnections = $null
$readyStatusCode = $null
if ($metricsAddress -and (Test-LoopbackAddr $metricsAddress)) {
    $readyStatusCode = Get-HttpStatusCode "http://$metricsAddress/ready"
}
if ($null -eq $readyStatusCode) {
    Add-Check $checks 'Cloudflare metrics readiness' 'PENDING' 'metrics /ready is not reachable; cloudflared may be stopped and will be checked again after startup'
} elseif ($readyStatusCode -eq 200) {
    Add-Check $checks 'Cloudflare metrics readiness' 'PASS' 'metrics /ready returned HTTP 200'
} else {
    Add-Check $checks 'Cloudflare metrics readiness' 'FAIL' "metrics /ready returned HTTP $readyStatusCode; the HA metric is not accepted as proof of readiness"
}

if ($readyStatusCode -eq 200) {
    try {
        $metricsBody = Get-HttpResponseBody "http://$metricsAddress/metrics"
        if ($null -eq $metricsBody) {
            throw 'metrics endpoint did not return a response'
        }
        $haMatches = [regex]::Matches([string]$metricsBody, '(?m)^cloudflared_tunnel_ha_connections(?:\{[^}\r\n]*\})?\s+([0-9]+(?:\.[0-9]+)?)\s*$')
        if ($haMatches.Count -gt 0) {
            $activeConnections = 0.0
            foreach ($haMatch in $haMatches) {
                $sample = [double]::Parse($haMatch.Groups[1].Value, [Globalization.CultureInfo]::InvariantCulture)
                if ($sample -gt $activeConnections) {
                    $activeConnections = $sample
                }
            }
        }
    } catch {
        $activeConnections = $null
    }
    if ($null -eq $activeConnections) {
        Add-Check $checks 'Cloudflare edge connectivity' 'FAIL' 'metrics /ready returned HTTP 200 but the cloudflared HA connection metric was unavailable'
    } elseif ($activeConnections -gt 0) {
        Add-Check $checks 'Cloudflare edge connectivity' 'PASS' "metrics /ready returned HTTP 200 and cloudflared reports $activeConnections active HA connection(s)"
    } else {
        Add-Check $checks 'Cloudflare edge connectivity' 'FAIL' 'metrics /ready returned HTTP 200 but cloudflared reports no active HA connections'
    }
} elseif ($null -ne $readyStatusCode) {
    Add-Check $checks 'Cloudflare edge connectivity' 'FAIL' "cloudflared metrics /ready is HTTP $readyStatusCode; any HA value is treated as stale until /ready returns HTTP 200"
} else {
    $edgeTargets = @('region1.v2.argotunnel.com', 'region2.v2.argotunnel.com')
    $reachableEdgeTargets = @($edgeTargets | Where-Object { Test-TcpEndpoint $_ 7844 })
    if ($reachableEdgeTargets.Count -gt 0) {
        Add-Check $checks 'Cloudflare edge connectivity' 'PENDING' 'a discovery target accepts TCP 7844, but metrics /ready is unavailable; TCP reachability alone is not tunnel readiness'
    } else {
        $broadBlockCount = 0
        try {
            $broadBlockCount = @(Get-NetFirewallRule -Enabled True -Direction Outbound -Action Block -ErrorAction Stop | Where-Object {
                $application = Get-NetFirewallApplicationFilter -AssociatedNetFirewallRule $_
                $port = Get-NetFirewallPortFilter -AssociatedNetFirewallRule $_
                $address = Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $_
                $remoteAddresses = @($address.RemoteAddress)
                $coversPublicInternet = $remoteAddresses -contains 'Any' -or
                    (($remoteAddresses -contains '0.0.0.0-126.255.255.255') -and
                    ($remoteAddresses -contains '128.0.0.0-255.255.255.255'))
                $application.Program -eq 'Any' -and $port.RemotePort -eq 'Any' -and $coversPublicInternet
            }).Count
        } catch {
            $broadBlockCount = 0
        }
        if ($broadBlockCount -gt 0) {
            Add-Check $checks 'Cloudflare edge connectivity' 'FAIL' "$broadBlockCount enabled broad outbound block rule(s) cover the public Internet"
        } else {
            Add-Check $checks 'Cloudflare edge connectivity' 'PENDING' 'discovery targets did not accept TCP 7844; cloudflared may still rotate to another edge, so verify its HA connection metric after startup'
        }
    }
}

if (Test-Path -LiteralPath $AccessConfigPath -PathType Leaf) {
    try {
        $access = Get-Content -Raw -LiteralPath $AccessConfigPath | ConvertFrom-Json
        $required = @('issuer', 'jwks_url', 'audience', 'principal_to_connection', 'public_hosts')
        $missing = @($required | Where-Object { $null -eq $access.PSObject.Properties[$_] })
        if ($missing.Count -gt 0) {
            Add-Check $checks 'Access verifier config' 'FAIL' 'issuer, jwks_url, audience, principal_to_connection, and public_hosts are required'
        } elseif ([string]::IsNullOrWhiteSpace([string]$access.issuer) -or
            [string]$access.issuer -match 'REPLACE_WITH' -or
            [string]$access.jwks_url -match 'REPLACE_WITH' -or
            [string]$access.audience -match 'REPLACE_WITH' -or
            $access.principal_to_connection.PSObject.Properties.Name -contains 'REPLACE_WITH_CLOUDFLARE_SUBJECT') {
            Add-Check $checks 'Access verifier config' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'replace the generated Cloudflare team, audience tag, and subject mapping placeholders'
        } elseif (@($access.principal_to_connection.PSObject.Properties).Count -eq 0 -or @($access.public_hosts).Count -eq 0) {
            Add-Check $checks 'Access verifier config' 'FAIL' 'principal_to_connection and public_hosts must not be empty'
        } elseif ($tunnelPublicHost -and -not (@($access.public_hosts) -contains $tunnelPublicHost)) {
            Add-Check $checks 'Access verifier config' 'FAIL' 'public_hosts must include the exact tunnel ingress hostname'
        } else {
            Add-Check $checks 'Access verifier config' 'PASS' 'required non-secret verifier settings are present'
            try {
                $jwksUri = [Uri][string]$access.jwks_url
                if ($jwksUri.Scheme -eq 'https' -and (Test-TcpEndpoint $jwksUri.DnsSafeHost 443)) {
                    Add-Check $checks 'Access JWKS network' 'PASS' 'JWKS host accepts HTTPS connections'
                } else {
                    Add-Check $checks 'Access JWKS network' 'FAIL' 'JWKS must use HTTPS and its host must accept TCP 443'
                }
            } catch {
                Add-Check $checks 'Access JWKS network' 'FAIL' 'jwks_url is not a valid HTTPS URL'
            }
        }
    } catch {
        Add-Check $checks 'Access verifier config' 'FAIL' 'config is not valid JSON'
    }
} else {
    Add-Check $checks 'Access verifier config' 'PENDING' 'run Initialize-CloudflareTunnel.ps1 first'
}

if (Test-Path -LiteralPath $TokenPath -PathType Leaf) {
    try {
        $token = Get-TrimmedFileText $TokenPath 8192
        if ($token -and $token -notmatch '[\r\n]') {
            Add-Check $checks 'Cloudflare tunnel token' 'PASS' 'token file contains one line; value is never displayed'
        } else {
            Add-Check $checks 'Cloudflare tunnel token' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'fill exactly one token line in the external token file'
        }
        $acl = Get-Acl -LiteralPath $TokenPath
        $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        $allowedSids = @($currentSid, 'S-1-5-18', 'S-1-5-32-544')
        $unsafeAllowRules = @($acl.Access | Where-Object {
            $_.AccessControlType -eq [Security.AccessControl.AccessControlType]::Allow -and
            $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value -notin $allowedSids
        })
        if ($acl.AreAccessRulesProtected -and $unsafeAllowRules.Count -eq 0) {
            Add-Check $checks 'Cloudflare token ACL' 'PASS' 'inheritance is disabled on the token file'
        } else {
            Add-Check $checks 'Cloudflare token ACL' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'token file inheritance or explicit allow rules are too broad; rerun initialization or tighten ACL'
        }
    } catch {
        Add-Check $checks 'Cloudflare tunnel token' 'FAIL' 'token file could not be checked'
    }
} else {
    Add-Check $checks 'Cloudflare tunnel token' ($(if ($RequireCredentials) { 'FAIL' } else { 'PENDING' })) 'fill the external cloudflared-tunnel-token.txt file'
}

if (Test-PathInside $AccessConfigPath $RepoRoot -and (Test-Path -LiteralPath $AccessConfigPath -PathType Leaf)) {
    $relative = $AccessConfigPath.Substring($RepoRoot.TrimEnd('\').Length).TrimStart('\').Replace('\', '/')
    & git -C $RepoRoot check-ignore --quiet -- $relative 2>$null
    if ($LASTEXITCODE -eq 0) {
        Add-Check $checks 'runtime config ignore rule' 'PASS' 'Access verifier config is ignored by git'
    } else {
        Add-Check $checks 'runtime config ignore rule' 'FAIL' 'Access verifier config is not ignored by git'
    }
}

Write-Output 'Local-Probe Cloudflare tunnel preflight (no Cloudflare login, resource creation, DNS change, or network API call is performed):'
foreach ($check in $checks) {
    Write-Output (('[{0}] {1}: {2}' -f $check.Status, $check.Name, $check.Detail))
}

$failCount = @($checks | Where-Object Status -eq 'FAIL').Count
if ($failCount -gt 0) {
    exit 1
}
exit 0
