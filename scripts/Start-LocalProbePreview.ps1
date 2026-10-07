[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$ConfigPath,
    [string]$AuditDir,
    [string]$ExecutablePath,
    [ValidateSet('cloudflare_named', 'openai_runtime')]
    [string]$Transport = 'cloudflare_named',
    [switch]$Build
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Test-PathInside([string]$Child, [string]$Parent) {
    $childFull = Get-FullPath $Child
    $parentFull = (Get-FullPath $Parent).TrimEnd([char]'\', [char]'/')
    $prefix = $parentFull + [IO.Path]::DirectorySeparatorChar
    return $childFull.Equals($parentFull, [StringComparison]::OrdinalIgnoreCase) -or
        $childFull.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)
}

function Quote-WindowsArgument([string]$Value) {
    # Windows PowerShell 5.1 has no ProcessStartInfo.ArgumentList and
    # Start-Process joins an argument array into one command line.  Encode
    # paths using the CommandLineToArgvW backslash/quote rules so spaces,
    # trailing separators and embedded quotes cannot change the flag shape.
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    $builder = New-Object System.Text.StringBuilder
    [void]$builder.Append('"')
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq [char]'\') {
            $slashes++
            continue
        }
        if ($character -eq [char]'"') {
            for ($index = 0; $index -lt (2 * $slashes + 1); $index++) {
                [void]$builder.Append('\')
            }
            [void]$builder.Append('"')
            $slashes = 0
            continue
        }
        for ($index = 0; $index -lt $slashes; $index++) {
            [void]$builder.Append('\')
        }
        $slashes = 0
        [void]$builder.Append($character)
    }
    # Backslashes before the closing quote must be doubled.
    for ($index = 0; $index -lt (2 * $slashes); $index++) {
        [void]$builder.Append('\')
    }
    [void]$builder.Append('"')
    return $builder.ToString()
}

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) {
    throw 'RepoRoot must be an existing directory.'
}
if ([string]::IsNullOrWhiteSpace($ExecutablePath)) {
    $ExecutablePath = Join-Path $RepoRoot 'bin\local-probe-preview.exe'
} else {
    if (-not [IO.Path]::IsPathRooted($ExecutablePath)) {
        $ExecutablePath = Join-Path $RepoRoot $ExecutablePath
    }
    $ExecutablePath = Get-FullPath $ExecutablePath
}
if (-not (Test-PathInside $ExecutablePath $RepoRoot) -or [IO.Path]::GetExtension($ExecutablePath) -ne '.exe') {
    throw 'ExecutablePath must be a .exe inside RepoRoot.'
}
if ($Build) {
    & (Join-Path $PSScriptRoot 'Build-LocalProbePreview.ps1') -RepoRoot $RepoRoot -OutputPath $ExecutablePath
}
if (-not (Test-Path -LiteralPath $ExecutablePath -PathType Leaf)) {
    throw 'Preview executable is missing; run with -Build first.'
}

if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = Join-Path $RepoRoot '.runtime\local-probe.json'
} else {
    if (-not [IO.Path]::IsPathRooted($ConfigPath)) {
        $ConfigPath = Join-Path $RepoRoot $ConfigPath
    }
    $ConfigPath = Get-FullPath $ConfigPath
}
if ([string]::IsNullOrWhiteSpace($AuditDir)) {
    $configRoot = [Environment]::GetFolderPath([Environment+SpecialFolder]::ApplicationData)
    if ([string]::IsNullOrWhiteSpace($configRoot)) {
        $configRoot = Join-Path $RepoRoot '.runtime'
    }
    $AuditDir = Join-Path $configRoot 'Local-Probe\audit'
} else {
    if (-not [IO.Path]::IsPathRooted($AuditDir)) {
        $AuditDir = Join-Path $RepoRoot $AuditDir
    }
    $AuditDir = Get-FullPath $AuditDir
}

# A missing config or audit directory is intentional: the GUI renders an
# explicit "unconfigured"/"unavailable" state instead of crashing.  Paths are
# passed as arguments only and are never printed with their contents.
#
# ProcessStartInfo.ArgumentList is used when available (PowerShell 7/.NET
# Core), avoiding all command-line quoting.  The fallback keeps Windows
# PowerShell 5.1 support with Quote-WindowsArgument above.
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $ExecutablePath
$psi.WorkingDirectory = $RepoRoot
$psi.UseShellExecute = $false
$argumentListProperty = $psi.PSObject.Properties['ArgumentList']
if ($null -ne $argumentListProperty) {
    [void]$psi.ArgumentList.Add('-config')
    [void]$psi.ArgumentList.Add($ConfigPath)
    [void]$psi.ArgumentList.Add('-audit-dir')
    [void]$psi.ArgumentList.Add($AuditDir)
    [void]$psi.ArgumentList.Add('-transport')
    [void]$psi.ArgumentList.Add($Transport)
} else {
    $psi.Arguments = @(
        (Quote-WindowsArgument '-config'),
        (Quote-WindowsArgument $ConfigPath),
        (Quote-WindowsArgument '-audit-dir'),
        (Quote-WindowsArgument $AuditDir),
        (Quote-WindowsArgument '-transport'),
        (Quote-WindowsArgument $Transport)
    ) -join ' '
}
$process = [System.Diagnostics.Process]::Start($psi)
if ($null -eq $process) {
    throw 'Preview process did not start.'
}
Write-Output ('Started Local-Probe Preview (PID ' + $process.Id.ToString() + ').')
