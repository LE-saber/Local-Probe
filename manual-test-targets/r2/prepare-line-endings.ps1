# Run from a Windows checkout when Git line-ending settings have normalized the text fixtures.
# This script only restores the two deterministic local test files; it does not touch secrets.
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$utf8 = [System.Text.UTF8Encoding]::new($false)
$crlf = "第一行：LP_R2_CRLF_UTF8_第一行 🌏`r`n第二行：LP_R2_CRLF_UTF8_第二行`r`n最后一行：LP_R2_NO_TRAILING_NEWLINE"
[IO.File]::WriteAllText((Join-Path $root 'crlf-utf8.txt'), $crlf, $utf8)
$noTrailing = 'LP_R2_UTF8_NO_TRAILING_NEWLINE：中文与 emoji ✅'
[IO.File]::WriteAllText((Join-Path $root 'no-trailing-newline.txt'), $noTrailing, $utf8)
Write-Output 'R2 line-ending fixtures restored.'
