[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$SecretRoot,
    [string]$RuntimeRoot,
    [string]$CloudflaredPath,
    [switch]$RequireCloudflared,
    [switch]$RequireCredentials,
    [switch]$Json
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$checks = New-Object System.Collections.ArrayList

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

function Add-Check([string]$Name, [string]$Status, [string]$Detail) {
    [void]$script:checks.Add([ordered]@{
            name = $Name
            status = $Status
            detail = $Detail
        })
}

function Test-JsonFile([string]$Path) {
    try {
        $null = Get-Content -Raw -LiteralPath $Path | ConvertFrom-Json
        return $true
    } catch {
        return $false
    }
}

function Get-LegacyCloudflaredPath([string]$Root) {
    return Join-Path (Split-Path -Parent $Root) '_tools\tunnel-client-v0.0.14-windows-amd64\bin\cloudflared.exe'
}

function Test-CommandAvailable([string]$Name, [string]$ProbeArgument) {
    $command = Get-Command -Name $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        Add-Check $Name 'FAIL' 'command is not available on PATH'
        return $false
    }
    try {
        $null = & $command.Source $ProbeArgument 2>$null
        if ($LASTEXITCODE -ne 0) {
            Add-Check $Name 'FAIL' 'command was found but the probe returned a failure'
            return $false
        }
    } catch {
        Add-Check $Name 'FAIL' 'command was found but could not be executed'
        return $false
    }
    Add-Check $Name 'PASS' 'command is available and responded to a local probe'
    return $true
}

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) {
    throw "RepoRoot does not exist: $RepoRoot"
}
if ([string]::IsNullOrWhiteSpace($SecretRoot)) {
    $SecretRoot = Join-Path (Split-Path -Parent $RepoRoot) '.secrets'
}
if ([string]::IsNullOrWhiteSpace($RuntimeRoot)) {
    $RuntimeRoot = Join-Path $RepoRoot '.runtime'
}
$SecretRoot = Get-FullPath $SecretRoot
$RuntimeRoot = Get-FullPath $RuntimeRoot

$psPass = $PSVersionTable.PSVersion.Major -ge 5
Add-Check 'PowerShell' ($(if ($psPass) { 'PASS' } else { 'FAIL' })) ("PowerShell $($PSVersionTable.PSVersion); Windows PowerShell 5.1+ syntax is supported")
$goPass = Test-CommandAvailable 'go' 'version'
$gitPass = Test-CommandAvailable 'git' '--version'

$goMod = Join-Path $RepoRoot 'go.mod'
if (Test-Path -LiteralPath $goMod -PathType Leaf) {
    $goDirective = Get-Content -LiteralPath $goMod | Where-Object { $_ -match '^go\s+\S+' } | Select-Object -First 1
    Add-Check 'go.mod' ($(if ($null -ne $goDirective) { 'PASS' } else { 'FAIL' })) ($(if ($null -ne $goDirective) { 'Go module directive is present' } else { 'Go module directive is missing' }))
} else {
    Add-Check 'go.mod' 'FAIL' 'go.mod is missing from the checkout'
}

foreach ($relative in @(
        'scripts\Build-LocalProbePreview.ps1',
        'scripts\Setup-LocalProbePreview.ps1',
        'scripts\Package-LocalProbePreview.ps1',
        'configs\local-probe.example.json',
        'configs\cloudflare-tunnel.example.json',
        'configs\cloudflare-access.example.json',
        'README.md'
    )) {
    $path = Join-Path $RepoRoot $relative
    Add-Check "repository file: $relative" ($(if (Test-Path -LiteralPath $path -PathType Leaf) { 'PASS' } else { 'FAIL' })) ($(if (Test-Path -LiteralPath $path -PathType Leaf) { 'file is present' } else { 'required file is missing' }))
}

$primaryCloudflared = Join-Path $RepoRoot '.tools\cloudflared.exe'
$legacyCloudflared = Get-LegacyCloudflaredPath $RepoRoot
$candidateCloudflared = $null
$candidateLabel = 'missing'
if (-not [string]::IsNullOrWhiteSpace($CloudflaredPath)) {
    $candidateCloudflared = Get-FullPath $CloudflaredPath
    $candidateLabel = 'explicit'
} elseif (Test-SafeRegularFile $primaryCloudflared) {
    $candidateCloudflared = $primaryCloudflared
    $candidateLabel = 'repo-local'
} elseif (Test-SafeRegularFile $legacyCloudflared) {
    $candidateCloudflared = $legacyCloudflared
    $candidateLabel = 'legacy-fallback'
}

