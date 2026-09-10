[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot)
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Get-FreeLoopbackPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    } finally {
        $listener.Stop()
    }
}

function Wait-LoopbackPort([int]$Port, [Diagnostics.Process]$Process, [int]$TimeoutMilliseconds = 3000) {
    $deadline = [DateTime]::UtcNow.AddMilliseconds($TimeoutMilliseconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        if ($Process.HasExited) {
            throw "metrics fixture exited before listening (exit code $($Process.ExitCode))"
        }
        $client = [Net.Sockets.TcpClient]::new()
        try {
            $connect = $client.ConnectAsync('127.0.0.1', $Port)
            if ($connect.Wait(100) -and $client.Connected) {
                return
            }
        } catch {
            # The fixture may still be binding its loopback listener.
        } finally {
            $client.Dispose()
        }
        Start-Sleep -Milliseconds 25
    }
    throw "metrics fixture did not listen on 127.0.0.1:$Port"
}

function Assert-Contains([string]$Text, [string]$Pattern, [string]$FailureMessage) {
    if ($Text -notmatch $Pattern) {
        throw $FailureMessage
    }
}

$RepoRoot = Get-FullPath $RepoRoot
$preflight = Join-Path $RepoRoot 'scripts\Test-CloudflareTunnelPrerequisites.ps1'
$exampleConfig = Join-Path $RepoRoot 'configs\cloudflare-tunnel.example.json'
$initializeScript = Join-Path $RepoRoot 'scripts\Initialize-CloudflareTunnel.ps1'
$startScript = Join-Path $RepoRoot 'scripts\Start-CloudflareTunnel.ps1'

$example = Get-Content -Raw -LiteralPath $exampleConfig | ConvertFrom-Json
if ([string]$example.transport_protocol -ne 'auto') {
    throw 'example config must default transport_protocol to auto'
}
$initializeText = Get-Content -Raw -LiteralPath $initializeScript
Assert-Contains $initializeText '\[string\]\$TransportProtocol = ''auto''' 'Initialize-CloudflareTunnel.ps1 must default TransportProtocol to auto'
$startText = Get-Content -Raw -LiteralPath $startScript
Assert-Contains $startText 'if \(\$transportProtocol -eq ''auto''\)' 'Start-CloudflareTunnel.ps1 must have an auto transport branch'
Assert-Contains $startText "Remove-Item Env:TUNNEL_TRANSPORT_PROTOCOL" 'auto transport must remove inherited TUNNEL_TRANSPORT_PROTOCOL'
Assert-Contains $startText '\$env:TUNNEL_TRANSPORT_PROTOCOL = \$transportProtocol' 'explicit transport must set TUNNEL_TRANSPORT_PROTOCOL'

$fixtureRoot = Join-Path ([IO.Path]::GetTempPath()) "local-probe-cloudflare-prerequisites-$PID"
if (Test-Path -LiteralPath $fixtureRoot) {
    throw "fixture path already exists: $fixtureRoot"
}
New-Item -ItemType Directory -Path $fixtureRoot | Out-Null
$serverScript = Join-Path $fixtureRoot 'metrics-server.ps1'
$fakeCloudflared = Join-Path $fixtureRoot 'cloudflared.ps1'

# A tiny HTTP fixture keeps this check independent from a real cloudflared
# process.  It intentionally serves a stale positive HA metric in the 503
# case so the preflight must reject that false-health combination.
$serverSource = @'
param(
    [int]$Port,
    [int]$ReadyStatus,
    [string]$HaValue
)
Set-StrictMode -Version Latest
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $Port)
$listener.Start()
try {
    while ($true) {
        $client = $listener.AcceptTcpClient()
        try {
            $stream = $client.GetStream()
            $buffer = New-Object byte[] 4096
            $read = $stream.Read($buffer, 0, $buffer.Length)
            $request = [Text.Encoding]::ASCII.GetString($buffer, 0, $read)
            $path = if ($request -match '(?m)^GET\s+([^\s]+)') { $Matches[1] } else { '/' }
            if ($path -eq '/ready') {
                $status = $ReadyStatus
                $body = if ($status -eq 200) { 'ready' } else { 'not ready' }
            } elseif ($path -eq '/metrics') {
                $status = 200
                $body = "# HELP cloudflared_tunnel_ha_connections active connections`ncloudflared_tunnel_ha_connections $HaValue`n"
            } else {
                $status = 404
                $body = 'not found'
            }
            $bodyBytes = [Text.Encoding]::UTF8.GetBytes($body)
            $header = "HTTP/1.1 $status`r`nContent-Type: text/plain`r`nContent-Length: $($bodyBytes.Length)`r`nConnection: close`r`n`r`n"
            $headerBytes = [Text.Encoding]::ASCII.GetBytes($header)
            $stream.Write($headerBytes, 0, $headerBytes.Length)
            $stream.Write($bodyBytes, 0, $bodyBytes.Length)
            $stream.Flush()
        } catch {
            [Console]::Error.WriteLine($_.Exception.ToString())
        } finally {
            $client.Dispose()
        }
    }
} finally {
    $listener.Stop()
}
'@
[IO.File]::WriteAllText($serverScript, $serverSource, [Text.UTF8Encoding]::new($false))
$fakeCloudflaredSource = @'
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$Arguments
)
if ($Arguments -contains '--version') {
    Write-Output 'cloudflared version isolated-test'
}
exit 0
'@
[IO.File]::WriteAllText($fakeCloudflared, $fakeCloudflaredSource, [Text.UTF8Encoding]::new($false))

