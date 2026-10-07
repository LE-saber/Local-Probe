[CmdletBinding(SupportsShouldProcess = $true, ConfirmImpact = 'Low')]
param(
    [switch]$Quick,
    [switch]$Json
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# R9 acceptance is intentionally local-only.  The repository is fixed by the
# script location so callers cannot redirect the checks to another tree.
$RepoRoot = [IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$MaxTrackedEntries = 100000
$MaxStatusEntries = 100000
$MaxGitLineLength = 8192
$MaxFixtureBytes = 16MB
$MaxJsonBytes = 262144

$checks = [System.Collections.Generic.List[object]]::new()
$startedAtUtc = [DateTime]::UtcNow.ToString('o')
$repositoryVerified = $false
$gitPath = $null
$goPath = $null
$gitBranch = ''
$gitHead = ''
$dirtyEntries = $null
$goVersion = ''
$environmentBackup = @{}
$environmentRestoreFailed = $false
$exitCode = 0

function Get-FullPath([string]$Path) {
    return [IO.Path]::GetFullPath($Path)
}

function Test-TrackedCredentialPath([string]$NormalizedPath) {
    # These exact upstream Go declarations describe keyboard events, not keys
    # used for authentication. Do not exempt a directory or all *.go files.
    if ($NormalizedPath -ieq 'third_party/go-webview2/pkg/edge/COREWEBVIEW2_KEY_EVENT_KIND.go' -or
        $NormalizedPath -ieq 'third_party/go-webview2/pkg/edge/COREWEBVIEW2_PHYSICAL_KEY_STATUS.go') {
        return $false
    }
    $leaf = [IO.Path]::GetFileName($NormalizedPath)
    return ($NormalizedPath -match '(?i)(^|/)\.runtime(/|$)' -or
        $leaf -match '(?i)(^|[-_.])(key|token|secret)([-_.]|$)' -or
        $leaf -match '(?i)\.(key|token|secret)(\.[^/]*)?$')
}

function Add-Check(
    [System.Collections.Generic.List[object]]$Checks,
    [string]$Name,
    [string]$Status,
    [bool]$Hard,
    [int64]$DurationMs,
    [string]$Detail,
    [string]$Command = ''
) {
    $record = [PSCustomObject][ordered]@{
        name = $Name
        status = $Status
        hard = $Hard
        duration_ms = $DurationMs
        detail = $Detail
    }
    if (-not [string]::IsNullOrWhiteSpace($Command)) {
        $record | Add-Member -NotePropertyName command -NotePropertyValue $Command
    }
    $null = $Checks.Add($record)
    return $record
}

function Add-Skip(
    [System.Collections.Generic.List[object]]$Checks,
    [string]$Name,
    [string]$Command,
    [string]$Detail,
    [bool]$Hard = $false
) {
    return Add-Check $Checks $Name 'SKIP' $Hard 0 $Detail $Command
}

function Resolve-Executable([string]$Name) {
    $command = Get-Command -Name $Name -CommandType Application -ErrorAction SilentlyContinue |
        Select-Object -First 1
    if ($null -eq $command) {
        return $null
    }
    if ($command.PSObject.Properties.Name -contains 'Path' -and $command.Path) {
        return [string]$command.Path
    }
    if ($command.PSObject.Properties.Name -contains 'Source' -and $command.Source) {
        return [string]$command.Source
    }
    return $null
}

function Test-PathInside([string]$Child, [string]$Parent) {
    $childFull = Get-FullPath $Child
    $parentFull = (Get-FullPath $Parent).TrimEnd([char]'\', [char]'/')
    $parentPrefix = $parentFull + [IO.Path]::DirectorySeparatorChar
    return $childFull.Equals($parentFull, [StringComparison]::OrdinalIgnoreCase) -or
        $childFull.StartsWith($parentPrefix, [StringComparison]::OrdinalIgnoreCase)
}

function Test-RepositoryAncestors([string]$Root) {
    $current = Get-FullPath $Root
    $rootItem = Get-Item -LiteralPath $current -Force
    if (-not $rootItem.PSIsContainer -or ($rootItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        return $false
    }
    while ($true) {
        $directory = [IO.DirectoryInfo]$current
        if ($null -eq $directory.Parent) {
            break
        }
        $parent = $directory.Parent.FullName
        $parentItem = Get-Item -LiteralPath $parent -Force
        if (-not $parentItem.PSIsContainer -or ($parentItem.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            return $false
        }
        if ($parent.Equals($current, [StringComparison]::OrdinalIgnoreCase)) {
            break
        }
        $current = $parent
    }
    return $true
}

function Invoke-InRepository(
    [string]$Executable,
    [string[]]$Arguments,
    [scriptblock]$Action,
    [object]$State = $null
) {
    $pushed = $false
    try {
        Push-Location -LiteralPath $RepoRoot
        $pushed = $true
        return & $Action $Executable $Arguments $State
    } finally {
        if ($pushed) {
            Pop-Location
        }
    }
}

function Invoke-CommandCheck(
    [System.Collections.Generic.List[object]]$Checks,
    [string]$Name,
    [string]$Command,
    [string]$Executable,
    [string[]]$Arguments,
    [bool]$Hard = $true
) {
    $watch = [Diagnostics.Stopwatch]::StartNew()
    if ([string]::IsNullOrWhiteSpace($Executable)) {
        $watch.Stop()
        return Add-Check $Checks $Name 'FAIL' $Hard $watch.ElapsedMilliseconds 'required executable is unavailable' $Command
    }
    try {
        # All stdout/stderr is discarded.  No logs or temporary files are created.
        $code = [int](Invoke-InRepository $Executable $Arguments {
            param($file, $argumentsArray)
            & $file @argumentsArray *> $null
            return [int]$LASTEXITCODE
        })
        $watch.Stop()
        if ($code -eq 0) {
            return Add-Check $Checks $Name 'PASS' $Hard $watch.ElapsedMilliseconds 'completed; command output was suppressed' $Command
        }
        return Add-Check $Checks $Name 'FAIL' $Hard $watch.ElapsedMilliseconds ("exited with code {0}; command output was suppressed" -f $code) $Command
    } catch {
        $watch.Stop()
        return Add-Check $Checks $Name 'FAIL' $Hard $watch.ElapsedMilliseconds 'could not execute; command output was suppressed' $Command
    }
}

function Get-GitText([string]$Executable, [string[]]$Arguments) {
    if ([string]::IsNullOrWhiteSpace($Executable)) {
        return [PSCustomObject]@{ output = ''; exit_code = -1 }
    }
    try {
        $text = Invoke-InRepository $Executable $Arguments {
            param($file, $argumentsArray)
            $captured = (& $file @argumentsArray 2>$null | Out-String).Trim()
            $nativeExitCode = [int]$LASTEXITCODE
            return [PSCustomObject]@{ output = $captured; exit_code = $nativeExitCode }
        }
        return [PSCustomObject]@{ output = [string]$text.output; exit_code = [int]$text.exit_code }
    } catch {
        return [PSCustomObject]@{ output = ''; exit_code = -1 }
    }
}

function Get-GitBoundedLines(
    [string]$Executable,
    [string[]]$Arguments,
    [int]$Maximum,
    [bool]$KeepLines
) {
    $lines = [System.Collections.Generic.List[string]]::new()
    $state = [PSCustomObject]@{
        lines = $lines
        count = [int64]0
        overflow = $false
        invalid = $false
        exit_code = -1
        keep_lines = $KeepLines
    }
    if (-not [string]::IsNullOrWhiteSpace($Executable)) {
        try {
            $state = Invoke-InRepository $Executable $Arguments {
                param($file, $argumentsArray, $stateObject)
                & $file @argumentsArray 2>$null | ForEach-Object {
                    $lineText = [string]$_
                    $stateObject.count++
                    if ($stateObject.count -gt $Maximum) {
                        $stateObject.overflow = $true
                    } else {
                        if ($lineText.Length -gt $script:MaxGitLineLength -or
                            $lineText -match '[\x00-\x1F\x7F]' -or
                            $lineText -match '^".*"$' -or
                            $lineText -match '\\(?:[0-7]{3}|[abtnvfr])') {
                            $stateObject.invalid = $true
                        }
                        if ($stateObject.keep_lines) {
                            $null = $stateObject.lines.Add($lineText)
                        }
                    }
                }
                $stateObject.exit_code = [int]$LASTEXITCODE
                return $stateObject
            } $state
        } catch {
            $state.exit_code = -1
        }
    }
    return $state
}

function Backup-EnvironmentOnce([string]$Name) {
    if (-not $environmentBackup.ContainsKey($Name)) {
        $environmentBackup[$Name] = [PSCustomObject]@{
            present = Test-Path -LiteralPath ('Env:' + $Name)
            value = [Environment]::GetEnvironmentVariable($Name, 'Process')
        }
    }
}

function Restore-Environment {
    $restoreErrors = [System.Collections.Generic.List[string]]::new()
    foreach ($name in @($environmentBackup.Keys)) {
        $saved = $environmentBackup[$name]
        try {
            if ($saved.present) {
                Set-Item -LiteralPath ('Env:' + $name) -Value $saved.value
            } else {
                Remove-Item -LiteralPath ('Env:' + $name) -ErrorAction Stop
            }
        } catch {
            $null = $restoreErrors.Add($name)
        }
    }
    if ($restoreErrors.Count -gt 0) {
        throw 'one or more temporary environment overrides could not be restored'
    }
}

function New-Result(
    [bool]$IsQuick,
    [bool]$RepositoryVerified,
    [string]$StartedUtc,
    [System.Collections.Generic.List[object]]$Checks
) {
    $all = @($Checks.ToArray())
    $hardFailures = @($all | Where-Object { $_.hard -and $_.status -eq 'FAIL' }).Count
    $hardSkips = @($all | Where-Object { $_.hard -and $_.status -eq 'SKIP' }).Count
    $safeBranch = if ($gitBranch -match '^[A-Za-z0-9._/()-]{1,128}$') { $gitBranch } else { '(redacted)' }
    $safeHead = if ($gitHead -match '^[0-9a-fA-F]{4,64}$') { $gitHead } else { '(redacted)' }
    $safeGoVersion = if ($goVersion -match '^go version go[0-9A-Za-z.+-]+(?: [A-Za-z0-9._/-]+){0,2}$') { $goVersion } else { '(redacted)' }
    return [PSCustomObject][ordered]@{
        schema_version = 'r9.windows_acceptance.v2'
        started_at_utc = $StartedUtc
        finished_at_utc = [DateTime]::UtcNow.ToString('o')
        repository_name = 'Local-Probe'
        repository_verified = $RepositoryVerified
        quick = $IsQuick
        local_automated_checks_passed = ($RepositoryVerified -and $hardFailures -eq 0 -and $hardSkips -eq 0)
        release_ready = $false
        information = [PSCustomObject][ordered]@{
            worktree = [PSCustomObject][ordered]@{
                branch = $safeBranch
                head = $safeHead
                dirty_entries = $dirtyEntries
            }
            go = [PSCustomObject][ordered]@{
                version = $safeGoVersion
            }
        }
        summary = [PSCustomObject][ordered]@{
            pass = @($all | Where-Object status -eq 'PASS').Count
            fail = @($all | Where-Object status -eq 'FAIL').Count
            skip = @($all | Where-Object status -eq 'SKIP').Count
            hard_fail = $hardFailures
            hard_skip = $hardSkips
        }
        checks = $all
        non_automated_gates = @(
            'real signing',
            'Tunnel connectivity',
            'ChatGPT/web flow',
            'install/uninstall',
            'dual-connection soak',
            '1m fixture'
        )
    }
}

function Write-HumanSummary([object]$Result) {
    Write-Output ('Local-Probe R9 Windows acceptance (local-only; release_ready=false; quick={0})' -f $Result.quick)
    foreach ($check in @($Result.checks)) {
        Write-Output ('[{0}] {1}: {2}' -f $check.status, $check.name, $check.detail)
    }
    Write-Output ('Summary: PASS={0} FAIL={1} SKIP={2} HARD_FAIL={3} HARD_SKIP={4}' -f `
        $Result.summary.pass, $Result.summary.fail, $Result.summary.skip, $Result.summary.hard_fail, $Result.summary.hard_skip)
}

function Get-BoundedJson([object]$Value) {
    $jsonText = $Value | ConvertTo-Json -Depth 10 -Compress
    if ([Text.Encoding]::UTF8.GetByteCount($jsonText) -gt $MaxJsonBytes) {
        throw 'acceptance JSON exceeded its bounded stdout size'
    }
    return $jsonText
}

$result = $null

if ($WhatIfPreference) {
    Add-Skip $checks 'repository boundary' 'filesystem ancestor check' '-WhatIf requested; no repository path was inspected' $true | Out-Null
    Add-Skip $checks 'git worktree root' 'git rev-parse --show-toplevel' '-WhatIf requested; no git command was run' $true | Out-Null
    Add-Skip $checks 'worktree status' 'git status --porcelain=v1' '-WhatIf requested; no git status was read' $true | Out-Null
    Add-Skip $checks 'tracked safety policy' 'git ls-files --cached' '-WhatIf requested; tracked names and fixture sizes were not inspected' $true | Out-Null
    Add-Skip $checks 'Go version information' 'go version' '-WhatIf requested; no Go command was run' $true | Out-Null
    Add-Skip $checks 'go test ./...' 'go test ./...' '-WhatIf requested; no test was run' $true | Out-Null
    Add-Skip $checks 'go test -race ./...' 'go test -race ./...' '-WhatIf requested; no race test was run' $true | Out-Null
    Add-Skip $checks 'go vet ./...' 'go vet ./...' '-WhatIf requested; no vet run was performed' $true | Out-Null
    Add-Skip $checks 'git diff --check' 'git diff --check' '-WhatIf requested; no diff check was run' $true | Out-Null
    Add-Skip $checks 'cross build' 'GOOS=windows GOARCH=amd64 go build ./...' '-WhatIf requested; no cross build was run' $true | Out-Null
    Add-Skip $checks 'real signing' '' '-WhatIf run does not perform release signing' | Out-Null
    Add-Skip $checks 'Tunnel connectivity' '' '-WhatIf run does not contact a Tunnel' | Out-Null
    Add-Skip $checks 'ChatGPT/web flow' '' '-WhatIf run does not open a browser or call a web service' | Out-Null
    Add-Skip $checks 'install/uninstall' '' '-WhatIf run does not install or uninstall anything' | Out-Null
    Add-Skip $checks 'dual-connection soak' '' '-WhatIf run does not start a soak test' | Out-Null
    Add-Skip $checks '1m fixture' '' '-WhatIf run does not create large fixtures' | Out-Null
    $result = New-Result ([bool]$Quick) $false $startedAtUtc $checks
    if ($Json) {
        Write-Output (Get-BoundedJson $result)
    } else {
        Write-HumanSummary $result
    }
    exit 0
}

if (-not $PSCmdlet.ShouldProcess('Local-Probe repository', 'run local Windows acceptance checks')) {
    Add-Skip $checks 'acceptance execution' '' 'operation was declined by ShouldProcess' | Out-Null
    $result = New-Result ([bool]$Quick) $false $startedAtUtc $checks
    if ($Json) {
        Write-Output (Get-BoundedJson $result)
    } else {
        Write-HumanSummary $result
    }
    exit 0
}

try {
    if (-not (Test-Path -LiteralPath $RepoRoot -PathType Container)) {
        Add-Check $checks 'repository boundary' 'FAIL' $true 0 'fixed Local-Probe repository directory is unavailable' | Out-Null
    } else {
        $boundaryWatch = [Diagnostics.Stopwatch]::StartNew()
        try {
            if (Test-RepositoryAncestors $RepoRoot) {
                Add-Check $checks 'repository boundary' 'PASS' $true $boundaryWatch.ElapsedMilliseconds 'repository and each filesystem ancestor passed the reparse-point check' | Out-Null
            } else {
                Add-Check $checks 'repository boundary' 'FAIL' $true $boundaryWatch.ElapsedMilliseconds 'repository or one filesystem ancestor is a reparse point' | Out-Null
            }
        } catch {
            Add-Check $checks 'repository boundary' 'FAIL' $true $boundaryWatch.ElapsedMilliseconds 'repository ancestors could not be checked safely' | Out-Null
        }
        $boundaryWatch.Stop()
    }

    $goPath = Resolve-Executable 'go.exe'
    if ([string]::IsNullOrWhiteSpace($goPath)) {
        $goPath = Resolve-Executable 'go'
    }
    $gitPath = Resolve-Executable 'git.exe'
    if ([string]::IsNullOrWhiteSpace($gitPath)) {
        $gitPath = Resolve-Executable 'git'
    }

    $gitTop = Get-GitText $gitPath @('rev-parse', '--show-toplevel')
    $gitTopMatches = $false
    if ($gitTop.exit_code -eq 0 -and -not [string]::IsNullOrWhiteSpace($gitTop.output)) {
        try {
            $gitTopMatches = (Get-FullPath $gitTop.output).Equals($RepoRoot, [StringComparison]::OrdinalIgnoreCase)
        } catch {
            $gitTopMatches = $false
        }
    }
    if ($gitTopMatches) {
        $repositoryVerified = @($checks | Where-Object { $_.name -eq 'repository boundary' -and $_.status -eq 'PASS' }).Count -eq 1
        Add-Check $checks 'git worktree root' 'PASS' $true 0 'git worktree root matches the fixed Local-Probe repository' 'git rev-parse --show-toplevel' | Out-Null
    } else {
        Add-Check $checks 'git worktree root' 'FAIL' $true 0 'git worktree root could not be verified against the fixed repository' 'git rev-parse --show-toplevel' | Out-Null
    }

    if ($repositoryVerified) {
        $branchText = Get-GitText $gitPath @('symbolic-ref', '--short', '-q', 'HEAD')
        $headText = Get-GitText $gitPath @('rev-parse', '--short=12', 'HEAD')
        if ($branchText.exit_code -eq 0) {
            $gitBranch = $branchText.output
        } else {
            $gitBranch = '(detached HEAD)'
        }
        if ($headText.exit_code -eq 0) {
            $gitHead = $headText.output
        } else {
            $gitHead = '(unavailable)'
        }

        $statusInfo = Get-GitBoundedLines $gitPath @('status', '--porcelain=v1', '--untracked-files=all') $MaxStatusEntries $false
        $dirtyEntries = $statusInfo.count
        if ($statusInfo.exit_code -ne 0 -or $statusInfo.overflow -or $statusInfo.invalid) {
            Add-Check $checks 'worktree status' 'FAIL' $true 0 'git status exceeded the bounded entry/control-character policy' 'git status --porcelain=v1' | Out-Null
        } else {
            Add-Check $checks 'worktree status' 'PASS' $true 0 ("worktree status recorded; entries={0}" -f $statusInfo.count) 'git status --porcelain=v1' | Out-Null
        }
    } else {
        Add-Skip $checks 'worktree status' 'git status --porcelain=v1' 'repository verification failed; status was not read' $true | Out-Null
    }

    if ($repositoryVerified) {
        $trackedInfo = Get-GitBoundedLines $gitPath @('-c', 'core.quotePath=false', 'ls-files', '--cached', '--full-name') $MaxTrackedEntries $true
        $secretPathCount = 0
        $largeFixtureCount = 0
        $fixtureStatFailureCount = 0
        if ($trackedInfo.exit_code -ne 0 -or $trackedInfo.overflow -or $trackedInfo.invalid) {
            Add-Check $checks 'tracked safety policy' 'FAIL' $true 0 'tracked path enumeration exceeded the bounded name/control-character policy; no file contents were read' 'git ls-files --cached' | Out-Null
        } else {
            foreach ($trackedPath in @($trackedInfo.lines)) {
                $normalized = $trackedPath.Replace('\', '/')
                $leaf = [IO.Path]::GetFileName($normalized)
                if (Test-TrackedCredentialPath $normalized) {
                    $secretPathCount++
                }
                $isFixture = $normalized -match '(?i)(^|/)(testdata|fixtures?|manual-test-targets|benchmark)(/|$)' -or
                    $leaf -match '(?i)(fixture|large)'
                if ($isFixture) {
                    try {
                        $fullPath = Get-FullPath (Join-Path $RepoRoot ($normalized -replace '/', '\'))
                        if (-not (Test-PathInside $fullPath $RepoRoot) -or -not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
                            $fixtureStatFailureCount++
                            continue
                        }
                        $item = Get-Item -LiteralPath $fullPath -Force
                        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                            $fixtureStatFailureCount++
                        } elseif ([int64]$item.Length -gt $MaxFixtureBytes) {
                            $largeFixtureCount++
                        }
                    } catch {
                        $fixtureStatFailureCount++
                    }
                }
            }
            if ($fixtureStatFailureCount -gt 0) {
                Add-Check $checks 'tracked safety policy' 'FAIL' $true 0 'tracked fixture metadata could not be safely inspected; file contents were not read' 'git ls-files --cached' | Out-Null
            } elseif ($secretPathCount -gt 0 -or $largeFixtureCount -gt 0) {
                Add-Check $checks 'tracked safety policy' 'FAIL' $true 0 ("tracked policy rejected {0} secret-like path(s) and {1} oversized fixture(s); only names and fixture sizes were inspected" -f $secretPathCount, $largeFixtureCount) 'git ls-files --cached' | Out-Null
            } else {
                Add-Check $checks 'tracked safety policy' 'PASS' $true 0 ("tracked entries={0}; no .runtime/key/token/secret path or oversized fixture was found; only names and fixture sizes were inspected" -f $trackedInfo.count) 'git ls-files --cached' | Out-Null
            }
        }
    } else {
        Add-Skip $checks 'tracked safety policy' 'git ls-files --cached' 'repository verification failed; tracked names were not read' $true | Out-Null
    }

    $goInfoWatch = [Diagnostics.Stopwatch]::StartNew()
    if (-not $repositoryVerified) {
        Add-Skip $checks 'Go version information' 'go version' 'repository verification failed; Go was not invoked' $true | Out-Null
    } elseif ([string]::IsNullOrWhiteSpace($goPath)) {
        Add-Check $checks 'Go version information' 'FAIL' $true $goInfoWatch.ElapsedMilliseconds 'go executable is unavailable' 'go version' | Out-Null
    } else {
        try {
            $goInfo = Invoke-InRepository $goPath @('version') {
                param($file, $argumentsArray)
                $captured = (& $file @argumentsArray 2>$null | Out-String).Trim()
                $nativeExitCode = [int]$LASTEXITCODE
                return [PSCustomObject]@{ output = $captured; exit_code = $nativeExitCode }
            }
            $goExit = [int]$goInfo.exit_code
            $goVersion = [string]$goInfo.output
            if ($goExit -eq 0 -and $goVersion -match '^go version go[0-9A-Za-z.+-]+(?: [A-Za-z0-9._/-]+){0,2}$') {
                Add-Check $checks 'Go version information' 'PASS' $true $goInfoWatch.ElapsedMilliseconds 'go version recorded; output was bounded and sanitized' 'go version' | Out-Null
            } else {
                Add-Check $checks 'Go version information' 'FAIL' $true $goInfoWatch.ElapsedMilliseconds 'go version could not be safely recorded' 'go version' | Out-Null
            }
        } catch {
            Add-Check $checks 'Go version information' 'FAIL' $true $goInfoWatch.ElapsedMilliseconds 'go version could not be safely recorded' 'go version' | Out-Null
        }
    }
    $goInfoWatch.Stop()

    if (-not $repositoryVerified) {
        Add-Skip $checks 'go test ./...' 'go test ./...' 'repository verification failed; test was not invoked' $true | Out-Null
    } else {
        Invoke-CommandCheck $checks 'go test ./...' 'go test ./...' $goPath @('test', './...') | Out-Null
    }
    if ($Quick) {
        Add-Skip $checks 'go test -race ./...' 'go test -race ./...' '-Quick requested; race check was not run' $true | Out-Null
    } elseif (-not $repositoryVerified) {
        Add-Skip $checks 'go test -race ./...' 'go test -race ./...' 'repository verification failed; race test was not invoked' $true | Out-Null
    } else {
        Invoke-CommandCheck $checks 'go test -race ./...' 'go test -race ./...' $goPath @('test', '-race', './...') | Out-Null
    }
    if (-not $repositoryVerified) {
        Add-Skip $checks 'go vet ./...' 'go vet ./...' 'repository verification failed; vet was not invoked' $true | Out-Null
    } else {
        Invoke-CommandCheck $checks 'go vet ./...' 'go vet ./...' $goPath @('vet', './...') | Out-Null
    }
    if (-not $repositoryVerified) {
        Add-Skip $checks 'git diff --check' 'git diff --check' 'repository verification failed; diff check was not invoked' $true | Out-Null
    } else {
        Invoke-CommandCheck $checks 'git diff --check' 'git diff --check' $gitPath @('diff', '--check') | Out-Null
    }
    if ($Quick) {
        Add-Skip $checks 'cross build' 'GOOS=windows GOARCH=amd64 go build ./...' '-Quick requested; Windows amd64 cross build was not run' $true | Out-Null
    } elseif (-not $repositoryVerified) {
        Add-Skip $checks 'cross build' 'GOOS=windows GOARCH=amd64 go build ./...' 'repository verification failed; cross build was not invoked' $true | Out-Null
    } elseif ([string]::IsNullOrWhiteSpace($goPath)) {
        Add-Check $checks 'cross build' 'FAIL' $true 0 'go executable is unavailable' 'GOOS=windows GOARCH=amd64 go build ./...' | Out-Null
    } else {
        $crossCheck = $null
        try {
            Backup-EnvironmentOnce 'GOOS'
            Backup-EnvironmentOnce 'GOARCH'
            Set-Item -LiteralPath 'Env:GOOS' -Value 'windows'
            Set-Item -LiteralPath 'Env:GOARCH' -Value 'amd64'
            $crossCheck = Invoke-CommandCheck $checks 'cross build' 'GOOS=windows GOARCH=amd64 go build ./...' $goPath @('build', './...')
        } catch {
            if ($null -eq $crossCheck) {
                Add-Check $checks 'cross build' 'FAIL' $true 0 'cross-build environment could not be configured' 'GOOS=windows GOARCH=amd64 go build ./...' | Out-Null
            }
        } finally {
            try {
                Restore-Environment
            } catch {
                $environmentRestoreFailed = $true
                Add-Check $checks 'environment restore' 'FAIL' $true 0 'temporary GOOS/GOARCH values could not be restored; values were not displayed' | Out-Null
            }
            $environmentBackup.Clear()
        }
    }

    Add-Skip $checks 'real signing' '' 'manual release-signing gate is intentionally outside this local script' | Out-Null
    Add-Skip $checks 'Tunnel connectivity' '' 'real Tunnel/network gate is intentionally skipped' | Out-Null
    Add-Skip $checks 'ChatGPT/web flow' '' 'browser and web-account gate is intentionally skipped' | Out-Null
    Add-Skip $checks 'install/uninstall' '' 'installer lifecycle gate is intentionally skipped' | Out-Null
    Add-Skip $checks 'dual-connection soak' '' 'long-running two-connection gate is intentionally skipped' | Out-Null
    Add-Skip $checks '1m fixture' '' 'million-file fixture is intentionally skipped to protect the local disk' | Out-Null

    $result = New-Result ([bool]$Quick) $repositoryVerified $startedAtUtc $checks
    if ($Json) {
        Write-Output (Get-BoundedJson $result)
    } else {
        Write-HumanSummary $result
    }
    $hardFailures = @($checks | Where-Object { $_.hard -and $_.status -eq 'FAIL' }).Count
    if ($hardFailures -gt 0 -or $environmentRestoreFailed) {
        $exitCode = 1
    }
} catch {
    $exitCode = 1
    Add-Check $checks 'acceptance script' 'FAIL' $true 0 'input validation or local orchestration failed; details are intentionally suppressed' | Out-Null
    $result = New-Result ([bool]$Quick) $repositoryVerified $startedAtUtc $checks
    if ($Json) {
        try {
            Write-Output (Get-BoundedJson $result)
        } catch {
            Write-Output '{"schema_version":"r9.windows_acceptance.v2","repository_name":"Local-Probe","repository_verified":false,"release_ready":false,"checks":[{"name":"acceptance script","status":"FAIL","hard":true,"detail":"bounded JSON serialization failed"}]}'
        }
    } else {
        Write-HumanSummary $result
    }
} finally {
    if ($environmentBackup.Count -gt 0) {
        try {
            Restore-Environment
            $environmentBackup.Clear()
        } catch {
            # Do not print environment values.  The cross-build path records a
            # hard failure when its immediate restore fails.
            $environmentRestoreFailed = $true
        }
    }
}

exit $exitCode
