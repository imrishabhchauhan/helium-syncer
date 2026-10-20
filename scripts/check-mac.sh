#!/usr/bin/env bash
# Helium Syncer — read-only check for macOS.
# Reports where Helium keeps its data and how the files are laid out.
# It NEVER prints passwords, cookies, keys or browsing data, and it changes nothing.
# Output: printed here, saved to ~/Desktop/helium-check-mac.txt and copied to the clipboard.

set -u
OUT="$HOME/Desktop/helium-check-mac.txt"

{
echo "=== Helium Syncer check (macOS) — $(date '+%Y-%m-%d %H:%M') ==="
echo "macOS: $(sw_vers -productVersion) ($(uname -m))"

# 1. The app
APP=""
for c in "/Applications/Helium.app" "$HOME/Applications/Helium.app"; do
  [ -d "$c" ] && APP="$c" && break
done
if [ -z "$APP" ]; then
  APP="$(mdfind 'kMDItemCFBundleIdentifier == "*helium*"c && kMDItemKind == "Application"' 2>/dev/null | head -n 1)"
fi
if [ -n "$APP" ]; then
  echo "App: $APP"
  echo "Bundle id: $(defaults read "$APP/Contents/Info" CFBundleIdentifier 2>/dev/null)"
  echo "Version: $(defaults read "$APP/Contents/Info" CFBundleShortVersionString 2>/dev/null)"
else
  echo "App: NOT FOUND"
fi
if pgrep -xq "Helium"; then echo "Running: yes"; else echo "Running: no"; fi

# 2. The user data folder (the one that holds "Local State")
UD=""
for c in "$HOME/Library/Application Support/net.imput.helium" \
         "$HOME/Library/Application Support/imput/Helium" \
         "$HOME/Library/Application Support/Helium"; do
  [ -f "$c/Local State" ] && UD="$c" && break
done
if [ -z "$UD" ]; then
  F="$(find "$HOME/Library/Application Support" -maxdepth 3 -name 'Local State' -ipath '*helium*' 2>/dev/null | head -n 1)"
  [ -n "$F" ] && UD="$(dirname "$F")"
fi
if [ -z "$UD" ]; then
  echo "User data: NOT FOUND"
  exit 0
fi
echo "User data: $UD"
echo "Top-level folders: $(ls -1 "$UD" | tr '\n' '|' )"

# 3. Encryption key presence (name only, never the value; no password prompt)
KC="$(security dump-keychain 2>/dev/null | grep -i '"svce"<blob>=".*safe storage"' | sort -u | sed 's/^ */Keychain item: /')"
if [ -n "$KC" ]; then echo "$KC"; else echo "Keychain item: none matching 'Safe Storage'"; fi

# 4. Profile structure (JavaScript for Automation ships with every Mac)
JS="$(mktemp -t helium-check).js"
cat > "$JS" <<'EOF'
ObjC.import('Foundation');
const fm = $.NSFileManager.defaultManager;
function read(p){ const s=$.NSString.stringWithContentsOfFileEncodingError(p,$.NSUTF8StringEncoding,null); return s.isNil()?null:ObjC.unwrap(s); }
function json(p){ const t=read(p); if(t===null) return null; try { return JSON.parse(t); } catch(e) { return {__error:String(e)}; } }
function ls(p){ const a=fm.contentsOfDirectoryAtPathError(p,null); return a.isNil()?[]:ObjC.deepUnwrap(a).sort(); }
function exists(p){ return fm.fileExistsAtPath(p); }
function kb(p){ const a=fm.attributesOfItemAtPathError(p,null); return a.isNil()?'-':Math.round(ObjC.unwrap(a.objectForKey('NSFileSize'))/1024)+' KB'; }
function keys(o){ return o && typeof o==='object' ? Object.keys(o).join(', ') : '(none)'; }
function countBm(n, c){ if(!n) return c; if(n.type==='url') c.urls++; if(n.type==='folder'){ c.folders++; (n.children||[]).forEach(x=>countBm(x,c)); } return c; }
function run(argv){
  const ud = argv[0], out = [];
  const ls_ = json(ud + '/Local State') || {};
  const cache = (ls_.profile && ls_.profile.info_cache) || {};
  out.push('Chromium/Helium stats version: ' + (ls_.stats_version || (ls_.user_experience_metrics && ls_.user_experience_metrics.stability && ls_.user_experience_metrics.stability.stats_version) || '?'));
  out.push('os_crypt keys (names only): ' + keys(ls_.os_crypt));
  out.push('Profiles: ' + Object.keys(cache).length);
  Object.keys(cache).forEach(dir => {
    const p = ud + '/' + dir;
    out.push('');
    out.push('--- Profile folder "' + dir + '" = "' + (cache[dir].name || '') + '"');
    const sp = json(p + '/Secure Preferences') || {};
    const pr = json(p + '/Preferences') || {};
    out.push('Secure Preferences top keys: ' + keys(sp));
    out.push('protection keys: ' + keys(sp.protection));
    out.push('protection.macs keys: ' + keys(sp.protection && sp.protection.macs));
    out.push('Extensions listed in Secure Preferences: ' + Object.keys((sp.extensions && sp.extensions.settings) || {}).length);
    out.push('Extensions listed in Preferences: ' + Object.keys((pr.extensions && pr.extensions.settings) || {}).length);
    out.push('Extension folders: ' + ls(p + '/Extensions').length);
    out.push('Local Extension Settings folders: ' + ls(p + '/Local Extension Settings').length);
    out.push('restore_on_startup: ' + (pr.session ? pr.session.restore_on_startup : '(default)'));
    const bm = json(p + '/Bookmarks');
    if (bm && bm.roots) {
      const c = {urls:0, folders:0};
      Object.keys(bm.roots).forEach(r => countBm(bm.roots[r], c));
      out.push('Bookmarks: ' + c.urls + ' links, ' + c.folders + ' folders, roots: ' + keys(bm.roots) + ', version ' + bm.version);
    } else out.push('Bookmarks: no file');
    out.push('History file: ' + (exists(p + '/History') ? kb(p + '/History') : 'none'));
    out.push('Cookies file: ' + (exists(p + '/Cookies') ? 'Cookies' : exists(p + '/Network/Cookies') ? 'Network/Cookies' : 'none'));
    out.push('Sessions files: ' + ls(p + '/Sessions').join(', '));
    out.push('Saved tab groups LevelDB: ' + (exists(p + '/Sync Data/LevelDB') ? 'yes' : 'no'));
    out.push('Files in profile: ' + ls(p).join(' | '));
  });
  return out.join('\n');
}
EOF
osascript -l JavaScript "$JS" "$UD"
rm -f "$JS"
echo ""
echo "=== End of check ==="
} 2>&1 | tee "$OUT"

pbcopy < "$OUT" 2>/dev/null && echo "(Saved to $OUT and copied to your clipboard.)"
