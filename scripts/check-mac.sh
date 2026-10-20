#!/usr/bin/env bash
# Helium Syncer - read-only check for macOS (v2).
# Finds Helium wherever it is installed and reports how its data is laid out.
# It NEVER prints passwords, cookies, keys or browsing data, and it changes nothing.
# Output: printed here, saved to ~/Desktop/helium-check-mac.txt and copied to the clipboard.

set -u
export LC_ALL=en_US.UTF-8
OUT="$HOME/Desktop/helium-check-mac.txt"
BUNDLE_ID="net.imput.helium"

{
echo "=== Helium Syncer check (macOS) v2 - $(date '+%Y-%m-%d %H:%M') ==="
echo "macOS: $(sw_vers -productVersion) build $(sw_vers -buildVersion)"
echo "CPU: $(uname -m) / $(sysctl -n machdep.cpu.brand_string 2>/dev/null)"
[ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = "1" ] && echo "Note: this shell runs under Rosetta"

# 1. Every installed copy of the app, found by bundle id (any folder, any disk)
APPS="$( { mdfind "kMDItemCFBundleIdentifier == '$BUNDLE_ID'" 2>/dev/null
           for c in /Applications/Helium.app "$HOME/Applications/Helium.app"; do [ -d "$c" ] && echo "$c"; done; } | sort -u )"
if [ -z "$APPS" ]; then
  echo "App: NOT FOUND (bundle id $BUNDLE_ID)"
else
  while IFS= read -r APP; do
    [ -z "$APP" ] && continue
    echo "App: $APP"
    echo "  Bundle id: $(defaults read "$APP/Contents/Info" CFBundleIdentifier 2>/dev/null)"
    echo "  Version: $(defaults read "$APP/Contents/Info" CFBundleShortVersionString 2>/dev/null)"
    EXE="$APP/Contents/MacOS/$(defaults read "$APP/Contents/Info" CFBundleExecutable 2>/dev/null)"
    echo "  Architectures: $(lipo -archs "$EXE" 2>/dev/null)"
  done <<< "$APPS"
fi

# 2. Running Helium and any custom --user-data-dir it was started with
PS_LINE="$(ps -axo command= 2>/dev/null | grep -E '/Helium\.app/Contents/MacOS/' | grep -v -- '--type=' | head -n 1)"
if [ -n "$PS_LINE" ]; then
  echo "Running: yes"
  CUSTOM_UD="$(printf '%s' "$PS_LINE" | sed -n 's/.*--user-data-dir=\([^ ]*\).*/\1/p')"
  [ -n "$CUSTOM_UD" ] && echo "Running with custom --user-data-dir: $CUSTOM_UD"
else
  echo "Running: no"
  CUSTOM_UD=""
fi

# 3. Every user data folder (the one that holds "Local State")
UDS="$( { [ -n "$CUSTOM_UD" ] && [ -f "$CUSTOM_UD/Local State" ] && echo "$CUSTOM_UD"
          for c in "$HOME/Library/Application Support/$BUNDLE_ID" \
                   "$HOME/Library/Application Support/imput/Helium" \
                   "$HOME/Library/Application Support/Helium"; do [ -f "$c/Local State" ] && echo "$c"; done
          find "$HOME/Library/Application Support" -maxdepth 3 -name 'Local State' \( -ipath '*helium*' -o -ipath '*imput*' \) 2>/dev/null | while IFS= read -r f; do dirname "$f"; done
        } | sort -u )"
if [ -z "$UDS" ]; then
  echo "User data: NOT FOUND"
  exit 0
fi

# 4. Encryption key presence (names only, never the value; no password prompt)
KC="$(security dump-keychain 2>/dev/null | grep -iE '"svce"<blob>=".*(helium|imput|chromium).*"' | sort -u | sed 's/^ */Keychain item: /')"
if [ -n "$KC" ]; then echo "$KC"; else echo "Keychain item: none mentioning Helium, imput or Chromium"; fi

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
function firstOf(p, names){ for (const n of names) if (exists(p + '/' + n)) return n; return 'none'; }
function run(argv){
  const ud = argv[0], out = [];
  const st = json(ud + '/Local State') || {};
  const cache = (st.profile && st.profile.info_cache) || {};
  out.push('');
  out.push('User data: ' + ud);
  out.push('Last Version file: ' + ((read(ud + '/Last Version') || '?').trim()));
  out.push('Chromium stats version: ' + ((st.user_experience_metrics && st.user_experience_metrics.stability && st.user_experience_metrics.stability.stats_version) || st.stats_version || '?'));
  out.push('os_crypt keys (names only): ' + keys(st.os_crypt));
  out.push('Last used profile: ' + ((st.profile && st.profile.last_used) || '?'));
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
    out.push('Extension folders: ' + ls(p + '/Extensions').filter(x => x !== 'Temp').length);
    out.push('Local Extension Settings folders: ' + ls(p + '/Local Extension Settings').length);
    out.push('restore_on_startup: ' + (pr.session && pr.session.restore_on_startup !== undefined ? pr.session.restore_on_startup : '(default)'));
    const bm = json(p + '/Bookmarks');
    if (bm && bm.roots) {
      const c = {urls:0, folders:0};
      Object.keys(bm.roots).forEach(r => countBm(bm.roots[r], c));
      out.push('Bookmarks: ' + c.urls + ' links, ' + c.folders + ' folders, roots: ' + keys(bm.roots) + ', version ' + bm.version);
    } else out.push('Bookmarks: no file');
    out.push('History file: ' + (exists(p + '/History') ? kb(p + '/History') : 'none'));
    out.push('Cookies file: ' + firstOf(p, ['Network/Cookies', 'Cookies']));
    out.push('Device Bound Sessions file: ' + (exists(p + '/Device Bound Sessions') ? 'yes' : 'no'));
    out.push('Sessions files: ' + ls(p + '/Sessions').join(', '));
    out.push('Saved tab groups LevelDB: ' + (exists(p + '/Sync Data/LevelDB') ? 'yes' : 'no'));
    out.push('Files in profile: ' + ls(p).join(' | '));
  });
  return out.join('\n');
}
EOF
while IFS= read -r UD; do
  [ -n "$UD" ] && osascript -l JavaScript "$JS" "$UD"
done <<< "$UDS"
rm -f "$JS"
echo ""
echo "=== End of check ==="
} 2>&1 | tee "$OUT"

pbcopy < "$OUT" 2>/dev/null && echo "(Saved to $OUT and copied to your clipboard.)"
