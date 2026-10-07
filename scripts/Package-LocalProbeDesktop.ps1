[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot),
    [string]$OutputDirectory,
    [string]$DesktopExecutable = 'bin\local-probe-desktop.exe',
    [string]$McpExecutable = 'bin\local-probe-mcp.exe'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) { return [IO.Path]::GetFullPath($Path) }

function Test-PathInside([string]$Child, [string]$Parent) {
    $childFull = Get-FullPath $Child
    $parentFull = (Get-FullPath $Parent).TrimEnd([char]'\', [char]'/')
    return $childFull.Equals($parentFull, [StringComparison]::OrdinalIgnoreCase) -or
        $childFull.StartsWith($parentFull + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)
}

function Test-ReparsePoint($Item) {
    return (($Item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)
}

function Test-ExcludedDirectory([string]$RelativePath) {
    $parts = $RelativePath -split '/'
    foreach ($part in $parts) {
        if ($part -in @('.git', '.planning', '.serena', '.runtime', '.secrets', '.tools', 'runtime', 'secrets', 'logs', 'userroots', 'node_modules', 'vendor', 'dist', 'bin', 'tmp', 'Build')) {
            return $true
        }
    }
    if ($parts -contains 'audit' -and
        -not ($RelativePath.Equals('internal/audit', [StringComparison]::OrdinalIgnoreCase) -or
            $RelativePath.StartsWith('internal/audit/', [StringComparison]::OrdinalIgnoreCase))) {
        return $true
    }
    return $false
}

function Test-ExcludedFile([string]$RelativePath, [string]$Name) {
    if ($Name -match '(?i)\.desktop-(?:events|backup)\.json(?:\.bak)?$') { return $true }
    if ($Name -match '(?i)^\.env(?:\..*)?$' -or $Name -match '(?i)\.(?:log|jsonl|pem|pfx|p12|key|crt|exe)$') { return $true }
    if ($Name -in @('local-probe.json', 'local-probe.json.bak', 'cloudflare-access.json', 'cloudflare-tunnel.json', 'tunnel-client.yaml', 'mcp-bearer-token.txt', 'cloudflared-tunnel-token.txt', 'control-plane-api-key.txt', 'tunnel-id.txt')) { return $true }
    if ($RelativePath.StartsWith('configs/', [StringComparison]::OrdinalIgnoreCase) -and
        ([IO.Path]::GetExtension($Name) -in @('.json', '.yaml', '.yml')) -and $Name -notmatch '(?i)\.example\.(?:json|yaml|yml)$') { return $true }
    return $false
}

function Add-AllowlistedTree([string]$Directory, [string]$RelativeDirectory, [System.Collections.Generic.List[string]]$Files) {
    $directoryItem = Get-Item -LiteralPath $Directory -Force
    if (Test-ReparsePoint $directoryItem) { throw "Allowlisted source directory is a reparse point: $RelativeDirectory" }
    foreach ($item in Get-ChildItem -LiteralPath $Directory -Force) {
        $relative = if ($RelativeDirectory) { "$RelativeDirectory/$($item.Name)" } else { $item.Name }
        if (Test-ReparsePoint $item) { throw "Reparse points are not package inputs: $relative" }
        if ($item.PSIsContainer) {
            if (-not (Test-ExcludedDirectory $relative)) { Add-AllowlistedTree $item.FullName $relative $Files }
        } elseif (-not (Test-ExcludedFile $relative $item.Name)) {
            $Files.Add($item.FullName)
        }
    }
}

function Assert-RegularFile([string]$Path, [string]$Description) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "$Description is missing: $Path" }
    $item = Get-Item -LiteralPath $Path -Force
    if (Test-ReparsePoint $item) { throw "$Description must not be a reparse point: $Path" }
    if ($item.Length -le 0) { throw "$Description is empty: $Path" }
    return $item.FullName
}

