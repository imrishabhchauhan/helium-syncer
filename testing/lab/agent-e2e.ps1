# Agent end-to-end: the background agent syncs by itself after a (test)
# Helium quits. Reuses a folder made by cli-e2e.ps1. Local server only.
# Requires PowerShell 7.
param([string]$Work)
$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path $PSScriptRoot)
if (-not $Work) { $Work = (Get-ChildItem (Join-Path $root 'test-profiles') -Directory -Filter 'cli-*' | Sort-Object Name | Select-Object -Last 1).FullName }
"Using $Work"
$exe = Join-Path $root 'bin\helium-syncer.exe'
$winUD = Join-Path $Work 'win-helium'; $macUD = Join-Path $Work 'mac-helium'
$winHome = Join-Path $Work 'win-syncer'; $macHome = Join-Path $Work 'mac-syncer'

$server = Start-Process node -ArgumentList 'node_modules/wrangler/bin/wrangler.js', 'dev', '--env', 'dev', '--local', '--persist-to', (Join-Path $Work 'server-data'), '--port', '8799', '--ip', '127.0.0.1', '--inspector-port', '0', '--show-interactive-dev-session=false' -WorkingDirectory (Join-Path $root 'server') -PassThru -WindowStyle Hidden
$agentProc = $null
try {
  for ($i = 0; $i -lt 100; $i++) { try { if ((Invoke-WebRequest http://127.0.0.1:8799/v1/health -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200) { break } } catch { Start-Sleep -Milliseconds 300 } }
  # The configs from cli-e2e point at this local server already.
  $env:HELIUM_SYNCER_HOME = $winHome
  $log = Join-Path $winHome 'agent.log'
  if (Test-Path $log) { Remove-Item $log }
  $agentProc = Start-Process $exe -ArgumentList 'agent' -PassThru -WindowStyle Hidden
  Start-Sleep 8  # first periodic sync happens at start

  "--- open test Helium (Windows, Profile 1) and add a bookmark"
  $hp = Start-Process 'C:\Program Files\imput\Helium\Application\chrome.exe' -ArgumentList "--user-data-dir=`"$winUD`"", '--profile-directory="Profile 1"', '--remote-debugging-port=9391', "--load-extension=`"$(Join-Path $root 'testing\probe-extension')`"", '--no-first-run', 'about:blank' -PassThru
  Start-Sleep 5
  $before = if (Test-Path $log) { (Get-Content $log).Count } else { 0 }
  # Single quotes inside the JavaScript: PowerShell strips double quotes from native arguments.
  $added = & (Join-Path $root 'bin\hslab.exe') eval 9391 'probe.js' "probe.addBookmark('', 'Added while agent watched', 'https://agent.example/').then(b => b.title)" 2>&1 | Out-String
  if ($added -notmatch 'Added while agent watched') { throw "could not add the test bookmark: $added" }
  Start-Sleep 6
  $during = if (Test-Path $log) { (Get-Content $log).Count } else { 0 }
  if ($during -ne $before) { throw "the agent logged activity while Helium was open:`n$(Get-Content $log -Tail 5)" }
  & (Join-Path $root 'bin\hslab.exe') close 9391 | Out-Null
  $null = $hp.WaitForExit(20000)
  "--- wait for the agent"
  $ok = $false
  for ($i = 0; $i -lt 30; $i++) { Start-Sleep 1; if ((Test-Path $log) -and (Select-String -Path $log -Pattern 'Helium quit: sent [1-9]' -Quiet)) { $ok = $true; break } }
  Get-Content $log -Tail 5
  if (-not $ok) { throw 'the agent did not sync after Helium quit' }

  "--- the Mac receives it"
  $env:HELIUM_SYNCER_HOME = $macHome
  & $exe sync
  $bm = Get-Content (Join-Path $macUD 'Default\Bookmarks') -Raw -Encoding utf8
  if ($bm -notmatch 'Added while agent watched') { throw 'the Mac did not get the bookmark' }
  "PASS: agent end-to-end"
} finally {
  if ($agentProc) { Stop-Process -Id $agentProc.Id -Force -ErrorAction SilentlyContinue }
  Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue
  Remove-Item Env:HELIUM_SYNCER_HOME -ErrorAction SilentlyContinue
}
