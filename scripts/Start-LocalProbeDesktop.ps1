[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$ConfigPath,
    [string]$AuditDir,
    [string]$ExecutablePath,
    [ValidateSet('cloudflare_named', 'openai_runtime')]
    [string]$Transport = 'cloudflare_named',
    [string]$GoPath = 'D:\Go\SDK\bin\go.exe',
    [switch]$Build,
    [switch]$Wait
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
    if ($Value -notmatch '[\s"]') {
        return $Value
    }
    $builder = New-Object System.Text.StringBuilder
    [void]$builder.Append('"')
    $slashes = 0
    foreach ($character in $Value.ToCharArray()) {
        if ($character -eq [char]'\') { $slashes++; continue }
        if ($character -eq [char]'"') {
            for ($index = 0; $index -lt (2 * $slashes + 1); $index++) { [void]$builder.Append('\') }
            [void]$builder.Append('"')
            $slashes = 0
            continue
        }
        for ($index = 0; $index -lt $slashes; $index++) { [void]$builder.Append('\') }
        $slashes = 0
        [void]$builder.Append($character)
    }
    for ($index = 0; $index -lt (2 * $slashes); $index++) { [void]$builder.Append('\') }
    [void]$builder.Append('"')
    return $builder.ToString()
}

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) {
    throw 'RepoRoot must be an existing directory.'
}
if ([string]::IsNullOrWhiteSpace($ExecutablePath)) {
    $ExecutablePath = Join-Path $RepoRoot 'bin\local-probe-desktop.exe'
} elseif (-not [IO.Path]::IsPathRooted($ExecutablePath)) {
    $ExecutablePath = Join-Path $RepoRoot $ExecutablePath
}
$ExecutablePath = Get-FullPath $ExecutablePath
if ([IO.Path]::GetExtension($ExecutablePath) -ne '.exe' -or -not (Test-PathInside $ExecutablePath $RepoRoot)) {
    throw 'ExecutablePath must be a .exe inside RepoRoot.'
}
if ($Build) {
    & (Join-Path $PSScriptRoot 'Build-LocalProbeDesktop.ps1') -RepoRoot $RepoRoot -OutputPath $ExecutablePath -GoPath $GoPath
}
if (-not (Test-Path -LiteralPath $ExecutablePath -PathType Leaf)) {
    throw 'Desktop executable is missing; run with -Build first.'
}

if ([string]::IsNullOrWhiteSpace($ConfigPath)) {
    $ConfigPath = Join-Path $RepoRoot '.runtime\local-probe.json'
} elseif (-not [IO.Path]::IsPathRooted($ConfigPath)) {
    $ConfigPath = Join-Path $RepoRoot $ConfigPath
}
$ConfigPath = Get-FullPath $ConfigPath
if ([string]::IsNullOrWhiteSpace($AuditDir)) {
    $configRoot = [Environment]::GetFolderPath([Environment+SpecialFolder]::ApplicationData)
    if ([string]::IsNullOrWhiteSpace($configRoot)) { $configRoot = Join-Path $RepoRoot '.runtime' }
    $AuditDir = Join-Path $configRoot 'Local-Probe\audit'
} elseif (-not [IO.Path]::IsPathRooted($AuditDir)) {
    $AuditDir = Join-Path $RepoRoot $AuditDir
}
$AuditDir = Get-FullPath $AuditDir

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = $ExecutablePath
$psi.WorkingDirectory = $RepoRoot
$psi.UseShellExecute = $false
$psi.WindowStyle = [System.Diagnostics.ProcessWindowStyle]::Normal
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
        (Quote-WindowsArgument '-config'), (Quote-WindowsArgument $ConfigPath),
        (Quote-WindowsArgument '-audit-dir'), (Quote-WindowsArgument $AuditDir),
        (Quote-WindowsArgument '-transport'), (Quote-WindowsArgument $Transport)
    ) -join ' '
}
$process = [System.Diagnostics.Process]::Start($psi)
if ($null -eq $process) {
    throw 'Local-Probe Desktop process did not start.'
}
if ($Wait) {
    $process.WaitForExit()
    exit $process.ExitCode
}
Write-Output ('Started Local-Probe Desktop (PID ' + $process.Id.ToString() + ').')
