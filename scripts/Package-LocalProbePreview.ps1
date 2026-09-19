[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$OutputPath,
    [string]$Version = 'preview',
    [switch]$Build,
    [switch]$Force
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
    return (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -eq 0) -and $item.Length -gt 0
}

function Test-ForbiddenPackagePath([string]$RelativePath) {
    $normalized = $RelativePath.Replace('\', '/').TrimStart('/')
    return $normalized -match '(?i)^(?:\.runtime|\.tools|\.secrets|bin|dist|tmp|secrets)(?:/|$)'
}

function Copy-RepositoryFile([string]$RepoRoot, [string]$StageRoot, [string]$RelativePath) {
    $relative = $RelativePath.Replace('/', '\')
    if ([IO.Path]::IsPathRooted($relative) -or $relative -match '(^|[\\/])\.\.(?:[\\/]|$)') {
        throw "Git returned an unsafe relative path: $RelativePath"
    }
    if (Test-ForbiddenPackagePath $relative) {
        return
    }
    $source = Join-Path $RepoRoot $relative
    if (-not (Test-Path -LiteralPath $source -PathType Leaf)) {
        throw "Git-listed file is missing from the working tree: $RelativePath"
    }
    $destination = Join-Path $StageRoot $relative
    $destinationDirectory = Split-Path -Parent $destination
    if (-not (Test-Path -LiteralPath $destinationDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $destinationDirectory -Force | Out-Null
    }
    Copy-Item -LiteralPath $source -Destination $destination -Force
}

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath (Join-Path $RepoRoot 'go.mod') -PathType Leaf)) {
    throw "RepoRoot is not a Local-Probe checkout: $RepoRoot"
}
if ([string]::IsNullOrWhiteSpace($Version) -or $Version -notmatch '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$') {
    throw 'Version must contain only letters, numbers, dot, underscore, and hyphen.'
}
if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path $RepoRoot ("dist\Local-Probe-Preview-$Version.zip")
} elseif (-not [IO.Path]::IsPathRooted($OutputPath)) {
    $OutputPath = Join-Path $RepoRoot $OutputPath
}
$OutputPath = Get-FullPath $OutputPath
if ([IO.Path]::GetExtension($OutputPath) -ne '.zip') {
    throw 'OutputPath must have a .zip extension.'
}
if ((Test-Path -LiteralPath $OutputPath -PathType Leaf) -and -not $Force) {
    throw "Output already exists; pass -Force to replace it: $OutputPath"
}

$buildScript = Join-Path $RepoRoot 'scripts\Build-LocalProbePreview.ps1'
$previewBinary = Join-Path $RepoRoot 'bin\local-probe-preview.exe'
$mcpBinary = Join-Path $RepoRoot 'bin\local-probe-mcp.exe'
if ($Build) {
    if (-not (Test-Path -LiteralPath $buildScript -PathType Leaf)) {
        throw "Build script is missing: $buildScript"
    }
    & $buildScript -RepoRoot $RepoRoot
    if ($LASTEXITCODE -ne 0) {
        throw 'Preview build failed; package creation was stopped.'
    }
}
if (-not (Test-SafeRegularFile $previewBinary)) {
    throw 'Preview executable is missing. Run Build-LocalProbePreview.ps1 or pass -Build.'
}
if (-not (Test-SafeRegularFile $mcpBinary)) {
    throw 'MCP executable is missing. Run Build-LocalProbePreview.ps1 or pass -Build.'
}

$git = Get-Command git -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $git) {
    throw 'Git is required so the package contains tracked source files without ignored local state.'
}

$stageRoot = Join-Path $RepoRoot ("tmp\.local-probe-preview-package-" + [Guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path $stageRoot -Force | Out-Null

    Push-Location $RepoRoot
    try {
        # Use the index only. A release package must never absorb arbitrary
        # untracked files from the working directory; commit the intended
        # source first, then package that reviewed tree.
        $gitFiles = @(& $git.Source '-C' $RepoRoot '-c' 'core.quotePath=false' 'ls-files' '--cached')
        if ($LASTEXITCODE -ne 0) {
            throw 'Git could not enumerate the source package file set.'
        }
    } finally {
        Pop-Location
    }
    foreach ($relative in $gitFiles) {
        if (-not [string]::IsNullOrWhiteSpace($relative)) {
            Copy-RepositoryFile $RepoRoot $stageRoot ([string]$relative)
        }
    }

    $packageBin = Join-Path $stageRoot 'bin'
    New-Item -ItemType Directory -Path $packageBin -Force | Out-Null
    Copy-Item -LiteralPath $previewBinary -Destination (Join-Path $packageBin 'local-probe-preview.exe') -Force
    Copy-Item -LiteralPath $mcpBinary -Destination (Join-Path $packageBin 'local-probe-mcp.exe') -Force

    foreach ($required in @(
            'README.md',
            'docs\PREVIEW.zh-CN.md',
            'configs\local-probe.example.json',
            'configs\cloudflare-tunnel.example.json',
            'configs\cloudflare-access.example.json'
        )) {
        if (-not (Test-Path -LiteralPath (Join-Path $stageRoot $required) -PathType Leaf)) {
            throw "Required package file is missing: $required"
        }
    }

    $forbidden = @(Get-ChildItem -LiteralPath $stageRoot -File -Recurse -Force | Where-Object {
            $relative = $_.FullName.Substring($stageRoot.Length + 1).Replace('\', '/')
            (Test-ForbiddenPackagePath $relative) -and
                ($relative -notmatch '(?i)^bin/(?:local-probe-preview|local-probe-mcp)\.exe$')
        })
    if ($forbidden.Count -gt 0) {
        throw 'Package staging unexpectedly contains an ignored secret/runtime/build path.'
    }

    $outputDirectory = Split-Path -Parent $OutputPath
    if (-not (Test-Path -LiteralPath $outputDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
    }
    if (Test-Path -LiteralPath $OutputPath -PathType Leaf) {
        Remove-Item -LiteralPath $OutputPath -Force
    }
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [IO.Compression.ZipFile]::CreateFromDirectory($stageRoot, $OutputPath, [IO.Compression.CompressionLevel]::Optimal, $false)
    $archive = Get-Item -LiteralPath $OutputPath
    if (-not (Test-SafeRegularFile $OutputPath)) {
        throw 'Package archive was not created as a regular non-empty file.'
    }
    Write-Output "Created Local-Probe Preview package: $OutputPath"
    Write-Output ("Package size: {0} bytes" -f $archive.Length)
    Write-Output 'Package excludes .runtime, .tools, .secrets, bin build state, dist, and tmp; the two Preview executables are included under package bin\.'
} finally {
    if (Test-Path -LiteralPath $stageRoot -PathType Container) {
        Remove-Item -LiteralPath $stageRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
