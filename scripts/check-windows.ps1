# Helium Syncer — read-only check for Windows.
# Reports where Helium keeps its data and how the files are laid out.
# It NEVER prints passwords, cookies, keys or browsing data, and it changes nothing.
# Output: printed here, saved to Desktop\helium-check-windows.txt and copied to the clipboard.

$out = Join-Path ([Environment]::GetFolderPath('Desktop')) 'helium-check-windows.txt'

function Read-Json($path) {
  if (-not (Test-Path -LiteralPath $path)) { return $null }
  try { return Get-Content -LiteralPath $path -Raw -Encoding UTF8 | ConvertFrom-Json } catch { return $null }
}
function Key-Names($obj) {
  if ($null -eq $obj) { return '(none)' }
  return ($obj.PSObject.Properties.Name -join ', ')
}
function Count-Bookmarks($node, $c) {
  if ($null -eq $node) { return }
  if ($node.type -eq 'url') { $c.urls++ }
  if ($node.type -eq 'folder') { $c.folders++; foreach ($ch in $node.children) { Count-Bookmarks $ch $c } }
}

$report = & {
  "=== Helium Syncer check (Windows) - $(Get-Date -Format 'yyyy-MM-dd HH:mm') ==="
  "Windows: $([Environment]::OSVersion.VersionString) ($env:PROCESSOR_ARCHITECTURE)"

  $exe = @("$env:LOCALAPPDATA\imput\Helium\Application\chrome.exe",
           "$env:LOCALAPPDATA\imput\Helium\Application\helium.exe",
           "$env:ProgramFiles\imput\Helium\Application\chrome.exe") | Where-Object { Test-Path $_ } | Select-Object -First 1
  if ($exe) { "App: $exe"; "Version: $((Get-Item $exe).VersionInfo.ProductVersion)" } else { "App: NOT FOUND in the usual places" }
  $running = Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -like '*\imput\Helium\*' } | Select-Object -First 1
  "Running: $(if ($running) { 'yes' } else { 'no' })"

  $ud = "$env:LOCALAPPDATA\imput\Helium\User Data"
  if (-not (Test-Path "$ud\Local State")) { "User data: NOT FOUND"; return }
  "User data: $ud"
  "Top-level folders: $((Get-ChildItem $ud -Directory).Name -join '|')"

  $ls = Read-Json "$ud\Local State"
  "Chromium/Helium stats version: $($ls.user_experience_metrics.stability.stats_version)"
  "os_crypt keys (names only): $(Key-Names $ls.os_crypt)"
  $cache = $ls.profile.info_cache
  "Profiles: $(@($cache.PSObject.Properties).Count)"

  foreach ($prop in $cache.PSObject.Properties) {
    $p = Join-Path $ud $prop.Name
    ""
    "--- Profile folder `"$($prop.Name)`" = `"$($prop.Value.name)`""
    $sp = Read-Json "$p\Secure Preferences"
    $pr = Read-Json "$p\Preferences"
    "Secure Preferences top keys: $(Key-Names $sp)"
    "protection keys: $(Key-Names $sp.protection)"
    "protection.macs keys: $(Key-Names $sp.protection.macs)"
    "Extensions listed in Secure Preferences: $(@($sp.extensions.settings.PSObject.Properties).Count)"
    "Extensions listed in Preferences: $(@($pr.extensions.settings.PSObject.Properties).Count)"
    "Extension folders: $(@(Get-ChildItem "$p\Extensions" -Directory -ErrorAction SilentlyContinue).Count)"
    "Local Extension Settings folders: $(@(Get-ChildItem "$p\Local Extension Settings" -Directory -ErrorAction SilentlyContinue).Count)"
    "restore_on_startup: $(if ($pr.session) { $pr.session.restore_on_startup } else { '(default)' })"
    $bm = Read-Json "$p\Bookmarks"
    if ($bm -and $bm.roots) {
      $c = @{ urls = 0; folders = 0 }
      foreach ($r in $bm.roots.PSObject.Properties) { Count-Bookmarks $r.Value $c }
      "Bookmarks: $($c.urls) links, $($c.folders) folders, roots: $(Key-Names $bm.roots), version $($bm.version)"
    } else { "Bookmarks: no file" }
    "History file: $(if (Test-Path "$p\History") { '{0} KB' -f [int]((Get-Item "$p\History").Length / 1KB) } else { 'none' })"
    "Cookies file: $(if (Test-Path "$p\Network\Cookies") { 'Network\Cookies' } elseif (Test-Path "$p\Cookies") { 'Cookies' } else { 'none' })"
    "Sessions files: $((Get-ChildItem "$p\Sessions" -File -ErrorAction SilentlyContinue).Name -join ', ')"
    "Saved tab groups LevelDB: $(if (Test-Path "$p\Sync Data\LevelDB") { 'yes' } else { 'no' })"
    "Files in profile: $((Get-ChildItem $p).Name -join ' | ')"
  }
  ""
  "=== End of check ==="
}

$report | Tee-Object -FilePath $out
try { $report | Set-Clipboard; "(Saved to $out and copied to your clipboard.)" } catch { "(Saved to $out.)" }
