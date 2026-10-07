[CmdletBinding()]
param(
    [string]$RepoRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Assert-Contains([string]$Text, [string]$Pattern, [string]$FailureMessage) {
    if ($Text -notmatch $Pattern) {
        throw $FailureMessage
    }
}

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = Split-Path -Parent $PSScriptRoot
}
$RepoRoot = Get-FullPath $RepoRoot
$buildScript = Join-Path $RepoRoot 'scripts\Build-LocalProbePreview.ps1'
$startScript = Join-Path $RepoRoot 'scripts\Start-LocalProbePreview.ps1'
$setupScript = Join-Path $RepoRoot 'scripts\Setup-LocalProbePreview.ps1'
$environmentScript = Join-Path $RepoRoot 'scripts\Test-LocalProbeEnvironment.ps1'
$packageScript = Join-Path $RepoRoot 'scripts\Package-LocalProbePreview.ps1'
$previewDoc = Join-Path $RepoRoot 'docs\PREVIEW.zh-CN.md'
$tunnelExample = Join-Path $RepoRoot 'configs\cloudflare-tunnel.example.json'
$accessExample = Join-Path $RepoRoot 'configs\cloudflare-access.example.json'
$ignoreFile = Join-Path $RepoRoot '.gitignore'

foreach ($path in @($buildScript, $startScript, $setupScript, $environmentScript, $packageScript, $previewDoc, $tunnelExample, $accessExample, $ignoreFile)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "required Preview packaging file is missing: $path"
    }
}

$buildText = Get-Content -Raw -LiteralPath $buildScript
$startText = Get-Content -Raw -LiteralPath $startScript
$setupText = Get-Content -Raw -LiteralPath $setupScript
$environmentText = Get-Content -Raw -LiteralPath $environmentScript
$packageText = Get-Content -Raw -LiteralPath $packageScript
$docText = Get-Content -Raw -LiteralPath $previewDoc
$tunnelExampleText = Get-Content -Raw -LiteralPath $tunnelExample
$ignoreText = Get-Content -Raw -LiteralPath $ignoreFile

# The packaging contract is intentionally source-level: this test never invokes
# Go, cloudflared, MCP, or a process with a credential-bearing argument.
Assert-Contains $buildText '\$McpOutputPath' 'Build script must expose a stable MCP output path.'
Assert-Contains $buildText "bin\\local-probe-mcp\.exe" 'Build script must default the MCP origin to bin\local-probe-mcp.exe.'
Assert-Contains $buildText "\./cmd/local-probe-mcp" 'Build script must compile cmd/local-probe-mcp.'
Assert-Contains $buildText 'McpOutputPath must be a \.exe path inside RepoRoot' 'MCP output must remain repository-local.'
Assert-Contains $buildText 'OutputPath and McpOutputPath must be different files' 'Preview and MCP outputs must not overwrite each other.'
Assert-Contains $startText 'Build-LocalProbePreview\.ps1' 'Preview start script must use the shared packaging script.'
Assert-Contains $startText "ValidateSet\('cloudflare_named', 'openai_runtime'\)" 'Preview start script must constrain the supported transport selector.'
Assert-Contains $docText '\.secrets[\\/]cloudflared-tunnel-token\.txt' 'Preview documentation must name the external token file.'
if (($docText -notmatch 'The token value is never displayed, logged') -and ($docText -notmatch 'Preview 不显示、记录或导出 token')) {
    throw 'Preview documentation must state that token values are not displayed or logged.'
}
Assert-Contains $setupText '\.tools[\\/]cloudflared\.exe' 'Preview setup must use the repo-local ignored cloudflared path.'
Assert-Contains $setupText 'CopyCloudflared' 'Preview setup must expose an explicit audited copy option.'
Assert-Contains $setupText 'No network download' 'Preview setup must not silently download a tunnel binary.'
Assert-Contains $environmentText 'network_download_attempted = \$false' 'Environment probe must record that it never downloads dependencies.'
Assert-Contains $packageText 'ZipFile\]::CreateFromDirectory' 'Preview package must create a ZIP from a bounded staging directory.'
Assert-Contains $packageText 'local-probe-preview\.exe' 'Preview package must include the GUI executable.'
Assert-Contains $packageText 'local-probe-mcp\.exe' 'Preview package must include the MCP origin executable.'
Assert-Contains $packageText "'--cached'" 'Preview package must enumerate the reviewed git index only.'
if ($packageText -match "'--others'") {
    throw 'Preview package must not absorb arbitrary untracked files.'
}
Assert-Contains $ignoreText '/\.tools/' 'Repo-local cloudflared tools must be ignored by git.'
if ($tunnelExampleText -match '(?i)(?:[A-Z]:[\\/]+[^\\/]+[\\/]+download[\\/]+|[A-Z]:[\\/]+Users[\\/]+)') {
    throw 'Tunnel example must not contain a developer-machine absolute path.'
}

Write-Output '[PASS] Preview packaging source contract is present.'
Write-Output '[PASS] Stable bin\local-probe-mcp.exe artifact is required by the Preview build.'
Write-Output '[PASS] External token location and no-secret-display rule are documented.'
Write-Output '[PASS] Setup, environment probe, package ZIP, and repo-local cloudflared contracts are present.'
