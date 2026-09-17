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
$previewDoc = Join-Path $RepoRoot 'docs\PREVIEW.zh-CN.md'

foreach ($path in @($buildScript, $startScript, $previewDoc)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "required Preview packaging file is missing: $path"
    }
}

$buildText = Get-Content -Raw -LiteralPath $buildScript
$startText = Get-Content -Raw -LiteralPath $startScript
$docText = Get-Content -Raw -LiteralPath $previewDoc

# The packaging contract is intentionally source-level: this test never invokes
# Go, cloudflared, MCP, or a process with a credential-bearing argument.
Assert-Contains $buildText '\$McpOutputPath' 'Build script must expose a stable MCP output path.'
Assert-Contains $buildText "bin\\local-probe-mcp\.exe" 'Build script must default the MCP origin to bin\local-probe-mcp.exe.'
Assert-Contains $buildText "\./cmd/local-probe-mcp" 'Build script must compile cmd/local-probe-mcp.'
Assert-Contains $buildText 'McpOutputPath must be a \.exe path inside RepoRoot' 'MCP output must remain repository-local.'
Assert-Contains $buildText 'OutputPath and McpOutputPath must be different files' 'Preview and MCP outputs must not overwrite each other.'
Assert-Contains $startText 'Build-LocalProbePreview\.ps1' 'Preview start script must use the shared packaging script.'
Assert-Contains $docText '\.secrets[\\/]cloudflared-tunnel-token\.txt' 'Preview documentation must name the external token file.'
Assert-Contains $docText 'The token value is never displayed, logged' 'Preview documentation must state that token values are not displayed or logged.'

Write-Output '[PASS] Preview packaging source contract is present.'
Write-Output '[PASS] Stable bin\local-probe-mcp.exe artifact is required by the Preview build.'
Write-Output '[PASS] External token location and no-secret-display rule are documented.'
