[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Extract only the pure predicate from the production script. Do not execute
# acceptance, change credentials or create fixture files for this unit test.
$scriptPath = Join-Path $PSScriptRoot 'acceptance.ps1'
$parseTokens = $null
$parseErrors = $null
$scriptAst = [Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$parseTokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Acceptance script failed to parse.' }
$definitions = @($scriptAst.FindAll({param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
    $node.Name -eq 'Test-TrackedCredentialPath'
}, $true))
if ($definitions.Count -ne 1) { throw 'Expected one production credential path predicate.' }
. ([ScriptBlock]::Create($definitions[0].Extent.Text))
$cases = @(
    @{ Path = 'third_party/go-webview2/pkg/edge/COREWEBVIEW2_KEY_EVENT_KIND.go'; Denied = $false },
    @{ Path = 'third_party/go-webview2/pkg/edge/COREWEBVIEW2_PHYSICAL_KEY_STATUS.go'; Denied = $false },
    @{ Path = 'THIRD_PARTY/GO-WEBVIEW2/PKG/EDGE/COREWEBVIEW2_KEY_EVENT_KIND.GO'; Denied = $false },
    @{ Path = 'internal/readcore/core.go'; Denied = $false },
    @{ Path = '.runtime/local-probe.json'; Denied = $true },
    @{ Path = '.runtime/third_party/go-webview2/pkg/edge/COREWEBVIEW2_KEY_EVENT_KIND.go'; Denied = $true },
    @{ Path = 'third_party/go-webview2/pkg/edge/private-key.pem'; Denied = $true },
    @{ Path = 'third_party/go-webview2/pkg/edge/COREWEBVIEW2_KEY_EVENT_KIND.go.key'; Denied = $true },
    @{ Path = 'internal/COREWEBVIEW2_KEY_EVENT_KIND.go'; Denied = $true },
    @{ Path = 'configs/access-token.json'; Denied = $true },
    @{ Path = 'docs/client.secret.txt'; Denied = $true }
)
foreach ($case in $cases) {
    $actual = Test-TrackedCredentialPath $case.Path
    if ($actual -isnot [bool] -or $actual -ne $case.Denied) {
        throw ('Credential path classification failed: ' + $case.Path)
    }
}
Write-Output ('PASS: ' + $cases.Count + ' production credential path classifications; keyboard enums only, no broad source exemption.')
