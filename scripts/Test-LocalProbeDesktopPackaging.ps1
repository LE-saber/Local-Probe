[CmdletBinding()]
param(
    [string]$RepoRoot = (Split-Path -Parent $PSScriptRoot)
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) { return [IO.Path]::GetFullPath($Path) }

function Assert-File([string]$Path, [string]$Reason) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw $Reason }
}

function Assert-Contains([string]$Path, [string]$Text, [string]$Reason) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw $Reason }
    if (-not (Select-String -LiteralPath $Path -Pattern $Text -SimpleMatch -Quiet)) { throw $Reason }
}

$RepoRoot = Get-FullPath $RepoRoot
$packageScript = Join-Path $RepoRoot 'scripts\Package-LocalProbeDesktop.ps1'
Assert-File $packageScript 'Package-LocalProbeDesktop.ps1 is missing.'
$distRoot = Join-Path $RepoRoot 'dist'
$testOutput = Join-Path $distRoot "desktop-packaging-test-$([Guid]::NewGuid().ToString('N'))"
$testOutput = Get-FullPath $testOutput

if (Test-Path -LiteralPath $testOutput) { throw 'Randomized packaging test output path unexpectedly already exists.' }
& $packageScript -RepoRoot $RepoRoot -OutputDirectory $testOutput

    $packages = @(Get-ChildItem -LiteralPath $testOutput -Directory -Force | Where-Object { $_.Name -like 'Local-Probe-Desktop-Preview-*' })
    if ($packages.Count -ne 1) { throw "Expected one uniquely named package, found $($packages.Count)." }
    $packageRoot = $packages[0].FullName
    $archive = Join-Path $packageRoot ($packages[0].Name + '.zip')
    Assert-File $archive 'Source archive was not created.'
    $stage = Join-Path $packageRoot 'source'
    Assert-File (Join-Path $stage 'bin\local-probe-desktop.exe') 'Desktop executable missing from package directory.'
    Assert-File (Join-Path $stage 'bin\local-probe-mcp.exe') 'MCP executable missing from package directory.'

    $extractRoot = Join-Path $packageRoot 'clean-unpack'
    $null = New-Item -ItemType Directory -Path $extractRoot -ErrorAction Stop
    Expand-Archive -LiteralPath $archive -DestinationPath $extractRoot -ErrorAction Stop

    foreach ($relative in @(
        'bin\local-probe-desktop.exe', 'bin\local-probe-mcp.exe',
        'LICENSE', 'go.mod', 'go.sum', 'cmd\local-probe-desktop\main.go', 'cmd\local-probe-mcp\main.go',
        'internal\desktopweb\assets\index.html', 'internal\desktopweb\assets\app.js', 'internal\desktopweb\assets\icons.js',
        'third_party\NOTICE.txt', 'third_party\licenses\go-webview2-LICENSE.txt',
        'third_party\licenses\WebView2SDK-LICENSE.txt'
    )) { Assert-File (Join-Path $extractRoot $relative) "Clean archive extraction is missing $relative." }

    $desktopSource = Join-Path $RepoRoot 'bin\local-probe-desktop.exe'
    $mcpSource = Join-Path $RepoRoot 'bin\local-probe-mcp.exe'
    if ((Get-FileHash $desktopSource -Algorithm SHA256).Hash -ne (Get-FileHash (Join-Path $extractRoot 'bin\local-probe-desktop.exe') -Algorithm SHA256).Hash) {
        throw 'Packaged desktop executable does not match the current build input.'
    }
    if ((Get-FileHash $mcpSource -Algorithm SHA256).Hash -ne (Get-FileHash (Join-Path $extractRoot 'bin\local-probe-mcp.exe') -Algorithm SHA256).Hash) {
        throw 'Packaged MCP executable does not match the current build input.'
    }

    $unexpectedConfig = @(Get-ChildItem -LiteralPath (Join-Path $extractRoot 'configs') -File -Recurse -Force | Where-Object { $_.Name -notmatch '(?i)\.example\.(?:json|yaml|yml)$' })
    if ($unexpectedConfig.Count -ne 0) { throw "Non-example configuration found in package: $($unexpectedConfig[0].FullName)" }
    $forbidden = @('.runtime', '.secrets', '.tools', '.planning', '.serena', 'logs', 'userroots', 'tmp')
    foreach ($item in Get-ChildItem -LiteralPath $extractRoot -Recurse -Force) {
        $relative = $item.FullName.Substring($extractRoot.Length).TrimStart('\')
        foreach ($fragment in $forbidden) {
            $segment = '(^|[\\/])' + [regex]::Escape($fragment) + '([\\/]|$)'
            if ($relative -match $segment) { throw "Forbidden runtime/user-data path included: $relative" }
        }
        if (-not $item.PSIsContainer -and $item.Name -match '(?i)\.(?:log|jsonl|pem|pfx|p12|key|crt)$') { throw "Sensitive or runtime artifact included: $relative" }
        if (-not $item.PSIsContainer -and $item.Name -match '(?i)\.desktop-(?:events|backup)\.json(?:\.bak)?$') { throw "Desktop user data included: $relative" }
    }
    Assert-Contains (Join-Path $extractRoot 'LICENSE') 'MIT License' 'The selected project license must be included.'
    Assert-Contains (Join-Path $extractRoot 'third_party\NOTICE.txt') 'not a production-ready release' 'Third-party notice must state the preview limitations.'

    $go = Get-Command go -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $go) { throw 'Go is required to build the source from a clean extraction.' }
    $node = Get-Command node -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -eq $node) { throw 'Node.js is required to run the extracted frontend checks.' }
    $buildDirectory = Join-Path $packageRoot 'clean-build'
    $null = New-Item -ItemType Directory -Path $buildDirectory -ErrorAction Stop
    Push-Location $extractRoot
    try {
        & $go.Source build -trimpath -o (Join-Path $buildDirectory 'local-probe-desktop.exe') ./cmd/local-probe-desktop
        if ($LASTEXITCODE -ne 0) { throw 'Desktop source build from clean extraction failed.' }
        & $go.Source build -trimpath -o (Join-Path $buildDirectory 'local-probe-mcp.exe') ./cmd/local-probe-mcp
        if ($LASTEXITCODE -ne 0) { throw 'MCP source build from clean extraction failed.' }
        & $node.Source (Join-Path $extractRoot 'scripts\Test-LocalProbeDesktopFrontend.mjs')
        if ($LASTEXITCODE -ne 0) { throw 'Frontend contract checks from clean extraction failed.' }
    } finally {
        Pop-Location
    }

Write-Output 'PASS: unique package creation, clean archive extraction, allowlist exclusions, current executable hashes, source builds, and frontend contract checks.'
Write-Output 'No MCP process or HTTP server was launched by this packaging test.'
