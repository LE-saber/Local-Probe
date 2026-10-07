[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$OutputPath,
    [string]$McpOutputPath,
    [switch]$Console
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

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) {
    throw 'RepoRoot must be an existing directory.'
}
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path $RepoRoot 'bin\local-probe-preview.exe'
} else {
    if (-not [IO.Path]::IsPathRooted($OutputPath)) {
        $OutputPath = Join-Path $RepoRoot $OutputPath
    }
    $OutputPath = Get-FullPath $OutputPath
}
if ([IO.Path]::GetExtension($OutputPath) -ne '.exe' -or -not (Test-PathInside $OutputPath $RepoRoot)) {
    throw 'OutputPath must be a .exe path inside RepoRoot.'
}
if ([string]::IsNullOrWhiteSpace($McpOutputPath)) {
    # Keep the MCP origin beside the Preview artifact.  The Preview connection
    # controller resolves this fixed, repo-local path and never accepts an
    # executable path from a model request.
    $McpOutputPath = Join-Path $RepoRoot 'bin\local-probe-mcp.exe'
} else {
    if (-not [IO.Path]::IsPathRooted($McpOutputPath)) {
        $McpOutputPath = Join-Path $RepoRoot $McpOutputPath
    }
    $McpOutputPath = Get-FullPath $McpOutputPath
}
if ([IO.Path]::GetExtension($McpOutputPath) -ne '.exe' -or -not (Test-PathInside $McpOutputPath $RepoRoot)) {
    throw 'McpOutputPath must be a .exe path inside RepoRoot.'
}
if ($OutputPath.Equals($McpOutputPath, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'OutputPath and McpOutputPath must be different files.'
}

$go = Get-Command go -CommandType Application -ErrorAction SilentlyContinue |
    Select-Object -First 1
if ($null -eq $go) {
    throw 'Go is required to build Local-Probe Preview.'
}

$outputDirectory = Split-Path -Parent $OutputPath
if (-not (Test-Path -LiteralPath $outputDirectory -PathType Container)) {
    $null = New-Item -ItemType Directory -Path $outputDirectory -Force
}
$mcpOutputDirectory = Split-Path -Parent $McpOutputPath
if (-not (Test-Path -LiteralPath $mcpOutputDirectory -PathType Container)) {
    $null = New-Item -ItemType Directory -Path $mcpOutputDirectory -Force
}

# The Preview binary has no network or credential flags.  Build arguments are
# passed as an array so paths never become shell fragments or secret output.
# GUI is the default so launching the Preview from Explorer does not leave a
# console window behind.  -Console is an explicit diagnostic escape hatch;
# it also keeps `local-probe-preview.exe --version` convenient in a terminal.
$ldflags = '-s -w'
if (-not $Console) {
    $ldflags = '-H=windowsgui -s -w'
}
$previewBuildArgs = @(
    'build',
    '-trimpath',
    '-ldflags',
    $ldflags,
    '-o',
    $OutputPath,
    './cmd/local-probe-preview'
)
$mcpBuildArgs = @(
    'build',
    '-trimpath',
    '-ldflags',
    '-s -w',
    '-o',
    $McpOutputPath,
    './cmd/local-probe-mcp'
)
Push-Location $RepoRoot
try {
    & $go.Source @previewBuildArgs
    if ($LASTEXITCODE -ne 0) {
        throw 'Go failed to build Local-Probe Preview.'
    }
    & $go.Source @mcpBuildArgs
    if ($LASTEXITCODE -ne 0) {
        throw 'Go failed to build the Local-Probe MCP origin required by Preview Connect.'
    }
} finally {
    Pop-Location
}

$artifact = Get-Item -LiteralPath $OutputPath -Force
if (-not $artifact.PSIsContainer -and $artifact.Length -gt 0) {
    Write-Output ('Built Local-Probe Preview: ' + $OutputPath)
} else {
    throw 'Preview build did not produce a regular executable.'
}
$mcpArtifact = Get-Item -LiteralPath $McpOutputPath -Force
if (-not $mcpArtifact.PSIsContainer -and $mcpArtifact.Length -gt 0) {
    Write-Output ('Built Local-Probe MCP origin for Preview Connect: ' + $McpOutputPath)
} else {
    throw 'MCP build did not produce a regular executable.'
}