$RepoRoot = Get-FullPath $RepoRoot
if (-not (Test-Path -LiteralPath (Join-Path $RepoRoot 'go.mod') -PathType Leaf)) { throw 'RepoRoot must be a Local-Probe checkout.' }
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) { $OutputDirectory = Join-Path $RepoRoot 'dist\desktop-preview' }
elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) { $OutputDirectory = Join-Path $RepoRoot $OutputDirectory }
$OutputDirectory = Get-FullPath $OutputDirectory
if (-not (Test-PathInside $OutputDirectory (Join-Path $RepoRoot 'dist'))) { throw 'OutputDirectory must be inside RepoRoot\dist, which is excluded from package inputs.' }
if (-not (Test-Path -LiteralPath (Join-Path $RepoRoot 'dist') -PathType Container)) {
    $null = New-Item -ItemType Directory -Path (Join-Path $RepoRoot 'dist') -ErrorAction Stop
}
$distRoot = Get-Item -LiteralPath (Join-Path $RepoRoot 'dist') -Force
if (Test-ReparsePoint $distRoot) { throw 'RepoRoot\dist must not be a reparse point.' }
if (-not (Test-Path -LiteralPath $OutputDirectory -PathType Container)) {
    $parent = Split-Path -Parent $OutputDirectory
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) { throw 'Only RepoRoot\dist may be created automatically; other output parents must already exist.' }
    $null = New-Item -ItemType Directory -Path $OutputDirectory -ErrorAction Stop
}
if (Test-ReparsePoint (Get-Item -LiteralPath $OutputDirectory -Force)) { throw 'OutputDirectory must not be a reparse point.' }

foreach ($relative in @($DesktopExecutable, $McpExecutable)) {
    if ([IO.Path]::IsPathRooted($relative)) { $resolved = Get-FullPath $relative } else { $resolved = Get-FullPath (Join-Path $RepoRoot $relative) }
    if (-not (Test-PathInside $resolved $RepoRoot)) { throw 'Executable paths must remain inside RepoRoot.' }
    if ($relative -eq $DesktopExecutable) { $desktopPath = Assert-RegularFile $resolved 'Desktop executable' } else { $mcpPath = Assert-RegularFile $resolved 'MCP executable' }
}
if ($desktopPath.Equals($mcpPath, [StringComparison]::OrdinalIgnoreCase)) { throw 'Desktop and MCP executable paths must be distinct.' }

$go = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $go) { throw 'Go is required to scan the module graph for package notices.' }
$format = '{{.Path}}|{{.Version}}|{{.Dir}}|{{if .Replace}}{{.Replace.Dir}}{{end}}'
Push-Location $RepoRoot
try {
    & $go.Source mod download all
    if ($LASTEXITCODE -ne 0) { throw 'go mod download all failed; no package was produced.' }
    $moduleLines = @(& $go.Source list -m -f $format all)
    if ($LASTEXITCODE -ne 0) { throw 'go list -m all failed; no package was produced.' }
}
finally { Pop-Location }

$moduleRecords = [System.Collections.Generic.List[object]]::new()
$seenModules = @{}
foreach ($line in $moduleLines) {
    $fields = ([string]$line).Split('|')
    if ($fields.Count -lt 4 -or [string]::IsNullOrWhiteSpace($fields[0]) -or $fields[0] -eq 'github.com/LE-saber/Local-Probe') { continue }
    $modulePath, $version, $moduleDir, $replaceDir = $fields[0..3]
    if ($seenModules.ContainsKey($modulePath)) { continue }
    $seenModules[$modulePath] = $true
    $moduleRecords.Add([pscustomobject]@{ Path = $modulePath; Version = $version; Dir = $(if ($replaceDir) { $replaceDir } else { $moduleDir }) })
}
foreach ($requiredModule in @('github.com/jchv/go-webview2', 'github.com/jchv/go-winloader')) {
    if (-not $seenModules.ContainsKey($requiredModule)) { throw "Expected desktop dependency missing from go list -m all: $requiredModule" }
}

