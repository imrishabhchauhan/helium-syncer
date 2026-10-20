# Phase-0 lab: does Helium accept a re-signed Secure Preferences file?
# Runs only on throwaway profiles under test-profiles\. Requires PowerShell 7.
#   A = signed for a foreign device id (simulates a profile copied from another PC)
#   B = re-signed for this PC, encrypted hashes dropped
#   C = re-signed for this PC, stale encrypted hashes kept
$ErrorActionPreference = 'Stop'
$root = Split-Path (Split-Path $PSScriptRoot)
Set-Location $root
$exe = 'C:\Program Files\imput\Helium\Application\chrome.exe'
$lab = Join-Path $root 'bin\hslab.exe'

function Run-Helium($ud, $port) {
  $p = Start-Process $exe -ArgumentList "--user-data-dir=`"$ud`"", "--remote-debugging-port=$port", '--no-first-run',
       '--no-default-browser-check', '--window-size=800,500', 'about:blank' -PassThru
  Start-Sleep 7
  & $lab close $port | Out-Null
  $null = $p.WaitForExit(20000)
  Start-Sleep 2
}
function Report($name, $ud) {
  $sp = Get-Content "$ud\Default\Secure Preferences" -Raw | ConvertFrom-Json
  $pr = Get-Content "$ud\Default\Preferences" -Raw | ConvertFrom-Json
  '[{0}] homepage={1} is_ntp={2} | homepage enc hash: {3} | super enc hash: {4} | reset_time: {5} | exts: {6}' -f $name,
    $sp.homepage, $sp.homepage_is_newtabpage, [bool]$sp.protection.macs.homepage_encrypted_hash,
    [bool]$sp.protection.super_encrypted_hash, $pr.prefs.preference_reset_time, @($sp.extensions.settings.PSObject.Properties).Count
}

$set = @('homepage="https://example.org/"', 'homepage_is_newtabpage=false')
$cases = [ordered]@{
  expA = @('--device=S-1-5-21-111111111-222222222-333333333')
  expB = @()
  expC = @('--keep-encrypted')
}
foreach ($n in $cases.Keys) {
  $ud = Join-Path $root "test-profiles\$n"
  if (Test-Path $ud) { Remove-Item -Recurse -Force $ud }
  Copy-Item -Recurse (Join-Path $root 'test-profiles\exp1') $ud
  & $lab resign "$ud\Default" @set @($cases[$n])
  Report "$n before" $ud
}
$port = 9341
foreach ($n in $cases.Keys) {
  $ud = Join-Path $root "test-profiles\$n"
  Run-Helium $ud $port; $port++
  Report "$n after " $ud
}
