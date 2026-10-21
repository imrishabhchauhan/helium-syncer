#!/bin/sh
# Captures real Helium windows on macOS for the app's pictures and the README.
# Mac version of capture-helium.ps1, with the same setup.
# Uses a NEW throwaway profile under test-profiles/ (never a real profile).
# Nothing is clicked: tabs, the tab group and bookmarks are made by the
# test probe extension through the DevTools port.
#
#   sh testing/lab/capture-helium-mac.sh [light|dark] [out-dir]
#
# Needs Screen Recording permission for the terminal that runs it (for
# screencapture -l). If macOS has not granted it, the capture step fails.
set -eu
cd "$(dirname "$0")/../.."
ROOT="$(pwd)"

THEME="${1:-light}"                       # light or dark
OUT="${2:-testing/lab/captures-mac}"
HELIUM="${HELIUM_EXE:-/Applications/Helium.app/Contents/MacOS/Helium}"
PORT="${PORT:-9341}"
case "$THEME" in light) SCHEME=1 ;; dark) SCHEME=2 ;; *) echo "theme must be light or dark" >&2; exit 2 ;; esac

UD="$ROOT/test-profiles/shots-$THEME-$(date +%H%M%S)"
case "$UD" in */test-profiles/*) ;; *) echo "not a test folder" >&2; exit 1 ;; esac
mkdir -p "$UD/Default"
# Bookmarks bar on; theme as asked (0 system, 1 light, 2 dark).
printf '{"bookmark_bar":{"show_on_all_tabs":true},"browser":{"theme":{"color_scheme2":%s}}}\n' "$SCHEME" >"$UD/Default/Preferences"

# A real extension with a real icon (copied from an earlier test profile).
PROBE="$ROOT/testing/probe-extension"
EXTS="$PROBE"
DR="$(ls -d "$ROOT"/test-profiles/*/Default/Extensions/eimadpbcbfnmbkopoojfekhnkhdbieeh/* 2>/dev/null | head -n 1 || true)"
if [ -n "$DR" ]; then
	mkdir -p "$UD/_ext"
	cp -R "$DR" "$UD/_ext/darkreader"
	rm -rf "$UD/_ext/darkreader/_metadata"
	EXTS="$PROBE,$UD/_ext/darkreader"
else
	echo "warning: no Dark Reader copy in test-profiles/; the extensions picture will show only the probe" >&2
fi

# The mock keychain keeps this instance away from the login Keychain.
"$HELIUM" --user-data-dir="$UD" --remote-debugging-port="$PORT" --no-first-run --no-default-browser-check \
	--use-mock-keychain --password-store=basic \
	--window-size=880,520 --window-position=20,20 \
	--load-extension="$EXTS" about:blank >/dev/null 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT
sleep 6

evaljs() { go run ./cmd/hslab eval "$PORT" '/probe.js' "$1"; }

evaljs '(async () => {
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
})()'
sleep 10

# Finds this Helium's main window id (largest normal window of that process).
WINID_SWIFT="$(mktemp -d)/winid.swift"
cat >"$WINID_SWIFT" <<'EOF'
import CoreGraphics
import Foundation
let pid = Int32(CommandLine.arguments[1])!
let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
var best: (id: Int, area: Double)? = nil
for w in list {
    guard (w[kCGWindowOwnerPID as String] as? Int32) == pid,
          (w[kCGWindowLayer as String] as? Int) == 0,
          let id = w[kCGWindowNumber as String] as? Int,
          let b = w[kCGWindowBounds as String] as? [String: Double] else { continue }
    let area = (b["Width"] ?? 0) * (b["Height"] ?? 0)
    if best == nil || area > best!.area { best = (id, area) }
}
guard let b = best else { exit(1) }
print(b.id)
EOF

capture() {
	WID="$(swift "$WINID_SWIFT" "$PID")" || { echo "no Helium window" >&2; exit 1; }
	mkdir -p "$OUT"
	FILE="$OUT/$1-$THEME.png"
	screencapture -x -o -l"$WID" "$FILE"
	echo "saved $FILE ($(sips -g pixelWidth -g pixelHeight "$FILE" | awk '/pixel/ {printf "%s ", $2}'))"
}

capture window
evaljs '(async () => { for (const h of await chrome.history.search({ text: "darkreader", maxResults: 50 })) await chrome.history.deleteUrl({ url: h.url }); const [t] = await chrome.tabs.query({ active: true }); await chrome.tabs.update(t.id, { url: "chrome://history" }); return "ok"; })()'
sleep 3; capture history
evaljs '(async () => { const [t] = await chrome.tabs.query({ active: true }); await chrome.tabs.update(t.id, { url: "chrome://settings/appearance" }); return "ok"; })()'
sleep 3; capture settings
evaljs '(async () => { const dr = (await chrome.management.getAll()).find((e) => e.name.includes("Dark Reader")); if (dr) await chrome.management.setEnabled(dr.id, true); await new Promise((r) => setTimeout(r, 1500)); for (const t of await chrome.tabs.query({})) if (t.url.includes("darkreader")) await chrome.tabs.remove(t.id); const [t] = await chrome.tabs.query({ active: true, lastFocusedWindow: true }); await chrome.tabs.update(t.id, { url: "chrome://extensions" }); setTimeout(() => chrome.management.uninstallSelf(), 1500); return "ok"; })()'
sleep 4
# The probe is gone now: reload the page through DevTools, so it shows from the top.
go run ./cmd/hslab eval "$PORT" 'extensions' 'location.reload(); "ok"' >/dev/null
sleep 3; capture extensions

go run ./cmd/hslab close "$PORT" >/dev/null || true
sleep 3
kill "$PID" 2>/dev/null || true
trap - EXIT
echo "profile: $UD"