$shellPath = (Get-Process -Id $PID).Path
if ([string]::IsNullOrWhiteSpace($shellPath)) {
    throw 'could not resolve the current PowerShell executable'
}

$cases = @(
    [PSCustomObject]@{ Name = 'stale HA with HTTP 503'; ReadyStatus = 503; HaValue = '1'; ExpectedExit = 1; ExpectEdgePass = $false },
    [PSCustomObject]@{ Name = 'ready with active HA'; ReadyStatus = 200; HaValue = '1'; ExpectedExit = 0; ExpectEdgePass = $true },
    [PSCustomObject]@{ Name = 'ready without active HA'; ReadyStatus = 200; HaValue = '0'; ExpectedExit = 1; ExpectEdgePass = $false }
)

try {
    foreach ($case in $cases) {
        $port = Get-FreeLoopbackPort
        $runtimeRoot = Join-Path $RepoRoot ("tmp\cloudflare-prereq-test-$PID-$port")
        $secretRoot = Join-Path $fixtureRoot "secrets-$port"
        New-Item -ItemType Directory -Path $runtimeRoot | Out-Null
        $tunnelConfig = [ordered]@{
            public_host = 'mcp.example.test'
            origin_url = 'http://127.0.0.1:8788'
            metrics_addr = "127.0.0.1:$port"
            transport_protocol = 'auto'
        } | ConvertTo-Json
        [IO.File]::WriteAllText((Join-Path $runtimeRoot 'cloudflare-tunnel.json'), $tunnelConfig, [Text.UTF8Encoding]::new($false))

        $server = $null
        $serverStdOut = Join-Path $fixtureRoot "server-$port.out"
        $serverStdErr = Join-Path $fixtureRoot "server-$port.err"
        try {
            $server = Start-Process -FilePath $shellPath -ArgumentList @(
                '-NoLogo', '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', $serverScript,
                '-Port', $port, '-ReadyStatus', $case.ReadyStatus, '-HaValue', $case.HaValue
            ) -PassThru -RedirectStandardOutput $serverStdOut -RedirectStandardError $serverStdErr
            Wait-LoopbackPort $port $server

            $output = & $shellPath -NoLogo -NoProfile -ExecutionPolicy Bypass -File $preflight `
                -RepoRoot $RepoRoot -RuntimeRoot $runtimeRoot `
                -SecretRoot $secretRoot `
                -CloudflaredPath $fakeCloudflared `
                -TunnelConfigPath (Join-Path $runtimeRoot 'cloudflare-tunnel.json') `
                -AccessConfigPath (Join-Path $runtimeRoot 'missing-access.json') `
                -TokenPath (Join-Path $secretRoot 'missing-token.txt') 2>&1 | Out-String
            $exitCode = $LASTEXITCODE
            if ($exitCode -ne $case.ExpectedExit) {
                throw "$($case.Name): expected preflight exit $($case.ExpectedExit), got $exitCode`n$output"
            }
            $edgeLine = (($output -split "`r?`n") | Where-Object { $_ -match 'Cloudflare edge connectivity' }) -join "`n"
            if ($case.ExpectEdgePass) {
                Assert-Contains $edgeLine '\[PASS\]' "$($case.Name): expected a PASS only after /ready 200 and HA > 0"
            } elseif ($edgeLine -match '\[PASS\]') {
                throw "$($case.Name): stale/unready state was reported as PASS`n$edgeLine"
            }
            Write-Output "[PASS] $($case.Name)"
        } finally {
            if ($null -ne $server -and -not $server.HasExited) {
                $server.Kill()
                $server.WaitForExit()
            }
            Remove-Item -LiteralPath $runtimeRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
} finally {
    Remove-Item -LiteralPath $fixtureRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Output 'Cloudflare tunnel PowerShell checks passed in an isolated loopback fixture.'