if ($null -eq $candidateCloudflared) {
    $missingStatus = if ($RequireCloudflared) { 'FAIL' } else { 'PENDING' }
    Add-Check 'cloudflared.exe' $missingStatus 'not installed; no network download was attempted'
} elseif (-not (Test-SafeRegularFile $candidateCloudflared)) {
    Add-Check 'cloudflared.exe' 'FAIL' 'candidate is missing or is a reparse point; use a verified regular file'
} else {
    try {
        $null = & $candidateCloudflared '--version' 2>$null
        if ($LASTEXITCODE -eq 0) {
            Add-Check 'cloudflared.exe' 'PASS' "local $candidateLabel binary responded to --version"
        } else {
            Add-Check 'cloudflared.exe' 'FAIL' "local $candidateLabel binary did not respond successfully to --version"
        }
    } catch {
        Add-Check 'cloudflared.exe' 'FAIL' "local $candidateLabel binary could not be executed"
    }
}

foreach ($relative in @('local-probe.json', 'cloudflare-tunnel.json', 'cloudflare-access.json')) {
    $path = Join-Path $RuntimeRoot $relative
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        $status = if ($RequireCredentials) { 'FAIL' } else { 'PENDING' }
        Add-Check "runtime/$relative" $status 'run Setup-LocalProbePreview.ps1 first'
    } elseif (-not (Test-JsonFile $path)) {
        Add-Check "runtime/$relative" 'FAIL' 'file exists but is not valid JSON'
    } else {
        Add-Check "runtime/$relative" 'PASS' 'JSON file is present and parseable'
    }
}

$tokenPath = Join-Path $SecretRoot 'cloudflared-tunnel-token.txt'
if (-not (Test-Path -LiteralPath $tokenPath -PathType Leaf)) {
    $status = if ($RequireCredentials) { 'FAIL' } else { 'PENDING' }
    Add-Check 'external Tunnel token file' $status 'create it outside the repository; the token value is never displayed'
} else {
    $tokenInfo = Get-Item -LiteralPath $tokenPath
    if ($tokenInfo.Length -le 0) {
        $status = if ($RequireCredentials) { 'FAIL' } else { 'PENDING' }
        Add-Check 'external Tunnel token file' $status 'file exists but is empty; the value was not read or displayed'
    } else {
        Add-Check 'external Tunnel token file' 'PASS' 'non-empty external file exists; the value was not read or displayed'
    }
}

$ignorePath = Join-Path $RepoRoot '.gitignore'
if (Test-Path -LiteralPath $ignorePath -PathType Leaf) {
    $ignoreText = Get-Content -Raw -LiteralPath $ignorePath
    $missingIgnore = @()
    foreach ($rule in @('/.runtime/', '/.tools/', '/bin/', '/tmp/')) {
        if ($ignoreText -notmatch [regex]::Escape($rule)) {
            $missingIgnore += $rule
        }
    }
    if ($missingIgnore.Count -eq 0) {
        Add-Check 'local artifact ignore rules' 'PASS' 'runtime, tools, build, and temporary paths are ignored'
    } else {
        Add-Check 'local artifact ignore rules' 'FAIL' ('missing rules: ' + ($missingIgnore -join ', '))
    }
} else {
    Add-Check 'local artifact ignore rules' 'FAIL' '.gitignore is missing'
}

$failures = @($checks | Where-Object { $_.status -eq 'FAIL' })
$ready = $failures.Count -eq 0
$summary = [ordered]@{
    schema_version = 'local-probe.environment.v1'
    repo_root = $RepoRoot
    cloudflared_source = $candidateLabel
    network_download_attempted = $false
    ready = $ready
    checks = @($checks)
}

if ($Json) {
    $summary | ConvertTo-Json -Depth 8
} else {
    Write-Output 'Local-Probe Preview environment probe (no network download or credential value display):'
    foreach ($check in $checks) {
        Write-Output ('[{0}] {1}: {2}' -f $check.status, $check.name, $check.detail)
    }
    Write-Output ("Ready: {0}" -f $ready)
}

if (-not $ready) {
    exit 1
}
