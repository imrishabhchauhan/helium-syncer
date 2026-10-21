# Captures real Helium windows for the app's pictures and the README.
# Uses a NEW throwaway profile under test-profiles\ (never a real profile).
# Nothing is clicked: tabs, the tab group and bookmarks are made by the
# test probe extension through the DevTools port.
#
#   pwsh -File testing\lab\capture-helium.ps1 -Out internal\ui\img
param(
    [string]$Out = 'internal\ui\img',
    [string]$Helium = 'C:\Program Files\imput\Helium\Application\chrome.exe',
    [int]$Port = 9341,
    [string]$Theme = 'light'   # light or dark
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent (Split-Path -Parent $PSScriptRoot))
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"

$ud = Join-Path (Resolve-Path 'test-profiles') ("shots-$Theme-" + (Get-Date -Format HHmmss))
if ($ud -notlike '*\test-profiles\*') { throw 'not a test folder' }
New-Item -ItemType Directory -Force "$ud\Default" | Out-Null
# Bookmarks bar on; theme as asked (0 system, 1 light, 2 dark).
$scheme = if ($Theme -eq 'dark') { 2 } else { 1 }
@{ bookmark_bar = @{ show_on_all_tabs = $true }; browser = @{ theme = @{ color_scheme2 = $scheme } } } |
    ConvertTo-Json -Depth 5 | Set-Content -Encoding utf8 "$ud\Default\Preferences"

# A real extension with a real icon (copied from an earlier test profile).
$dr = Get-ChildItem 'test-profiles\*\Default\Extensions\eimadpbcbfnmbkopoojfekhnkhdbieeh\*' -Directory | Select-Object -First 1
$ext = "$ud\_ext\darkreader"
Copy-Item -Recurse $dr.FullName $ext
Remove-Item -Recurse -Force "$ext\_metadata" -ErrorAction SilentlyContinue
$probe = (Resolve-Path 'testing\probe-extension').Path

$hargs = @("--user-data-dir=$ud", "--remote-debugging-port=$Port", '--no-first-run', '--no-default-browser-check',
    '--force-device-scale-factor=2', '--window-size=880,520', '--window-position=20,20',
    "--load-extension=$probe,$ext", 'about:blank')
$p = Start-Process -FilePath $Helium -ArgumentList $hargs -PassThru
Start-Sleep 6

function Eval($js) { go run .\cmd\hslab eval $Port '/probe.js' $js }
$setup = @'
(async () => {
  const bar = (await chrome.bookmarks.getTree())[0].children[0];
  for (const [title, url] of [["Wikipedia", "https://www.wikipedia.org/"], ["GitHub", "https://github.com/"], ["MDN Web Docs", "https://developer.mozilla.org/"], ["Hacker News", "https://news.ycombinator.com/"], ["BBC News", "https://www.bbc.com/news"]])
    await chrome.bookmarks.create({ parentId: bar.id, title, url });
  await chrome.bookmarks.create({ parentId: bar.id, title: "Reading" });
  // Dark Reader stays off for the browser pictures (it darkens pages).
  const dr = (await chrome.management.getAll()).find((e) => e.name.includes("Dark Reader"));
  if (dr) await chrome.management.setEnabled(dr.id, false);
  const [w] = await chrome.windows.getAll();
  const mk = (url, o = {}) => chrome.tabs.create({ windowId: w.id, url, active: false, ...o });
  const gh = await mk("https://github.com/", { pinned: true });
  const hn = await mk("https://news.ycombinator.com/", { pinned: true });
  const mdn = await mk("https://developer.mozilla.org/en-US/");
  const css = await mk("https://developer.mozilla.org/en-US/docs/Web/CSS");
  const g = await chrome.tabs.group({ tabIds: [mdn.id, css.id] });
  await chrome.tabGroups.update(g, { title: "Work", color: "blue" });
  const wiki = await mk("https://en.wikipedia.org/wiki/Helium", { active: true });
  for (const t of await chrome.tabs.query({})) if (t.url === "about:blank" || t.url.includes("darkreader")) await chrome.tabs.remove(t.id);
  return "ok";
})()
'@
Eval $setup
Start-Sleep 10

Add-Type -AssemblyName System.Drawing
Add-Type @'
using System; using System.Runtime.InteropServices;
public static class Win {
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr dc, uint flags);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L, T, R, B; }
}
'@
function Capture($name) {
    $p.Refresh()
    $h = $p.MainWindowHandle
    if ($h -eq 0) { throw 'no Helium window' }
    $r = New-Object Win+RECT; [Win]::GetWindowRect($h, [ref]$r) | Out-Null
    $bmp = New-Object System.Drawing.Bitmap ($r.R - $r.L), ($r.B - $r.T)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $dc = $g.GetHdc(); [Win]::PrintWindow($h, $dc, 2) | Out-Null; $g.ReleaseHdc($dc)
    New-Item -ItemType Directory -Force $Out | Out-Null
    $file = Join-Path $Out "$name-$Theme.png"
    $bmp.Save($file); "saved $file ($($bmp.Width)x$($bmp.Height))"
}
Capture 'window'
Eval '(async () => { for (const h of await chrome.history.search({ text: "darkreader", maxResults: 50 })) await chrome.history.deleteUrl({ url: h.url }); const [t] = await chrome.tabs.query({ active: true }); await chrome.tabs.update(t.id, { url: "chrome://history" }); return "ok"; })()'
Start-Sleep 3; Capture 'history'
Eval '(async () => { const [t] = await chrome.tabs.query({ active: true }); await chrome.tabs.update(t.id, { url: "chrome://settings/appearance" }); return "ok"; })()'
Start-Sleep 3; Capture 'settings'
Eval '(async () => { const dr = (await chrome.management.getAll()).find((e) => e.name.includes("Dark Reader")); if (dr) await chrome.management.setEnabled(dr.id, true); await new Promise((r) => setTimeout(r, 1500)); for (const t of await chrome.tabs.query({})) if (t.url.includes("darkreader")) await chrome.tabs.remove(t.id); const [t] = await chrome.tabs.query({ active: true, lastFocusedWindow: true }); await chrome.tabs.update(t.id, { url: "chrome://extensions" }); return "ok"; })()'
Start-Sleep 3; Capture 'extensions'

go run .\cmd\hslab close $Port | Out-Null
Start-Sleep 3
if (-not $p.HasExited) { Stop-Process -Id $p.Id -Force }
"profile: $ud"
