[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$OutputPath,
    [string]$GoPath = 'D:\Go\SDK\bin\go.exe',
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
    $OutputPath = Join-Path $RepoRoot 'bin\local-probe-desktop.exe'
} elseif (-not [IO.Path]::IsPathRooted($OutputPath)) {
    $OutputPath = Join-Path $RepoRoot $OutputPath
}
$OutputPath = Get-FullPath $OutputPath
if ([IO.Path]::GetExtension($OutputPath) -ne '.exe' -or -not (Test-PathInside $OutputPath $RepoRoot)) {
    throw 'OutputPath must be a .exe path inside RepoRoot.'
}

if (-not (Test-Path -LiteralPath $GoPath -PathType Leaf)) {
    $goCommand = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $goCommand) {
        throw 'Go 1.25 or newer is required to build Local-Probe Desktop.'
    }
    $GoPath = $goCommand.Source
} else {
    $GoPath = Get-FullPath $GoPath
}
$versionOutput = & $GoPath version
if ($LASTEXITCODE -ne 0 -or $versionOutput -notmatch '^go version go([0-9]+)\.([0-9]+)') {
    throw 'The configured Go executable did not report a supported version.'
}
$goMajor = [int]$Matches[1]
$goMinor = [int]$Matches[2]
if ($goMajor -lt 1 -or ($goMajor -eq 1 -and $goMinor -lt 25)) {
    throw 'Go 1.25 or newer is required by this repository.'
}

$outputDirectory = Split-Path -Parent $OutputPath
if (-not (Test-Path -LiteralPath $outputDirectory -PathType Container)) {
    $null = New-Item -ItemType Directory -Path $outputDirectory -Force
}
$mcpOutputPath = Join-Path $RepoRoot 'bin\local-probe-mcp.exe'
$mcpOutputDirectory = Split-Path -Parent $mcpOutputPath
if (-not (Test-Path -LiteralPath $mcpOutputDirectory -PathType Container)) {
    $null = New-Item -ItemType Directory -Path $mcpOutputDirectory -Force
}
$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    $env:CGO_ENABLED = '0'
    $ldflags = if ($Console) { '-s -w' } else { '-H=windowsgui -s -w' }
    $buildArgs = @('build', '-trimpath', '-ldflags', $ldflags, '-o', $OutputPath, './cmd/local-probe-desktop')
    Push-Location $RepoRoot
    try {
        & $GoPath @buildArgs
        if ($LASTEXITCODE -ne 0) {
            throw 'Go failed to build Local-Probe Desktop.'
        }
        $mcpBuildArgs = @('build', '-trimpath', '-ldflags', '-s -w', '-o', $mcpOutputPath, './cmd/local-probe-mcp')
        & $GoPath @mcpBuildArgs
        if ($LASTEXITCODE -ne 0) {
            throw 'Go failed to build the bundled Local-Probe MCP executable.'
        }
    } finally {
        Pop-Location
    }
} finally {
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
}

$artifact = Get-Item -LiteralPath $OutputPath -Force
if ($artifact.PSIsContainer -or $artifact.Length -le 0) {
    throw 'Desktop build did not produce a non-empty executable.'
}
$mcpArtifact = Get-Item -LiteralPath $mcpOutputPath -Force
if ($mcpArtifact.PSIsContainer -or $mcpArtifact.Length -le 0) {
    throw 'MCP build did not produce a non-empty executable.'
}
Write-Output ('Built Local-Probe Desktop: ' + $OutputPath)
Write-Output ('Built matching Local-Probe MCP: ' + $mcpOutputPath)