$stamp = [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ')
$packageName = "Local-Probe-Desktop-Preview-$stamp-$([Guid]::NewGuid().ToString('N'))"
$packageRoot = Join-Path $OutputDirectory $packageName
if (Test-Path -LiteralPath $packageRoot) { throw 'Unique package output path already exists; refusing to overwrite.' }
$null = New-Item -ItemType Directory -Path $packageRoot -ErrorAction Stop
$stage = Join-Path $packageRoot 'source'
$null = New-Item -ItemType Directory -Path $stage -ErrorAction Stop

$files = [System.Collections.Generic.List[string]]::new()
foreach ($top in @('cmd', 'internal', 'configs', 'scripts', 'docs', 'prototypes', 'third_party', 'manual-test-targets', 'testdata', 'assets')) {
    $path = Join-Path $RepoRoot $top
    if (Test-Path -LiteralPath $path -PathType Container) { Add-AllowlistedTree $path $top $files }
}
foreach ($topFile in @('README.md', 'LICENSE', 'go.mod', 'go.sum')) {
    $path = Join-Path $RepoRoot $topFile
    if (Test-Path -LiteralPath $path -PathType Leaf) { $files.Add((Assert-RegularFile $path "Allowlisted file $topFile")) }
}
foreach ($file in $files) {
    $relative = [IO.Path]::GetRelativePath($RepoRoot, $file)
    $destination = Join-Path $stage $relative
    if (-not (Test-PathInside $destination $stage)) { throw "Package path escaped staging: $relative" }
    $null = [IO.Directory]::CreateDirectory((Split-Path -Parent $destination))
    [IO.File]::Copy($file, $destination, $false)
}

$licenseRoot = Join-Path $stage 'third_party\licenses\go-modules'
$noticeLines = [System.Collections.Generic.List[string]]::new()
$noticeLines.Add('Local-Probe Desktop Preview: third-party notices')
$noticeLines.Add('Project-owned code is MIT licensed (see LICENSE). This is not a production-ready release.')
$noticeLines.Add('Go module graph scanned with `go list -m all` at package time. License texts are retained below.')
$noticeLines.Add('')
$licenseNames = @('LICENSE', 'LICENSE.txt', 'LICENSE.md', 'COPYING', 'COPYING.txt', 'NOTICE', 'NOTICE.txt')
foreach ($module in $moduleRecords) {
    if ([string]::IsNullOrWhiteSpace($module.Dir) -or -not (Test-Path -LiteralPath $module.Dir -PathType Container)) { throw "Module source is not available for license review: $($module.Path)@$($module.Version)" }
    $moduleDir = Get-FullPath $module.Dir
    $licenseFiles = @()
    foreach ($name in $licenseNames) {
        $candidate = Join-Path $moduleDir $name
        if (Test-Path -LiteralPath $candidate -PathType Leaf) { $licenseFiles += Assert-RegularFile $candidate "License for $($module.Path)" }
    }
    if ($licenseFiles.Count -eq 0) { throw "No recognized license or notice file found for $($module.Path)@$($module.Version)." }
    $slug = [regex]::Replace("$($module.Path)@$($module.Version)", '[^A-Za-z0-9._-]', '_')
    $destinationDirectory = Join-Path $licenseRoot $slug
    $null = [IO.Directory]::CreateDirectory($destinationDirectory)
    $copied = [System.Collections.Generic.List[string]]::new()
    foreach ($licenseFile in $licenseFiles) {
        $destination = Join-Path $destinationDirectory ([IO.Path]::GetFileName($licenseFile))
        [IO.File]::Copy($licenseFile, $destination, $false)
        $copied.Add("third_party/licenses/go-modules/$slug/$([IO.Path]::GetFileName($licenseFile))")
    }
    $noticeLines.Add("- $($module.Path) $($module.Version): $($copied -join ', ')")
}

$webviewRoot = Join-Path $RepoRoot 'third_party\go-webview2'
$webviewLicense = Assert-RegularFile (Join-Path $webviewRoot 'LICENSE') 'jchv go-webview2 MIT license'
$sdkLicense = Assert-RegularFile (Join-Path $webviewRoot 'webviewloader\sdk\LICENSE.txt') 'Microsoft WebView2 SDK license'
$standardLicenses = Join-Path $stage 'third_party\licenses'
$null = [IO.Directory]::CreateDirectory($standardLicenses)
[IO.File]::Copy($webviewLicense, (Join-Path $standardLicenses 'go-webview2-LICENSE.txt'), $false)
[IO.File]::Copy($sdkLicense, (Join-Path $standardLicenses 'WebView2SDK-LICENSE.txt'), $false)
$noticeLines.Add('- Microsoft WebView2 SDK loader: third_party/licenses/WebView2SDK-LICENSE.txt')
$noticeLines.Add('- Local go-webview2 fork: third_party/licenses/go-webview2-LICENSE.txt; upstream commit and changes are documented in third_party/go-webview2/UPSTREAM.md.')
$noticePath = Join-Path $stage 'third_party\NOTICE.txt'
[IO.File]::WriteAllLines($noticePath, $noticeLines, [Text.UTF8Encoding]::new($false))

$desktopDestination = Join-Path $stage 'bin\local-probe-desktop.exe'
$mcpDestination = Join-Path $stage 'bin\local-probe-mcp.exe'
$null = [IO.Directory]::CreateDirectory((Split-Path -Parent $desktopDestination))
[IO.File]::Copy($desktopPath, $desktopDestination, $false)
[IO.File]::Copy($mcpPath, $mcpDestination, $false)

Add-Type -AssemblyName System.IO.Compression.FileSystem
$archivePath = Join-Path $packageRoot "$packageName.zip"
[System.IO.Compression.ZipFile]::CreateFromDirectory($stage, $archivePath, [System.IO.Compression.CompressionLevel]::Optimal, $false)
Write-Output "Desktop Preview package directory: $packageRoot"
Write-Output "Desktop Preview source archive: $archivePath"
Write-Output "Allowlisted source files: $($files.Count); Go modules with bundled license notices: $($moduleRecords.Count)"
