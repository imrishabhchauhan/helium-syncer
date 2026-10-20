# Phase 0 findings

Verified on Windows 11 x64, Helium 0.19.4.1 / Chromium 155.0.8059.84, throwaway profiles only (`test-profiles/`, git-ignored).

## Test instances

- A separate Helium instance starts with `--user-data-dir=<test folder> --remote-debugging-port=<port>`; it is fully independent of the user's own Helium.
- `--load-extension=testing/probe-extension` works on Helium (unbranded Chromium), so tests can drive tabs, tab groups, bookmarks, history and extensions through the probe's service worker over the DevTools protocol.
- `Browser.close` over DevTools shuts the instance down cleanly, so it writes its session files like a normal quit.
- Helium ships uBlock Origin (`blockjmkbacgjkknlgpkjjiijinjdanf`) as a built-in extension (MV2, background page, `location: 5`).

## Protected settings (Secure Preferences)

Source: `services/preferences/tracked/pref_hash_calculator.cc`, `device_id_win.cc`, `device_id_mac.cc` at tag 155.0.8059.84.

- Legacy MAC = `UPPERHEX(HMAC-SHA256(key = seed, device_id ‖ path ‖ value_json))`.
- **Seed is empty** for Helium (all 26 MACs + `super_mac` reproduced with `seed = ""`).
- Device id: Windows = machine SID string from `LookupAccountName(<computer name>)`; macOS = `IOPlatformUUID`.
- `value_json` = `base::WriteJson` after removing empty dicts/lists nested in dicts. Exact rules: sorted keys, no spaces, `<` / U+2028 / U+2029 / control chars escaped as backslash-u + 4 upper-case hex, ints only within int32, doubles in shortest ECMAScript form with `.0` appended when integral.
- `super_mac` = MAC over path `""` and the serialised `protection.macs` dictionary.
- Encrypted hashes (`*_encrypted_hash`, `super_encrypted_hash`) = os_crypt-encrypted `SHA256(seed ‖ path ‖ value_json)`.
- Our serialiser re-creates `Secure Preferences`, `Preferences` and `Local State` **byte for byte**.

### Re-signing experiment (`testing/lab/resign-experiment.ps1`)

| Case | Result after Helium start + clean quit |
|---|---|
| A: signed for a foreign device id | homepage reset, 1 extension removed (the classic "copied profile" failure) |
| B: re-signed for this PC, encrypted hashes **dropped** | change kept, all extensions kept, Helium rebuilt the encrypted hashes itself |
| C: re-signed for this PC, stale encrypted hashes kept | homepage reset |

**Rule:** when writing protected settings on a device, recompute legacy MACs with that device's id and delete every encrypted hash. No access to the OS key store (DPAPI / macOS Keychain) is needed for settings or extensions.

Open: confirm the same on macOS (device id = IOPlatformUUID) with a throwaway profile on the owner's Mac.

## Bookmarks (`internal/bookmarks`)

- `Bookmarks` is pretty-printed JSON (3-space indent, `[ a, b ]` lists, CRLF on Windows, LF elsewhere).
- Checksum = MD5 (and SHA-256 when `checksum_sha256` exists) over, per node in encode order (bookmark_bar, other, synced; depth-first): UTF-8 id, UTF-16LE title, then `url` + URL or `folder`.
- Chromium 155 no longer rejects a file on checksum mismatch, but we still write the correct value.
- Every node has a `guid` that Helium keeps: the stable identity for merging across devices.
- Test: Helium-written file (Hindi, emoji, HTML-ish titles) re-encodes byte for byte; a node added by our writer shows in Helium with its guid intact.

## History (`internal/history`)

- SQLite, `meta.version` = **70** (last compatible 16) — the version gate refuses anything else.
- Visits from another device: inserted with original `visit_time`, title and transition; tagged with `originator_cache_guid` (source device) and `originator_visit_id` so they are never sent back (no echo). Same (url, visit_time) twice = skipped.
- Helium only lists visits whose transition has the chain start/end bits (`0x30000000`); the writer adds them if missing.
- Note: Chromium deletes history older than 90 days; older visits are pointless to sync.
- Test: synced visits show in `chrome.history` with exact original times; typed visits count as typed; re-applying a batch adds 0.

## Open tabs, pinned tabs, tab groups (`internal/sessions`)

