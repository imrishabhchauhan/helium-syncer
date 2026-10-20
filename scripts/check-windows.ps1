# Helium Syncer - read-only check for Windows (v2).
# Finds Helium wherever it is installed and reports how its data is laid out.
# It NEVER prints passwords, cookies, keys or browsing data, and it changes nothing.
# Works in Windows PowerShell 5.1 and PowerShell 7.
# Output: printed here, saved to Desktop\helium-check-windows.txt and copied to the clipboard.

$ErrorActionPreference = 'SilentlyContinue'
$desktop = [Environment]::GetFolderPath('Desktop')
if (-not $desktop -or -not (Test-Path $desktop)) { $desktop = $env:TEMP }
$out = Join-Path $desktop 'helium-check-windows.txt'

function Read-Json($path) {
  if (-not (Test-Path -LiteralPath $path)) { return $null }
  try { return [IO.File]::ReadAllText($path) | ConvertFrom-Json } catch { return $null }
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
function Get-UserDataDirArg($cmd) {
  if ($cmd -match '--user-data-dir=(?:"([^"]+)"|(\S+))') { if ($Matches[1]) { return $Matches[1] } else { return $Matches[2] } }
  return $null
}

$report = & {
  "=== Helium Syncer check (Windows) v2 - $(Get-Date -Format 'yyyy-MM-dd HH:mm') ==="
  $os = Get-CimInstance Win32_OperatingSystem
  "Windows: $($os.Caption) $($os.Version) build $($os.BuildNumber)"
  "CPU: $env:PROCESSOR_ARCHITECTURE / $((Get-CimInstance Win32_Processor | Select-Object -First 1).Name)"
  "PowerShell: $($PSVersionTable.PSVersion)"

  # 1. Installed copies, found from the registry (per-user and machine-wide), not from a fixed path
  $uninstall = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*',
               'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*',
               'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*'
  $installs = @(Get-ItemProperty $uninstall | Where-Object { $_.DisplayName -eq 'Helium' -or $_.Publisher -eq 'The Helium Authors' })
  foreach ($i in $installs) {
    $scope = if ($i.PSPath -like '*HKEY_CURRENT_USER*') { 'per-user' } else { 'machine-wide' }
    "Install: $($i.DisplayName) $($i.DisplayVersion) ($scope) at $($i.InstallLocation)"
  }
  $smi = Get-ChildItem 'HKLM:\SOFTWARE\Clients\StartMenuInternet','HKCU:\SOFTWARE\Clients\StartMenuInternet' | Where-Object { $_.PSChildName -like 'Helium*' }
  foreach ($s in $smi) { "Registered browser: $($s.PSChildName) -> $((Get-ItemProperty "$($s.PSPath)\shell\open\command").'(default)')" }

  $exeCandidates = @($installs | ForEach-Object { Join-Path $_.InstallLocation 'chrome.exe' }) +
                   @("$env:LOCALAPPDATA\imput\Helium\Application\chrome.exe", "$env:ProgramFiles\imput\Helium\Application\chrome.exe",
                     "$env:USERPROFILE\scoop\apps\helium\current\chrome.exe")
  $exes = @($exeCandidates | Where-Object { $_ -and (Test-Path $_) } | Select-Object -Unique)
  foreach ($e in $exes) { "App: $e (Chromium $((Get-Item $e).VersionInfo.ProductVersion))" }
  if ($exes.Count -eq 0 -and $installs.Count -eq 0) { "App: NOT FOUND" }

  # 2. Running Helium and any custom --user-data-dir it was started with
  $procs = @(Get-CimInstance Win32_Process -Filter "Name='chrome.exe'" | Where-Object {
    $_.ExecutablePath -and (((Get-Item $_.ExecutablePath).VersionInfo.CompanyName -like '*Helium*') -or ($_.ExecutablePath -like '*\imput\Helium\*')) })
  $main = $procs | Where-Object { $_.CommandLine -notmatch '--type=' } | Select-Object -First 1
  "Running: $(if ($procs.Count) { 'yes' } else { 'no' })"
  $custom = @()
  if ($main) { $u = Get-UserDataDirArg $main.CommandLine; if ($u) { "Running with custom --user-data-dir: $u"; $custom += $u } }

  # Shortcuts that start Helium with a custom profile folder
  $lnkDirs = "$env:APPDATA\Microsoft\Windows\Start Menu", "$env:ProgramData\Microsoft\Windows\Start Menu", $desktop,
             "$env:APPDATA\Microsoft\Internet Explorer\Quick Launch\User Pinned\TaskBar"
  $shell = New-Object -ComObject WScript.Shell
  foreach ($l in Get-ChildItem $lnkDirs -Recurse -Filter *.lnk) {
    $sc = $shell.CreateShortcut($l.FullName)
    if ($sc.TargetPath -like '*imput\Helium*') {
      $u = Get-UserDataDirArg $sc.Arguments
      "Shortcut: $($l.Name)$(if ($u) { " (custom --user-data-dir: $u)" })"
      if ($u) { $custom += $u }
    }
  }

  # 3. Every user data folder (the one that holds "Local State")
  $uds = @($custom + "$env:LOCALAPPDATA\imput\Helium\User Data" | Where-Object { Test-Path (Join-Path $_ 'Local State') } | Select-Object -Unique)
  if ($uds.Count -eq 0) { "User data: NOT FOUND"; return }

  foreach ($ud in $uds) {
    ""
    "User data: $ud"
    "Last Version file: $([IO.File]::ReadAllText((Join-Path $ud 'Last Version')).Trim())"
    $ls = Read-Json (Join-Path $ud 'Local State')
    "Chromium stats version: $($ls.user_experience_metrics.stability.stats_version)"
    "os_crypt keys (names only): $(Key-Names $ls.os_crypt)"
    "Last used profile: $($ls.profile.last_used)"
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
      "Extension folders: $(@(Get-ChildItem "$p\Extensions" -Directory | Where-Object Name -ne 'Temp').Count)"
      "Local Extension Settings folders: $(@(Get-ChildItem "$p\Local Extension Settings" -Directory).Count)"
      "restore_on_startup: $(if ($null -ne $pr.session.restore_on_startup) { $pr.session.restore_on_startup } else { '(default)' })"
      $bm = Read-Json "$p\Bookmarks"
      if ($bm -and $bm.roots) {
        $c = @{ urls = 0; folders = 0 }
        foreach ($r in $bm.roots.PSObject.Properties) { Count-Bookmarks $r.Value $c }
        "Bookmarks: $($c.urls) links, $($c.folders) folders, roots: $(Key-Names $bm.roots), version $($bm.version)"
      } else { "Bookmarks: no file" }
      "History file: $(if (Test-Path "$p\History") { '{0} KB' -f [int]((Get-Item "$p\History").Length / 1KB) } else { 'none' })"
      "Cookies file: $(if (Test-Path "$p\Network\Cookies") { 'Network\Cookies' } elseif (Test-Path "$p\Cookies") { 'Cookies' } else { 'none' })"
      "Device Bound Sessions file: $(if (Test-Path "$p\Device Bound Sessions") { 'yes' } else { 'no' })"
      "Sessions files: $((Get-ChildItem "$p\Sessions" -File).Name -join ', ')"
      "Saved tab groups LevelDB: $(if (Test-Path "$p\Sync Data\LevelDB") { 'yes' } else { 'no' })"
      "Files in profile: $((Get-ChildItem $p).Name -join ' | ')"
    }
  }
  ""
  "=== End of check ==="
}

$report | Tee-Object -FilePath $out
try { $report | Set-Clipboard; "(Saved to $out and copied to your clipboard.)" } catch { "(Saved to $out.)" }
