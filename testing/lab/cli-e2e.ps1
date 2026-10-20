# Command-line end-to-end test: two "computers" (separate HELIUM_SYNCER_HOME
# folders) sync throwaway Helium folders through a LOCAL server (wrangler dev).
# Never touches the real Helium folder or the live server. Requires PowerShell 7.
param([string]$MacSource)  # an existing test Helium folder with data (e.g. test-profiles\e2e-mac-...)
$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path $PSScriptRoot)
$tp = Join-Path $root 'test-profiles'
$run = Get-Date -Format 'yyyyMMdd-HHmmss'
$work = Join-Path $tp "cli-$run"
New-Item -ItemType Directory -Force $work | Out-Null
$exe = Join-Path $root 'bin\helium-syncer.exe'

if (-not $MacSource) { $MacSource = (Get-ChildItem $tp -Directory -Filter 'e2e-mac-*' | Where-Object Name -notlike '*-syncer' | Sort-Object Name | Select-Object -Last 1).FullName }
"Mac data from: $MacSource"
Copy-Item -Recurse $MacSource (Join-Path $work 'mac-helium')
Get-ChildItem (Join-Path $work 'mac-helium') -Recurse -Filter lockfile | Remove-Item -Force

# A fresh 'Windows' Helium folder made by Helium itself.
$winUD = Join-Path $work 'win-helium'
$p = Start-Process 'C:\Program Files\imput\Helium\Application\chrome.exe' -ArgumentList "--user-data-dir=`"$winUD`"", '--remote-debugging-port=9390', '--no-first-run', 'about:blank' -PassThru
Start-Sleep 5
& (Join-Path $root 'bin\hslab.exe') close 9390 | Out-Null
$null = $p.WaitForExit(20000); Start-Sleep 2

# Local server.
$server = Start-Process node -ArgumentList 'node_modules/wrangler/bin/wrangler.js', 'dev', '--env', 'dev', '--local', '--persist-to', (Join-Path $work 'server-data'), '--port', '8799', '--ip', '127.0.0.1', '--inspector-port', '0', '--show-interactive-dev-session=false' -WorkingDirectory (Join-Path $root 'server') -PassThru -WindowStyle Hidden
try {
  Push-Location (Join-Path $root 'server')
  node node_modules/wrangler/bin/wrangler.js d1 execute helium-syncer-dev --env dev --local --persist-to (Join-Path $work 'server-data') --file schema.sql --yes | Out-Null
  Pop-Location
  for ($i = 0; $i -lt 100; $i++) { try { if ((Invoke-WebRequest http://127.0.0.1:8799/v1/health -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200) { break } } catch { Start-Sleep -Milliseconds 300 } }

  function HS($hsHome, [string[]]$a) {
    $env:HELIUM_SYNCER_HOME = $hsHome
    $out = & $exe @a --server http://127.0.0.1:8799 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0) { throw "helium-syncer $($a -join ' ') failed:`n$out" }
    return $out
  }
  $macHome, $winHome = (Join-Path $work 'mac-syncer'), (Join-Path $work 'win-syncer')
  $macUD = Join-Path $work 'mac-helium'

  "--- MAC: start"
  $o = HS $macHome @('start', '--helium-folder', $macUD, '--name', 'Test Mac'); $o
  $code = ([regex]'[A-Z2-9]{4}(-[A-Z2-9]{4}){7}').Match($o).Value
  if (-not $code) { throw 'no sync code printed' }
  HS $macHome @('link', 'Default')
  "--- MAC: dry run"
  $o = HS $macHome @('sync', '--dry-run'); $o
  if ($o -notmatch 'No Helium file on this computer would change') { throw 'the first Mac sync must not change Mac files' }
  "--- MAC: sync"
  HS $macHome @('sync')

  "--- WIN: join + dry run"
  function Snap($d) { (Get-ChildItem $d -File -Recurse | Where-Object { $_.FullName -notmatch '\\(Cache|Code Cache|GPUCache|DawnGraphiteCache|DawnWebGPUCache)\\' } | Sort-Object FullName | ForEach-Object { $_.FullName + '=' + (Get-FileHash $_.FullName).Hash }) -join "`n" }
  $winDefault = Snap (Join-Path $winUD 'Default')
  HS $winHome @('join', $code.ToLower().Replace('-', ' '), '--helium-folder', $winUD, '--name', 'Test Windows')
  $o = HS $winHome @('sync', '--dry-run'); $o
  if ($o -notmatch 'Would add profile') { throw 'dry run should show the new profile' }
  if (Test-Path (Join-Path $winUD 'Profile 1')) { throw 'dry run created a profile' }
  "--- WIN: sync"
  $o = HS $winHome @('sync'); $o
  $bm = Get-Content (Join-Path $winUD 'Profile 1\Bookmarks') -Raw -Encoding utf8
  foreach ($want in 'Company wiki', 'हिंदी समाचार') { if ($bm -notmatch [regex]::Escape($want)) { throw "Windows is missing bookmark $want" } }
  if ((Snap (Join-Path $winUD 'Default')) -ne $winDefault) { throw "Windows' own profile changed" }
  "--- WIN: status"
  HS $winHome @('status')
  "--- MAC: sync again (should be quiet)"
  HS $macHome @('sync')
  "PASS: CLI end-to-end"
} finally {
  Remove-Item Env:HELIUM_SYNCER_HOME -ErrorAction SilentlyContinue
  Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
  Get-CimInstance Win32_Process -Filter "Name='workerd.exe'" | Where-Object { $_.CommandLine -like "*$run*" -or $_.ParentProcessId -eq $server.Id } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
}