- `Sessions/Session_<time>` = SNSS **version 3, not encrypted** (version 5 would need the OS key; detect and refuse).
- Commands `[uint16 size][uint8 id][payload]`; fixed C structs (with padding) or base::Pickle payloads. A file is restorable only if it contains the marker command 255; Helium restores the newest such file.
- Helium 0.19.4.1 stores **no page titles** in Session files (only in `Tabs_*`, the recently-closed list); synced tabs get titles from History.
- Tab groups: `SetTabGroup` (tab → 128-bit token) + `SetTabGroupMetadata2` (title, colour 0–8, collapsed, saved-group guid).
- Helium only reopens tabs if "Continue where you left off" is on: `session.restore_on_startup = 1`, a protected pref — set it and re-sign.
- Test: reader matches Helium's own tabs/pins/groups exactly; encode→parse is lossless; Helium restores added tabs, a new pinned tab and a new red group "Mac 🍎". Pinned tabs are moved to the front by Helium.
- Not carried yet: split-view tabs, per-tab user-agent override, session storage, window workspace.

## Profiles (`internal/profiles`)

- Adding a profile = new `Profile N` folder with a small `Preferences` (`profile.name`, avatar) + an entry in `Local State` → `profile.info_cache` and `profile.profiles_order`.
- Test: with "Your Helium" (Default) present, adding "Personal" and "Work 💼" gives three profiles; Helium opens both new ones and keeps their names; Default's files are untouched.

## Extensions (`internal/extensions`)

- A Web Store install is `location: 1`, `from_webstore: true`; files in `Extensions/<id>/<version>_0`; registration in `Secure Preferences` → `extensions.settings.<id>` (split protected pref, per-id MAC); data in `Local Extension Settings/<id>` (chrome.storage.local), `Sync Extension Settings/<id>` (chrome.storage.sync), and IndexedDB folders.
- `path` uses the OS separator (`id\4.9.133_0` on Windows) — convert when moving between OSes.
- Transfer = copy files + data folders (skip LevelDB `LOCK`), copy the registration without the source's service-worker state, re-sign for the target device, drop encrypted hashes.
- Test (Dark Reader 4.9.133, installed through Helium's own Web Store flow): arrives enabled, `chrome.storage.local` (Hindi/emoji value) and `chrome.storage.sync` carried over, still enabled after background verification + a second restart.
- Built-in uBlock Origin (`location: 5`) and other component extensions are never moved.

### Helium services gate Web Store installs

- Web Store downloads only work when `helium.services.user_consented = true` (set by Helium's first-run "setup" screen; also `helium.completed_onboarding`, `helium.services.enabled`, `helium.services.ext_proxy`). Otherwise Helium shows "Extension downloads are disabled".
- The owner's Windows profile has never stored this consent, so Web Store installs (and extension updates through Helium's proxy) are likely off there. Helium Syncer must not flip a privacy consent silently: the setup screen should explain it and let the user turn it on in Helium.
- Test fixture `test-profiles/ext-src` is created by seeding the consent in a throwaway profile, opening the store page, clicking "Add to Helium", and confirming the native dialog.

### Test-kit safety lessons

- A test instance uses the user's real Downloads folder by default: a CRX landed there once (moved back into the test folder). `testkit.SeedDownloadDir` now gives every new test profile its own download folder.
- `Browser.setDownloadBehavior` over DevTools makes Helium refuse extension downloads; don't use it.

## Saved tab groups (`Sync Data/LevelDB`)

- Helium **saves every tab group automatically**: each open group and each of its tabs gets a record keyed `saved_tab_group-dt-<guid>`; the session's `SetTabGroupMetadata2` carries the group's saved guid.
- After restoring a group that only existed in our session file, Helium created its saved records itself → open groups need only the session file.
- Record value (protobuf): `1` = version; `2` = synced specifics {`1` guid, `2` creation time, `3` update time (WebKit µs), `4` group {`1` position, `2` title, `3` colour = session colour + 1, `4` ?}, or `5` tab {`1` group guid, `2` position, `3` URL, `4` title}, `7` version}; `3` = device-local data (last interaction time etc., never synced).
- Tab titles are present here (unlike Session files) → a second title source.
- Writing closed saved groups (groups the user saved and closed) is the remaining piece: copy field 2 per guid, newest `update time` wins. Needs a UI check (saved-groups menu) because there is no extension API that lists saved groups.

## Still to do

- Closed saved groups write path + UI check (phase 1).
- Repeat the extension test on macOS (cross-OS path separator).

## Tooling notes

- Windows PowerShell 5.1 strips embedded double quotes when passing arguments to native programs; lab scripts run under PowerShell 7.
- Some editing tools decode backslash-u escapes in source text; the serialiser builds escapes with `%c` and tests build them with a helper, so no raw escape sequences live in the code.
